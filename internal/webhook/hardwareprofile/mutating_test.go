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

package hardwareprofile_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/opendatahub-io/workbenches-operator/internal/gvk"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
	"github.com/opendatahub-io/workbenches-operator/internal/webhook/hardwareprofile"
)

const (
	testNamespace       = "test-ns"
	hwpNamespace        = "hwp-ns"
	testNotebook        = "test-notebook"
	testHardwareProfile = "test-hardware-profile"
)

// --- Test helpers ---

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	NewWithT(t).Expect(corev1.AddToScheme(s)).ShouldNot(HaveOccurred())

	return s
}

func createInjector(t *testing.T, s *runtime.Scheme, objects ...runtime.Object) *hardwareprofile.Injector {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(s)

	for _, obj := range objects {
		builder = builder.WithRuntimeObjects(obj)
	}

	cli := builder.Build()

	return &hardwareprofile.Injector{
		Client:  cli,
		Decoder: admission.NewDecoder(s),
		Name:    "test",
	}
}

func newNotebook(annotations map[string]string) *unstructured.Unstructured {
	nb := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": gvk.Notebook.Group + "/" + gvk.Notebook.Version,
			"kind":       gvk.Notebook.Kind,
			"metadata": map[string]any{
				"name":      testNotebook,
				"namespace": testNamespace,
			},
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name":  "notebook",
								"image": "jupyter/base-notebook:latest",
							},
						},
					},
				},
			},
		},
	}

	if len(annotations) > 0 {
		nb.SetAnnotations(annotations)
	}

	return nb
}

func hwpAnnotations(profileName string) map[string]string {
	return map[string]string{metadata.HardwareProfileNameAnnotation: profileName}
}

func hwpAnnotationsWithNamespace(profileName, ns string) map[string]string {
	return map[string]string{
		metadata.HardwareProfileNameAnnotation:      profileName,
		metadata.HardwareProfileNamespaceAnnotation: ns,
	}
}

func newHWP(name, namespace string, identifiers []any, nodeSelector map[string]any, tolerations []any) *unstructured.Unstructured {
	spec := map[string]any{}

	if nodeSelector != nil || tolerations != nil {
		node := map[string]any{}
		if nodeSelector != nil {
			node["nodeSelector"] = nodeSelector
		}

		if tolerations != nil {
			node["tolerations"] = tolerations
		}

		spec["scheduling"] = map[string]any{
			"type": "Node",
			"node": node,
		}
	}

	if identifiers != nil {
		spec["identifiers"] = identifiers
	}

	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": gvk.HardwareProfile.Group + "/" + gvk.HardwareProfile.Version,
			"kind":       gvk.HardwareProfile.Kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
}

func newKueueHWP(name, queueName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": gvk.HardwareProfile.Group + "/" + gvk.HardwareProfile.Version,
			"kind":       gvk.HardwareProfile.Kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": testNamespace,
			},
			"spec": map[string]any{
				"scheduling": map[string]any{
					"kueue": map[string]any{
						"localQueueName": queueName,
					},
				},
			},
		},
	}
}

func cpuIdentifier(minCount, defaultCount string, maxCount ...string) map[string]any {
	id := map[string]any{
		"displayName":  "CPU",
		"identifier":   "cpu",
		"minCount":     minCount,
		"defaultCount": defaultCount,
		"resourceType": "CPU",
	}

	if len(maxCount) > 0 {
		id["maxCount"] = maxCount[0]
	}

	return id
}

func memoryIdentifier(minCount, defaultCount string, maxCount ...string) map[string]any {
	id := map[string]any{
		"displayName":  "Memory",
		"identifier":   "memory",
		"minCount":     minCount,
		"defaultCount": defaultCount,
		"resourceType": "Memory",
	}

	if len(maxCount) > 0 {
		id["maxCount"] = maxCount[0]
	}

	return id
}

func gpuIdentifier(minCount, defaultCount string, maxCount ...string) map[string]any {
	id := map[string]any{
		"displayName":  "GPU",
		"identifier":   "nvidia.com/gpu",
		"minCount":     minCount,
		"defaultCount": defaultCount,
		"resourceType": "Accelerator",
	}

	if len(maxCount) > 0 {
		id["maxCount"] = maxCount[0]
	}

	return id
}

func newAdmissionRequest(t *testing.T, op admissionv1.Operation, obj *unstructured.Unstructured, kind schema.GroupVersionKind) admission.Request {
	t.Helper()

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:  "test-uid",
			Kind: metav1.GroupVersionKind{Group: kind.Group, Version: kind.Version, Kind: kind.Kind},
			Resource: metav1.GroupVersionResource{
				Group:    kind.Group,
				Version:  kind.Version,
				Resource: "notebooks",
			},
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
			Operation: op,
		},
	}

	if op != admissionv1.Delete {
		objBytes, err := json.Marshal(obj)
		if err != nil {
			t.Fatalf("failed to marshal object: %v", err)
		}

		req.Object = runtime.RawExtension{Raw: objBytes}
	}

	return req
}

func newUpdateAdmissionRequest(t *testing.T, newObj, oldObj *unstructured.Unstructured) admission.Request {
	t.Helper()

	newBytes, err := json.Marshal(newObj)
	if err != nil {
		t.Fatalf("failed to marshal new object: %v", err)
	}

	oldBytes, err := json.Marshal(oldObj)
	if err != nil {
		t.Fatalf("failed to marshal old object: %v", err)
	}

	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:  "test-uid",
			Kind: metav1.GroupVersionKind{Group: gvk.Notebook.Group, Version: gvk.Notebook.Version, Kind: gvk.Notebook.Kind},
			Resource: metav1.GroupVersionResource{
				Group:    gvk.Notebook.Group,
				Version:  gvk.Notebook.Version,
				Resource: "notebooks",
			},
			Name:      newObj.GetName(),
			Namespace: newObj.GetNamespace(),
			Operation: admissionv1.Update,
			Object:    runtime.RawExtension{Raw: newBytes},
			OldObject: runtime.RawExtension{Raw: oldBytes},
		},
	}
}

func hasResourcePatches(patches []jsonpatch.JsonPatchOperation) bool {
	for _, patch := range patches {
		if strings.Contains(patch.Path, "/resources") {
			return true
		}
	}

	return false
}

// --- Tests ---

