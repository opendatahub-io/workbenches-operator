/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package controller contains the Workbenches reconciler.
package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
	"github.com/opendatahub-io/workbenches-operator/internal/platform"
	"github.com/opendatahub-io/workbenches-operator/internal/releases"
	statusutil "github.com/opendatahub-io/workbenches-operator/internal/status"
)

const (
	conditionTypeReady                    = "Ready"
	conditionTypeProvisioningSucceeded    = "ProvisioningSucceeded"
	conditionTypeDegraded                 = "Degraded"
	conditionTypeDeploymentsAvailable     = "DeploymentsAvailable"
	conditionTypeReleaseMetadataAvailable = "ReleaseMetadataAvailable"
	requeueDelay                          = 30 * time.Second

	rateLimiterBaseDelay = 5 * time.Second
	rateLimiterMaxDelay  = 5 * time.Minute

	workbenchesFinalizer = "components.platform.opendatahub.io/workbenches-cleanup"
)

// WorkbenchesReconciler reconciles a Workbenches object.
type WorkbenchesReconciler struct {
	client.Client

	Scheme            *runtime.Scheme
	ManifestsBasePath string
}

// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=workbenches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=workbenches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=workbenches/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps;secrets;services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// escalate and bind for RBAC resources are granted in a separate hand-maintained ClusterRole
// (config/rbac/rbac_escalate_role.yaml) scoped to specific resourceNames from upstream manifests.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete
// Write verbs are required because the operator creates/patches webhook configs from upstream manifests via SSA
// and deletes them during component removal.
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations;validatingwebhookconfigurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeflow.org,resources=notebooks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=image.openshift.io,resources=imagestreams,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles the reconciliation loop for Workbenches resources.
func (r *WorkbenchesReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	workbenches := &componentsv1alpha1.Workbenches{}

	err := r.Get(ctx, req.NamespacedName, workbenches)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	l.Info("reconciling Workbenches", "name", workbenches.Name, "generation", workbenches.Generation)

	if !workbenches.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, workbenches)
	}

	if !controllerutil.ContainsFinalizer(workbenches, workbenchesFinalizer) {
		controllerutil.AddFinalizer(workbenches, workbenchesFinalizer)

		if err := r.Update(ctx, workbenches); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	if workbenches.Spec.ManagementState == "Removed" {
		return r.reconcileRemoved(ctx, workbenches)
	}

	return r.reconcileManaged(ctx, workbenches)
}

// SetupWithManager sets up the controller with the Manager.
// A custom rate limiter is configured with exponential backoff (5s base, 5m max)
// to avoid tight retry loops on persistent failures like missing manifests.
func (r *WorkbenchesReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// TODO: Add Owns() watches for managed child resources once applyObjects() sets
	// OwnerReferences on created objects. Without owner refs, Owns() watches are
	// ineffective because controller-runtime relies on them to map child events
	// back to the parent Workbenches CR.
	// See: https://github.com/opendatahub-io/workbenches-operator/issues/30
	return ctrl.NewControllerManagedBy(mgr).
		For(&componentsv1alpha1.Workbenches{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&appsv1.Deployment{},
			handler.EnqueueRequestsFromMapFunc(r.mapComponentDeploymentToWorkbenches),
			builder.WithPredicates(deploymentAvailabilityChangedPredicate{}),
		).
		Named("workbenches").
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[ctrl.Request](
				rateLimiterBaseDelay,
				rateLimiterMaxDelay,
			),
		}).
		Complete(r)
}

func (r *WorkbenchesReconciler) mapComponentDeploymentToWorkbenches(ctx context.Context, obj client.Object) []reconcile.Request {
	deploy, ok := obj.(*appsv1.Deployment)
	if !ok {
		return nil
	}

	if deploy.GetLabels()[metadata.ComponentLabelKey] != metadata.LabelTrue {
		return nil
	}

	wb := &componentsv1alpha1.Workbenches{}

	err := r.Get(ctx, types.NamespacedName{Name: componentsv1alpha1.WorkbenchesInstanceName}, wb)
	if err != nil {
		if !errors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "failed to get Workbenches for deployment watch")
		}

		return nil
	}

	if deploy.GetNamespace() != r.resolveWorkbenchNamespace(wb) {
		return nil
	}

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: componentsv1alpha1.WorkbenchesInstanceName},
	}}
}

type deploymentAvailabilityChangedPredicate struct{}

