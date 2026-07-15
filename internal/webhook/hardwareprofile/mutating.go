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

// Package hardwareprofile implements the mutating webhook for HardwareProfile injection into Notebooks.
// This is a faithful port of the opendatahub-operator webhook, scoped to Notebooks only
// (InferenceService/LLMInferenceService are managed by the KServe component).
// The HardwareProfile CR is accessed as unstructured to avoid importing the opendatahub-operator API.
package hardwareprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"

	admissionv1 "k8s.io/api/admission/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/opendatahub-io/workbenches-operator/internal/gvk"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
)

const specField = "spec"

var (
	notebookContainersPath   = []string{specField, "template", specField, "containers"}
	notebookNodeSelectorPath = []string{specField, "template", specField, "nodeSelector"}
	notebookTolerationsPath  = []string{specField, "template", specField, "tolerations"}
)

//+kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=hardwareprofiles,verbs=get
//+kubebuilder:webhook:path=/workbenches-hardware-profile,mutating=true,failurePolicy=fail,timeoutSeconds=5,groups=kubeflow.org,resources=notebooks,verbs=create;update,versions=v1,name=hardwareprofile-notebook-injector.opendatahub.io,sideEffects=None,admissionReviewVersions=v1

// Injector implements a mutating admission webhook for hardware profile injection
// into Notebook resources.
type Injector struct {
	Client  client.Reader
	Decoder admission.Decoder
	Name    string
}

var _ admission.Handler = &Injector{}

// SetupWithManager registers the webhook with the manager.
func (i *Injector) SetupWithManager(mgr ctrl.Manager) error {
	hookServer := mgr.GetWebhookServer()
	hookServer.Register("/workbenches-hardware-profile", &webhook.Admission{
		Handler: i,
	})

	return nil
}

// Handle processes admission requests for Notebook create/update operations.
func (i *Injector) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := logf.FromContext(ctx)

	if req.Operation != admissionv1.Create && req.Operation != admissionv1.Update {
		return admission.Allowed(fmt.Sprintf("Operation %s on %s allowed", req.Operation, req.Kind.Kind))
	}

	if i.Decoder == nil {
		log.Error(nil, "Decoder is nil - webhook not properly initialized")

		return admission.Errored(http.StatusInternalServerError, errors.New("webhook decoder not initialized"))
	}

	notebook := &unstructured.Unstructured{}
	if err := i.Decoder.Decode(req, notebook); err != nil {
		log.Error(err, "failed to decode object")

		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to decode object: %w", err))
	}

	if !notebook.GetDeletionTimestamp().IsZero() {
		return admission.Allowed("Object marked for deletion, skipping hardware profile injection")
	}

	return i.performHardwareProfileInjection(ctx, &req, notebook)
}

