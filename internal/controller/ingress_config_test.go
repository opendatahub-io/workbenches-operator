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

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
)

func newIngressConfigReconciler(objects ...runtime.Object) *WorkbenchesReconciler {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = componentsv1alpha1.AddToScheme(scheme)
	return &WorkbenchesReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build(), Scheme: scheme}
}

func ingressConfigConfigMap(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: NotebookControllerConfigMap, Namespace: "opendatahub"},
		Data:       data,
	}
}

func ingressConfigWorkbenches() *componentsv1alpha1.Workbenches {
	return &componentsv1alpha1.Workbenches{
		Spec: componentsv1alpha1.WorkbenchesSpec{Platform: "OpenDataHub"},
	}
}

// deliveredConfigMap builds the ConfigMap this controller renders for the
// given ingresses: the payload plus its sha256 hash.
func deliveredConfigMap(ingresses ...componentsv1alpha1.Ingress) *corev1.ConfigMap {
	raw, err := json.Marshal(ingresses)
	if err != nil {
		panic(err)
	}
	return ingressConfigConfigMap(map[string]string{
		IngressesConfigMapKey:                        string(raw),
		componentsv1alpha1.IngressesHashConfigMapKey: ingressConfigHash(string(raw)),
	})
}

func TestSyncIngressConfigStatus(t *testing.T) {
	ctx := context.Background()

	delivered := []componentsv1alpha1.Ingress{{Name: "alpha", GatewayName: "alpha", GatewayNamespace: "opendatahub"}}
	raw, err := json.Marshal(delivered)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	hash := ingressConfigHash(string(raw))

	workbenchesWithIngresses := func() *componentsv1alpha1.Workbenches {
		wb := ingressConfigWorkbenches()
		wb.Generation = 3
		wb.Spec.Ingresses = delivered
		return wb
	}

	t.Run("delivered hash matching the spec reports Ready", func(t *testing.T) {
		r := newIngressConfigReconciler(deliveredConfigMap(delivered...))
		wb := workbenchesWithIngresses()
		r.syncIngressConfigStatus(ctx, wb)

		cond := meta.FindStatusCondition(wb.Status.Conditions, conditionTypeIngressConfigReady)
		if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "IngressConfigReady" {
			t.Fatalf("condition = %+v, want True/IngressConfigReady", cond)
		}
		if cond.ObservedGeneration != 3 {
			t.Fatalf("ObservedGeneration = %d, want 3", cond.ObservedGeneration)
		}
		if wb.Status.IngressConfig.Hash != hash {
			t.Fatalf("hash = %q, want %q", wb.Status.IngressConfig.Hash, hash)
		}
	})

	t.Run("no ConfigMap yet reports DeliveryPending", func(t *testing.T) {
		r := newIngressConfigReconciler()
		wb := workbenchesWithIngresses()
		r.syncIngressConfigStatus(ctx, wb)

		cond := meta.FindStatusCondition(wb.Status.Conditions, conditionTypeIngressConfigReady)
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "IngressConfigDeliveryPending" {
			t.Fatalf("condition = %+v, want False/IngressConfigDeliveryPending", cond)
		}
		if wb.Status.IngressConfig != (componentsv1alpha1.IngressConfigStatus{}) {
			t.Fatalf("status = %+v, want cleared", wb.Status.IngressConfig)
		}
	})

	t.Run("hash mismatch reports DeliveryPending", func(t *testing.T) {
		tampered := ingressConfigConfigMap(map[string]string{
			IngressesConfigMapKey:                        string(raw),
			componentsv1alpha1.IngressesHashConfigMapKey: "deadbeef",
		})
		r := newIngressConfigReconciler(tampered)
		wb := workbenchesWithIngresses()
		r.syncIngressConfigStatus(ctx, wb)

		cond := meta.FindStatusCondition(wb.Status.Conditions, conditionTypeIngressConfigReady)
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "IngressConfigDeliveryPending" {
			t.Fatalf("condition = %+v, want False/IngressConfigDeliveryPending", cond)
		}
	})

	t.Run("legacy ConfigMap without a hash reports DeliveryPending", func(t *testing.T) {
		legacy := ingressConfigConfigMap(map[string]string{
			IngressesConfigMapKey: string(raw),
		})
		r := newIngressConfigReconciler(legacy)
		wb := workbenchesWithIngresses()
		r.syncIngressConfigStatus(ctx, wb)

		cond := meta.FindStatusCondition(wb.Status.Conditions, conditionTypeIngressConfigReady)
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "IngressConfigDeliveryPending" {
			t.Fatalf("condition = %+v, want False/IngressConfigDeliveryPending", cond)
		}
	})
}

func TestSetErrorStatusClearsIngressConfigEvidence(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = componentsv1alpha1.AddToScheme(scheme)

	wb := ingressConfigWorkbenches()
	wb.Generation = 5
	wb.Status.IngressConfig = componentsv1alpha1.IngressConfigStatus{
		Hash: "abc123",
	}
	meta.SetStatusCondition(&wb.Status.Conditions, metav1.Condition{
		Type:               conditionTypeIngressConfigReady,
		Status:             metav1.ConditionTrue,
		Reason:             "IngressConfigReady",
		ObservedGeneration: 4,
	})

	r := &WorkbenchesReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).
			WithStatusSubresource(&componentsv1alpha1.Workbenches{}).
			WithRuntimeObjects(wb).Build(),
		Scheme: scheme,
	}

	_, _ = r.setErrorStatus(context.Background(), wb, "ManifestApplyFailed", errors.New("apply boom"))

	cond := meta.FindStatusCondition(wb.Status.Conditions, conditionTypeIngressConfigReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "IngressConfigEvidenceStale" {
		t.Fatalf("IngressConfigReady after error = %+v, want False/IngressConfigEvidenceStale", cond)
	}
	if cond.ObservedGeneration != 5 {
		t.Fatalf("ObservedGeneration = %d, want 5", cond.ObservedGeneration)
	}
	if wb.Status.IngressConfig != (componentsv1alpha1.IngressConfigStatus{}) {
		t.Fatalf("status.ingressConfig = %+v, want cleared", wb.Status.IngressConfig)
	}
}
