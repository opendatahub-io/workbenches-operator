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
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
	"github.com/opendatahub-io/workbenches-operator/internal/platform"
)

const testDir = "/test"

var errSimulatedDeleteFailure = errors.New("simulated delete failure")

func TestManifestGroupsForPlatform(t *testing.T) {
	const wantKF = "workbenches/kf-notebook-controller/overlays/openshift"

	tests := []struct {
		name          string
		platformType  string
		wantNotebooks string
	}{
		{"OpenShift self-managed", platform.SelfManagedRhoai, "workbenches/notebooks/rhoai/base"},
		{"OpenDataHub", platform.OpenDataHub, "workbenches/notebooks/odh/base"},
		{"empty platform", "", "workbenches/notebooks/odh/base"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := manifestGroupsForPlatform(tt.platformType)
			if len(groups) != 3 {
				t.Fatalf("expected 3 groups, got %d", len(groups))
			}

			if groups[0] != wantKF {
				t.Errorf("kf group = %q, want %q", groups[0], wantKF)
			}

			if groups[1] != "workbenches/odh-notebook-controller/base" {
				t.Errorf("odh group = %q, want workbenches/odh-notebook-controller/base", groups[1])
			}

			if groups[2] != tt.wantNotebooks {
				t.Errorf("notebooks group = %q, want %q", groups[2], tt.wantNotebooks)
			}
		})
	}
}

func TestWriteParamsEnv(t *testing.T) {
	fSys := filesys.MakeFsInMemory()
	dir := testDir

	if err := fSys.Mkdir(dir); err != nil {
		t.Fatal(err)
	}

	params := map[string]string{
		"gateway-url":    "https://gw.example.com",
		"section-title":  "OpenShift AI",
		"mlflow-enabled": "true",
	}

	if err := writeParamsEnv(fSys, dir, params); err != nil {
		t.Fatalf("writeParamsEnv() error = %v", err)
	}

	data, err := fSys.ReadFile(filepath.Join(dir, "params.env"))
	if err != nil {
		t.Fatalf("reading params.env: %v", err)
	}

	content := string(data)

	for k, v := range params {
		expected := k + "=" + v
		if !strings.Contains(content, expected) {
			t.Errorf("params.env missing %q; got:\n%s", expected, content)
		}
	}

	if !strings.HasSuffix(content, "\n") {
		t.Error("params.env should end with a newline")
	}
}

func TestWriteParamsEnvMergesWithExisting(t *testing.T) {
	fSys := filesys.MakeFsInMemory()
	dir := testDir

	if err := fSys.Mkdir(dir); err != nil {
		t.Fatal(err)
	}

	existing := "odh-notebook-controller-image=quay.io/opendatahub/odh-notebook-controller:main\n" +
		"kube-rbac-proxy=quay.io/opendatahub/proxy:latest\n" +
		"gateway-url=\n" +
		"mlflow-enabled=false\n"

	if err := fSys.WriteFile(filepath.Join(dir, "params.env"), []byte(existing)); err != nil {
		t.Fatal(err)
	}

	params := map[string]string{
		"gateway-url":    "https://gw.example.com",
		"mlflow-enabled": "true",
	}

	if err := writeParamsEnv(fSys, dir, params); err != nil {
		t.Fatalf("writeParamsEnv() error = %v", err)
	}

	data, err := fSys.ReadFile(filepath.Join(dir, "params.env"))
	if err != nil {
		t.Fatalf("reading params.env: %v", err)
	}

	content := string(data)

	if !strings.Contains(content, "odh-notebook-controller-image=quay.io/opendatahub/odh-notebook-controller:main") {
		t.Error("existing image reference was not preserved")
	}

	if !strings.Contains(content, "kube-rbac-proxy=quay.io/opendatahub/proxy:latest") {
		t.Error("existing kube-rbac-proxy reference was not preserved")
	}

	if !strings.Contains(content, "gateway-url=https://gw.example.com") {
		t.Error("gateway-url was not overwritten")
	}

	if !strings.Contains(content, "mlflow-enabled=true") {
		t.Error("mlflow-enabled was not overwritten")
	}
}

