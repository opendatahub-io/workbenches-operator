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
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
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

var (
	replicasOne  = int32(1)
	replicasZero = int32(0)
)

// rolloutGateDeploymentSpec describes one deployment for the rollout gate
// table test. Named fields keep each scenario explicit; every value is a
// deliberate scenario input, not a default. A nil stamp leaves the annotation
// absent, and nil replicas exercises the desired=1 default.
type rolloutGateDeploymentSpec struct {
	name       string
	component  string
	replicas   *int32
	stamp      *string
	generation int64
	observed   int64
	updated    int32
	ready      int32
	total      int32
}

// stampedDeployment is a notebook controller deployment fully rolled out at
// the given generation with the given content hash on its pod template.
func stampedDeployment(name, component string, generation int64, stamp string) rolloutGateDeploymentSpec {
	return rolloutGateDeploymentSpec{name: name, component: component,
		replicas: &replicasOne, stamp: &stamp, generation: generation, observed: generation,
		updated: 1, ready: 1, total: 1}
}

func (s rolloutGateDeploymentSpec) build() *appsv1.Deployment {
	annotations := map[string]string{}
	if s.stamp != nil {
		annotations[metadata.IngressConfigHashAnnotationKey] = *s.stamp
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       s.name,
			Namespace:  "opendatahub",
			Generation: s.generation,
			Labels: map[string]string{
				metadata.ComponentNameLabelKey: s.component,
				metadata.ComponentLabelKey:     metadata.LabelTrue,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: s.replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: annotations},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: s.observed,
			UpdatedReplicas:    s.updated,
			ReadyReplicas:      s.ready,
			Replicas:           totalReplicasOr(s.total, s.updated, s.ready),
		},
	}
}

func totalReplicasOr(total, updated, ready int32) int32 {
	if total != 0 {
		return total
	}
	if updated > ready {
		return updated
	}
	return ready
}

func unstructuredDeployment(name string, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("apps/v1")
	obj.SetKind("Deployment")
	obj.SetName(name)
	obj.SetNamespace("opendatahub")
	obj.SetLabels(labels)
	_ = unstructured.SetNestedField(obj.Object, map[string]any{}, "spec", "template", "metadata")
	return obj
}

func podTemplateAnnotation(t *testing.T, obj *unstructured.Unstructured) any {
	t.Helper()
	value, found, err := unstructured.NestedMap(obj.Object, "spec", "template", "metadata", "annotations")
	if err != nil {
		t.Fatalf("failed to read pod template annotations: %v", err)
	}
	if !found {
		return nil
	}
	return value[metadata.IngressConfigHashAnnotationKey]
}

func TestStampIngressConfig(t *testing.T) {
	hash := ingressConfigHash(`[{"name":"alpha"}]`)

	notebookController := unstructuredDeployment("odh-notebook-controller-manager", map[string]string{
		metadata.ComponentNameLabelKey: "odh-notebook-controller",
	})
	kfNotebookController := unstructuredDeployment("kf-notebook-controller-manager", map[string]string{
		metadata.ComponentNameLabelKey: "kf-notebook-controller",
	})
	otherComponent := unstructuredDeployment("webhook", map[string]string{
		metadata.ComponentNameLabelKey: "workbenches-webhook",
	})
	configMap := &unstructured.Unstructured{}
	configMap.SetAPIVersion("v1")
	configMap.SetKind("ConfigMap")
	configMap.SetName("cfg")

	objects := []*unstructured.Unstructured{notebookController, kfNotebookController, otherComponent, configMap}

	if err := stampIngressConfig(objects, hash); err != nil {
		t.Fatalf("stampIngressConfig() error = %v", err)
	}

	for _, obj := range []*unstructured.Unstructured{notebookController, kfNotebookController} {
		if got := podTemplateAnnotation(t, obj); got != hash {
			t.Fatalf("deployment %s stamp = %v, want %s", obj.GetName(), got, hash)
		}
	}
	for _, obj := range []*unstructured.Unstructured{otherComponent} {
		if got := podTemplateAnnotation(t, obj); got != nil {
			t.Fatalf("deployment %s stamp = %v, want unset", obj.GetName(), got)
		}
	}

	// An empty hash leaves annotations untouched: the stale stamp is inert
	// (the rollout gate only evaluates it against the hash in the live
	// ConfigMap), and a null write would coerce to "" on the typed map.
	if err := stampIngressConfig(objects, ""); err != nil {
		t.Fatalf("stampIngressConfig() error = %v", err)
	}
	if got := podTemplateAnnotation(t, notebookController); got != hash {
		t.Fatalf("deployment stamp after empty hash = %v, want unchanged", got)
	}
}