func TestHardwareProfile_AllowsRequests(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)
	ctx := t.Context()

	testCases := []struct {
		name      string
		operation admissionv1.Operation
		notebook  *unstructured.Unstructured
	}{
		{
			name:      "requests without hardware profile annotation",
			operation: admissionv1.Create,
			notebook:  newNotebook(nil),
		},
		{
			name:      "unsupported operations (DELETE)",
			operation: admissionv1.Delete,
			notebook:  newNotebook(hwpAnnotations(testHardwareProfile)),
		},
		{
			name:      "empty hardware profile annotation value",
			operation: admissionv1.Create,
			notebook:  newNotebook(map[string]string{metadata.HardwareProfileNameAnnotation: ""}),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			injector := createInjector(t, s)
			req := newAdmissionRequest(t, tc.operation, tc.notebook, gvk.Notebook)
			resp := injector.Handle(ctx, req)
			g.Expect(resp.Allowed).Should(BeTrue())
			g.Expect(resp.Patches).Should(BeEmpty())
		})
	}
}

func TestHardwareProfile_DeniesWhenDecoderNotInitialized(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	injector := &hardwareprofile.Injector{
		Name: "test-injector",
	}

	req := newAdmissionRequest(t, admissionv1.Create, newNotebook(nil), gvk.Notebook)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeFalse())
	g.Expect(resp.Result.Message).Should(ContainSubstring("webhook decoder not initialized"))
}

func TestHardwareProfile_DeniesWhenProfileNotFound(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	injector := createInjector(t, s)
	req := newAdmissionRequest(t, admissionv1.Create, newNotebook(hwpAnnotations("nonexistent")), gvk.Notebook)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeFalse())
	g.Expect(resp.Result.Message).Should(ContainSubstring("hardware profile 'nonexistent' not found in namespace 'test-ns'"))
}

func TestHardwareProfile_SetsNamespaceAnnotation(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, testNamespace, []any{
		map[string]any{
			"displayName":  "Test Resource",
			"identifier":   "test.com/resource",
			"minCount":     "1",
			"defaultCount": "1",
		},
	}, nil, nil)

	injector := createInjector(t, s, hwp)
	req := newAdmissionRequest(t, admissionv1.Create, newNotebook(hwpAnnotations(testHardwareProfile)), gvk.Notebook)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Patches).Should(Not(BeEmpty()))
}

func TestHardwareProfile_HandlesUpdateOperations(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, testNamespace, []any{
		memoryIdentifier("4Gi", "4Gi"),
	}, nil, nil)

	injector := createInjector(t, s, hwp)
	req := newAdmissionRequest(t, admissionv1.Update, newNotebook(hwpAnnotations(testHardwareProfile)), gvk.Notebook)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Patches).Should(Not(BeEmpty()))
}

func TestHardwareProfile_ErrorPaths(t *testing.T) {
	t.Parallel()
	s := newScheme(t)

	testCases := []struct {
		name          string
		injector      *hardwareprofile.Injector
		workload      *unstructured.Unstructured
		kind          schema.GroupVersionKind
		expectAllowed bool
		expectMessage string
	}{
		{
			name: "nil decoder",
			injector: &hardwareprofile.Injector{
				Client: fake.NewClientBuilder().WithScheme(s).Build(),
				Name:   "test",
			},
			workload:      newNotebook(nil),
			kind:          gvk.Notebook,
			expectAllowed: false,
			expectMessage: "webhook decoder not initialized",
		},
		{
			name:     "missing hardware profile namespace",
			injector: createInjector(t, s),
			workload: func() *unstructured.Unstructured {
				nb := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": gvk.Notebook.Group + "/" + gvk.Notebook.Version,
					"kind":       gvk.Notebook.Kind,
					"metadata": map[string]any{
						"name": testNotebook,
					},
				}}
				nb.SetAnnotations(map[string]string{
					metadata.HardwareProfileNameAnnotation: testHardwareProfile,
				})

				return nb
			}(),
			kind:          gvk.Notebook,
			expectAllowed: false,
			expectMessage: "unable to determine hardware profile namespace",
		},
		{
			name:          "hardware profile not found",
			injector:      createInjector(t, s),
			workload:      newNotebook(hwpAnnotations("non-existent")),
			kind:          gvk.Notebook,
			expectAllowed: false,
			expectMessage: "hardware profile 'non-existent' not found in namespace 'test-ns'",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			req := newAdmissionRequest(t, admissionv1.Create, tc.workload, tc.kind)
			resp := tc.injector.Handle(t.Context(), req)
			g.Expect(resp.Allowed).Should(Equal(tc.expectAllowed))
			if tc.expectMessage != "" {
				g.Expect(resp.Result.Message).Should(ContainSubstring(tc.expectMessage))
			}
		})
	}
}

func TestHardwareProfile_NotFoundOnUpdateWarnsAndAllows(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	// No HWP registered — simulates a deleted profile
	injector := createInjector(t, s)

	// Old notebook already had "deleted-profile" — it's a stale reference
	nb := newNotebook(hwpAnnotations("deleted-profile"))
	oldNB := newNotebook(hwpAnnotations("deleted-profile"))

	req := newUpdateAdmissionRequest(t, nb, oldNB)
	resp := injector.Handle(t.Context(), req)

	// UPDATE with stale reference should be allowed with a warning
	g.Expect(resp.Allowed).Should(BeTrue(),
		"UPDATE with a stale HWP reference should be allowed")
	g.Expect(resp.Result.Message).Should(ContainSubstring("not found"))
	g.Expect(resp.Warnings).ShouldNot(BeEmpty(),
		"Should include a warning about the missing profile")
	g.Expect(resp.Warnings[0]).Should(ContainSubstring("deleted-profile"))
}

func TestHardwareProfile_NotFoundOnUpdateDeniesNewProfile(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	// No HWP "nonexistent-profile" registered
	injector := createInjector(t, s)

	// User is switching FROM a valid profile TO a non-existent one
	nb := newNotebook(hwpAnnotations("nonexistent-profile"))
	oldNB := newNotebook(hwpAnnotations("original-profile"))

	req := newUpdateAdmissionRequest(t, nb, oldNB)
	resp := injector.Handle(t.Context(), req)

	// Should be denied — the user is actively setting a non-existent profile
	g.Expect(resp.Allowed).Should(BeFalse(),
		"UPDATE switching to a non-existent HWP should be denied")
	g.Expect(resp.Result.Message).Should(ContainSubstring("not found"))
}

func TestHardwareProfile_SchedulingConfiguration_Notebook(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)
	ctx := t.Context()

	testCases := []struct {
		name          string
		hwp           *unstructured.Unstructured
		setupWorkload func() *unstructured.Unstructured
		expectPatches bool
	}{
		{
			name: "applies node scheduling to clean workload",
			hwp: newHWP(testHardwareProfile, testNamespace,
				[]any{cpuIdentifier("2", "2")},
				map[string]any{"node-type": "gpu-node"},
				[]any{map[string]any{
					"key":      "nvidia.com/gpu",
					"operator": "Equal",
					"value":    "true",
					"effect":   "NoSchedule",
				}},
			),
			setupWorkload: func() *unstructured.Unstructured {
				return newNotebook(hwpAnnotations(testHardwareProfile))
			},
			expectPatches: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			injector := createInjector(t, s, tc.hwp)
			workload := tc.setupWorkload()
			req := newAdmissionRequest(t, admissionv1.Create, workload, gvk.Notebook)
			resp := injector.Handle(ctx, req)
			g.Expect(resp.Allowed).Should(BeTrue())
			g.Expect(resp.Patches).Should(Not(BeEmpty()))
		})
	}
}