func (i *Injector) performHardwareProfileInjection(
	ctx context.Context,
	req *admission.Request,
	obj *unstructured.Unstructured,
) admission.Response {
	log := logf.FromContext(ctx)

	profileName := getAnnotation(obj, metadata.HardwareProfileNameAnnotation)
	if profileName == "" {
		if req.Operation == admissionv1.Update {
			if resp := i.handleHWPRemoval(ctx, req, obj); resp != nil {
				return *resp
			}
		}

		return admission.Allowed("No hardware profile annotation found")
	}

	profileNamespace := getAnnotation(obj, metadata.HardwareProfileNamespaceAnnotation)
	if profileNamespace == "" {
		profileNamespace = obj.GetNamespace()
	}

	if profileNamespace == "" {
		return admission.Errored(http.StatusBadRequest, errors.New("unable to determine hardware profile namespace"))
	}

	hwp, err := i.fetchHardwareProfile(ctx, profileNamespace, profileName)
	if err != nil {
		if k8serr.IsNotFound(err) {
			log.V(1).Info("Hardware profile not found", "profile", profileName, "namespace", profileNamespace, "request", req.Name)

			if req.Operation == admissionv1.Update && i.isStaleProfileReference(req, profileName) {
				return admission.Allowed(
					fmt.Sprintf("hardware profile '%s' not found in namespace '%s'; allowing update with warning",
						profileName, profileNamespace)).
					WithWarnings(
						fmt.Sprintf("Referenced hardware profile '%s' in namespace '%s' no longer exists. "+
							"Profile settings were not applied. Remove or update the annotation to clear this warning.",
							profileName, profileNamespace))
			}

			return admission.Errored(http.StatusBadRequest,
				fmt.Errorf("hardware profile '%s' not found in namespace '%s'", profileName, profileNamespace))
		}

		log.Error(err, "Failed to get hardware profile", "profile", profileName, "namespace", profileNamespace)

		if req.Operation == admissionv1.Update && i.isStaleProfileReference(req, profileName) {
			return admission.Allowed(
				fmt.Sprintf("failed to fetch hardware profile '%s'; allowing update with warning",
					profileName)).
				WithWarnings(
					fmt.Sprintf("Failed to fetch hardware profile '%s' from namespace '%s': %v. "+
						"Profile settings were not applied.", profileName, profileNamespace, err))
		}

		return admission.Errored(http.StatusInternalServerError,
			fmt.Errorf("failed to get hardware profile '%s' from namespace '%s': %w", profileName, profileNamespace, err))
	}

	if getAnnotation(obj, metadata.HardwareProfileNamespaceAnnotation) == "" {
		setAnnotation(obj, metadata.HardwareProfileNamespaceAnnotation, profileNamespace)
	}

	if validateErr := i.validateContainerNames(obj); validateErr != nil {
		warningMsg := fmt.Sprintf("Hardware profile '%s' was not applied: %s. "+
			"All hardware profile settings (identifiers, scheduling, etc.) are skipped.",
			profileName, validateErr.Error())

		log.Info("skipping all hardware profile application due to container name mismatch",
			"workload", obj.GetName(), "namespace", obj.GetNamespace(),
			"hardwareProfile", profileName)

		marshaledObj, marshalErr := json.Marshal(obj)
		if marshalErr != nil {
			log.Error(marshalErr, "Failed to marshal object after container validation, admitting anyway")
			resp := admission.Allowed("Admitted but marshal failed after validation")
			resp.Warnings = []string{warningMsg, "Internal error: failed to marshal object"}

			return resp
		}

		resp := admission.PatchResponseFromRaw(req.Object.Raw, marshaledObj)
		resp.Warnings = []string{warningMsg}

		return resp
	}

	profileChanged := i.detectProfileChange(req, profileName, profileNamespace)
	if profileChanged {
		log.V(1).Info("hardware profile changed, will clear existing scheduling settings",
			"workload", obj.GetName(), "newProfile", profileName, "newNamespace", profileNamespace)
	}

	warnings, err := i.applyHardwareProfileToNotebook(ctx, obj, hwp, profileChanged)
	if err != nil {
		log.Error(err, "Failed to apply hardware profile", "profile", profileName)

		return admission.Errored(http.StatusInternalServerError, err)
	}

	marshaledObj, err := json.Marshal(obj)
	if err != nil {
		log.Error(err, "Failed to marshal modified object")

		return admission.Errored(http.StatusInternalServerError, err)
	}

	resp := admission.PatchResponseFromRaw(req.Object.Raw, marshaledObj)
	if len(warnings) > 0 {
		resp.Warnings = warnings
		log.V(1).Info("admission response includes warnings", "warnings", warnings)
	}

	return resp
}

func (i *Injector) detectProfileChange(req *admission.Request, newProfileName, newProfileNamespace string) bool {
	if req.Operation == admissionv1.Create {
		return false
	}

	if req.OldObject.Raw == nil {
		return true
	}

	oldObj := &unstructured.Unstructured{}
	if err := json.Unmarshal(req.OldObject.Raw, oldObj); err != nil {
		return true
	}

	oldProfileName := getAnnotation(oldObj, metadata.HardwareProfileNameAnnotation)
	if oldProfileName == "" {
		return false
	}

	oldProfileNamespace := getAnnotation(oldObj, metadata.HardwareProfileNamespaceAnnotation)
	if oldProfileNamespace == "" {
		oldProfileNamespace = oldObj.GetNamespace()
	}

	return oldProfileName != newProfileName || oldProfileNamespace != newProfileNamespace
}