func TestIngressConfigRolloutState(t *testing.T) {
	ctx := context.Background()
	wb := ingressConfigWorkbenches()

	delivered := []componentsv1alpha1.Ingress{
		{Name: "alpha", GatewayName: "alpha", GatewayNamespace: "opendatahub", Hostname: "alpha.example.com"},
	}
	raw, err := json.Marshal(delivered)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	deliveredHash := ingressConfigHash(string(raw))

	stampedHash := deliveredHash
	rolled := func(component string, generation int64) rolloutGateDeploymentSpec {
		return rolloutGateDeploymentSpec{
			name: component + "-manager", component: component,
			replicas: &replicasOne, stamp: &stampedHash,
			generation: generation, observed: generation,
			updated: 1, ready: 1, total: 1,
		}
	}

	state := func(t *testing.T, objects []runtime.Object) (string, int64, string) {
		t.Helper()
		r := newIngressConfigReconciler(objects...)
		return r.ingressConfigRolloutState(ctx, wb)
	}

	t.Run("no ConfigMap means nothing delivered yet", func(t *testing.T) {
		_, _, msg := state(t, nil)
		if !strings.Contains(msg, "no ingress configuration delivered") {
			t.Fatalf("message = %q, want nothing delivered", msg)
		}
	})

	t.Run("ConfigMap without a hash key means nothing delivered yet", func(t *testing.T) {
		legacy := ingressConfigConfigMap(map[string]string{IngressesConfigMapKey: string(raw)})
		_, _, msg := state(t, []runtime.Object{legacy})
		if !strings.Contains(msg, "no ingress configuration delivered") {
			t.Fatalf("message = %q, want nothing delivered", msg)
		}
	})

	t.Run("deployments not stamped with the delivered hash are pending", func(t *testing.T) {
		stale := "deadbeef"
		objects := []runtime.Object{
			deliveredConfigMap(delivered...),
			stampedDeployment("odh-notebook-controller-manager", "odh-notebook-controller", 2, stale).build(),
			stampedDeployment("kf-notebook-controller-manager", "kf-notebook-controller", 2, stale).build(),
		}
		_, _, msg := state(t, objects)
		if !strings.Contains(msg, "not yet carrying the delivered ingress configuration") {
			t.Fatalf("message = %q, want stamp mismatch", msg)
		}
	})

	t.Run("scaled to zero", func(t *testing.T) {
		objects := []runtime.Object{
			deliveredConfigMap(delivered...),
			rolloutGateDeploymentSpec{name: "odh-notebook-controller-manager", component: "odh-notebook-controller",
				replicas: &replicasZero, stamp: &stampedHash, generation: 1, observed: 1}.build(),
			rolled("kf-notebook-controller", 1).build(),
		}
		_, _, msg := state(t, objects)
		if !strings.Contains(msg, "scaled to zero") {
			t.Fatalf("message = %q, want scaled to zero", msg)
		}
	})

	t.Run("observed generation lag", func(t *testing.T) {
		objects := []runtime.Object{
			deliveredConfigMap(delivered...),
			rolloutGateDeploymentSpec{name: "odh-notebook-controller-manager", component: "odh-notebook-controller",
				replicas: &replicasOne, stamp: &stampedHash, generation: 3, observed: 2, updated: 1, ready: 1, total: 1}.build(),
			rolled("kf-notebook-controller", 1).build(),
		}
		_, _, msg := state(t, objects)
		if !strings.Contains(msg, "not observed by controller") {
			t.Fatalf("message = %q, want observed generation lag", msg)
		}
	})

	t.Run("surge rollout with total above desired", func(t *testing.T) {
		objects := []runtime.Object{
			deliveredConfigMap(delivered...),
			rolloutGateDeploymentSpec{name: "odh-notebook-controller-manager", component: "odh-notebook-controller",
				replicas: &replicasOne, stamp: &stampedHash, generation: 3, observed: 3, updated: 1, ready: 1, total: 2}.build(),
			rolled("kf-notebook-controller", 1).build(),
		}
		_, _, msg := state(t, objects)
		if !strings.Contains(msg, "rollout in progress") {
			t.Fatalf("message = %q, want rollout in progress", msg)
		}
	})

	t.Run("unrelated component deployments are ignored", func(t *testing.T) {
		objects := []runtime.Object{
			deliveredConfigMap(delivered...),
			rolled("odh-notebook-controller", 4).build(),
			rolled("kf-notebook-controller", 7).build(),
			rolloutGateDeploymentSpec{name: "model-mesh", component: "model-mesh", replicas: &replicasZero,
				generation: 2, observed: 2}.build(),
		}
		hash, generation, msg := state(t, objects)
		if msg != "" {
			t.Fatalf("message = %q, want empty", msg)
		}
		if hash != deliveredHash {
			t.Fatalf("hash = %q, want the delivered hash", hash)
		}
		if generation != 7 {
			t.Fatalf("generation = %d, want 7", generation)
		}
	})

	t.Run("missing notebook controller deployment is pending", func(t *testing.T) {
		objects := []runtime.Object{
			deliveredConfigMap(delivered...),
			rolled("odh-notebook-controller", 4).build(),
		}
		_, _, msg := state(t, objects)
		if !strings.Contains(msg, "no notebook controller deployment found for component kf-notebook-controller") {
			t.Fatalf("message = %q, want missing component", msg)
		}
	})

	t.Run("list failures surface as pending, not errors", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		_ = componentsv1alpha1.AddToScheme(scheme)
		r := &WorkbenchesReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						return errors.New("api server unreachable")
					},
				}).Build(),
			Scheme: scheme,
		}

		_, _, msg := r.ingressConfigRolloutState(ctx, wb)
		// The ConfigMap read succeeds via the fake client's Get; the
		// deployment list fails and must surface as a pending message.
		if msg == "" {
			t.Fatal("message empty, want pending message")
		}
	})
}