func TestHardwareProfile_ResourceInjection_Notebook(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)
	ctx := t.Context()

	hwp := newHWP(testHardwareProfile, testNamespace, []any{
		cpuIdentifier("0", "4", "8"),
		memoryIdentifier("0", "8Gi", "16Gi"),
	}, nil, nil)

	injector := createInjector(t, s, hwp)

	testCases := []struct {
		name                string
		setupWorkload       func() *unstructured.Unstructured
		expectResourcePatch bool
	}{
		{
			name: "applies resources when none exist",
			setupWorkload: func() *unstructured.Unstructured {
				return newNotebook(hwpAnnotations(testHardwareProfile))
			},
			expectResourcePatch: true,
		},
		{
			name: "preserves existing resources",
			setupWorkload: func() *unstructured.Unstructured {
				nb := newNotebook(hwpAnnotations(testHardwareProfile))
				containers, _, _ := unstructured.NestedSlice(nb.Object, "spec", "template", "spec", "containers")
				containerMap, _ := containers[0].(map[string]any)
				containerMap["resources"] = map[string]any{
					"requests": map[string]any{"cpu": "2", "memory": "4Gi"},
					"limits":   map[string]any{"cpu": "4", "memory": "8Gi"},
				}
				_ = unstructured.SetNestedSlice(nb.Object, containers, "spec", "template", "spec", "containers")

				return nb
			},
			expectResourcePatch: false,
		},
		{
			name: "applies only missing resources when single container has partial resources",
			setupWorkload: func() *unstructured.Unstructured {
				nb := newNotebook(hwpAnnotations(testHardwareProfile))
				containers, _, _ := unstructured.NestedSlice(nb.Object, "spec", "template", "spec", "containers")
				containerMap, _ := containers[0].(map[string]any)
				containerMap["resources"] = map[string]any{
					"requests": map[string]any{"cpu": "1"},
				}
				_ = unstructured.SetNestedSlice(nb.Object, containers, "spec", "template", "spec", "containers")

				return nb
			},
			expectResourcePatch: true,
		},
		{
			name: "applies resources only to main container when multiple containers exist",
			setupWorkload: func() *unstructured.Unstructured {
				nb := newNotebook(hwpAnnotations(testHardwareProfile))
				containers := []any{
					map[string]any{
						"name":  testNotebook,
						"image": "notebook:latest",
						"resources": map[string]any{
							"requests": map[string]any{"cpu": "1"},
						},
					},
					map[string]any{
						"name":  "oauth-proxy",
						"image": "oauth-proxy:latest",
						"resources": map[string]any{
							"requests": map[string]any{"memory": "64Mi", "cpu": "10m"},
						},
					},
				}
				_ = unstructured.SetNestedSlice(nb.Object, containers, "spec", "template", "spec", "containers")

				return nb
			},
			expectResourcePatch: true,
		},
		{
			name: "multiple containers but none match Notebook name; admits with warning",
			setupWorkload: func() *unstructured.Unstructured {
				nb := newNotebook(hwpAnnotations(testHardwareProfile))
				containers := []any{
					map[string]any{"name": "container-a", "image": "notebook:latest", "resources": map[string]any{}},
					map[string]any{"name": "container-b", "image": "oauth-proxy:latest", "resources": map[string]any{}},
				}
				_ = unstructured.SetNestedSlice(nb.Object, containers, "spec", "template", "spec", "containers")

				return nb
			},
			expectResourcePatch: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			workload := tc.setupWorkload()
			req := newAdmissionRequest(t, admissionv1.Create, workload, gvk.Notebook)
			resp := injector.Handle(ctx, req)
			g.Expect(resp.Allowed).Should(BeTrue(), "All requests should be admitted")
			g.Expect(hasResourcePatches(resp.Patches)).Should(Equal(tc.expectResourcePatch))

			if tc.name == "multiple containers but none match Notebook name; admits with warning" {
				g.Expect(resp.Warnings).ShouldNot(BeEmpty(), "Should have warning when no matching container found")
				g.Expect(resp.Warnings[0]).Should(ContainSubstring("was not applied"))
				g.Expect(resp.Warnings[0]).Should(ContainSubstring(testNotebook), "Warning should mention expected container name")
				g.Expect(resp.Warnings[0]).Should(ContainSubstring("All hardware profile settings"), "Warning should indicate all settings skipped")
			}

			if tc.name == "applies resources only to main container when multiple containers exist" {
				for _, patch := range resp.Patches {
					path := patch.Path
					if strings.Contains(path, "/resources") {
						g.Expect(path).Should(Not(ContainSubstring("containers/1")),
							"Sidecar (containers/1) must NOT receive HWP resource patch; path was %s", path)
					}
				}
			}
		})
	}
}

func TestHardwareProfile_Notebook_MainContainerOnly_NoResourcePatchForSidecar(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)
	ctx := t.Context()

	hwp := newHWP(testHardwareProfile, testNamespace, []any{
		cpuIdentifier("1", "2", "4"),
		memoryIdentifier("1Gi", "2Gi", "4Gi"),
		gpuIdentifier("1", "1"),
	}, nil, nil)

	injector := createInjector(t, s, hwp)

	nb := newNotebook(hwpAnnotations(testHardwareProfile))
	containers := []any{
		map[string]any{"name": testNotebook, "image": "notebook:latest", "resources": map[string]any{}},
		map[string]any{
			"name":  "sidecar",
			"image": "busybox:latest",
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "10m", "memory": "32Mi"},
			},
		},
	}
	_ = unstructured.SetNestedSlice(nb.Object, containers, "spec", "template", "spec", "containers")

	req := newAdmissionRequest(t, admissionv1.Create, nb, gvk.Notebook)
	resp := injector.Handle(ctx, req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Patches).Should(Not(BeEmpty()))

	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "/resources") {
			g.Expect(patch.Path).Should(Not(ContainSubstring("containers/1")),
				"sidecar must not get HWP resources; path=%s", patch.Path)
		}
	}

	mainContainerGotResources := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "containers/0") && strings.Contains(patch.Path, "/resources") {
			mainContainerGotResources = true

			break
		}
	}

	if !mainContainerGotResources {
		for _, patch := range resp.Patches {
			if strings.HasSuffix(patch.Path, "/containers") && patch.Value != nil {
				if arr, ok := patch.Value.([]any); ok && len(arr) >= 2 {
					c0, _ := arr[0].(map[string]any)
					if c0 != nil {
						if res, _ := c0["resources"].(map[string]any); res != nil {
							if req, _ := res["requests"].(map[string]any); req != nil && req["nvidia.com/gpu"] != nil {
								mainContainerGotResources = true
							}
						}
					}

					break
				}
			}
		}
	}

	g.Expect(mainContainerGotResources).Should(BeTrue(), "Main container (containers/0) should receive HWP resource patch")
}