func TestWriteParamsEnvEmpty(t *testing.T) {
	fSys := filesys.MakeFsInMemory()
	dir := testDir

	if err := fSys.Mkdir(dir); err != nil {
		t.Fatal(err)
	}

	if err := writeParamsEnv(fSys, dir, map[string]string{}); err != nil {
		t.Fatalf("writeParamsEnv() with empty params error = %v", err)
	}

	data, err := fSys.ReadFile(filepath.Join(dir, "params.env"))
	if err != nil {
		t.Fatalf("reading params.env: %v", err)
	}

	if string(data) != "\n" {
		t.Errorf("expected single newline for empty params, got %q", string(data))
	}
}

func TestWriteParamsEnvNewKeysDeterministicOrder(t *testing.T) {
	fSys := filesys.MakeFsInMemory()
	dir := testDir

	if err := fSys.Mkdir(dir); err != nil {
		t.Fatal(err)
	}

	params := map[string]string{
		"zebra":  "z",
		"alpha":  "a",
		"middle": "m",
		"beta":   "b",
	}

	if err := writeParamsEnv(fSys, dir, params); err != nil {
		t.Fatalf("writeParamsEnv() error = %v", err)
	}

	data, err := fSys.ReadFile(filepath.Join(dir, "params.env"))
	if err != nil {
		t.Fatalf("reading params.env: %v", err)
	}

	expected := "alpha=a\nbeta=b\nmiddle=m\nzebra=z\n"
	if string(data) != expected {
		t.Errorf("new keys not in sorted order\ngot:  %q\nwant: %q", string(data), expected)
	}
}

func TestWriteParamsEnvRejectsControlCharacters(t *testing.T) {
	fSys := filesys.MakeFsInMemory()
	dir := testDir

	if err := fSys.Mkdir(dir); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		params map[string]string
	}{
		{"newline in value", map[string]string{"key": "val\nMALICIOUS=injected"}},
		{"carriage return in value", map[string]string{"key": "val\rMALICIOUS=injected"}},
		{"newline in key", map[string]string{"bad\nkey": "value"}},
		{"carriage return in key", map[string]string{"bad\rkey": "value"}},
		{"equals in key", map[string]string{"bad=key": "value"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writeParamsEnv(fSys, dir, tt.params)
			if err == nil {
				t.Error("expected error for params containing control characters, got nil")
			}
		})
	}
}

func TestSetComponentLabels(t *testing.T) {
	t.Run("adds labels to unlabeled object", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		obj.SetName("test")

		setComponentLabels(obj)

		labels := obj.GetLabels()
		if labels[metadata.ComponentLabelKey] != metadata.LabelTrue {
			t.Errorf("expected %s=%s, got %s",
				metadata.ComponentLabelKey, metadata.LabelTrue, labels[metadata.ComponentLabelKey])
		}

		if labels[metadata.PartOfLabelKey] != metadata.ComponentLabelValue {
			t.Errorf("expected %s=%s, got %s",
				metadata.PartOfLabelKey, metadata.ComponentLabelValue, labels[metadata.PartOfLabelKey])
		}
	})

	t.Run("preserves existing labels", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		obj.SetLabels(map[string]string{"existing": "label"})

		setComponentLabels(obj)

		labels := obj.GetLabels()
		if labels["existing"] != "label" {
			t.Error("existing label was not preserved")
		}

		if labels[metadata.ComponentLabelKey] != metadata.LabelTrue {
			t.Error("component label was not set")
		}
	})
}

func TestIsNamespaced(t *testing.T) {
	tests := []struct {
		kind     string
		expected bool
	}{
		{"Deployment", true},
		{"Service", true},
		{"ConfigMap", true},
		{"ServiceAccount", true},
		{"Namespace", false},
		{"ClusterRole", false},
		{"ClusterRoleBinding", false},
		{"CustomResourceDefinition", false},
		{"MutatingWebhookConfiguration", false},
		{"ValidatingWebhookConfiguration", false},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			obj.Object = map[string]interface{}{
				"kind": tt.kind,
			}

			got := isNamespaced(obj)
			if got != tt.expected {
				t.Errorf("isNamespaced(%q) = %v, want %v", tt.kind, got, tt.expected)
			}
		})
	}
}

func TestRenderKustomize(t *testing.T) {
	dir := t.TempDir()

	deployYAML := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-controller
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: test
  template:
    metadata:
      labels:
        app: test
    spec:
      containers:
      - name: manager
        image: test:latest
`
	if err := os.WriteFile(filepath.Join(dir, "deployment.yaml"), []byte(deployYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	kustomizationYAML := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
`
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(kustomizationYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	objects, err := renderKustomize(dir, map[string]string{})
	if err != nil {
		t.Fatalf("renderKustomize() error = %v", err)
	}

	if len(objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objects))
	}

	obj := objects[0]
	if obj.GetKind() != "Deployment" {
		t.Errorf("expected kind Deployment, got %s", obj.GetKind())
	}

	if obj.GetName() != "test-controller" {
		t.Errorf("expected name test-controller, got %s", obj.GetName())
	}
}