func TestGateIngressesForDelivery(t *testing.T) {
	ctx := context.Background()
	wb := ingressConfigWorkbenches()

	desired := []componentsv1alpha1.Ingress{
		{Name: "alpha", GatewayName: "gateway-b", GatewayNamespace: "opendatahub", Hostname: "alpha.example.com"},
		{Name: "beta", GatewayName: "beta", GatewayNamespace: "opendatahub", Hostname: "beta.example.com"},
	}
	delivered := []componentsv1alpha1.Ingress{
		{Name: "alpha", GatewayName: "gateway-a", GatewayNamespace: "opendatahub"},
	}

	rolledDeployments := func(hash string) []runtime.Object {
		return []runtime.Object{
			stampedDeployment("odh-notebook-controller-manager", "odh-notebook-controller", 4, hash).build(),
			stampedDeployment("kf-notebook-controller-manager", "kf-notebook-controller", 3, hash).build(),
		}
	}

	gate := func(t *testing.T, objects []runtime.Object) []componentsv1alpha1.Ingress {
		t.Helper()
		r := newIngressConfigReconciler(objects...)
		gated, err := r.gateIngressesForDelivery(ctx, wb, desired)
		if err != nil {
			t.Fatalf("gateIngressesForDelivery() error = %v", err)
		}
		return gated
	}

	names := func(ingresses []componentsv1alpha1.Ingress) []string {
		out := make([]string, 0, len(ingresses))
		for _, ingress := range ingresses {
			out = append(out, ingress.Name)
		}
		return out
	}

	deliveredHash := func() string {
		raw, err := json.Marshal(delivered)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return ingressConfigHash(string(raw))
	}

	t.Run("completed rollout renders the spec verbatim", func(t *testing.T) {
		gated := gate(t, append(rolledDeployments(deliveredHash()), deliveredConfigMap(delivered...)))
		if len(gated) != len(desired) {
			t.Fatalf("gated = %v, want %d entries", names(gated), len(desired))
		}
	})

	t.Run("pending rollout keeps delivered records and withholds new ones", func(t *testing.T) {
		// Deployments carry a stale hash: the rollout of the currently
		// delivered content has not completed, so nothing new flows and the
		// delivered record survives verbatim (old gateway, not the desired
		// gatewayB).
		stale := "deadbeef"
		gated := gate(t, append(rolledDeployments(stale), deliveredConfigMap(delivered...)))
		if got := names(gated); !slices.Equal(got, []string{"alpha"}) {
			t.Fatalf("gated = %v, want [alpha]", got)
		}
		if gated[0].GatewayName != "gateway-a" {
			t.Fatalf("gated gateway = %q, want the delivered gateway-a", gated[0].GatewayName)
		}
	})

	t.Run("pending rollout with no ConfigMap withholds everything", func(t *testing.T) {
		stale := "deadbeef"
		gated := gate(t, rolledDeployments(stale))
		if len(gated) != 0 {
			t.Fatalf("gated = %v, want empty", names(gated))
		}
	})

	t.Run("malformed ConfigMap data withholds new assignments", func(t *testing.T) {
		malformed := ingressConfigConfigMap(map[string]string{
			IngressesConfigMapKey:                        `[{"name": `,
			componentsv1alpha1.IngressesHashConfigMapKey: "corrupted",
		})
		gated := gate(t, append(rolledDeployments("deadbeef"), malformed))
		if len(gated) != 0 {
			t.Fatalf("gated = %v, want empty", names(gated))
		}
	})

	t.Run("incomplete or schema-invalid delivered records do not satisfy the gate", func(t *testing.T) {
		raw := `[{"name":"alpha"},` +
			`{"name":"beta","gatewayName":"beta","gatewayNamespace":"opendatahub"},` +
			`{"name":"gamma","gatewayName":"Invalid!!","gatewayNamespace":"opendatahub"}]`
		incomplete := ingressConfigConfigMap(map[string]string{
			IngressesConfigMapKey:                        raw,
			componentsv1alpha1.IngressesHashConfigMapKey: "corrupted",
		})
		gated := gate(t, append(rolledDeployments("deadbeef"), incomplete))
		if got := names(gated); !slices.Equal(got, []string{"beta"}) {
			t.Fatalf("gated = %v, want [beta] (only complete, schema-valid records count as delivered)", got)
		}
	})

	t.Run("delivered state is read through the API reader when set", func(t *testing.T) {
		// The cached client is empty (no ConfigMap): only the API reader
		// knows about the delivered ingress, so the gate keeps it.
		apiReader := newIngressConfigReconciler(deliveredConfigMap(delivered...))
		r := newIngressConfigReconciler(rolledDeployments("stale")...)
		r.APIReader = apiReader.Client

		gated, err := r.gateIngressesForDelivery(ctx, wb, desired)
		if err != nil {
			t.Fatalf("gateIngressesForDelivery() error = %v", err)
		}
		if got := names(gated); !slices.Equal(got, []string{"alpha"}) {
			t.Fatalf("gated = %v, want [alpha] read through the API reader", got)
		}
	})
}