func TestHardwareProfile_SupportsCrossNamespaceAccess_Notebook(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, hwpNamespace, []any{
		gpuIdentifier("1", "1"),
	}, nil, nil)

	injector := createInjector(t, s, hwp)

	nb := newNotebook(hwpAnnotationsWithNamespace(testHardwareProfile, hwpNamespace))
	req := newAdmissionRequest(t, admissionv1.Create, nb, gvk.Notebook)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Patches).Should(Not(BeEmpty()))
}

func TestHardwareProfile_ResourceLimits_Notebook(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, testNamespace, []any{
		cpuIdentifier("1", "2", "4"),
		memoryIdentifier("1Gi", "2Gi", "4Gi"),
		gpuIdentifier("1", "1", "1"),
	}, nil, nil)

	injector := createInjector(t, s, hwp)

	nb := newNotebook(hwpAnnotations(testHardwareProfile))
	req := newAdmissionRequest(t, admissionv1.Create, nb, gvk.Notebook)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Patches).Should(Not(BeEmpty()))

	hasResourcesPatch := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "/resources") {
			hasResourcesPatch = true

			if resourcesMap, ok := patch.Value.(map[string]any); ok {
				requests, hasRequests := resourcesMap["requests"].(map[string]any)
				limits, hasLimits := resourcesMap["limits"].(map[string]any)

				g.Expect(hasRequests).Should(BeTrue(), "Resources patch should contain requests")
				g.Expect(hasLimits).Should(BeTrue(), "Resources patch should contain limits")

				g.Expect(requests).Should(HaveKey("cpu"))
				g.Expect(requests).Should(HaveKey("memory"))
				g.Expect(requests).Should(HaveKey("nvidia.com/gpu"))
				g.Expect(requests["cpu"]).Should(Equal("2"), "CPU request should be DefaultCount")
				g.Expect(requests["memory"]).Should(Equal("2Gi"), "Memory request should be DefaultCount")
				g.Expect(requests["nvidia.com/gpu"]).Should(Equal("1"), "GPU request should be DefaultCount")

				g.Expect(limits).Should(HaveKey("cpu"))
				g.Expect(limits).Should(HaveKey("memory"))
				g.Expect(limits).Should(HaveKey("nvidia.com/gpu"))
				g.Expect(limits["cpu"]).Should(Equal("2"), "CPU limit should be DefaultCount for Guaranteed QoS")
				g.Expect(limits["memory"]).Should(Equal("2Gi"), "Memory limit should be DefaultCount for Guaranteed QoS")
				g.Expect(limits["nvidia.com/gpu"]).Should(Equal("1"), "GPU limit should equal request")
			}

			break
		}
	}

	g.Expect(hasResourcesPatch).Should(BeTrue(), "Should have resources patch")
}

func TestHardwareProfile_MixedResourceLimits_Notebook(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, testNamespace, []any{
		cpuIdentifier("1", "4", "8"),
		memoryIdentifier("2Gi", "8Gi", "16Gi"),
		gpuIdentifier("2", "2", "4"),
		map[string]any{
			"displayName":  "Intel QAT",
			"identifier":   "intel.com/qat",
			"minCount":     "1",
			"defaultCount": "1",
			"resourceType": "Accelerator",
		},
		map[string]any{
			"displayName":  "Ephemeral Storage",
			"identifier":   "ephemeral-storage",
			"minCount":     "1Gi",
			"defaultCount": "10Gi",
		},
	}, nil, nil)

	injector := createInjector(t, s, hwp)

	nb := newNotebook(hwpAnnotations(testHardwareProfile))
	req := newAdmissionRequest(t, admissionv1.Create, nb, gvk.Notebook)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Patches).Should(Not(BeEmpty()))

	hasResourcesPatch := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "/resources") {
			hasResourcesPatch = true

			if resourcesMap, ok := patch.Value.(map[string]any); ok {
				requests, hasRequests := resourcesMap["requests"].(map[string]any)
				limits, hasLimits := resourcesMap["limits"].(map[string]any)

				g.Expect(hasRequests).Should(BeTrue(), "Resources patch should contain requests")
				g.Expect(hasLimits).Should(BeTrue(), "Resources patch should contain limits")

				g.Expect(requests["cpu"]).Should(Equal("4"), "CPU request = DefaultCount")
				g.Expect(requests["memory"]).Should(Equal("8Gi"), "Memory request = DefaultCount")
				g.Expect(requests["nvidia.com/gpu"]).Should(Equal("2"), "GPU request = DefaultCount")
				g.Expect(requests["intel.com/qat"]).Should(Equal("1"), "QAT request = DefaultCount")
				g.Expect(requests["ephemeral-storage"]).Should(Equal("10Gi"), "Ephemeral storage request = DefaultCount")

				g.Expect(limits["cpu"]).Should(Equal("4"), "CPU limit should equal DefaultCount, not MaxCount")
				g.Expect(limits["memory"]).Should(Equal("8Gi"), "Memory limit should equal DefaultCount, not MaxCount")
				g.Expect(limits["ephemeral-storage"]).Should(Equal("10Gi"), "Ephemeral-storage limit should equal DefaultCount")
				g.Expect(limits["nvidia.com/gpu"]).Should(Equal("2"), "GPU limit should equal request")
				g.Expect(limits["intel.com/qat"]).Should(Equal("1"), "QAT limit should equal request")
			}

			break
		}
	}

	g.Expect(hasResourcesPatch).Should(BeTrue(), "Should have resources patch")
}