func (deploymentAvailabilityChangedPredicate) Create(e event.CreateEvent) bool {
	return hasComponentLabel(e.Object)
}

func (deploymentAvailabilityChangedPredicate) Update(e event.UpdateEvent) bool {
	oldHasLabel := hasComponentLabel(e.ObjectOld)
	newHasLabel := hasComponentLabel(e.ObjectNew)
	if oldHasLabel != newHasLabel {
		return true
	}

	if !newHasLabel {
		return false
	}

	oldDeploy, oldOK := e.ObjectOld.(*appsv1.Deployment)
	newDeploy, newOK := e.ObjectNew.(*appsv1.Deployment)
	if !oldOK || !newOK {
		return true
	}

	oldDesired := deploymentDesiredReplicas(oldDeploy)
	newDesired := deploymentDesiredReplicas(newDeploy)

	return oldDeploy.Status.ReadyReplicas != newDeploy.Status.ReadyReplicas ||
		oldDeploy.Status.AvailableReplicas != newDeploy.Status.AvailableReplicas ||
		oldDesired != newDesired
}

func (deploymentAvailabilityChangedPredicate) Delete(e event.DeleteEvent) bool {
	return hasComponentLabel(e.Object)
}

func (deploymentAvailabilityChangedPredicate) Generic(_ event.GenericEvent) bool {
	return false
}

func hasComponentLabel(obj client.Object) bool {
	return obj.GetLabels()[metadata.ComponentLabelKey] == metadata.LabelTrue
}

func deploymentDesiredReplicas(deploy *appsv1.Deployment) int32 {
	if deploy.Spec.Replicas == nil {
		return 1
	}

	return *deploy.Spec.Replicas
}

func (r *WorkbenchesReconciler) reconcileDelete(ctx context.Context, wb *componentsv1alpha1.Workbenches) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	l.Info("workbenches CR is being deleted, cleaning up managed resources")

	if controllerutil.ContainsFinalizer(wb, workbenchesFinalizer) {
		nsName := r.resolveWorkbenchNamespace(wb)

		if err := r.cleanupManagedResources(ctx, nsName); err != nil {
			l.Error(err, "failed to cleanup managed resources")

			return ctrl.Result{}, err
		}

		controllerutil.RemoveFinalizer(wb, workbenchesFinalizer)

		if err := r.Update(ctx, wb); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

func (r *WorkbenchesReconciler) reconcileRemoved(ctx context.Context, wb *componentsv1alpha1.Workbenches) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	l.Info("workbenches management state is Removed")

	nsName := r.resolveWorkbenchNamespace(wb)

	if err := r.cleanupManagedResources(ctx, nsName); err != nil {
		return r.setErrorStatus(ctx, wb, "CleanupFailed", err)
	}

	meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Removed",
		Message:            "Workbenches component has been removed",
		ObservedGeneration: wb.Generation,
	})

	meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
		Type:               conditionTypeProvisioningSucceeded,
		Status:             metav1.ConditionFalse,
		Reason:             "Removed",
		Message:            "Workbenches component has been removed",
		ObservedGeneration: wb.Generation,
	})

	wb.Status.Phase = statusutil.ComputePhase(statusutil.PhaseContext{Removed: true})
	wb.Status.Releases = nil
	wb.Status.ObservedGeneration = wb.Generation

	err := r.Status().Update(ctx, wb)

	return ctrl.Result{}, err
}

func (r *WorkbenchesReconciler) populateReleases(wb *componentsv1alpha1.Workbenches) error {
	componentReleases, err := releases.CollectWorkbenchesReleases(r.ManifestsBasePath)
	if err != nil {
		return fmt.Errorf("collecting component releases: %w", err)
	}

	wb.Status.Releases = componentReleases

	return nil
}

