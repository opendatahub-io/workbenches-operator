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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/kustomize/kyaml/yaml"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
	"github.com/opendatahub-io/workbenches-operator/internal/platform"
)

const fieldOwner = "workbenches-operator"

// manifestGroupsForPlatform returns the kustomize root paths (relative to
// ManifestsBasePath) for the given platform type. The OpenShift overlay is
// always used for kf-notebook-controller because the operator only runs on
// OpenShift (both ODH and RHOAI). The notebooks overlay varies by platform.
func manifestGroupsForPlatform(platformType string) []string {
	notebooksOverlay := "workbenches/notebooks/odh/base"

	if platformType == platform.SelfManagedRhoai {
		notebooksOverlay = "workbenches/notebooks/rhoai/base"
	}

	return []string{
		"workbenches/kf-notebook-controller/overlays/openshift",
		"workbenches/odh-notebook-controller/base",
		notebooksOverlay,
	}
}

// renderAndApply renders the upstream kustomize manifests with parameter injection
// and applies them to the cluster via Server-Side Apply with ForceOwnership.
// It copies manifests to a temp directory so the baked-in /opt/manifests stays immutable.
func (r *WorkbenchesReconciler) renderAndApply(ctx context.Context, params map[string]string, namespace string, platformType string) error {
	l := log.FromContext(ctx)

	workDir, err := os.MkdirTemp("", "workbenches-manifests-*")
	if err != nil {
		return fmt.Errorf("failed to create temp work directory: %w", err)
	}

	defer func() {
		if removeErr := os.RemoveAll(workDir); removeErr != nil {
			log.FromContext(ctx).Error(removeErr, "failed to remove temp manifest directory")
		}
	}()

	// Copy the entire manifests tree once so overlay relative paths (../../base) resolve.
	srcRoot := filepath.Join(r.ManifestsBasePath, "workbenches")
	dstRoot := filepath.Join(workDir, "workbenches")

	if err := copyDir(srcRoot, dstRoot); err != nil {
		return fmt.Errorf("failed to copy manifests tree: %w", err)
	}

	groups := manifestGroupsForPlatform(platformType)

	for _, group := range groups {
		renderDir := filepath.Join(workDir, group)

		if _, statErr := os.Stat(renderDir); os.IsNotExist(statErr) {
			l.V(1).Info("manifest directory not found, skipping", "path", renderDir)

			continue
		}

		if err := patchKustomizeNamespace(renderDir, namespace, l); err != nil {
			return fmt.Errorf("failed to patch kustomize namespace for %s: %w", group, err)
		}

		objects, err := renderKustomize(renderDir, params)
		if err != nil {
			return fmt.Errorf("failed to render manifests for %s: %w", group, err)
		}

		l.Info("rendered manifests", "group", group, "count", len(objects))

		if err := r.applyObjects(ctx, objects); err != nil {
			return fmt.Errorf("failed to apply manifests for %s: %w", group, err)
		}
	}

	return nil
}

// copyDir recursively copies src to dst, creating dst and all subdirectories.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		target := filepath.Join(dst, rel)

		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dst)+string(os.PathSeparator)) && filepath.Clean(target) != filepath.Clean(dst) {
			return fmt.Errorf("path traversal detected: %s escapes destination %s", target, dst)
		}

		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}

		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to copy symlink %s", path)
		}

		data, err := os.ReadFile(path) //nolint:gosec // reading baked-in manifests from a known path
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}

		return os.WriteFile(filepath.Clean(target), data, 0o600) //nolint:gosec // target is validated above to stay within dst
	})
}

// renderKustomize runs kustomize on a directory, injecting params.env values.
func renderKustomize(kustomizeDir string, params map[string]string) ([]*unstructured.Unstructured, error) {
	fSys := filesys.MakeFsOnDisk()

	if err := ensureKustomization(fSys, kustomizeDir); err != nil {
		return nil, fmt.Errorf("failed to ensure kustomization: %w", err)
	}

	if err := writeParamsEnv(fSys, kustomizeDir, params); err != nil {
		return nil, fmt.Errorf("failed to write params.env: %w", err)
	}

	opts := krusty.MakeDefaultOptions()
	opts.Reorder = krusty.ReorderOptionLegacy

	k := krusty.MakeKustomizer(opts)

	resMap, err := k.Run(fSys, kustomizeDir)
	if err != nil {
		return nil, fmt.Errorf("kustomize run failed for %s: %w", kustomizeDir, err)
	}

	objects := make([]*unstructured.Unstructured, 0, resMap.Size())

	for _, res := range resMap.Resources() {
		jsonBytes, err := res.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal resource %s: %w", res.OrgId(), err)
		}

		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(jsonBytes); err != nil {
			return nil, fmt.Errorf("failed to unmarshal resource: %w", err)
		}

		objects = append(objects, obj)
	}

	return objects, nil
}