func TestHardwareProfile_ProfileChangeClearsScheduling(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwpWithTolerations := newHWP("hwp-with-tolerations", testNamespace, nil,
		map[string]any{"gpu": "true"},
		[]any{map[string]any{
			"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule",
		}},
	)
	hwpEmpty := newHWP("hwp-empty", testNamespace, nil, nil, nil)

	injector := createInjector(t, s, hwpWithTolerations, hwpEmpty)

	oldNB := newNotebook(hwpAnnotations("hwp-with-tolerations"))
	_ = unstructured.SetNestedSlice(oldNB.Object, []any{
		map[string]any{"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule"},
	}, "spec", "template", "spec", "tolerations")
	_ = unstructured.SetNestedStringMap(oldNB.Object, map[string]string{"gpu": "true"}, "spec", "template", "spec", "nodeSelector")

	newNB := newNotebook(hwpAnnotations("hwp-empty"))
	_ = unstructured.SetNestedSlice(newNB.Object, []any{
		map[string]any{"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule"},
	}, "spec", "template", "spec", "tolerations")
	_ = unstructured.SetNestedStringMap(newNB.Object, map[string]string{"gpu": "true"}, "spec", "template", "spec", "nodeSelector")

	req := newUpdateAdmissionRequest(t, newNB, oldNB)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())

	foundTolerationRemove := false
	foundNodeSelectorRemove := false

	for _, patch := range resp.Patches {
		if patch.Operation == "remove" && strings.Contains(patch.Path, "tolerations") {
			foundTolerationRemove = true
		}

		if patch.Operation == "remove" && strings.Contains(patch.Path, "nodeSelector") {
			foundNodeSelectorRemove = true
		}
	}

	g.Expect(foundTolerationRemove).Should(BeTrue(), "Should have patch to remove tolerations when switching profiles")
	g.Expect(foundNodeSelectorRemove).Should(BeTrue(), "Should have patch to remove nodeSelector when switching profiles")
}

func TestHardwareProfile_ProfileChangeClearsAndReplacesResources(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwpGPU := newHWP("hwp-gpu", testNamespace, []any{
		cpuIdentifier("2", "4"),
		memoryIdentifier("4Gi", "8Gi"),
		gpuIdentifier("1", "1"),
	}, nil, nil)

	hwpCPU := newHWP("hwp-cpu", testNamespace, []any{
		cpuIdentifier("1", "2"),
		memoryIdentifier("1Gi", "2Gi"),
	}, nil, nil)

	injector := createInjector(t, s, hwpGPU, hwpCPU)

	// Old notebook had gpu profile applied with its resources
	oldNB := newNotebook(hwpAnnotations("hwp-gpu"))
	containers, _, _ := unstructured.NestedSlice(oldNB.Object, "spec", "template", "spec", "containers")
	containerMap, _ := containers[0].(map[string]any)
	containerMap["resources"] = map[string]any{
		"requests": map[string]any{"cpu": "4", "memory": "8Gi", "nvidia.com/gpu": "1"},
		"limits":   map[string]any{"cpu": "4", "memory": "8Gi", "nvidia.com/gpu": "1"},
	}
	_ = unstructured.SetNestedSlice(oldNB.Object, containers, "spec", "template", "spec", "containers")

	// New notebook switches to cpu profile but still has old resources in spec
	newNB := newNotebook(hwpAnnotations("hwp-cpu"))
	containers2, _, _ := unstructured.NestedSlice(newNB.Object, "spec", "template", "spec", "containers")
	containerMap2, _ := containers2[0].(map[string]any)
	containerMap2["resources"] = map[string]any{
		"requests": map[string]any{"cpu": "4", "memory": "8Gi", "nvidia.com/gpu": "1"},
		"limits":   map[string]any{"cpu": "4", "memory": "8Gi", "nvidia.com/gpu": "1"},
	}
	_ = unstructured.SetNestedSlice(newNB.Object, containers2, "spec", "template", "spec", "containers")

	req := newUpdateAdmissionRequest(t, newNB, oldNB)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Patches).ShouldNot(BeEmpty())

	// Apply patches to get the final state
	hasResourcesPatch := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "/resources") {
			hasResourcesPatch = true

			if resourcesMap, ok := patch.Value.(map[string]any); ok {
				requests, _ := resourcesMap["requests"].(map[string]any)
				limits, _ := resourcesMap["limits"].(map[string]any)

				// New profile values should be applied
				g.Expect(requests["cpu"]).Should(Equal("2"), "CPU should be updated to new profile's defaultCount")
				g.Expect(requests["memory"]).Should(Equal("2Gi"), "Memory should be updated to new profile's defaultCount")
				g.Expect(limits["cpu"]).Should(Equal("2"), "CPU limit should match new profile")
				g.Expect(limits["memory"]).Should(Equal("2Gi"), "Memory limit should match new profile")

				// GPU should NOT be present (new profile doesn't have it)
				g.Expect(requests).ShouldNot(HaveKey("nvidia.com/gpu"), "GPU should be removed when switching to CPU-only profile")
				g.Expect(limits).ShouldNot(HaveKey("nvidia.com/gpu"), "GPU limit should be removed when switching to CPU-only profile")
			}
		}
	}

	g.Expect(hasResourcesPatch).Should(BeTrue(), "Should have resources patch replacing old values with new profile")
}

func TestHardwareProfile_SameProfileMergesTolerations(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, testNamespace, nil, nil,
		[]any{map[string]any{
			"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule",
		}},
	)

	injector := createInjector(t, s, hwp)

	oldNB := newNotebook(hwpAnnotations(testHardwareProfile))
	_ = unstructured.SetNestedSlice(oldNB.Object, []any{
		map[string]any{"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule"},
		map[string]any{"key": "my-manual-toleration", "operator": "Equal", "value": "true", "effect": "NoSchedule"},
	}, "spec", "template", "spec", "tolerations")

	// New notebook only has the manual toleration (user removed the HWP one, webhook should re-add it)
	newNB := newNotebook(hwpAnnotations(testHardwareProfile))
	_ = unstructured.SetNestedSlice(newNB.Object, []any{
		map[string]any{"key": "my-manual-toleration", "operator": "Equal", "value": "true", "effect": "NoSchedule"},
	}, "spec", "template", "spec", "tolerations")

	req := newUpdateAdmissionRequest(t, newNB, oldNB)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())

	foundTolerationsPatch := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "tolerations") &&
			(patch.Operation == "add" || patch.Operation == "replace") {
			foundTolerationsPatch = true

			if tolerations, ok := patch.Value.([]any); ok {
				foundGPU := false
				foundManual := false

				for _, tol := range tolerations {
					if tolMap, ok := tol.(map[string]any); ok {
						if tolMap["key"] == "nvidia.com/gpu" {
							foundGPU = true
						}
						if tolMap["key"] == "my-manual-toleration" {
							foundManual = true
						}
					}
				}

				g.Expect(foundGPU).Should(BeTrue(), "HWP toleration should be re-added")
				g.Expect(foundManual).Should(BeTrue(), "Manual toleration should be preserved")
			}
		}
	}

	g.Expect(foundTolerationsPatch).Should(BeTrue(), "Should have tolerations patch with merged results")
}

