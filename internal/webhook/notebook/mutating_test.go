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

package notebook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/opendatahub-io/workbenches-operator/internal/gvk"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
	nb "github.com/opendatahub-io/workbenches-operator/internal/webhook/notebook"
)

const (
	testNamespace    = "test-namespace"
	testNamespace2   = "test-namespace2"
	testNotebook     = "test-notebook"
	testSecret1      = "secret1"
	testSecret2      = "secret2"
	addOperation     = "add"
	replaceOperation = "replace"
	removeOperation  = "remove"
)

func createTestWebhook(t *testing.T, cli client.Client) *nb.NotebookWebhook {
	t.Helper()

	return &nb.NotebookWebhook{
		Client:    cli,
		APIReader: cli,
		Decoder:   admission.NewDecoder(scheme.Scheme),
		Name:      "test-webhook",
	}
}

func createNotebook(options ...func(*unstructured.Unstructured)) *unstructured.Unstructured {
	notebook := &unstructured.Unstructured{}
	notebook.SetGroupVersionKind(gvk.Notebook)
	notebook.SetName(testNotebook)
	notebook.SetNamespace(testNamespace)

	spec := map[string]any{
		"template": map[string]any{
			"spec": map[string]any{
				"containers": []any{
					map[string]any{
						"name":  "notebook",
						"image": "notebook:latest",
					},
				},
			},
		},
	}
	notebook.Object["spec"] = spec

	for _, opt := range options {
		opt(notebook)
	}

	return notebook
}

func withAnnotations(annotations map[string]string) func(*unstructured.Unstructured) {
	return func(notebook *unstructured.Unstructured) {
		notebook.SetAnnotations(annotations)
	}
}

func withExistingEnvFrom(envFrom []any) func(*unstructured.Unstructured) {
	return func(notebook *unstructured.Unstructured) {
		containers, _, _ := unstructured.NestedSlice(notebook.Object, nb.NotebookContainersPath...)
		if len(containers) > 0 {
			if container, ok := containers[0].(map[string]any); ok {
				container["envFrom"] = envFrom
				containers[0] = container
				_ = unstructured.SetNestedSlice(notebook.Object, containers, nb.NotebookContainersPath...)
			}
		}
	}
}

func createAdmissionRequest(t *testing.T, operation admissionv1.Operation, obj *unstructured.Unstructured, oldObj *unstructured.Unstructured) admission.Request {
	t.Helper()

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:  "test-uid",
			Kind: metav1.GroupVersionKind{Group: gvk.Notebook.Group, Version: gvk.Notebook.Version, Kind: gvk.Notebook.Kind},
			Resource: metav1.GroupVersionResource{
				Group:    gvk.Notebook.Group,
				Version:  gvk.Notebook.Version,
				Resource: "notebooks",
			},
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
			Operation: operation,
		},
	}

	if operation != admissionv1.Delete {
		objBytes, err := json.Marshal(obj)
		if err != nil {
			t.Fatalf("failed to marshal object: %v", err)
		}

		req.Object = runtime.RawExtension{Raw: objBytes}
	}

	if oldObj != nil {
		oldObjBytes, err := json.Marshal(oldObj)
		if err != nil {
			t.Fatalf("failed to marshal oldObj: %v", err)
		}

		req.OldObject = runtime.RawExtension{Raw: oldObjBytes}
	}

	return req
}

type mockClient struct {
	client.Client

	allowPermissions map[string]bool // key: "namespace/name"
}

func (m *mockClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if sar, ok := obj.(*authorizationv1.SubjectAccessReview); ok {
		key := fmt.Sprintf("%s/%s", sar.Spec.ResourceAttributes.Namespace, sar.Spec.ResourceAttributes.Name)
		allowed, exists := m.allowPermissions[key]
		sar.Status = authorizationv1.SubjectAccessReviewStatus{
			Allowed: exists && allowed,
		}

		if !allowed {
			sar.Status.Reason = "insufficient permissions"
		}

		return nil
	}

	return m.Client.Create(ctx, obj, opts...)
}

