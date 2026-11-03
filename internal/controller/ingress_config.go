package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
)

// ingressConfigHash returns the sha256 of the rendered ingresses payload,
// hex encoded. It is written to the notebook controller ConfigMap next to
// the payload so the consumer can verify the integrity of what it reads.
func ingressConfigHash(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// desiredIngressConfigHash returns the hash of the payload the current spec
// renders, using the same serialization as applyIngressConfig.
func desiredIngressConfigHash(wb *componentsv1alpha1.Workbenches) (string, error) {
	payload, err := json.Marshal(append([]componentsv1alpha1.Ingress{}, wb.Spec.Ingresses...))
	if err != nil {
		return "", fmt.Errorf("marshal ingresses: %w", err)
	}
	return ingressConfigHash(string(payload)), nil
}

// syncIngressConfigStatus publishes the IngressConfigReady condition and
// status.ingressConfig evidence: whether the ingress payload currently in
// the notebook controller ConfigMap is the one the current spec renders.
// Delivery is confirmed by comparing the delivered hash with the desired
// hash, both written by this controller's render path.
//
//   - spec not renderable or ConfigMap not readable: condition False
//     (delivery pending).
//   - delivered hash missing or different from the desired hash: condition
//     False (delivery pending).
//   - otherwise: condition True with the delivered hash.
//
// The condition is independent of the overall Ready condition: it never gates
// deployment success, it only reports delivery evidence. The consumer reads
// the ConfigMap dynamically on each reconciliation, so delivery does not
// depend on any operand rollout.
func (r *WorkbenchesReconciler) syncIngressConfigStatus(ctx context.Context, wb *componentsv1alpha1.Workbenches) {
	l := log.FromContext(ctx)

	desiredHash, err := desiredIngressConfigHash(wb)
	if err != nil {
		wb.Status.IngressConfig = componentsv1alpha1.IngressConfigStatus{}
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeIngressConfigReady,
			Status:             metav1.ConditionFalse,
			Reason:             "IngressConfigDeliveryPending",
			Message:            err.Error(),
			ObservedGeneration: wb.Generation,
		})
		return
	}

	config := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: NotebookControllerConfigMap, Namespace: r.resolveOperandNamespace(wb.Spec.Platform)}
	if err := r.readIngressConfig(ctx, key, config); err != nil {
		wb.Status.IngressConfig = componentsv1alpha1.IngressConfigStatus{}
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeIngressConfigReady,
			Status:             metav1.ConditionFalse,
			Reason:             "IngressConfigDeliveryPending",
			Message:            err.Error(),
			ObservedGeneration: wb.Generation,
		})
		return
	}

	deliveredHash := config.Data[componentsv1alpha1.IngressesHashConfigMapKey]
	if deliveredHash == "" || deliveredHash != desiredHash {
		wb.Status.IngressConfig = componentsv1alpha1.IngressConfigStatus{}
		meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
			Type:               conditionTypeIngressConfigReady,
			Status:             metav1.ConditionFalse,
			Reason:             "IngressConfigDeliveryPending",
			Message:            fmt.Sprintf("delivered ingress configuration hash %q does not match the desired %q", deliveredHash, desiredHash),
			ObservedGeneration: wb.Generation,
		})
		return
	}

	wb.Status.IngressConfig = componentsv1alpha1.IngressConfigStatus{
		Hash: deliveredHash,
	}
	meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
		Type:               conditionTypeIngressConfigReady,
		Status:             metav1.ConditionTrue,
		Reason:             "IngressConfigReady",
		Message:            fmt.Sprintf("ingress configuration %s delivered", deliveredHash),
		ObservedGeneration: wb.Generation,
	})
	l.V(1).Info("ingress config delivery evaluated", "hash", deliveredHash)
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