func TestTolerationKey_DistinguishesByValue(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	testCases := []struct {
		name        string
		toleration1 map[string]any
		toleration2 map[string]any
		shouldMatch bool
	}{
		{
			name:        "same key/operator/effect but different value should NOT match",
			toleration1: map[string]any{"key": "gpu-type", "operator": "Equal", "value": "nvidia", "effect": "NoSchedule"},
			toleration2: map[string]any{"key": "gpu-type", "operator": "Equal", "value": "amd", "effect": "NoSchedule"},
			shouldMatch: false,
		},
		{
			name:        "identical tolerations should match",
			toleration1: map[string]any{"key": "gpu-type", "operator": "Equal", "value": "nvidia", "effect": "NoSchedule"},
			toleration2: map[string]any{"key": "gpu-type", "operator": "Equal", "value": "nvidia", "effect": "NoSchedule"},
			shouldMatch: true,
		},
		{
			name:        "same key/operator/effect/value but different tolerationSeconds should NOT match",
			toleration1: map[string]any{"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": int64(300)},
			toleration2: map[string]any{"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": int64(600)},
			shouldMatch: false,
		},
		{
			name:        "same tolerations with same tolerationSeconds should match",
			toleration1: map[string]any{"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": int64(300)},
			toleration2: map[string]any{"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": int64(300)},
			shouldMatch: true,
		},
		{
			name:        "toleration with tolerationSeconds vs without should NOT match",
			toleration1: map[string]any{"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": int64(300)},
			toleration2: map[string]any{"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute"},
			shouldMatch: false,
		},
		{
			name:        "Exists operator with empty value should match same toleration",
			toleration1: map[string]any{"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule"},
			toleration2: map[string]any{"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule"},
			shouldMatch: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key1 := hardwareprofile.TolerationKey(tc.toleration1)
			key2 := hardwareprofile.TolerationKey(tc.toleration2)

			if tc.shouldMatch {
				g.Expect(key1).Should(Equal(key2), "Keys should match for identical tolerations")
			} else {
				g.Expect(key1).ShouldNot(Equal(key2), "Keys should NOT match for different tolerations")
			}
		})
	}
}

func TestHardwareProfile_HWPRemovalPreservesUserTolerations(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, testNamespace, nil,
		map[string]any{"hwp-node": "true"},
		[]any{map[string]any{
			"key": "gpu-type", "operator": "Equal", "value": "amd", "effect": "NoSchedule",
		}},
	)

	injector := createInjector(t, s, hwp)

	oldNB := newNotebook(hwpAnnotations(testHardwareProfile))
	_ = unstructured.SetNestedSlice(oldNB.Object, []any{
		map[string]any{"key": "gpu-type", "operator": "Equal", "value": "amd", "effect": "NoSchedule"},
		map[string]any{"key": "gpu-type", "operator": "Equal", "value": "nvidia", "effect": "NoSchedule"},
	}, "spec", "template", "spec", "tolerations")
	_ = unstructured.SetNestedStringMap(oldNB.Object, map[string]string{"hwp-node": "true"}, "spec", "template", "spec", "nodeSelector")

	newNB := newNotebook(nil) // no HWP annotation
	_ = unstructured.SetNestedSlice(newNB.Object, []any{
		map[string]any{"key": "gpu-type", "operator": "Equal", "value": "amd", "effect": "NoSchedule"},
		map[string]any{"key": "gpu-type", "operator": "Equal", "value": "nvidia", "effect": "NoSchedule"},
	}, "spec", "template", "spec", "tolerations")
	_ = unstructured.SetNestedStringMap(newNB.Object, map[string]string{"hwp-node": "true"}, "spec", "template", "spec", "nodeSelector")

	req := newUpdateAdmissionRequest(t, newNB, oldNB)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())

	foundTolerationsPatch := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "tolerations") &&
			(patch.Operation == "add" || patch.Operation == "replace") {
			foundTolerationsPatch = true

			if tolerations, ok := patch.Value.([]any); ok {
				foundUserToleration := false
				foundHWPToleration := false

				for _, tol := range tolerations {
					if tolMap, ok := tol.(map[string]any); ok {
						if tolMap["key"] == "gpu-type" && tolMap["value"] == "nvidia" {
							foundUserToleration = true
						}
						if tolMap["key"] == "gpu-type" && tolMap["value"] == "amd" {
							foundHWPToleration = true
						}
					}
				}

				g.Expect(foundUserToleration).Should(BeTrue(), "User's nvidia toleration should be preserved")
				g.Expect(foundHWPToleration).Should(BeFalse(), "HWP's amd toleration should NOT be in patch")
			}
		}
	}

	if !foundTolerationsPatch {
		for _, patch := range resp.Patches {
			if strings.Contains(patch.Path, "tolerations") && patch.Operation == "remove" {
				foundTolerationsPatch = true
			}
		}
	}

	g.Expect(foundTolerationsPatch).Should(BeTrue(), "Should have a patch modifying tolerations")
}

func TestHardwareProfile_HWPRemovalWithTolerationSeconds(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, testNamespace, nil, nil,
		[]any{map[string]any{
			"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute",
			"tolerationSeconds": int64(300),
		}},
	)

	injector := createInjector(t, s, hwp)

	oldNB := newNotebook(hwpAnnotations(testHardwareProfile))
	_ = unstructured.SetNestedSlice(oldNB.Object, []any{
		map[string]any{
			"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute",
			"tolerationSeconds": int64(300),
		},
		map[string]any{
			"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute",
			"tolerationSeconds": int64(600),
		},
	}, "spec", "template", "spec", "tolerations")

	newNB := newNotebook(nil)
	_ = unstructured.SetNestedSlice(newNB.Object, []any{
		map[string]any{
			"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute",
			"tolerationSeconds": int64(300),
		},
		map[string]any{
			"key": "node.kubernetes.io/unreachable", "operator": "Exists", "effect": "NoExecute",
			"tolerationSeconds": int64(600),
		},
	}, "spec", "template", "spec", "tolerations")

	req := newUpdateAdmissionRequest(t, newNB, oldNB)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())

	foundTolerationsPatch := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "tolerations") &&
			(patch.Operation == "add" || patch.Operation == "replace") {
			foundTolerationsPatch = true

			if tolerations, ok := patch.Value.([]any); ok {
				found300s := false
				found600s := false

				for _, tol := range tolerations {
					if tolMap, ok := tol.(map[string]any); ok {
						if ts, exists := tolMap["tolerationSeconds"]; exists {
							switch v := ts.(type) {
							case int64:
								if v == 300 {
									found300s = true
								}
								if v == 600 {
									found600s = true
								}
							case float64:
								if v == 300 {
									found300s = true
								}
								if v == 600 {
									found600s = true
								}
							}
						}
					}
				}

				g.Expect(found600s).Should(BeTrue(), "User's 600s toleration should be preserved")
				g.Expect(found300s).Should(BeFalse(), "HWP's 300s toleration should NOT be in patch")
			}
		}
	}

	if !foundTolerationsPatch {
		for _, patch := range resp.Patches {
			if strings.Contains(patch.Path, "tolerations") && patch.Operation == "remove" {
				foundTolerationsPatch = true
			}
		}
	}

	g.Expect(foundTolerationsPatch).Should(BeTrue(), "Should have a patch modifying tolerations")
}