func TestSyncIngressConfigStatus(t *testing.T) {
	ctx := context.Background()
	wb := ingressConfigWorkbenches()
	wb.Generation = 3

	delivered := []componentsv1alpha1.Ingress{{Name: "alpha", GatewayName: "alpha", GatewayNamespace: "opendatahub"}}
	raw, err := json.Marshal(delivered)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	hash := ingressConfigHash(string(raw))
	stamped := hash

	rolled := []runtime.Object{
		stampedDeployment("odh-notebook-controller-manager", "odh-notebook-controller", 4, stamped).build(),
		stampedDeployment("kf-notebook-controller-manager", "kf-notebook-controller", 7, stamped).build(),
	}

	t.Run("pending rollout clears evidence and reports RolloutPending", func(t *testing.T) {
		r := newIngressConfigReconciler(rolled...)
		wb := ingressConfigWorkbenches()
		r.syncIngressConfigStatus(ctx, wb)

		cond := meta.FindStatusCondition(wb.Status.Conditions, conditionTypeIngressConfigReady)
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "IngressConfigRolloutPending" {
			t.Fatalf("condition = %+v, want False/IngressConfigRolloutPending", cond)
		}
		if wb.Status.IngressConfig != (componentsv1alpha1.IngressConfigStatus{}) {
			t.Fatalf("status = %+v, want cleared", wb.Status.IngressConfig)
		}
	})

	t.Run("completed rollout reports the hash and deployment generation", func(t *testing.T) {
		r := newIngressConfigReconciler(append(rolled, deliveredConfigMap(delivered...))...)
		wb := ingressConfigWorkbenches()
		r.syncIngressConfigStatus(ctx, wb)

		cond := meta.FindStatusCondition(wb.Status.Conditions, conditionTypeIngressConfigReady)
		if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != "IngressConfigReady" {
			t.Fatalf("condition = %+v, want True/IngressConfigReady", cond)
		}
		if wb.Status.IngressConfig.Hash != hash {
			t.Fatalf("hash = %q, want %q", wb.Status.IngressConfig.Hash, hash)
		}
		if wb.Status.IngressConfig.DeploymentGeneration != 7 {
			t.Fatalf("deploymentGeneration = %d, want 7", wb.Status.IngressConfig.DeploymentGeneration)
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
		Hash:                 "abc123",
		DeploymentGeneration: 4,
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