func (i *Injector) validateContainerNames(obj *unstructured.Unstructured) error {
	containers, found, err := unstructured.NestedSlice(obj.Object, notebookContainersPath...)
	if err != nil {
		return fmt.Errorf("failed to access containers: %w", err)
	}

	if !found || len(containers) == 0 {
		return nil
	}

	if len(containers) == 1 {
		return nil
	}

	expectedName := obj.GetName()
	for _, c := range containers {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}

		name, _ := m["name"].(string)
		if name == expectedName {
			return nil
		}
	}

	return fmt.Errorf("no matching main container found in Notebook '%s/%s': expected container name '%s'",
		obj.GetNamespace(), obj.GetName(), expectedName)
}

func (i *Injector) handleHWPRemoval(
	ctx context.Context,
	req *admission.Request,
	obj *unstructured.Unstructured,
) *admission.Response {
	log := logf.FromContext(ctx)

	if req.OldObject.Raw == nil {
		return nil
	}

	oldObj := &unstructured.Unstructured{}
	if err := json.Unmarshal(req.OldObject.Raw, oldObj); err != nil {
		return nil
	}

	oldProfileName := getAnnotation(oldObj, metadata.HardwareProfileNameAnnotation)
	if oldProfileName == "" {
		return nil
	}

	log.V(1).Info("HWP annotation removed, cleaning up HWP-applied settings",
		"workload", obj.GetName(), "oldProfile", oldProfileName)

	oldProfileNamespace := getAnnotation(oldObj, metadata.HardwareProfileNamespaceAnnotation)
	if oldProfileNamespace == "" {
		oldProfileNamespace = oldObj.GetNamespace()
	}

	oldHWP, err := i.fetchHardwareProfile(ctx, oldProfileNamespace, oldProfileName)
	if err != nil {
		log.V(1).Info("Could not fetch old HWP for cleanup, HWP-applied settings may remain",
			"error", err, "oldProfile", oldProfileName, "oldNamespace", oldProfileNamespace)

		removeAnnotation(obj, metadata.HardwareProfileNamespaceAnnotation)

		marshaledObj, marshalErr := json.Marshal(obj)
		if marshalErr != nil {
			return nil
		}

		resp := admission.PatchResponseFromRaw(req.Object.Raw, marshaledObj)

		return &resp
	}

	if removeErr := i.removeHWPSettings(obj, oldHWP); removeErr != nil {
		log.Error(removeErr, "Failed to remove HWP settings")
		resp := admission.Errored(http.StatusInternalServerError, removeErr)

		return &resp
	}

	removeAnnotation(obj, metadata.HardwareProfileNamespaceAnnotation)

	marshaledObj, err := json.Marshal(obj)
	if err != nil {
		log.Error(err, "Failed to marshal modified object")
		resp := admission.Errored(http.StatusInternalServerError, err)

		return &resp
	}

	resp := admission.PatchResponseFromRaw(req.Object.Raw, marshaledObj)

	return &resp
}

func (i *Injector) removeHWPSettings(obj, hwp *unstructured.Unstructured) error {
	nodeSelector, nsFound, _ := unstructured.NestedStringMap(hwp.Object, "spec", "scheduling", "node", "nodeSelector")
	if nsFound && len(nodeSelector) > 0 {
		if err := removeHWPNodeSelector(obj, notebookNodeSelectorPath, nodeSelector); err != nil {
			return fmt.Errorf("failed to remove HWP nodeSelector: %w", err)
		}
	}

	tolerations, tolFound, _ := unstructured.NestedSlice(hwp.Object, "spec", "scheduling", "node", "tolerations")
	if tolFound && len(tolerations) > 0 {
		if err := removeHWPTolerations(obj, notebookTolerationsPath, tolerations); err != nil {
			return fmt.Errorf("failed to remove HWP tolerations: %w", err)
		}
	}

	return nil
}