func TestNotebookWebhook_Handle_BasicValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		annotations        map[string]string
		expectedAllowed    bool
		expectedMessage    string
		expectedPatchesLen int
	}{
		{
			name:               "no annotations",
			annotations:        nil,
			expectedAllowed:    true,
			expectedMessage:    "no injection needed",
			expectedPatchesLen: 0,
		},
		{
			name: "empty connections annotation",
			annotations: map[string]string{
				metadata.ConnectionAnnotation: "",
			},
			expectedAllowed:    true,
			expectedMessage:    "no injection needed",
			expectedPatchesLen: 0,
		},
		{
			name: "invalid annotation format - missing namespace",
			annotations: map[string]string{
				metadata.ConnectionAnnotation: "invalid-format",
			},
			expectedAllowed:    false,
			expectedMessage:    "failed to parse connections annotation",
			expectedPatchesLen: 0,
		},
		{
			name: "invalid annotation format - empty name",
			annotations: map[string]string{
				metadata.ConnectionAnnotation: testNamespace + "/",
			},
			expectedAllowed:    false,
			expectedMessage:    "failed to parse connections annotation",
			expectedPatchesLen: 0,
		},
		{
			name: "invalid annotation format - empty namespace",
			annotations: map[string]string{
				metadata.ConnectionAnnotation: "/" + testSecret1,
			},
			expectedAllowed:    false,
			expectedMessage:    "failed to parse connections annotation",
			expectedPatchesLen: 0,
		},
		{
			name: "annotation exceeds max length",
			annotations: map[string]string{
				metadata.ConnectionAnnotation: strings.Repeat("a/b,", 2000),
			},
			expectedAllowed:    false,
			expectedMessage:    "exceeds",
			expectedPatchesLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			cli := fake.NewClientBuilder().Build()
			webhook := createTestWebhook(t, cli)

			notebook := createNotebook(withAnnotations(tt.annotations))
			req := createAdmissionRequest(t, admissionv1.Create, notebook, nil)

			resp := webhook.Handle(t.Context(), req)

			g.Expect(resp.Allowed).Should(Equal(tt.expectedAllowed))
			g.Expect(resp.Patches).Should(HaveLen(tt.expectedPatchesLen))
			g.Expect(resp.Result.Message).Should(ContainSubstring(tt.expectedMessage))
		})
	}
}

func TestNotebookWebhook_Handle_NilDecoder(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	injector := &nb.NotebookWebhook{
		Client: fake.NewClientBuilder().Build(),
		Name:   "test-webhook",
	}

	notebook := createNotebook()
	req := createAdmissionRequest(t, admissionv1.Create, notebook, nil)

	resp := injector.Handle(t.Context(), req)

	g.Expect(resp.Allowed).Should(BeFalse())
	g.Expect(resp.Result.Message).Should(ContainSubstring("webhook decoder not initialized"))
}

func TestNotebookWebhook_Handle_DeletionTimestamp(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	cli := fake.NewClientBuilder().Build()
	webhook := createTestWebhook(t, cli)

	now := metav1.Now()
	notebook := createNotebook(
		withAnnotations(map[string]string{
			metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s", testNamespace, testSecret1),
		}),
		func(nb *unstructured.Unstructured) {
			nb.SetDeletionTimestamp(&now)
		},
	)
	req := createAdmissionRequest(t, admissionv1.Update, notebook, nil)

	resp := webhook.Handle(t.Context(), req)

	g.Expect(resp.Allowed).Should(BeTrue())
	g.Expect(resp.Result.Message).Should(ContainSubstring("marked for deletion"))
	g.Expect(resp.Patches).Should(BeEmpty())
}

