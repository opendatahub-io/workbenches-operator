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

package e2e

import (
	"time"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
	metadata "github.com/opendatahub-io/workbenches-operator/internal/metadata"
)

const (
	timeout  = 3 * time.Minute
	interval = 5 * time.Second

	webhookTimeout  = 2 * time.Minute
	webhookInterval = 3 * time.Second

	defaultTestWorkbenchNamespace = "e2e-test-notebooks"
	webhookTestNamespace          = "e2e-webhook-test"
)

// workbenchNamespace holds the actual workbench namespace used by the CR.
// It is set during the lifecycle test after the CR is created or fetched.
var workbenchNamespace string

func workbenchesCR() *componentsv1alpha1.Workbenches {
	return &componentsv1alpha1.Workbenches{
		ObjectMeta: metav1.ObjectMeta{
			Name: componentsv1alpha1.WorkbenchesInstanceName,
		},
		Spec: componentsv1alpha1.WorkbenchesSpec{
			ManagementState:    "Managed",
			WorkbenchNamespace: defaultTestWorkbenchNamespace,
			Platform:           "OpenDataHub",
		},
	}
}

func getWorkbenches() *componentsv1alpha1.Workbenches {
	wb := &componentsv1alpha1.Workbenches{}
	ExpectWithOffset(1, k8sClient.Get(ctx, types.NamespacedName{
		Name: componentsv1alpha1.WorkbenchesInstanceName,
	}, wb)).To(Succeed())

	return wb
}

// updateWorkbenchesSpec re-fetches the CR and applies the mutator to avoid
// conflicts with the controller's concurrent status updates.
func updateWorkbenchesSpec(mutate func(*componentsv1alpha1.Workbenches)) {
	EventuallyWithOffset(1, func() error {
		wb := &componentsv1alpha1.Workbenches{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name: componentsv1alpha1.WorkbenchesInstanceName,
		}, wb); err != nil {
			return err
		}

		mutate(wb)

		return k8sClient.Update(ctx, wb)
	}, 30*time.Second, 1*time.Second).Should(Succeed())
}

func waitForCondition(condType string, status metav1.ConditionStatus) {
	EventuallyWithOffset(1, func() metav1.ConditionStatus {
		wb := &componentsv1alpha1.Workbenches{}

		err := k8sClient.Get(ctx, types.NamespacedName{
			Name: componentsv1alpha1.WorkbenchesInstanceName,
		}, wb)
		if err != nil {
			return ""
		}

		cond := meta.FindStatusCondition(wb.Status.Conditions, condType)
		if cond == nil {
			return ""
		}

		return cond.Status
	}, timeout, interval).Should(Equal(status),
		"condition %s should be %s", condType, status)
}

func waitForPhase(phase string) {
	EventuallyWithOffset(1, func() string {
		wb := &componentsv1alpha1.Workbenches{}

		err := k8sClient.Get(ctx, types.NamespacedName{
			Name: componentsv1alpha1.WorkbenchesInstanceName,
		}, wb)
		if err != nil {
			return ""
		}

		return wb.Status.Phase
	}, timeout, interval).Should(Equal(phase), "phase should be %s", phase)
}

func newNotebook(name string, annotations map[string]string) *unstructured.Unstructured {
	nb := &unstructured.Unstructured{}
	nb.SetGroupVersionKind(notebookGVK())
	nb.SetName(name)
	nb.SetNamespace(webhookTestNamespace)

	if annotations != nil {
		nb.SetAnnotations(annotations)
	}

	_ = unstructured.SetNestedField(nb.Object, map[string]any{
		"spec": map[string]any{
			"containers": []any{
				map[string]any{
					"name":  name,
					"image": "jupyter/minimal-notebook:latest",
				},
			},
		},
	}, "spec", "template")

	return nb
}

func notebookGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "kubeflow.org",
		Version: "v1",
		Kind:    "Notebook",
	}
}

// expectDriftRecovery deletes the first component-labeled object from list and
// waits for the operator to recreate it with a new UID.
func expectDriftRecovery(
	kind string,
	list client.ObjectList,
	firstItem func() client.Object,
	newObj func() client.Object,
) {
	ExpectWithOffset(1, workbenchNamespace).NotTo(BeEmpty())

	componentLabels := client.MatchingLabels{
		metadata.ComponentLabelKey: metadata.LabelTrue,
	}

	ExpectWithOffset(1, k8sClient.List(ctx, list,
		client.InNamespace(workbenchNamespace),
		componentLabels,
	)).To(Succeed())

	target := firstItem()
	ExpectWithOffset(1, target).NotTo(BeNil(),
		"at least one labeled %s should exist before drift test", kind)

	deletedUID := target.GetUID()
	ExpectWithOffset(1, k8sClient.Delete(ctx, target)).To(Succeed())

	EventuallyWithOffset(1, func(g Gomega) {
		fresh := newObj()
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      target.GetName(),
			Namespace: target.GetNamespace(),
		}, fresh)).To(Succeed())
		g.Expect(fresh.GetUID()).NotTo(Equal(deletedUID),
			"recreated %s should have a new UID", kind)
	}, timeout, interval).Should(Succeed(),
		"operator should recreate the deleted %s", kind)
}