func removeHWPNodeSelector(obj *unstructured.Unstructured, nodeSelectorPath []string, hwpNodeSelector map[string]string) error {
	existingNodeSelector, found, err := unstructured.NestedStringMap(obj.Object, nodeSelectorPath...)
	if err != nil {
		return err
	}

	if !found || len(existingNodeSelector) == 0 {
		return nil
	}

	for key, value := range hwpNodeSelector {
		if existingValue, exists := existingNodeSelector[key]; exists && existingValue == value {
			delete(existingNodeSelector, key)
		}
	}

	if len(existingNodeSelector) == 0 {
		unstructured.RemoveNestedField(obj.Object, nodeSelectorPath...)
	} else {
		if err := unstructured.SetNestedStringMap(obj.Object, existingNodeSelector, nodeSelectorPath...); err != nil {
			return err
		}
	}

	return nil
}

func removeHWPTolerations(obj *unstructured.Unstructured, tolerationsPath []string, hwpTolerations []any) error {
	existingTolerations, found, err := unstructured.NestedSlice(obj.Object, tolerationsPath...)
	if err != nil {
		return err
	}

	if !found || len(existingTolerations) == 0 {
		return nil
	}

	hwpTolKeys := make(map[string]bool)
	for _, tol := range hwpTolerations {
		if tolMap, ok := tol.(map[string]any); ok {
			hwpTolKeys[TolerationKey(tolMap)] = true
		}
	}

	remaining := make([]any, 0, len(existingTolerations))
	for _, existing := range existingTolerations {
		if existingMap, ok := existing.(map[string]any); ok {
			if !hwpTolKeys[TolerationKey(existingMap)] {
				remaining = append(remaining, existing)
			}
		}
	}

	if len(remaining) == 0 {
		unstructured.RemoveNestedField(obj.Object, tolerationsPath...)
	} else {
		if err := unstructured.SetNestedSlice(obj.Object, remaining, tolerationsPath...); err != nil {
			return err
		}
	}

	return nil
}

func (i *Injector) fetchHardwareProfile(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	hwp := &unstructured.Unstructured{}
	hwp.SetGroupVersionKind(gvk.HardwareProfile)

	if err := i.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, hwp); err != nil {
		return nil, err
	}

	return hwp, nil
}

// isStaleProfileReference checks whether the old object already referenced the same
// hardware profile. If true, the profile was deleted after being applied — the user
// shouldn't be blocked from editing their notebook because of a stale reference.
// If false, the user is actively switching to a non-existent profile, which should be denied.
func (i *Injector) isStaleProfileReference(req *admission.Request, currentProfileName string) bool {
	if req.OldObject.Raw == nil {
		return false
	}

	oldObj := &unstructured.Unstructured{}
	if err := json.Unmarshal(req.OldObject.Raw, oldObj); err != nil {
		return false
	}

	oldProfileName := getAnnotation(oldObj, metadata.HardwareProfileNameAnnotation)

	return oldProfileName == currentProfileName
}

func (i *Injector) applyHardwareProfileToNotebook(
	ctx context.Context,
	notebook, hwp *unstructured.Unstructured,
	profileChanged bool,
) ([]string, error) {
	log := logf.FromContext(ctx)

	var warnings []string

	if profileChanged {
		log.V(1).Info("clearing existing scheduling settings due to profile change",
			"workload", notebook.GetName(), "hardwareProfile", hwp.GetName())

		unstructured.RemoveNestedField(notebook.Object, notebookNodeSelectorPath...)
		unstructured.RemoveNestedField(notebook.Object, notebookTolerationsPath...)
	}

	identifiers, idFound, _ := unstructured.NestedSlice(hwp.Object, "spec", "identifiers")
	if idFound && len(identifiers) > 0 {
		if err := applyResourcesToNotebookContainer(ctx, identifiers, notebook, profileChanged); err != nil {
			return nil, fmt.Errorf("failed to apply resource requirements: %w", err)
		}
	}

	nodeSelector, nsFound, _ := unstructured.NestedStringMap(hwp.Object, "spec", "scheduling", "node", "nodeSelector")
	if nsFound && len(nodeSelector) > 0 {
		nsWarnings, err := applyNodeSelector(notebook, nodeSelector, profileChanged, hwp.GetName())
		if err != nil {
			return nil, err
		}

		warnings = append(warnings, nsWarnings...)
	}

	tolerations, tolFound, _ := unstructured.NestedSlice(hwp.Object, "spec", "scheduling", "node", "tolerations")
	if tolFound && len(tolerations) > 0 {
		if err := applyTolerations(notebook, tolerations, profileChanged); err != nil {
			return nil, err
		}
	}

	return warnings, nil
}

