package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
)

// notebookControllerComponentNames lists the component names of the
// deployments stamped and gated for the ingress configuration. Only the odh
// controller reads odh-notebook-controller-config today (the kf operand
// consumes its own config and culler ConfigMaps, see
// opt/manifests/workbenches/kf-notebook-controller/manager/manager.yaml),
// but the kf deployment is stamped and gated conservatively so the rollout
// evidence stays complete if the notebooks controller unification makes it
// a config consumer.
var notebookControllerComponentNames = []string{"odh-notebook-controller", "kf-notebook-controller"}

// ingressConfigHash returns the sha256 of the rendered ingresses payload,
// hex encoded. The same value is written to the notebook controller ConfigMap
// (so the consumer can verify the integrity of what it reads) and stamped on
// the notebook controller deployment pod templates (so rollout evidence binds
// to the exact content delivered).
func ingressConfigHash(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// isNotebookControllerDeployment reports whether object labels identify a
// notebook controller deployment (the ingress config consumer).
func isNotebookControllerDeployment(labels map[string]string) bool {
	name, found := labels[metadata.ComponentNameLabelKey]
	return found && slices.Contains(notebookControllerComponentNames, name)
}

// stampIngressConfig records the rendered content hash on notebook controller
// deployment pod templates. An empty hash leaves annotations untouched: the
// stale stamp is inert (the rollout gate only evaluates it against the hash
// in the live ConfigMap), and writing null would coerce to "" on the typed
// map[string]string annotations, bumping the deployment generation and
// restarting pods for no reason.
func stampIngressConfig(
	objects []*unstructured.Unstructured,
	hash string,
) error {
	if hash == "" {
		return nil
	}

	for _, obj := range objects {
		if obj.GetKind() != "Deployment" || !isNotebookControllerDeployment(obj.GetLabels()) {
			continue
		}

		annotations, _, err := unstructured.NestedMap(obj.Object, "spec", "template", "metadata", "annotations")
		if err != nil {
			return fmt.Errorf("failed to read pod template annotations for deployment %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		}

		if annotations == nil {
			annotations = map[string]any{}
		}

		annotations[metadata.IngressConfigHashAnnotationKey] = hash

		if err := unstructured.SetNestedMap(obj.Object, annotations, "spec", "template", "metadata", "annotations"); err != nil {
			return fmt.Errorf("failed to set pod template annotations for deployment %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		}
	}

	return nil
}

// syncIngressConfigStatus publishes the IngressConfigReady condition and
// status.ingressConfig evidence: whether the notebook controller deployments
// have fully rolled out the content currently delivered in the ConfigMap.
//
//   - rendered content not yet delivered (ConfigMap read fails): condition
//     False (delivery pending).
//   - operand deployment rollout incomplete for the delivered content:
//     condition False (rollout pending).
//   - otherwise: condition True with the delivered hash and the deployment
//     generation that completed the rollout.
//
// The condition is independent of the overall Ready condition: it never gates
// deployment success, it only reports delivery evidence.
func (r *WorkbenchesReconciler) syncIngressConfigStatus(ctx context.Context, wb *componentsv1alpha1.Workbenches) {
	l := log.FromContext(ctx)

	deliveredHash, rolloutGeneration, rolloutMsg := r.ingressConfigRolloutState(ctx, wb)
	if rolloutMsg != "" {
		wb.Status.IngressConfig = componentsv1alpha1.IngressConfigStatus{}
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeIngressConfigReady,
			Status:             metav1.ConditionFalse,
			Reason:             "IngressConfigRolloutPending",
			Message:            rolloutMsg,
			ObservedGeneration: wb.Generation,
		})
		return
	}

	wb.Status.IngressConfig = componentsv1alpha1.IngressConfigStatus{
		Hash:                 deliveredHash,
		DeploymentGeneration: rolloutGeneration,
	}
	meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
		Type:   conditionTypeIngressConfigReady,
		Status: metav1.ConditionTrue,
		Reason: "IngressConfigReady",
		Message: fmt.Sprintf("ingress configuration %s delivered and operand rollout complete",
			deliveredHash),
		ObservedGeneration: wb.Generation,
	})
	l.V(1).Info("ingress config delivery evaluated",
		"hash", deliveredHash,
		"deploymentGeneration", rolloutGeneration)
}

// gateIngressesForDelivery withholds ingresses that have not yet been delivered
// to the notebook controller until the operand rollout carrying the currently
// delivered content completes (RHOAIENG-102011 scenario 3, consumer side).
// This controller is the only component that knows whether the
// notebook-controller deployments consuming the ConfigMap have rolled out the
// content they were last given. New entries are held back while that rollout
// is pending; entries already delivered stay verbatim (so a re-rollout
// triggered by a content change does not yank in-use shards into
// IngressNotFound, and a spec change to a delivered shard's gateway does not
// reach the ConfigMap before the rollout validating it completes), and
// removals from the spec are always honored.
func (r *WorkbenchesReconciler) gateIngressesForDelivery(
	ctx context.Context,
	wb *componentsv1alpha1.Workbenches,
	desired []componentsv1alpha1.Ingress,
) ([]componentsv1alpha1.Ingress, error) {
	if _, _, rolloutMsg := r.ingressConfigRolloutState(ctx, wb); rolloutMsg == "" {
		return desired, nil
	}

	delivered, err := r.deliveredIngresses(ctx, wb)
	if err != nil {
		return nil, err
	}

	gated := make([]componentsv1alpha1.Ingress, 0, len(desired))
	for _, ingress := range desired {
		if record, ok := delivered[ingress.Name]; ok {
			// Keep the delivered record, not the desired one: a spec change to
			// a delivered shard's gateway must not reach the ConfigMap before
			// the rollout that validates it completes.
			gated = append(gated, record)
			continue
		}
		log.FromContext(ctx).V(1).Info(
			"withholding ingress until the delivered content rollout completes",
			"ingress", ingress.Name)
	}
	return gated, nil
}

// deliveredIngresses reads the ingress records currently in the notebook
// controller ConfigMap, the artifact the notebook controller actually
// consumes. It is the delivery truth for the gate: what is in the ConfigMap
// has been handed out, what is not has not. The read goes through the API
// reader when available: a cache-stale read after this controller's own apply
// could miss a delivered ingress and yank it from the live ConfigMap during a
// pending rollout.
func (r *WorkbenchesReconciler) deliveredIngresses(
	ctx context.Context,
	wb *componentsv1alpha1.Workbenches,
) (map[string]componentsv1alpha1.Ingress, error) {
	delivered := map[string]componentsv1alpha1.Ingress{}

	config := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: NotebookControllerConfigMap, Namespace: r.resolveOperandNamespace(wb.Spec.Platform)}
	if err := r.readIngressConfig(ctx, key, config); err != nil {
		return nil, err
	}

	raw, found := config.Data[IngressesConfigMapKey]
	if !found {
		return delivered, nil
	}

	var ingresses []componentsv1alpha1.Ingress
	// Malformed data was not written by this controller's render path; treat
	// nothing as delivered so the gate fails closed for new assignments until
	// the ConfigMap is healed by the next apply.
	if err := json.Unmarshal([]byte(raw), &ingresses); err != nil {
		log.FromContext(ctx).V(1).Info("ignoring malformed ingresses data in ConfigMap", "error", err)
		ingresses = nil
	}
	for _, ingress := range ingresses {
		// Only records this controller could have written count as
		// delivered: the spec is validated by the CRD, so anything failing
		// the same constraints is foreign data and must not satisfy the gate
		// for a matching desired ingress, nor be written back verbatim while
		// a rollout is pending.
		if !validIngressRecord(ingress) {
			continue
		}
		delivered[ingress.Name] = ingress
	}
	return delivered, nil
}

