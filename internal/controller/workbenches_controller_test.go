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

package controller_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
	"github.com/opendatahub-io/workbenches-operator/internal/controller"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
)

var _ = Describe("Workbenches Controller", func() {
	var (
		reconciler   *controller.WorkbenchesReconciler
		manifestsDir string
	)

	BeforeEach(func() {
		var err error
		manifestsDir, err = os.MkdirTemp("", "wb-test-manifests-*")
		Expect(err).NotTo(HaveOccurred())

		kustomizationContent := []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n")
		for _, sub := range []string{
			"workbenches/kf-notebook-controller/overlays/openshift",
			"workbenches/kf-notebook-controller",
			"workbenches/odh-notebook-controller/base",
			"workbenches/notebooks/odh/base",
			"workbenches/notebooks/rhoai/base",
		} {
			dir := filepath.Join(manifestsDir, sub)
			Expect(os.MkdirAll(dir, 0o750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "kustomization.yaml"), kustomizationContent, 0o600)).To(Succeed())
		}

		metadataContent := []byte(`releases:
  - name: Kubeflow Notebook Controller
    version: 1.10.0
    repoUrl: https://github.com/kubeflow/kubeflow
`)
		Expect(os.WriteFile(
			filepath.Join(manifestsDir, "workbenches/kf-notebook-controller/component_metadata.yaml"),
			metadataContent,
			0o600,
		)).To(Succeed())

		reconciler = &controller.WorkbenchesReconciler{
			Client:            k8sClient,
			Scheme:            scheme.Scheme,
			ManifestsBasePath: manifestsDir,
		}
	})

	AfterEach(func() {
		if manifestsDir != "" {
			os.RemoveAll(manifestsDir)
		}
	})

	Context("When reconciling a managed Workbenches resource", func() {
		It("Should create the workbench namespace and set status conditions", func() {
			nsName := "test-ns-managed-create"

			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupNamespace(nsName)
			})

			result, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			// Requeue expected since no deployments are present
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nsName}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKeyWithValue("opendatahub.io/generated-namespace", "true"))

			updated := getWorkbenches(wb.Name)
			Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
			Expect(updated.Status.WorkbenchNamespace).To(Equal(nsName))

			provCond := meta.FindStatusCondition(updated.Status.Conditions, "ProvisioningSucceeded")
			Expect(provCond).NotTo(BeNil())
			Expect(provCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(provCond.Reason).To(Equal("Provisioned"))

			Expect(updated.Status.Releases).To(HaveLen(1))
			Expect(updated.Status.Releases[0].Name).To(Equal("Kubeflow Notebook Controller"))
			Expect(updated.Status.Releases[0].Version).To(Equal("1.10.0"))
			Expect(updated.Status.Releases[0].RepoURL).To(Equal("https://github.com/kubeflow/kubeflow"))

			releaseCond := meta.FindStatusCondition(updated.Status.Conditions, "ReleaseMetadataAvailable")
			Expect(releaseCond).NotTo(BeNil())
			Expect(releaseCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(releaseCond.Reason).To(Equal("Available"))
		})

		It("Should continue reconciliation when release metadata is malformed", func() {
			nsName := "test-ns-bad-metadata"
			Expect(os.WriteFile(
				filepath.Join(manifestsDir, "workbenches/kf-notebook-controller/component_metadata.yaml"),
				[]byte("not: valid: yaml: ["),
				0o600,
			)).To(Succeed())

			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupNamespace(nsName)
			})

			result, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			updated := getWorkbenches(wb.Name)

			provCond := meta.FindStatusCondition(updated.Status.Conditions, "ProvisioningSucceeded")
			Expect(provCond).NotTo(BeNil())
			Expect(provCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(provCond.Reason).To(Equal("Provisioned"))
			Expect(updated.Status.Releases).To(BeEmpty())

			releaseCond := meta.FindStatusCondition(updated.Status.Conditions, "ReleaseMetadataAvailable")
			Expect(releaseCond).NotTo(BeNil())
			Expect(releaseCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(releaseCond.Reason).To(Equal("ReleaseMetadataFailed"))
		})

		It("Should set DeploymentsAvailable=False when no deployments exist", func() {
			nsName := "test-ns-no-deploys"
			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupNamespace(nsName)
			})

			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			updated := getWorkbenches(wb.Name)

			deplCond := meta.FindStatusCondition(updated.Status.Conditions, "DeploymentsAvailable")
			Expect(deplCond).NotTo(BeNil())
			Expect(deplCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(deplCond.Reason).To(Equal("Unavailable"))
		})

		It("Should set Ready=True when deployments are available", func() {
			nsName := "test-ns-ready"
			createNamespace(nsName)
			createDeployment(nsName, "odh-notebook-controller", 1)

			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupDeployments(nsName)
				cleanupNamespace(nsName)
			})

			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			updated := getWorkbenches(wb.Name)
			Expect(updated.Status.Phase).To(Equal("Ready"))

			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCond.Reason).To(Equal("ReconcileSuccess"))

			degradedCond := meta.FindStatusCondition(updated.Status.Conditions, "Degraded")
			Expect(degradedCond).NotTo(BeNil())
			Expect(degradedCond.Status).To(Equal(metav1.ConditionFalse))
		})

		It("Should use RHOAI default namespace when platform is SelfManagedRhoai", func() {
			wb := createWorkbenches("Managed", "", "SelfManagedRhoai")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupNamespace("rhods-notebooks")
			})

			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			updated := getWorkbenches(wb.Name)
			Expect(updated.Status.WorkbenchNamespace).To(Equal("rhods-notebooks"))

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "rhods-notebooks"}, ns)).To(Succeed())
		})

		It("Should label a pre-existing namespace", func() {
			nsName := "test-ns-preexist"
			createNamespace(nsName)

			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupNamespace(nsName)
			})

			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nsName}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKeyWithValue("opendatahub.io/generated-namespace", "true"))
		})

		It("Should resolve workbench namespace from spec for SelfManagedRhoai platform", func() {
			nsName := "test-ns-params"
			wb := &componentsv1alpha1.Workbenches{
				ObjectMeta: metav1.ObjectMeta{Name: componentsv1alpha1.WorkbenchesInstanceName},
				Spec: componentsv1alpha1.WorkbenchesSpec{
					ManagementState:    "Managed",
					WorkbenchNamespace: nsName,
					Platform:           "SelfManagedRhoai",
					GatewayDomain:      "gateway.example.com",
					MLflowEnabled:      true,
				},
			}
			Expect(k8sClient.Create(ctx, wb)).To(Succeed())

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupNamespace(nsName)
			})

			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			updated := getWorkbenches(wb.Name)
			Expect(updated.Status.WorkbenchNamespace).To(Equal(nsName))
		})
	})

	Context("When reconciling a Removed Workbenches resource", func() {
		It("Should set Ready=False and ProvisioningSucceeded=False", func() {
			wb := createWorkbenches("Removed", "", "")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
			})

			result, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updated := getWorkbenches(wb.Name)
			Expect(updated.Status.Phase).To(Equal("Not Ready"))

			readyCond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("Removed"))

			provCond := meta.FindStatusCondition(updated.Status.Conditions, "ProvisioningSucceeded")
			Expect(provCond).NotTo(BeNil())
			Expect(provCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(provCond.Reason).To(Equal("Removed"))

			Expect(updated.Status.Releases).To(BeEmpty())
		})

		It("Should clean up labeled resources when transitioning to Removed", func() {
			nsName := "test-ns-removed-cleanup"
			createNamespace(nsName)
			createDeployment(nsName, "notebook-controller", 1)

			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupDeployments(nsName)
				cleanupNamespace(nsName)
			})

			// First reconcile in Managed state adds the finalizer
			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			// Transition to Removed
			updated := getWorkbenches(wb.Name)
			updated.Spec.ManagementState = "Removed"
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			// Verify the labeled deployment was deleted
			deploys := &appsv1.DeploymentList{}
			Expect(k8sClient.List(ctx, deploys, client.InNamespace(nsName), client.MatchingLabels{
				metadata.ComponentLabelKey: metadata.LabelTrue,
				metadata.PartOfLabelKey:    metadata.ComponentLabelValue,
			})).To(Succeed())
			Expect(deploys.Items).To(BeEmpty())

			// Verify status is set correctly
			final := getWorkbenches(wb.Name)
			Expect(final.Status.Phase).To(Equal("Not Ready"))
		})
	})

	Context("When the resource does not exist", func() {
		It("Should return no error and empty result", func() {
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	Context("Finalizer management", func() {
		It("Should add the finalizer on first reconcile", func() {
			nsName := "test-ns-finalizer-add"
			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupNamespace(nsName)
			})

			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			updated := getWorkbenches(wb.Name)
			Expect(updated.Finalizers).To(ContainElement("components.platform.opendatahub.io/workbenches-cleanup"))
		})

		It("Should clean up labeled resources and remove finalizer on deletion", func() {
			nsName := "test-ns-finalizer-del"
			createNamespace(nsName)
			createDeployment(nsName, "notebook-controller-deployment", 1)

			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupDeployments(nsName)
				cleanupNamespace(nsName)
			})

			// First reconcile adds the finalizer
			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			updated := getWorkbenches(wb.Name)
			Expect(updated.Finalizers).To(ContainElement("components.platform.opendatahub.io/workbenches-cleanup"))

			// Delete the CR (sets DeletionTimestamp)
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())

			// Reconcile should trigger cleanup and remove the finalizer
			_, err = reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			// Verify the deployment was deleted
			deploys := &appsv1.DeploymentList{}
			Expect(k8sClient.List(ctx, deploys, client.InNamespace(nsName), client.MatchingLabels{
				metadata.ComponentLabelKey: metadata.LabelTrue,
				metadata.PartOfLabelKey:    metadata.ComponentLabelValue,
			})).To(Succeed())
			Expect(deploys.Items).To(BeEmpty())
		})

		It("Should skip cleanup and complete deletion when finalizer is absent", func() {
			nsName := "test-ns-no-finalizer"
			createNamespace(nsName)
			createDeployment(nsName, "should-survive", 1)

			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupDeployments(nsName)
				cleanupNamespace(nsName)
			})

			// First reconcile adds the workbenches finalizer
			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			// Remove the workbenches finalizer manually (simulating it was never added)
			// and add a temporary holder so the object isn't immediately deleted
			updated := getWorkbenches(wb.Name)
			controllerutil.RemoveFinalizer(updated, "components.platform.opendatahub.io/workbenches-cleanup")
			controllerutil.AddFinalizer(updated, "test-holder")
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())

			// Delete the CR (DeletionTimestamp is set, held by test-holder)
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())

			// Reconcile should see DeletionTimestamp but no workbenches finalizer,
			// so it skips cleanup entirely. test-holder keeps the object alive.
			result, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// The labeled deployment should still exist (no cleanup was performed)
			deploys := &appsv1.DeploymentList{}
			Expect(k8sClient.List(ctx, deploys, client.InNamespace(nsName), client.MatchingLabels{
				metadata.ComponentLabelKey: metadata.LabelTrue,
				metadata.PartOfLabelKey:    metadata.ComponentLabelValue,
			})).To(Succeed())
			Expect(deploys.Items).To(HaveLen(1))
		})

		It("Should handle idempotent deletion when resources are already gone", func() {
			nsName := "test-ns-idempotent"
			createNamespace(nsName)

			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupNamespace(nsName)
			})

			// First reconcile adds the finalizer
			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			updated := getWorkbenches(wb.Name)
			Expect(updated.Finalizers).To(ContainElement("components.platform.opendatahub.io/workbenches-cleanup"))

			// Delete the CR — no labeled resources exist in the namespace
			Expect(k8sClient.Delete(ctx, updated)).To(Succeed())

			// Reconcile should succeed even though there's nothing to clean up
			result, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	Context("When transitioning between states", func() {
		It("Should transition from Managed to Removed", func() {
			nsName := "test-ns-transition"
			wb := createWorkbenches("Managed", nsName, "OpenDataHub")

			DeferCleanup(func() {
				cleanupWorkbenches(wb)
				cleanupNamespace(nsName)
			})

			_, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())

			// Transition to Removed
			updated := getWorkbenches(wb.Name)
			updated.Spec.ManagementState = "Removed"
			Expect(k8sClient.Update(ctx, updated)).To(Succeed())

			result, err := reconciler.Reconcile(ctx, requestFor(wb))
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			final := getWorkbenches(wb.Name)
			Expect(final.Status.Phase).To(Equal("Not Ready"))

			readyCond := meta.FindStatusCondition(final.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
		})
	})
})