// writeParamsEnv merges operator parameters into the existing params.env file.
// Existing keys are overwritten; keys not in params are preserved so that
// upstream image references and other defaults remain intact.
func writeParamsEnv(fSys filesys.FileSystem, kustomizeDir string, params map[string]string) error {
	paramsPath := filepath.Join(kustomizeDir, "params.env")

	existing := make(map[string]string)
	var orderedKeys []string

	if data, err := fSys.ReadFile(paramsPath); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}

			existing[k] = v
			orderedKeys = append(orderedKeys, k)
		}
	}

	var newKeys []string

	for k, v := range params {
		if strings.ContainsAny(k, "\n\r=") {
			return fmt.Errorf("params key contains invalid characters: %q", k)
		}

		if strings.ContainsAny(v, "\n\r") {
			return fmt.Errorf("params value for key %q contains invalid control characters", k)
		}

		if _, found := existing[k]; !found {
			newKeys = append(newKeys, k)
		}

		existing[k] = v
	}

	sort.Strings(newKeys)
	orderedKeys = append(orderedKeys, newKeys...)

	lines := make([]string, 0, len(orderedKeys))
	for _, k := range orderedKeys {
		lines = append(lines, k+"="+existing[k])
	}

	content := strings.Join(lines, "\n") + "\n"

	return fSys.WriteFile(paramsPath, []byte(content))
}

// applyObjects applies a set of unstructured objects to the cluster using Server-Side Apply.
// Namespace references are already set correctly by kustomize (via patchKustomizeNamespace).
func (r *WorkbenchesReconciler) applyObjects(ctx context.Context, objects []*unstructured.Unstructured) error {
	l := log.FromContext(ctx)

	for _, obj := range objects {
		setComponentLabels(obj)

		obj.SetManagedFields(nil)

		//nolint:staticcheck // client.Apply via Patch is the correct pattern for unstructured SSA
		err := r.Patch(ctx, obj,
			client.Apply,
			client.FieldOwner(fieldOwner),
			client.ForceOwnership,
		)
		if err != nil {
			l.Error(err, "SSA patch failed",
				"gvk", obj.GroupVersionKind(),
				"name", obj.GetName(),
				"namespace", obj.GetNamespace())

			return fmt.Errorf("failed to apply %s %s/%s: %w",
				obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}

		l.V(1).Info("applied resource",
			"kind", obj.GetKind(),
			"name", obj.GetName(),
			"namespace", obj.GetNamespace())
	}

	return nil
}

func setComponentLabels(obj *unstructured.Unstructured) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	labels[metadata.ComponentLabelKey] = metadata.LabelTrue
	labels[metadata.PartOfLabelKey] = metadata.ComponentLabelValue

	obj.SetLabels(labels)
}

var clusterScopedKinds = map[string]bool{
	"Namespace":                      true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"CustomResourceDefinition":       true,
	"MutatingWebhookConfiguration":   true,
	"ValidatingWebhookConfiguration": true,
}

func isNamespaced(obj *unstructured.Unstructured) bool {
	return !clusterScopedKinds[obj.GetKind()]
}

// patchKustomizeNamespace sets the namespace field in the kustomization file
// at the given directory. If the file already has a namespace field it is
// replaced; otherwise one is added. This lets kustomize's built-in namespace
// transformer handle ALL namespace references — including internal refs in
// ClusterRoleBinding subjects and webhook service configs — so the operator
// does not need to post-process them.
//
// Uses structured YAML parsing to avoid injection risks and to preserve
// nested "namespace:" fields (e.g. inside replacements selectors).
func patchKustomizeNamespace(dir string, namespace string, logger logr.Logger) error {
	kustomizationPath := findKustomizationFile(dir)
	if kustomizationPath == "" {
		return fmt.Errorf("no kustomization file found in %s — namespace %q would not be applied", dir, namespace)
	}

	data, err := os.ReadFile(kustomizationPath) //nolint:gosec // reading from operator-owned temp directory
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", kustomizationPath, err)
	}

	var kustomization map[string]any
	if unmarshalErr := sigyaml.Unmarshal(data, &kustomization); unmarshalErr != nil {
		return fmt.Errorf("failed to parse %s: %w", kustomizationPath, unmarshalErr)
	}

	oldNS, _ := kustomization["namespace"].(string)
	if oldNS == namespace {
		logger.Info("kustomization namespace already set, skipping patch",
			"file", kustomizationPath,
			"namespace", namespace)

		return nil
	}

	kustomization["namespace"] = namespace

	logger.Info("patching kustomization namespace",
		"file", kustomizationPath,
		"oldNamespace", oldNS,
		"newNamespace", namespace)

	out, marshalErr := sigyaml.Marshal(kustomization)
	if marshalErr != nil {
		return fmt.Errorf("failed to serialize %s: %w", kustomizationPath, marshalErr)
	}

	return os.WriteFile(kustomizationPath, out, 0o600)
}