func (r *WorkbenchesReconciler) reconcileManaged(ctx context.Context, wb *componentsv1alpha1.Workbenches) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	phaseCtx := statusutil.PhaseContext{
		PreviousObservedGeneration: wb.Status.ObservedGeneration,
		Generation:                 wb.Generation,
		WasReady:                   meta.IsStatusConditionTrue(wb.Status.Conditions, conditionTypeReady),
	}

	if wb.Status.Phase == "" && wb.Status.ObservedGeneration == 0 {
		wb.Status.Phase = statusutil.PhasePending

		if err := r.Status().Update(ctx, wb); err != nil {
			l.Error(err, "failed to update Pending status")

			return ctrl.Result{RequeueAfter: requeueDelay}, err
		}

		// Requeue immediately to continue provisioning after the first Pending status write.
		return ctrl.Result{Requeue: true}, nil
	}

	if err := validateSpec(wb.Spec); err != nil {
		return r.setErrorStatus(ctx, wb, "InvalidSpec", err)
	}

	if err := r.configureDependencies(ctx, wb); err != nil {
		return r.setErrorStatus(ctx, wb, "ConfigureDependenciesFailed", err)
	}

	params := r.computeKustomizeParams(wb)
	l.V(1).Info("computed kustomize params", "params", params)

	nsName := r.resolveWorkbenchNamespace(wb)

	if err := r.renderAndApply(ctx, params, nsName, wb.Spec.Platform); err != nil {
		return r.setErrorStatus(ctx, wb, "ManifestApplyFailed", err)
	}

	if err := r.populateReleases(wb); err != nil {
		// Release metadata is informational; a missing or malformed
		// component_metadata.yaml must not block a successful deploy.
		l.Error(err, "failed to populate release metadata; continuing with empty releases")
		wb.Status.Releases = nil
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReleaseMetadataAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             "ReleaseMetadataFailed",
			Message:            err.Error(),
			ObservedGeneration: wb.Generation,
		})
	} else {
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReleaseMetadataAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             "Available",
			Message:            "Component release metadata is available",
			ObservedGeneration: wb.Generation,
		})
	}

	meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
		Type:               conditionTypeProvisioningSucceeded,
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "Workbenches manifests have been provisioned",
		ObservedGeneration: wb.Generation,
	})

	deploymentsReady, deployMsg := r.checkDeployments(ctx, wb)
	r.setDeploymentCondition(wb, deploymentsReady, deployMsg)

	wb.Status.WorkbenchNamespace = nsName
	wb.Status.ObservedGeneration = wb.Generation
	r.setReadyCondition(wb, deploymentsReady, deployMsg, phaseCtx.WasReady)

	phaseCtx.Ready = meta.IsStatusConditionTrue(wb.Status.Conditions, conditionTypeReady)
	phaseCtx.Degraded = meta.IsStatusConditionTrue(wb.Status.Conditions, conditionTypeDegraded)
	phaseCtx.ProvisioningSucceeded = meta.IsStatusConditionTrue(wb.Status.Conditions, conditionTypeProvisioningSucceeded)
	wb.Status.Phase = statusutil.ComputePhase(phaseCtx)

	err := r.Status().Update(ctx, wb)
	if err != nil {
		l.Error(err, "failed to update Workbenches status")

		return ctrl.Result{RequeueAfter: requeueDelay}, err
	}

	l.Info("reconciliation complete", "phase", wb.Status.Phase)

	if !deploymentsReady {
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	return ctrl.Result{}, nil
}

func (r *WorkbenchesReconciler) setDeploymentCondition(wb *componentsv1alpha1.Workbenches, ready bool, msg string) {
	if ready {
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeDeploymentsAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             "Available",
			Message:            "All deployments are available",
			ObservedGeneration: wb.Generation,
		})
	} else {
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeDeploymentsAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             "Unavailable",
			Message:            msg,
			ObservedGeneration: wb.Generation,
		})
	}
}

func (r *WorkbenchesReconciler) setReadyCondition(
	wb *componentsv1alpha1.Workbenches,
	deploymentsReady bool,
	deployMsg string,
	wasReady bool,
) {
	if deploymentsReady {
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "ReconcileSuccess",
			Message:            "Workbenches component is ready",
			ObservedGeneration: wb.Generation,
		})

		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeDegraded,
			Status:             metav1.ConditionFalse,
			Reason:             "NotDegraded",
			Message:            "Workbenches component is operating normally",
			ObservedGeneration: wb.Generation,
		})

		return
	}

	meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "DeploymentsNotReady",
		Message:            deployMsg,
		ObservedGeneration: wb.Generation,
	})

	if wasReady {
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeDegraded,
			Status:             metav1.ConditionTrue,
			Reason:             "DeploymentsNotReady",
			Message:            deployMsg,
			ObservedGeneration: wb.Generation,
		})
	}
}

