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
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
)

const (
	notebookControllerSettingsConfigMap = "notebook-controller-setting-config"
	workbenchProbeTimeoutKey            = "WORKBENCH_PROBE_TIMEOUT_SECONDS"
)

// configureWorkbenchProbes projects the CR policy into the notebook controller's
// runtime settings after rendering. The settings ConfigMap is not included in all
// upstream bundles, so create a desired object when necessary. It goes through the
// same ownership, SSA, drift reconciliation and garbage collection as other operands.
func configureWorkbenchProbes(
	objects []*unstructured.Unstructured,
	probes *componentsv1alpha1.ODHNotebookControllerProbeSpec,
	namespace string,
) ([]*unstructured.Unstructured, error) {
	for _, obj := range objects {
		if obj.GetAPIVersion() != "v1" || obj.GetKind() != "ConfigMap" ||
			obj.GetName() != notebookControllerSettingsConfigMap || obj.GetNamespace() != namespace {
			continue
		}

		if probes == nil {
			unstructured.RemoveNestedField(obj.Object, "data", workbenchProbeTimeoutKey)
		} else if err := unstructured.SetNestedField(obj.Object,
			strconv.FormatInt(int64(probes.Timeout), 10), "data", workbenchProbeTimeoutKey); err != nil {
			return nil, fmt.Errorf("setting %s: %w", workbenchProbeTimeoutKey, err)
		}

		return objects, nil
	}

	if probes != nil {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name":      notebookControllerSettingsConfigMap,
				"namespace": namespace,
			},
			"data": map[string]any{
				workbenchProbeTimeoutKey: strconv.FormatInt(int64(probes.Timeout), 10),
			},
		}})
	}

	return objects, nil
}