// Test helpers

func createWorkbenches(state, ns, platformType string) *componentsv1alpha1.Workbenches {
	wb := &componentsv1alpha1.Workbenches{
		ObjectMeta: metav1.ObjectMeta{Name: componentsv1alpha1.WorkbenchesInstanceName},
		Spec: componentsv1alpha1.WorkbenchesSpec{
			ManagementState:    state,
			WorkbenchNamespace: ns,
			Platform:           platformType,
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, wb)).To(Succeed())

	return wb
}

func getWorkbenches(name string) *componentsv1alpha1.Workbenches {
	wb := &componentsv1alpha1.Workbenches{}
	ExpectWithOffset(1, k8sClient.Get(ctx, types.NamespacedName{Name: name}, wb)).To(Succeed())

	return wb
}

func requestFor(wb *componentsv1alpha1.Workbenches) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{Name: wb.Name},
	}
}

func createNamespace(name string) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, ns)).To(Succeed())
}

func createDeployment(namespace, name string, readyReplicas int32) {
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				metadata.ComponentLabelKey: metadata.LabelTrue,
				metadata.PartOfLabelKey:    metadata.ComponentLabelValue,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "manager", Image: "test:latest"},
					},
				},
			},
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, deploy)).To(Succeed())

	deploy.Status.ReadyReplicas = readyReplicas
	deploy.Status.Replicas = 1
	deploy.Status.AvailableReplicas = readyReplicas
	ExpectWithOffset(1, k8sClient.Status().Update(ctx, deploy)).To(Succeed())
}