func (r *WorkbenchesReconciler) configureDependencies(ctx context.Context, wb *componentsv1alpha1.Workbenches) error {
	l := log.FromContext(ctx)
	nsName := r.resolveWorkbenchNamespace(wb)

	ns := &corev1.Namespace{}

	err := r.Get(ctx, client.ObjectKey{Name: nsName}, ns)
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get namespace %s: %w", nsName, err)
		}

		l.Info("creating workbench namespace", "namespace", nsName)

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
				Labels: map[string]string{
					metadata.OwnedNamespaceLabel: metadata.LabelTrue,
				},
			},
		}

		if createErr := r.Create(ctx, ns); createErr != nil {
			return fmt.Errorf("failed to create namespace %s: %w", nsName, createErr)
		}

		return nil
	}

	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}

	if ns.Labels[metadata.OwnedNamespaceLabel] != metadata.LabelTrue {
		ns.Labels[metadata.OwnedNamespaceLabel] = metadata.LabelTrue

		if updateErr := r.Update(ctx, ns); updateErr != nil {
			return fmt.Errorf("failed to update namespace %s labels: %w", nsName, updateErr)
		}
	}

	return nil
}

func validateSpec(spec componentsv1alpha1.WorkbenchesSpec) error {
	if spec.Platform != "" && !platform.IsValid(spec.Platform) {
		return fmt.Errorf("unsupported platform %q, must be one of: %s, %s",
			spec.Platform, platform.OpenDataHub, platform.SelfManagedRhoai)
	}

	return nil
}

func (r *WorkbenchesReconciler) resolveWorkbenchNamespace(wb *componentsv1alpha1.Workbenches) string {
	if wb.Spec.WorkbenchNamespace != "" {
		return wb.Spec.WorkbenchNamespace
	}

	return platform.DefaultNotebooksNamespace(wb.Spec.Platform)
}

func (r *WorkbenchesReconciler) computeKustomizeParams(wb *componentsv1alpha1.Workbenches) map[string]string {
	gatewayURL := ""
	if wb.Spec.GatewayDomain != "" {
		gatewayURL = wb.Spec.GatewayDomain
	}

	return map[string]string{
		"section-title":  platform.SectionTitle(wb.Spec.Platform),
		"mlflow-enabled": strconv.FormatBool(wb.Spec.MLflowEnabled),
		"gateway-url":    gatewayURL,
	}
}

func (r *WorkbenchesReconciler) checkDeployments(ctx context.Context, wb *componentsv1alpha1.Workbenches) (bool, string) {
	l := log.FromContext(ctx)
	nsName := r.resolveWorkbenchNamespace(wb)

	deployments := &appsv1.DeploymentList{}

	err := r.List(ctx, deployments, client.InNamespace(nsName), client.MatchingLabels{
		metadata.ComponentLabelKey: metadata.LabelTrue,
	})
	if err != nil {
		l.V(1).Info("failed to list deployments, treating as not ready", "error", err)

		return false, fmt.Sprintf("failed to list deployments: %v", err)
	}

	if len(deployments.Items) == 0 {
		return false, "no notebook controller deployments found"
	}

	return deploymentsAvailability(deployments.Items)
}

func (r *WorkbenchesReconciler) setErrorStatus(
	ctx context.Context,
	wb *componentsv1alpha1.Workbenches,
	reason string,
	reconcileErr error,
) (ctrl.Result, error) {
	meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            reconcileErr.Error(),
		ObservedGeneration: wb.Generation,
	})

	wb.Status.Phase = statusutil.ComputePhase(statusutil.PhaseContext{Failed: true})
	wb.Status.ObservedGeneration = wb.Generation

	if err := r.Status().Update(ctx, wb); err != nil {
		log.FromContext(ctx).Error(err, "failed to update error status")
	}

	return ctrl.Result{}, reconcileErr
}

// deploymentsAvailability reports whether all component deployments have the desired
// number of ready replicas. A deployment scaled to zero is treated as unavailable.
func deploymentsAvailability(deployments []appsv1.Deployment) (bool, string) {
	if len(deployments) == 0 {
		return false, "no notebook controller deployments found"
	}

	for i := range deployments {
		d := &deployments[i]
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}

		if desired < 1 {
			return false, fmt.Sprintf("deployment %s/%s is scaled to zero", d.Namespace, d.Name)
		}

		if d.Status.ReadyReplicas < desired {
			return false, fmt.Sprintf("deployment %s/%s has %d/%d ready replicas",
				d.Namespace, d.Name, d.Status.ReadyReplicas, desired)
		}
	}

	return true, ""
}