func TestHardwareProfile_HWPRemovalWithDeletedProfile(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	// Do NOT register any HWP — simulates a deleted profile
	injector := createInjector(t, s)

	// Old notebook had an HWP annotation referencing a now-deleted profile
	oldNB := newNotebook(hwpAnnotationsWithNamespace(testHardwareProfile, testNamespace))
	_ = unstructured.SetNestedStringMap(oldNB.Object,
		map[string]string{"gpu": "true"}, "spec", "template", "spec", "nodeSelector")

	// New notebook removes the name annotation but still has the namespace annotation
	// (simulates user removing the profile name only)
	newNB := newNotebook(map[string]string{
		metadata.HardwareProfileNamespaceAnnotation: testNamespace,
	})
	_ = unstructured.SetNestedStringMap(newNB.Object,
		map[string]string{"gpu": "true"}, "spec", "template", "spec", "nodeSelector")

	req := newUpdateAdmissionRequest(t, newNB, oldNB)
	resp := injector.Handle(t.Context(), req)

	// Should still be allowed — the fallback removes the namespace annotation
	g.Expect(resp.Allowed).Should(BeTrue(),
		"Should allow the update even when old HWP cannot be fetched")

	// Should have a patch removing the namespace annotation
	foundNamespaceRemoval := false
	for _, patch := range resp.Patches {
		if patch.Operation == "remove" && strings.Contains(patch.Path, "hardware-profile-namespace") {
			foundNamespaceRemoval = true
		}
	}

	g.Expect(foundNamespaceRemoval).Should(BeTrue(),
		"Should patch to remove the hardware-profile-namespace annotation")
}

func TestHardwareProfile_NodeSelectorOverrideWarning(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, testNamespace, nil,
		map[string]any{"kubernetes.io/os": "linux", "gpu-type": "nvidia"},
		nil,
	)

	injector := createInjector(t, s, hwp)

	oldNB := newNotebook(hwpAnnotations(testHardwareProfile))
	_ = unstructured.SetNestedStringMap(oldNB.Object,
		map[string]string{"kubernetes.io/os": "linux", "gpu-type": "nvidia"},
		"spec", "template", "spec", "nodeSelector")

	newNB := newNotebook(hwpAnnotations(testHardwareProfile))
	_ = unstructured.SetNestedStringMap(newNB.Object,
		map[string]string{
			"kubernetes.io/os": "windows",
			"gpu-type":         "nvidia",
			"my-custom-key":    "my-value",
		},
		"spec", "template", "spec", "nodeSelector")

	req := newUpdateAdmissionRequest(t, newNB, oldNB)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())

	g.Expect(resp.Warnings).ShouldNot(BeEmpty(), "Should have at least one warning")

	foundOverrideWarning := false
	for _, warning := range resp.Warnings {
		if strings.Contains(warning, "kubernetes.io/os") &&
			strings.Contains(warning, "windows") &&
			strings.Contains(warning, "linux") &&
			strings.Contains(warning, "overwritten") {
			foundOverrideWarning = true

			break
		}
	}

	g.Expect(foundOverrideWarning).Should(BeTrue(), "Should warn about nodeSelector key being overwritten")

	for _, warning := range resp.Warnings {
		g.Expect(warning).ShouldNot(ContainSubstring("gpu-type"), "Should not warn about unchanged HWP values")
		g.Expect(warning).ShouldNot(ContainSubstring("my-custom-key"), "Should not warn about user-added keys")
	}
}

func TestHardwareProfile_NoWarningWhenNodeSelectorUnchanged(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newHWP(testHardwareProfile, testNamespace, nil,
		map[string]any{"kubernetes.io/os": "linux"},
		nil,
	)

	injector := createInjector(t, s, hwp)

	oldNB := newNotebook(hwpAnnotations(testHardwareProfile))
	_ = unstructured.SetNestedStringMap(oldNB.Object,
		map[string]string{"kubernetes.io/os": "linux"},
		"spec", "template", "spec", "nodeSelector")

	newNB := newNotebook(hwpAnnotations(testHardwareProfile))
	_ = unstructured.SetNestedStringMap(newNB.Object,
		map[string]string{
			"kubernetes.io/os": "linux",
			"my-custom-key":    "my-value",
		},
		"spec", "template", "spec", "nodeSelector")

	req := newUpdateAdmissionRequest(t, newNB, oldNB)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Warnings).Should(BeEmpty(), "Should have no warnings when user doesn't modify HWP values")
}

func TestHardwareProfile_DeleteOperation(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	injector := createInjector(t, s)

	nb := newNotebook(map[string]string{
		metadata.HardwareProfileNameAnnotation: testHardwareProfile,
	})
	req := newAdmissionRequest(t, admissionv1.Delete, nb, gvk.Notebook)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Result.Message).Should(ContainSubstring("Operation DELETE"))
}

func TestHardwareProfile_DeletionTimestamp(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	injector := createInjector(t, s)

	nb := newNotebook(map[string]string{
		metadata.HardwareProfileNameAnnotation: testHardwareProfile,
	})

	now := metav1.Now()
	nb.SetDeletionTimestamp(&now)

	req := newAdmissionRequest(t, admissionv1.Update, nb, gvk.Notebook)
	resp := injector.Handle(t.Context(), req)
	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Result.Message).Should(ContainSubstring("marked for deletion"))
	g.Expect(resp.Patches).Should(BeEmpty())
}

func TestHardwareProfile_AppliesKueueLabel(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newKueueHWP(testHardwareProfile, "my-queue")
	injector := createInjector(t, s, hwp)

	nb := newNotebook(hwpAnnotations(testHardwareProfile))
	req := newAdmissionRequest(t, admissionv1.Create, nb, gvk.Notebook)
	resp := injector.Handle(t.Context(), req)

	g.Expect(resp.Allowed).Should(BeTrue())

	foundLabelPatch := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "labels") {
			if strings.Contains(fmt.Sprintf("%v", patch.Value), "my-queue") {
				foundLabelPatch = true
			}
		}
	}

	g.Expect(foundLabelPatch).Should(BeTrue(),
		"Should have a patch setting the Kueue queue-name label")
}

