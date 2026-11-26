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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/opendatahub-io/workbenches-operator/internal/platform"
)

var inventoryPlatforms = []struct{ profile, platform string }{
	{"odh", platform.OpenDataHub},
	{"rhoai", platform.SelfManagedRhoai},
}

type inventoryResource struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
}

// The chaos profiles can cover a subset of the bundled resources, but each
// modeled operand resource must be available on a fresh install.
func TestChaosOperandInventory(t *testing.T) {
	for _, target := range inventoryPlatforms {
		t.Run(target.profile, func(t *testing.T) {
			modelPath := filepath.Join("..", "..", "chaos", "profiles", target.profile, "knowledge", "workbenches.yaml")
			data, readErr := os.ReadFile(modelPath) //nolint:gosec // fixed repository-relative test input
			if readErr != nil {
				t.Fatal(readErr)
			}
			var model struct {
				Components []struct {
					Name             string              `json:"name"`
					ManagedResources []inventoryResource `json:"managedResources"`
				} `json:"components"`
			}
			if err := sigyaml.Unmarshal(data, &model); err != nil {
				t.Fatalf("parse %s: %v", modelPath, err)
			}

			namespace := platform.DefaultApplicationsNamespace(target.platform)
			rendered := renderOperandInventory(t, target.platform, false)
			// This ConfigMap is applied by applyIngressConfig, not by Kustomize.
			projectedConfig := objectRef{
				gvk:       corev1.SchemeGroupVersion.WithKind("ConfigMap"),
				namespace: namespace,
				name:      NotebookControllerConfigMap,
			}
			checked := make(map[string]int)
			for _, component := range model.Components {
				if component.Name == "workbenches-operator" {
					continue // The operator's own resources aren't operand manifests.
				}
				for _, resource := range component.ManagedResources {
					ref := inventoryObjectRef(t, resource)
					if ref == projectedConfig {
						continue
					}
					checked[component.Name]++
					assertInventoryResource(t, rendered, ref, component.Name)
				}
			}
			for _, name := range []string{"odh-notebook-controller", "notebook-controller", "notebook-images"} {
				if checked[name] == 0 {
					t.Errorf("%s has no operand resources to check for %s", modelPath, name)
				}
			}
		})
	}
}

// V2 is not in the chaos profiles: these resources are expected only when v2
// is managed. Track stable names, not hash-suffixed generated ConfigMaps.
func TestManagedWorkbenchesV2Inventory(t *testing.T) {
	for _, target := range inventoryPlatforms {
		t.Run(target.profile, func(t *testing.T) {
			namespace := platform.DefaultApplicationsNamespace(target.platform)
			rendered := renderOperandInventory(t, target.platform, true)
			for _, resource := range []inventoryResource{
				{"apps/v1", "Deployment", "workspaces-controller", namespace},
				{"v1", "ServiceAccount", "workspaces-controller", namespace},
				{"v1", "Service", "workspaces-webhook-service", namespace},
				{"v1", "Service", "workspaces-controller-metrics-service", namespace},
				{"networking.k8s.io/v1", "NetworkPolicy", "workspaces-controller-allow-ports", namespace},
				{"networking.k8s.io/v1", "NetworkPolicy", "workspaces-controller-metrics-allow-ports", namespace},
				{"rbac.authorization.k8s.io/v1", "ClusterRole", "workspaces-manager-role", ""},
				{"rbac.authorization.k8s.io/v1", "ClusterRoleBinding", "workspaces-manager-rolebinding", ""},
				{"admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", "workspaces-validating-webhook-configuration", ""},
				{"apiextensions.k8s.io/v1", "CustomResourceDefinition", "workspaces.kubeflow.org", ""},
				{"apiextensions.k8s.io/v1", "CustomResourceDefinition", "workspacekinds.kubeflow.org", ""},
				{"kubeflow.org/v1beta1", "WorkspaceKind", "jupyterlab", ""},
				{"kubeflow.org/v1beta1", "WorkspaceKind", "codeserver", ""},
			} {
				assertInventoryResource(t, rendered, inventoryObjectRef(t, resource), "managed workbenches v2")
			}
		})
	}
}

func renderOperandInventory(t *testing.T, platformType string, v2Managed bool) map[objectRef]struct{} {
	t.Helper()

	workDir := t.TempDir()
	source := filepath.Join("..", "..", "opt", "manifests", "workbenches")
	if err := copyDir(source, filepath.Join(workDir, "workbenches")); err != nil {
		t.Fatal(err)
	}

	namespace := platform.DefaultApplicationsNamespace(platformType)
	rendered := make(map[objectRef]struct{})
	for _, group := range manifestGroupsForPlatform(platformType, v2Managed) {
		dir := filepath.Join(workDir, group)
		if !strings.Contains(group, "workspacekinds/") {
			if err := patchKustomizeNamespace(dir, namespace, logr.Discard()); err != nil {
				t.Fatalf("patch namespace for %s: %v", group, err)
			}
		}
		objects, err := renderKustomize(dir, nil)
		if err != nil {
			t.Fatalf("render %s: %v", group, err)
		}
		for _, obj := range prepareRenderedObjects(objects) {
			rendered[objectRefFrom(obj)] = struct{}{}
		}
	}

	return rendered
}

func inventoryObjectRef(t *testing.T, resource inventoryResource) objectRef {
	t.Helper()

	version, err := schema.ParseGroupVersion(resource.APIVersion)
	if err != nil {
		t.Fatalf("parse apiVersion %q for %s %s: %v", resource.APIVersion, resource.Kind, resource.Name, err)
	}

	return objectRef{gvk: version.WithKind(resource.Kind), namespace: resource.Namespace, name: resource.Name}
}

func assertInventoryResource(t *testing.T, rendered map[objectRef]struct{}, ref objectRef, component string) {
	t.Helper()

	if _, found := rendered[ref]; !found {
		t.Errorf("%s expects %s %q (namespace %q), but it is missing from the rendered operands", component, ref.gvk, ref.name, ref.namespace)
	}
}