func TestRenderKustomizeWithParams(t *testing.T) {
	dir := t.TempDir()

	cmYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: workbench-config
data: {}
`
	if err := os.WriteFile(filepath.Join(dir, "configmap.yaml"), []byte(cmYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	kustomizationYAML := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- configmap.yaml
configMapGenerator:
- name: workbench-params
  envs:
  - params.env
`
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(kustomizationYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	params := map[string]string{
		"section-title": "Red Hat OpenShift AI",
	}

	objects, err := renderKustomize(dir, params)
	if err != nil {
		t.Fatalf("renderKustomize() error = %v", err)
	}

	if len(objects) != 2 {
		t.Fatalf("expected 2 objects (configmap + generated configmap), got %d", len(objects))
	}

	var foundGeneratedCM bool

	for _, obj := range objects {
		if obj.GetKind() == "ConfigMap" && strings.HasPrefix(obj.GetName(), "workbench-params") {
			foundGeneratedCM = true

			data, ok, err := unstructured.NestedStringMap(obj.Object, "data")
			if err != nil || !ok {
				t.Fatal("generated ConfigMap missing data field")
			}

			if data["section-title"] != "Red Hat OpenShift AI" {
				t.Errorf("expected section-title=%q, got %q", "Red Hat OpenShift AI", data["section-title"])
			}
		}
	}

	if !foundGeneratedCM {
		t.Error("generated ConfigMap not found in rendered objects")
	}
}

func TestRenderKustomizeNonexistentDir(t *testing.T) {
	_, err := renderKustomize("/nonexistent/path", map[string]string{})
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestEnsureKustomization(t *testing.T) {
	t.Run("creates kustomization when none exists", func(t *testing.T) {
		dir := t.TempDir()

		if err := os.WriteFile(filepath.Join(dir, "deploy.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "service.yml"), []byte("apiVersion: v1\nkind: Service\nmetadata:\n  name: test\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		fSys := filesys.MakeFsOnDisk()
		if err := ensureKustomization(fSys, dir); err != nil {
			t.Fatalf("ensureKustomization() error = %v", err)
		}

		data, err := os.ReadFile(filepath.Join(dir, "kustomization.yaml")) //nolint:gosec // test reads from controlled temp dir
		if err != nil {
			t.Fatalf("reading generated kustomization.yaml: %v", err)
		}

		content := string(data)
		if !strings.Contains(content, "deploy.yaml") {
			t.Error("kustomization.yaml should reference deploy.yaml")
		}

		if !strings.Contains(content, "service.yml") {
			t.Error("kustomization.yaml should reference service.yml")
		}
	})

	t.Run("skips when kustomization.yaml already exists", func(t *testing.T) {
		dir := t.TempDir()

		existing := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n"
		if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}

		fSys := filesys.MakeFsOnDisk()
		if err := ensureKustomization(fSys, dir); err != nil {
			t.Fatalf("ensureKustomization() error = %v", err)
		}

		data, err := os.ReadFile(filepath.Join(dir, "kustomization.yaml")) //nolint:gosec // test reads from controlled temp dir
		if err != nil {
			t.Fatal(err)
		}

		if string(data) != existing {
			t.Error("existing kustomization.yaml should not be modified")
		}
	})
}

func TestRenderKustomizeMultipleResources(t *testing.T) {
	dir := t.TempDir()

	deployYAML := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ctrl
  template:
    metadata:
      labels:
        app: ctrl
    spec:
      containers:
      - name: mgr
        image: ctrl:v1
`
	saYAML := `apiVersion: v1
kind: ServiceAccount
metadata:
  name: controller-sa
`
	crYAML := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: controller-role
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get"]
`

	for name, content := range map[string]string{
		"deployment.yaml":     deployYAML,
		"serviceaccount.yaml": saYAML,
		"clusterrole.yaml":    crYAML,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	kustomizationYAML := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
- serviceaccount.yaml
- clusterrole.yaml
`
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(kustomizationYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	objects, err := renderKustomize(dir, map[string]string{})
	if err != nil {
		t.Fatalf("renderKustomize() error = %v", err)
	}

	if len(objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(objects))
	}

	kinds := make([]string, 0, len(objects))
	for _, obj := range objects {
		kinds = append(kinds, obj.GetKind())
	}

	sort.Strings(kinds)

	expected := []string{"ClusterRole", "Deployment", "ServiceAccount"}
	for i, k := range expected {
		if kinds[i] != k {
			t.Errorf("expected kind[%d]=%s, got %s", i, k, kinds[i])
		}
	}
}

func TestRenderKustomizeFromReadOnlySource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only directory semantics do not apply to root")
	}

	srcDir := t.TempDir()

	deployYAML := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: readonly-test
spec:
  replicas: 1
  selector:
    matchLabels:
      app: test
  template:
    metadata:
      labels:
        app: test
    spec:
      containers:
      - name: mgr
        image: test:latest
`
	if err := os.WriteFile(filepath.Join(srcDir, "deployment.yaml"), []byte(deployYAML), 0o400); err != nil {
		t.Fatal(err)
	}

	kustomizationYAML := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
`
	if err := os.WriteFile(filepath.Join(srcDir, "kustomization.yaml"), []byte(kustomizationYAML), 0o400); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(srcDir, 0o500); err != nil { //nolint:gosec // read+execute is intentional to simulate read-only /opt/manifests
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = os.Chmod(srcDir, 0o700) //nolint:gosec // restore permissions for t.TempDir cleanup
	})

	_, err := renderKustomize(srcDir, map[string]string{"section-title": "Test"})
	if err == nil {
		t.Fatal("expected error when rendering directly into read-only directory")
	}

	renderDir := t.TempDir()

	cpErr := copyDir(srcDir, renderDir)
	if cpErr != nil {
		t.Fatalf("copyDir() from read-only source failed: %v", cpErr)
	}

	objects, err := renderKustomize(renderDir, map[string]string{"section-title": "Test"})
	if err != nil {
		t.Fatalf("renderKustomize() on copied dir error = %v", err)
	}

	if len(objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objects))
	}

	if objects[0].GetName() != "readonly-test" {
		t.Errorf("expected name readonly-test, got %s", objects[0].GetName())
	}
}

func TestCopyDir(t *testing.T) {
	srcDir := t.TempDir()

	subDir := filepath.Join(srcDir, "sub")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(srcDir, "root.yaml"), []byte("root"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(subDir, "nested.yaml"), []byte("nested"), 0o600); err != nil {
		t.Fatal(err)
	}

	dstDir := filepath.Join(t.TempDir(), "copy")
	if err := copyDir(srcDir, dstDir); err != nil {
		t.Fatalf("copyDir() error = %v", err)
	}

	rootData, err := os.ReadFile(filepath.Join(dstDir, "root.yaml")) //nolint:gosec // test reads from controlled temp dir
	if err != nil {
		t.Fatalf("reading copied root.yaml: %v", err)
	}

	if string(rootData) != "root" {
		t.Errorf("expected 'root', got %q", string(rootData))
	}

	nestedData, err := os.ReadFile(filepath.Join(dstDir, "sub", "nested.yaml")) //nolint:gosec // test reads from controlled temp dir
	if err != nil {
		t.Fatalf("reading copied nested.yaml: %v", err)
	}

	if string(nestedData) != "nested" {
		t.Errorf("expected 'nested', got %q", string(nestedData))
	}
}

// TestRenderRealManifests validates that the manifest groups for every platform
// point to directories that exist and can be rendered by kustomize. It runs
// against the locally fetched manifests (opt/manifests/) and skips if they
// are not present (e.g. in CI without a prior `make manifests-fetch`).
func TestRenderRealManifests(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	basePath := filepath.Join(repoRoot, "opt", "manifests")

	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		t.Skip("opt/manifests not found — run 'make manifests-fetch' first")
	}

	platforms := []string{
		platform.OpenDataHub,
		platform.SelfManagedRhoai,
		"",
	}

	params := map[string]string{
		"section-title":  "Test",
		"mlflow-enabled": "false",
		"gateway-url":    "",
	}

	for _, p := range platforms {
		name := p
		if name == "" {
			name = "empty"
		}

		t.Run(name, func(t *testing.T) {
			groups := manifestGroupsForPlatform(p)

			workDir := t.TempDir()
			srcRoot := filepath.Join(basePath, "workbenches")
			dstRoot := filepath.Join(workDir, "workbenches")

			if err := copyDir(srcRoot, dstRoot); err != nil {
				t.Fatalf("copyDir() failed: %v", err)
			}

			for _, group := range groups {
				t.Run(filepath.Base(group), func(t *testing.T) {
					srcDir := filepath.Join(basePath, group)

					if _, statErr := os.Stat(srcDir); os.IsNotExist(statErr) {
						t.Fatalf("manifest group directory does not exist: %s", srcDir)
					}

					renderDir := filepath.Join(workDir, group)

					objects, err := renderKustomize(renderDir, params)
					if err != nil {
						t.Fatalf("renderKustomize(%s) failed: %v", group, err)
					}

					if len(objects) == 0 {
						t.Errorf("renderKustomize(%s) produced 0 objects", group)
					}

					t.Logf("rendered %d objects", len(objects))
				})
			}
		})
	}
}

func TestCleanupManagedResources(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))

	namespace := "test-cleanup-ns"

	labeledDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notebook-controller",
			Namespace: namespace,
			Labels: map[string]string{
				metadata.ComponentLabelKey: metadata.LabelTrue,
				metadata.PartOfLabelKey:    metadata.ComponentLabelValue,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "notebook-controller"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "notebook-controller"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}

	unlabeledDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-deployment",
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "other"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "other"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}

	labeledSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notebook-svc",
			Namespace: namespace,
			Labels: map[string]string{
				metadata.ComponentLabelKey: metadata.LabelTrue,
				metadata.PartOfLabelKey:    metadata.ComponentLabelValue,
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(labeledDeploy, unlabeledDeploy, labeledSvc).
		Build()

	reconciler := &WorkbenchesReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := context.Background()

	if err := reconciler.cleanupManagedResources(ctx, namespace); err != nil {
		t.Fatalf("cleanupManagedResources() error = %v", err)
	}

	// Labeled deployment should be gone
	deployList := &appsv1.DeploymentList{}
	if err := fakeClient.List(ctx, deployList, client.InNamespace(namespace)); err != nil {
		t.Fatalf("failed to list deployments: %v", err)
	}

	if len(deployList.Items) != 1 {
		t.Errorf("expected 1 remaining deployment, got %d", len(deployList.Items))
	}

	if len(deployList.Items) > 0 && deployList.Items[0].Name != "other-deployment" {
		t.Errorf("expected remaining deployment to be 'other-deployment', got %q", deployList.Items[0].Name)
	}

	// Labeled service should be gone
	svcList := &corev1.ServiceList{}
	if err := fakeClient.List(ctx, svcList, client.InNamespace(namespace)); err != nil {
		t.Fatalf("failed to list services: %v", err)
	}

	if len(svcList.Items) != 0 {
		t.Errorf("expected 0 services, got %d", len(svcList.Items))
	}
}

func TestReconcileDeleteReturnsErrorWhenCleanupFails(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(componentsv1alpha1.AddToScheme(scheme))

	namespace := "test-delete-cleanup-fail"
	deleteErr := errSimulatedDeleteFailure

	labeledDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "notebook-controller",
			Namespace: namespace,
			Labels: map[string]string{
				metadata.ComponentLabelKey: metadata.LabelTrue,
				metadata.PartOfLabelKey:    metadata.ComponentLabelValue,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "notebook-controller"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "notebook-controller"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(labeledDeploy).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				return deleteErr
			},
		}).
		Build()

	reconciler := &WorkbenchesReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	wb := &componentsv1alpha1.Workbenches{
		ObjectMeta: metav1.ObjectMeta{
			Name: componentsv1alpha1.WorkbenchesInstanceName,
		},
		Spec: componentsv1alpha1.WorkbenchesSpec{
			ManagementState:    "Managed",
			WorkbenchNamespace: namespace,
			Platform:           "OpenDataHub",
		},
	}
	controllerutil.AddFinalizer(wb, workbenchesFinalizer)

	ctx := context.Background()

	result, err := reconciler.reconcileDelete(ctx, wb)
	if err == nil {
		t.Fatal("expected reconcileDelete to return an error when cleanup fails, got nil")
	}

	if !strings.Contains(err.Error(), "simulated delete failure") {
		t.Errorf("expected error to contain 'simulated delete failure', got: %v", err)
	}

	if result.RequeueAfter != 0 {
		t.Error("expected no explicit requeue on error")
	}

	if !controllerutil.ContainsFinalizer(wb, workbenchesFinalizer) {
		t.Error("finalizer should not be removed when cleanup fails")
	}
}