func cleanupWorkbenches(wb *componentsv1alpha1.Workbenches) {
	latest := &componentsv1alpha1.Workbenches{}

	err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wb), latest)
	if err != nil {
		ExpectWithOffset(1, client.IgnoreNotFound(err)).To(Succeed())
		return
	}

	if len(latest.Finalizers) > 0 {
		latest.Finalizers = nil

		if err := k8sClient.Update(ctx, latest); err != nil {
			ExpectWithOffset(1, client.IgnoreNotFound(err)).To(Succeed())

			return
		}
	}

	ExpectWithOffset(1, client.IgnoreNotFound(k8sClient.Delete(ctx, latest))).To(Succeed())
}

func cleanupNamespace(name string) {
	ns := &corev1.Namespace{}

	err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, ns)
	if err != nil {
		ExpectWithOffset(1, client.IgnoreNotFound(err)).To(Succeed())
		return
	}

	ExpectWithOffset(1, k8sClient.Delete(ctx, ns)).To(Succeed())
}

func cleanupDeployments(namespace string) {
	deployments := &appsv1.DeploymentList{}

	ExpectWithOffset(1, k8sClient.List(ctx, deployments, client.InNamespace(namespace))).To(Succeed())

	for i := range deployments.Items {
		ExpectWithOffset(1, k8sClient.Delete(ctx, &deployments.Items[i])).To(Succeed())
	}
}