func TestNotebookWebhook_Handle_Permissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		connections       string
		allowPermissions  map[string]bool
		expectedAllowed   bool
		expectedMessage   string
		shouldHavePatches bool
		forbiddenSecrets  []string
		secretsToCreate   []string
	}{
		{
			name:        "successful injection with single secret",
			connections: fmt.Sprintf("%s/%s", testNamespace, testSecret1),
			allowPermissions: map[string]bool{
				fmt.Sprintf("%s/%s", testNamespace, testSecret1): true,
			},
			expectedAllowed:   true,
			shouldHavePatches: true,
			secretsToCreate:   []string{testSecret1},
		},
		{
			name:        "permission denied for single secret",
			connections: fmt.Sprintf("%s/%s", testNamespace, testSecret1),
			allowPermissions: map[string]bool{
				fmt.Sprintf("%s/%s", testNamespace, testSecret1): false,
			},
			expectedAllowed:   false,
			expectedMessage:   "connection secret(s) are invalid",
			shouldHavePatches: false,
			forbiddenSecrets:  []string{fmt.Sprintf("%s/%s", testNamespace, testSecret1)},
			secretsToCreate:   []string{testSecret1},
		},
		{
			name:        "mixed permissions for multiple secrets",
			connections: fmt.Sprintf("%s/%s,%s/%s", testNamespace, testSecret1, testNamespace, testSecret2),
			allowPermissions: map[string]bool{
				fmt.Sprintf("%s/%s", testNamespace, testSecret1): true,
				fmt.Sprintf("%s/%s", testNamespace, testSecret2): false,
			},
			expectedAllowed:   false,
			expectedMessage:   "connection secret(s) are invalid",
			shouldHavePatches: false,
			forbiddenSecrets:  []string{fmt.Sprintf("%s/%s", testNamespace, testSecret2)},
			secretsToCreate:   []string{testSecret1, testSecret2},
		},
		{
			name:        "secret does not exist",
			connections: fmt.Sprintf("%s/%s,%s/%s", testNamespace, testSecret1, testNamespace, testSecret2),
			allowPermissions: map[string]bool{
				fmt.Sprintf("%s/%s", testNamespace, testSecret1): false,
				fmt.Sprintf("%s/%s", testNamespace, testSecret2): false,
			},
			expectedAllowed:   false,
			expectedMessage:   "connection secret(s) are invalid",
			shouldHavePatches: false,
			forbiddenSecrets:  []string{fmt.Sprintf("%s/%s", testNamespace, testSecret2)},
		},
		{
			name:        "secret in a different namespace than Notebook CR's",
			connections: fmt.Sprintf("%s/%s,%s/%s", testNamespace, testSecret1, testNamespace2, testSecret2),
			allowPermissions: map[string]bool{
				fmt.Sprintf("%s/%s", testNamespace, testSecret1):  true,
				fmt.Sprintf("%s/%s", testNamespace2, testSecret2): true,
			},
			expectedAllowed:   false,
			expectedMessage:   "connection secret(s) are invalid",
			shouldHavePatches: false,
			forbiddenSecrets:  []string{fmt.Sprintf("%s/%s", testNamespace2, testSecret2)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			baseCli := fake.NewClientBuilder().Build()
			cli := &mockClient{
				Client:           baseCli,
				allowPermissions: tt.allowPermissions,
			}

			for _, secretName := range tt.secretsToCreate {
				g.Expect(cli.Create(t.Context(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secretName,
						Namespace: testNamespace,
					},
				})).Should(Succeed())
			}

			webhook := createTestWebhook(t, cli)

			notebook := createNotebook(withAnnotations(map[string]string{
				metadata.ConnectionAnnotation: tt.connections,
			}))
			req := createAdmissionRequest(t, admissionv1.Create, notebook, nil)

			resp := webhook.Handle(t.Context(), req)

			g.Expect(resp.Allowed).Should(Equal(tt.expectedAllowed))

			if tt.shouldHavePatches {
				g.Expect(resp.Patches).ShouldNot(BeEmpty())
			} else {
				g.Expect(resp.Patches).Should(BeEmpty())
			}

			if tt.expectedMessage != "" {
				g.Expect(resp.Result.Message).Should(ContainSubstring(tt.expectedMessage))
			}

			for _, forbidden := range tt.forbiddenSecrets {
				g.Expect(resp.Result.Message).Should(ContainSubstring(forbidden))
			}
		})
	}
}