func findKustomizationFile(dir string) string {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}

// cleanupGVKs lists the GroupVersionKinds of namespaced resources to clean up.
var cleanupGVKs = []schema.GroupVersionKind{
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "", Version: "v1", Kind: "ConfigMap"},
	{Group: "", Version: "v1", Kind: "Service"},
	{Group: "", Version: "v1", Kind: "ServiceAccount"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"},
	{Group: "image.openshift.io", Version: "v1", Kind: "ImageStream"},
}

// cleanupClusterGVKs lists the GroupVersionKinds of cluster-scoped resources to clean up.
var cleanupClusterGVKs = []schema.GroupVersionKind{
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"},
	{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "MutatingWebhookConfiguration"},
	{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration"},
	{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"},
}

// cleanupManagedResources deletes all resources that were applied by this operator,
// identified by the component labels. It cleans both namespaced and cluster-scoped resources.
// Cleanup is best-effort: all resource types are attempted even if some fail, and
// any errors are aggregated and returned at the end.
func (r *WorkbenchesReconciler) cleanupManagedResources(ctx context.Context, namespace string) error {
	l := log.FromContext(ctx)
	l.Info("cleaning up managed resources", "namespace", namespace)

	var errs []error

	componentLabel := client.MatchingLabels{
		metadata.ComponentLabelKey: metadata.LabelTrue,
		metadata.PartOfLabelKey:    metadata.ComponentLabelValue,
	}

	for _, gvk := range cleanupGVKs {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)

		if err := r.List(ctx, list, client.InNamespace(namespace), componentLabel); err != nil {
			if meta.IsNoMatchError(err) {
				l.Info("skipping GVK during cleanup (API not available)", "gvk", gvk)

				continue
			}

			errs = append(errs, fmt.Errorf("failed to list %s: %w", gvk, err))

			continue
		}

		for i := range list.Items {
			obj := &list.Items[i]
			l.Info("deleting resource", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace())

			if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
				errs = append(errs, fmt.Errorf("failed to delete %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err))
			}
		}
	}

	for _, gvk := range cleanupClusterGVKs {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)

		if err := r.List(ctx, list, componentLabel); err != nil {
			if meta.IsNoMatchError(err) {
				l.Info("skipping cluster GVK during cleanup (API not available)", "gvk", gvk)

				continue
			}

			errs = append(errs, fmt.Errorf("failed to list cluster %s: %w", gvk, err))

			continue
		}

		for i := range list.Items {
			obj := &list.Items[i]
			l.Info("deleting cluster resource", "kind", obj.GetKind(), "name", obj.GetName())

			if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
				errs = append(errs, fmt.Errorf("failed to delete %s %s: %w", obj.GetKind(), obj.GetName(), err))
			}
		}
	}

	l.Info("managed resources cleanup complete")

	return errors.Join(errs...)
}

// ensureKustomization creates a minimal kustomization.yaml if one does not exist,
// pointing to all YAML files in the directory. This handles upstream directories
// that rely on being included as bases rather than standalone kustomize roots.
func ensureKustomization(fSys filesys.FileSystem, dir string) error {
	kustomizationPath := filepath.Join(dir, "kustomization.yaml")
	if fSys.Exists(kustomizationPath) {
		return nil
	}

	kustomizationPath = filepath.Join(dir, "kustomization.yml")
	if fSys.Exists(kustomizationPath) {
		return nil
	}

	kustomizationPath = filepath.Join(dir, "Kustomization")
	if fSys.Exists(kustomizationPath) {
		return nil
	}

	kustomization := types.Kustomization{
		TypeMeta: types.TypeMeta{
			APIVersion: types.KustomizationVersion,
			Kind:       types.KustomizationKind,
		},
	}

	entries, err := fSys.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", dir, err)
	}

	for _, name := range entries {
		if !fSys.IsDir(filepath.Join(dir, name)) && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			kustomization.Resources = append(kustomization.Resources, name)
		}
	}

	node, err := yaml.FromMap(map[string]any{
		"apiVersion": kustomization.APIVersion,
		"kind":       kustomization.Kind,
		"resources":  kustomization.Resources,
	})
	if err != nil {
		return fmt.Errorf("failed to build kustomization node: %w", err)
	}

	content, err := node.String()
	if err != nil {
		return fmt.Errorf("failed to serialize kustomization: %w", err)
	}

	return fSys.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(content))
}