func applyNodeSelector(notebook *unstructured.Unstructured, nodeSelector map[string]string, profileChanged bool, hwpName string) ([]string, error) {
	if profileChanged {
		if err := unstructured.SetNestedStringMap(notebook.Object, nodeSelector, notebookNodeSelectorPath...); err != nil {
			return nil, fmt.Errorf("failed to set nodeSelector: %w", err)
		}

		return nil, nil
	}

	mergedNS, nsWarnings, err := mergeNodeSelector(notebook, notebookNodeSelectorPath, nodeSelector, hwpName)
	if err != nil {
		return nil, fmt.Errorf("failed to merge nodeSelector: %w", err)
	}

	if err := unstructured.SetNestedStringMap(notebook.Object, mergedNS, notebookNodeSelectorPath...); err != nil {
		return nil, fmt.Errorf("failed to set merged nodeSelector: %w", err)
	}

	return nsWarnings, nil
}

func applyTolerations(notebook *unstructured.Unstructured, tolerations []any, profileChanged bool) error {
	if profileChanged {
		if err := unstructured.SetNestedSlice(notebook.Object, tolerations, notebookTolerationsPath...); err != nil {
			return fmt.Errorf("failed to set tolerations: %w", err)
		}

		return nil
	}

	mergedTol, err := mergeTolerations(notebook, notebookTolerationsPath, tolerations)
	if err != nil {
		return fmt.Errorf("failed to merge tolerations: %w", err)
	}

	if err := unstructured.SetNestedSlice(notebook.Object, mergedTol, notebookTolerationsPath...); err != nil {
		return fmt.Errorf("failed to set merged tolerations: %w", err)
	}

	return nil
}

func applyResourcesToNotebookContainer(ctx context.Context, identifiers []any, notebook *unstructured.Unstructured, profileChanged bool) error {
	log := logf.FromContext(ctx)

	containers, found, err := unstructured.NestedSlice(notebook.Object, notebookContainersPath...)
	if err != nil {
		return err
	}
	if !found || len(containers) == 0 {
		return nil
	}

	mainIdx := notebookMainContainerIndex(containers, notebook.GetName())
	if mainIdx < 0 {
		log.Info("No matching main container found; skipping HWP resource injection",
			"workload", notebook.GetName(), "namespace", notebook.GetNamespace())

		return nil
	}

	container, ok := containers[mainIdx].(map[string]any)
	if !ok {
		return errors.New("container is not a map[string]any")
	}

	if profileChanged {
		delete(container, "resources")
	}

	if err := applyIdentifiersToContainer(container, identifiers); err != nil {
		return err
	}

	containers[mainIdx] = container

	return unstructured.SetNestedSlice(notebook.Object, containers, notebookContainersPath...)
}

func notebookMainContainerIndex(containers []any, notebookName string) int {
	if len(containers) == 1 {
		return 0
	}

	for idx, c := range containers {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}

		name, _ := m["name"].(string)
		if name == notebookName {
			return idx
		}
	}

	return -1
}

func applyIdentifiersToContainer(container map[string]any, identifiers []any) error {
	resourcesMap, err := getOrCreateNestedMap(container, "resources")
	if err != nil {
		return err
	}

	requests, err := getOrCreateNestedMap(resourcesMap, "requests")
	if err != nil {
		return err
	}

	limits, err := getOrCreateNestedMap(resourcesMap, "limits")
	if err != nil {
		return err
	}

	for _, id := range identifiers {
		idMap, ok := id.(map[string]any)
		if !ok {
			continue
		}

		identifier, _, _ := unstructured.NestedString(idMap, "identifier")
		if identifier == "" {
			continue
		}

		defaultCount, _, _ := unstructured.NestedFieldNoCopy(idMap, "defaultCount")
		if defaultCount == nil {
			continue
		}

		quantity, err := parseQuantityValue(defaultCount)
		if err != nil {
			return fmt.Errorf("failed to convert resource quantity for %s: %w", identifier, err)
		}

		if _, exists := requests[identifier]; !exists {
			requests[identifier] = quantity.String()
		}

		if _, exists := limits[identifier]; !exists {
			if reqVal, reqExists := requests[identifier]; reqExists {
				limits[identifier] = reqVal
			}
		}
	}

	resourcesMap["requests"] = requests
	resourcesMap["limits"] = limits
	container["resources"] = resourcesMap

	return nil
}