func TestNotebookWebhook_Handle_Operations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		operation         admissionv1.Operation
		oldNotebook       *unstructured.Unstructured
		expectedAllowed   bool
		expectedMessage   string
		shouldHavePatches bool
	}{
		{
			name:              "create operation with valid permissions",
			operation:         admissionv1.Create,
			expectedAllowed:   true,
			shouldHavePatches: true,
		},
		{
			name:      "update operation with valid permissions",
			operation: admissionv1.Update,
			oldNotebook: createNotebook(withAnnotations(map[string]string{
				metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s", testNamespace, testSecret1),
			})),
			expectedAllowed:   true,
			shouldHavePatches: true,
		},
		{
			name:              "delete operation",
			operation:         admissionv1.Delete,
			expectedAllowed:   true,
			expectedMessage:   "Operation DELETE on Notebook allowed",
			shouldHavePatches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			baseCli := fake.NewClientBuilder().Build()
			cli := &mockClient{
				Client: baseCli,
				allowPermissions: map[string]bool{
					fmt.Sprintf("%s/%s", testNamespace, testSecret1): true,
				},
			}

			g.Expect(cli.Create(t.Context(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testSecret1,
					Namespace: testNamespace,
				},
			})).Should(Succeed())

			webhook := createTestWebhook(t, cli)

			notebook := createNotebook(withAnnotations(map[string]string{
				metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s", testNamespace, testSecret1),
			}))
			req := createAdmissionRequest(t, tt.operation, notebook, tt.oldNotebook)

			resp := webhook.Handle(t.Context(), req)

			g.Expect(resp.Allowed).Should(Equal(tt.expectedAllowed))

			if tt.shouldHavePatches {
				g.Expect(resp.Patches).ShouldNot(BeEmpty())
			} else {
				g.Expect(resp.Patches).Should(BeEmpty())
			}

			if tt.expectedMessage != "" {
				g.Expect(resp.Result.Message).Should(ContainSubstring(tt.expectedMessage))
			}
		})
	}
}