func TestHardwareProfile_KueueSkipsNodeScheduling(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	// HWP with BOTH Kueue AND node scheduling — Kueue takes precedence
	hwpObj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": gvk.HardwareProfile.Group + "/" + gvk.HardwareProfile.Version,
			"kind":       gvk.HardwareProfile.Kind,
			"metadata": map[string]any{
				"name":      testHardwareProfile,
				"namespace": testNamespace,
			},
			"spec": map[string]any{
				"scheduling": map[string]any{
					"kueue": map[string]any{
						"localQueueName": "gpu-queue",
					},
					"node": map[string]any{
						"nodeSelector": map[string]any{"gpu": "true"},
						"tolerations": []any{map[string]any{
							"key": "gpu", "operator": "Exists", "effect": "NoSchedule",
						}},
					},
				},
			},
		},
	}

	injector := createInjector(t, s, hwpObj)
	nb := newNotebook(hwpAnnotations(testHardwareProfile))
	req := newAdmissionRequest(t, admissionv1.Create, nb, gvk.Notebook)
	resp := injector.Handle(t.Context(), req)

	g.Expect(resp.Allowed).Should(BeTrue())

	// Should NOT have nodeSelector or tolerations patches
	for _, patch := range resp.Patches {
		g.Expect(patch.Path).ShouldNot(ContainSubstring("nodeSelector"),
			"Kueue scheduling should skip nodeSelector")
		g.Expect(patch.Path).ShouldNot(ContainSubstring("tolerations"),
			"Kueue scheduling should skip tolerations")
	}
}

func TestHardwareProfile_KueueLabelOverrideWarning(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newKueueHWP(testHardwareProfile, "new-queue")
	injector := createInjector(t, s, hwp)

	// Notebook already has a different Kueue label
	nb := newNotebook(hwpAnnotations(testHardwareProfile))
	nb.SetLabels(map[string]string{"kueue.x-k8s.io/queue-name": "old-queue"})
	oldNB := newNotebook(hwpAnnotations(testHardwareProfile))
	oldNB.SetLabels(map[string]string{"kueue.x-k8s.io/queue-name": "old-queue"})

	req := newUpdateAdmissionRequest(t, nb, oldNB)
	resp := injector.Handle(t.Context(), req)

	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Warnings).ShouldNot(BeEmpty(),
		"Should warn when Kueue label is being overwritten")
	g.Expect(resp.Warnings[0]).Should(ContainSubstring("old-queue"))
	g.Expect(resp.Warnings[0]).Should(ContainSubstring("new-queue"))
}

func TestHardwareProfile_NoKueueWarningWhenLabelUnchanged(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	hwp := newKueueHWP(testHardwareProfile, "same-queue")
	injector := createInjector(t, s, hwp)

	// Notebook already has the same Kueue label
	nb := newNotebook(hwpAnnotations(testHardwareProfile))
	nb.SetLabels(map[string]string{"kueue.x-k8s.io/queue-name": "same-queue"})
	oldNB := newNotebook(hwpAnnotations(testHardwareProfile))
	oldNB.SetLabels(map[string]string{"kueue.x-k8s.io/queue-name": "same-queue"})

	req := newUpdateAdmissionRequest(t, nb, oldNB)
	resp := injector.Handle(t.Context(), req)

	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Warnings).Should(BeEmpty(),
		"Should NOT warn when Kueue label matches the HWP value")
}

func TestHardwareProfile_ProfileChangeRemovesKueueLabel(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	// New profile has node scheduling (no Kueue)
	newHWPObj := newHWP("new-profile", testNamespace, nil,
		map[string]any{"cpu-node": "true"}, nil)
	injector := createInjector(t, s, newHWPObj)

	// Notebook switches from old Kueue profile to new node-scheduling profile
	nb := newNotebook(hwpAnnotations("new-profile"))
	nb.SetLabels(map[string]string{"kueue.x-k8s.io/queue-name": "old-queue"})
	oldNB := newNotebook(hwpAnnotations("old-kueue-profile"))
	oldNB.SetLabels(map[string]string{"kueue.x-k8s.io/queue-name": "old-queue"})

	req := newUpdateAdmissionRequest(t, nb, oldNB)
	resp := injector.Handle(t.Context(), req)

	g.Expect(resp.Allowed).Should(BeTrue())

	// The Kueue label should be removed (profile change clears it)
	foundLabelRemoval := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "labels") {
			if !strings.Contains(fmt.Sprintf("%v", patch.Value), "kueue.x-k8s.io/queue-name") {
				foundLabelRemoval = true
			}
		}
	}

	g.Expect(foundLabelRemoval).Should(BeTrue(),
		"Profile change should remove the old Kueue label")
}

func TestHardwareProfile_HWPRemovalClearsKueueLabel(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := newScheme(t)

	// Register the old HWP (Kueue-based) so removal can fetch it
	oldHWP := newKueueHWP("kueue-profile", "my-queue")
	injector := createInjector(t, s, oldHWP)

	// Old notebook had the Kueue profile; new notebook removes the annotation
	oldNB := newNotebook(hwpAnnotationsWithNamespace("kueue-profile", testNamespace))
	oldNB.SetLabels(map[string]string{"kueue.x-k8s.io/queue-name": "my-queue"})

	newNB := newNotebook(map[string]string{
		metadata.HardwareProfileNamespaceAnnotation: testNamespace,
	})
	newNB.SetLabels(map[string]string{"kueue.x-k8s.io/queue-name": "my-queue"})

	req := newUpdateAdmissionRequest(t, newNB, oldNB)
	resp := injector.Handle(t.Context(), req)

	g.Expect(resp.Allowed).Should(BeTrue())

	// Should have a patch removing the Kueue label
	foundKueueRemoval := false
	for _, patch := range resp.Patches {
		if strings.Contains(patch.Path, "labels") {
			if !strings.Contains(fmt.Sprintf("%v", patch.Value), "kueue.x-k8s.io/queue-name") {
				foundKueueRemoval = true
			}
		}
	}

	g.Expect(foundKueueRemoval).Should(BeTrue(),
		"HWP removal should clear the Kueue queue-name label")
}

func TestParseQuantityValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		expected string
		wantErr  bool
	}{
		{name: "string integer", input: "2", expected: "2"},
		{name: "string milli", input: "500m", expected: "500m"},
		{name: "string memory", input: "1Gi", expected: "1Gi"},
		{name: "string fractional", input: "0.5", expected: "500m"},
		{name: "int64 value", input: int64(4), expected: "4"},
		{name: "int64 zero", input: int64(0), expected: "0"},
		{name: "float64 integer", input: float64(2), expected: "2"},
		{name: "float64 fractional", input: float64(0.5), expected: "500m"},
		{name: "float64 large fractional", input: float64(1.5), expected: "1500m"},
		{name: "invalid string", input: "not-a-quantity", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			result, err := hardwareprofile.ParseQuantityValue(tc.input)
			if tc.wantErr {
				g.Expect(err).Should(HaveOccurred())

				return
			}

			g.Expect(err).ShouldNot(HaveOccurred())
			g.Expect(result.Cmp(resource.MustParse(tc.expected))).Should(Equal(0),
				"expected %s but got %s", tc.expected, result.String())
		})
	}
}