var (
	dnsLabelPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dnsNamePattern  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	hostnamePattern = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?$`)
)

// validIngressRecord mirrors the CRD constraints on spec.ingresses entries
// (see the Ingress type in api/v1alpha1) for records read back from the
// ConfigMap.
func validIngressRecord(ingress componentsv1alpha1.Ingress) bool {
	return len(ingress.Name) >= 1 && len(ingress.Name) <= 63 && dnsLabelPattern.MatchString(ingress.Name) &&
		len(ingress.GatewayName) >= 1 && len(ingress.GatewayName) <= 253 && dnsNamePattern.MatchString(ingress.GatewayName) &&
		len(ingress.GatewayNamespace) >= 1 && len(ingress.GatewayNamespace) <= 63 && dnsLabelPattern.MatchString(ingress.GatewayNamespace) &&
		len(ingress.Hostname) <= 253 && hostnamePattern.MatchString(ingress.Hostname)
}

// ingressConfigRolloutState reports whether the notebook controller
// deployments have completed the rollout of the content currently delivered
// in the ConfigMap. A non-empty message means the rollout is not complete.
// The delivered hash is read through the API reader (see deliveredIngresses);
// a missing ConfigMap or a missing hash key means nothing has been delivered
// under this scheme yet, which counts as pending: the first delivery must
// roll out before any assignment is handed out.
func (r *WorkbenchesReconciler) ingressConfigRolloutState(
	ctx context.Context,
	wb *componentsv1alpha1.Workbenches,
) (string, int64, string) {
	config := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: NotebookControllerConfigMap, Namespace: r.resolveOperandNamespace(wb.Spec.Platform)}
	if err := r.readIngressConfig(ctx, key, config); err != nil {
		return "", 0, fmt.Sprintf("failed to read %s ConfigMap: %v", NotebookControllerConfigMap, err)
	}

	deliveredHash, found := config.Data[componentsv1alpha1.IngressesHashConfigMapKey]
	if !found || deliveredHash == "" {
		return "", 0, "no ingress configuration delivered yet"
	}

	generation, msg := r.notebookControllerRolloutFor(ctx, wb, deliveredHash)
	return deliveredHash, generation, msg
}

// notebookControllerRolloutFor reports the rollout state of the notebook
// controller deployments for the given content hash: every deployment must
// carry the hash on its pod template and have completed its rollout. Only
// deployments identified as notebook controllers are evaluated, so unrelated
// component rollouts can neither block the check nor pollute the reported
// generation.
func (r *WorkbenchesReconciler) notebookControllerRolloutFor(
	ctx context.Context,
	wb *componentsv1alpha1.Workbenches,
	hash string,
) (int64, string) {
	nsName := r.resolveOperandNamespace(wb.Spec.Platform)

	deployments := &appsv1.DeploymentList{}
	err := r.List(ctx, deployments, client.InNamespace(nsName), client.MatchingLabels{
		metadata.ComponentLabelKey: metadata.LabelTrue,
	})
	if err != nil {
		return 0, fmt.Sprintf("failed to list deployments: %v", err)
	}

	seen := make(map[string]bool, len(notebookControllerComponentNames))
	rolloutGeneration := int64(0)
	for i := range deployments.Items {
		d := &deployments.Items[i]

		if !isNotebookControllerDeployment(d.Labels) {
			continue
		}

		seen[d.Labels[metadata.ComponentNameLabelKey]] = true
		if d.Generation > rolloutGeneration {
			rolloutGeneration = d.Generation
		}

		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}

		if desired < 1 {
			return 0, fmt.Sprintf("deployment %s/%s is scaled to zero", d.Namespace, d.Name)
		}

		if d.Spec.Template.Annotations[metadata.IngressConfigHashAnnotationKey] != hash {
			return 0, fmt.Sprintf("deployment %s/%s is not yet carrying the delivered ingress configuration", d.Namespace, d.Name)
		}

		if d.Status.ObservedGeneration < d.Generation {
			return 0, fmt.Sprintf("deployment %s/%s rollout not observed by controller (generation %d, observed %d)",
				d.Namespace, d.Name, d.Generation, d.Status.ObservedGeneration)
		}

		// Requiring total replicas to equal the desired count rules out surge
		// rollouts where updated and ready replicas both reach the desired
		// count while old pods are still terminating.
		if d.Status.UpdatedReplicas < desired || d.Status.ReadyReplicas < desired || d.Status.Replicas != desired {
			return 0, fmt.Sprintf("deployment %s/%s rollout in progress (%d/%d updated, %d/%d ready, %d total)",
				d.Namespace, d.Name, d.Status.UpdatedReplicas, desired, d.Status.ReadyReplicas, desired, d.Status.Replicas)
		}
	}

	for _, name := range notebookControllerComponentNames {
		if !seen[name] {
			return 0, "no notebook controller deployment found for component " + name
		}
	}

	return rolloutGeneration, ""
}

// readIngressConfig reads the notebook controller ConfigMap through the API
// reader when available: reads that must not observe stale state after this
// controller's own writes bypass the cache.
func (r *WorkbenchesReconciler) readIngressConfig(ctx context.Context, key client.ObjectKey, config *corev1.ConfigMap) error {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}

	if err := reader.Get(ctx, key, config); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to read %s ConfigMap: %w", NotebookControllerConfigMap, err)
	}
	return nil
}