//nolint:maintidx
func TestNotebookWebhook_Handle_EnvFromInjection(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	baseCli := fake.NewClientBuilder().Build()
	cli := &mockClient{
		Client: baseCli,
		allowPermissions: map[string]bool{
			fmt.Sprintf("%s/%s", testNamespace, testSecret1): true,
			fmt.Sprintf("%s/%s", testNamespace, testSecret2): true,
		},
	}

	g.Expect(cli.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSecret1,
			Namespace: testNamespace,
		},
	})).Should(Succeed())
	g.Expect(cli.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSecret2,
			Namespace: testNamespace,
		},
	})).Should(Succeed())

	webhook := createTestWebhook(t, cli)

	containsSecretRef := func(value any, secretName string) bool {
		if envFromArray, ok := value.([]any); ok {
			for _, entry := range envFromArray {
				if entryMap, ok := entry.(map[string]any); ok {
					if secretRef, hasSecret := entryMap["secretRef"]; hasSecret {
						if secretRefMap, ok := secretRef.(map[string]any); ok {
							if name, hasName := secretRefMap["name"]; hasName && name == secretName {
								return true
							}
						}
					}
				}
			}
		} else if entryMap, ok := value.(map[string]any); ok {
			if secretRef, hasSecret := entryMap["secretRef"]; hasSecret {
				if secretRefMap, ok := secretRef.(map[string]any); ok {
					if name, hasName := secretRefMap["name"]; hasName && name == secretName {
						return true
					}
				}
			}
		}

		return false
	}

	tests := []struct {
		name           string
		operation      admissionv1.Operation
		notebook       *unstructured.Unstructured
		oldNotebook    *unstructured.Unstructured
		expectedChecks []func(jsonpatch.JsonPatchOperation) bool
	}{
		{
			name:      "inject single secret",
			operation: admissionv1.Create,
			notebook: createNotebook(withAnnotations(map[string]string{
				metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s", testNamespace, testSecret1),
			})),
			expectedChecks: []func(jsonpatch.JsonPatchOperation) bool{
				func(patch jsonpatch.JsonPatchOperation) bool {
					return patch.Operation == addOperation &&
						patch.Path == "/spec/template/spec/containers/0/envFrom" &&
						containsSecretRef(patch.Value, testSecret1)
				},
			},
		},
		{
			name:      "inject multiple secrets",
			operation: admissionv1.Create,
			notebook: createNotebook(withAnnotations(map[string]string{
				metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s,%s/%s", testNamespace, testSecret1, testNamespace, testSecret2),
			})),
			expectedChecks: []func(jsonpatch.JsonPatchOperation) bool{
				func(patch jsonpatch.JsonPatchOperation) bool {
					return patch.Operation == addOperation &&
						patch.Path == "/spec/template/spec/containers/0/envFrom" &&
						containsSecretRef(patch.Value, testSecret1) &&
						containsSecretRef(patch.Value, testSecret2)
				},
			},
		},
		{
			name:      "preserve existing configMapRef and inject secret",
			operation: admissionv1.Update,
			notebook: createNotebook(
				withAnnotations(map[string]string{
					metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s", testNamespace, testSecret1),
				}),
				withExistingEnvFrom([]any{
					map[string]any{
						"configMapRef": map[string]any{
							"name": "existing-config",
						},
					},
				}),
			),
			oldNotebook: createNotebook(
				withAnnotations(map[string]string{
					metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s", testNamespace, testSecret2),
				}),
				withExistingEnvFrom([]any{
					map[string]any{
						"configMapRef": map[string]any{
							"name": "existing-config",
						},
					},
				}),
			),
			expectedChecks: []func(jsonpatch.JsonPatchOperation) bool{
				func(patch jsonpatch.JsonPatchOperation) bool {
					return patch.Operation == addOperation &&
						patch.Path == "/spec/template/spec/containers/0/envFrom/1" &&
						containsSecretRef(patch.Value, testSecret1)
				},
			},
		},
		{
			name:      "replace existing connection secret with a new secret",
			operation: admissionv1.Update,
			notebook: createNotebook(
				withAnnotations(map[string]string{
					metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s", testNamespace, testSecret1),
				}),
				withExistingEnvFrom([]any{
					map[string]any{
						"secretRef": map[string]any{
							"name": testSecret2,
						},
					},
				}),
			),
			oldNotebook: createNotebook(
				withAnnotations(map[string]string{
					metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s", testNamespace, testSecret2),
				}),
				withExistingEnvFrom([]any{
					map[string]any{
						"secretRef": map[string]any{
							"name": testSecret2,
						},
					},
				}),
			),
			expectedChecks: []func(jsonpatch.JsonPatchOperation) bool{
				func(patch jsonpatch.JsonPatchOperation) bool {
					return patch.Operation == replaceOperation &&
						patch.Path == "/spec/template/spec/containers/0/envFrom/0/secretRef/name" &&
						patch.Value == testSecret1
				},
			},
		},
		{
			name:      "preserve non-connection secret while removing connection secret",
			operation: admissionv1.Update,
			notebook: createNotebook(
				withExistingEnvFrom([]any{
					map[string]any{
						"secretRef": map[string]any{
							"name": testSecret1,
						},
					},
					map[string]any{
						"secretRef": map[string]any{
							"name": testSecret2,
						},
					},
				}),
			),
			oldNotebook: createNotebook(
				withAnnotations(map[string]string{
					metadata.ConnectionAnnotation: fmt.Sprintf("%s/%s", testNamespace, testSecret2),
				}),
				withExistingEnvFrom([]any{
					map[string]any{
						"secretRef": map[string]any{
							"name": testSecret1,
						},
					},
					map[string]any{
						"secretRef": map[string]any{
							"name": testSecret2,
						},
					},
				}),
			),
			expectedChecks: []func(jsonpatch.JsonPatchOperation) bool{
				func(patch jsonpatch.JsonPatchOperation) bool {
					return patch.Operation == removeOperation &&
						patch.Path == "/spec/template/spec/containers/0/envFrom/1"
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			req := createAdmissionRequest(t, tt.operation, tt.notebook, tt.oldNotebook)
			resp := webhook.Handle(t.Context(), req)

			g.Expect(resp.Allowed).Should(BeTrue())
			g.Expect(resp.Patches).ShouldNot(BeEmpty())

			verifyExpectedPatches(t, resp.Patches, tt.expectedChecks)
		})
	}
}

func verifyExpectedPatches(t *testing.T, actualPatches []jsonpatch.JsonPatchOperation, expectedPatchChecks []func(jsonpatch.JsonPatchOperation) bool) {
	t.Helper()
	g := NewWithT(t)

	g.Expect(actualPatches).Should(HaveLen(len(expectedPatchChecks)), "Expected number of patches to match")

	for i, check := range expectedPatchChecks {
		g.Expect(check(actualPatches[i])).Should(BeTrue(), fmt.Sprintf("Patch %d failed validation", i))
	}
}