func mergeNodeSelector(
	obj *unstructured.Unstructured,
	nodeSelectorPath []string,
	hwpNodeSelector map[string]string,
	hwpName string,
) (map[string]string, []string, error) {
	existingNodeSelector, _, err := unstructured.NestedStringMap(obj.Object, nodeSelectorPath...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get existing nodeSelector: %w", err)
	}

	var warnings []string

	merged := make(map[string]string)
	maps.Copy(merged, existingNodeSelector)

	for k, v := range hwpNodeSelector {
		if existingValue, exists := existingNodeSelector[k]; exists && existingValue != v {
			warnings = append(warnings, fmt.Sprintf(
				"nodeSelector key '%s' has value '%s' which will be overwritten by HardwareProfile '%s' which has value '%s'",
				k, existingValue, hwpName, v))
		}

		merged[k] = v
	}

	return merged, warnings, nil
}

func mergeTolerations(
	obj *unstructured.Unstructured,
	tolerationsPath []string,
	hwpTolerations []any,
) ([]any, error) {
	existingTolerations, _, err := unstructured.NestedSlice(obj.Object, tolerationsPath...)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing tolerations: %w", err)
	}

	hwpTolKeys := make(map[string]bool)
	for _, tol := range hwpTolerations {
		if tolMap, ok := tol.(map[string]any); ok {
			hwpTolKeys[TolerationKey(tolMap)] = true
		}
	}

	merged := make([]any, 0, len(hwpTolerations)+len(existingTolerations))
	merged = append(merged, hwpTolerations...)

	for _, existing := range existingTolerations {
		if existingMap, ok := existing.(map[string]any); ok {
			if !hwpTolKeys[TolerationKey(existingMap)] {
				merged = append(merged, existing)
			}
		}
	}

	return merged, nil
}

// TolerationKey generates a unique key for a toleration for comparison.
func TolerationKey(tol map[string]any) string {
	key, _ := tol["key"].(string)
	operator, _ := tol["operator"].(string)
	value, _ := tol["value"].(string)
	effect, _ := tol["effect"].(string)

	ts := ""
	if v, ok := tol["tolerationSeconds"]; ok {
		ts = fmt.Sprintf("%v", v)
	}

	return fmt.Sprintf("%s:%s:%s:%s:%s", key, operator, value, effect, ts)
}

func parseQuantityValue(val any) (resource.Quantity, error) {
	switch v := val.(type) {
	case string:
		return resource.ParseQuantity(v)
	case int64:
		return *resource.NewQuantity(v, resource.DecimalSI), nil
	case float64:
		return resource.ParseQuantity(strconv.FormatFloat(v, 'f', -1, 64))
	default:
		return resource.ParseQuantity(fmt.Sprintf("%v", v))
	}
}

func getOrCreateNestedMap(obj map[string]any, field string) (map[string]any, error) {
	nested, found, err := unstructured.NestedMap(obj, field)
	if err != nil {
		return nil, fmt.Errorf("failed to get nested map for field %s: %w", field, err)
	}

	if !found {
		nested = make(map[string]any)
	}

	return nested, nil
}

func getAnnotation(obj *unstructured.Unstructured, key string) string {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return ""
	}

	return annotations[key]
}

func setAnnotation(obj *unstructured.Unstructured, key, value string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	annotations[key] = value
	obj.SetAnnotations(annotations)
}

func removeAnnotation(obj *unstructured.Unstructured, key string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return
	}

	delete(annotations, key)
	obj.SetAnnotations(annotations)
}
