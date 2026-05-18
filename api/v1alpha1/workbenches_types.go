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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Workbenches component constants.
const (
	WorkbenchesComponentName = "workbenches"
	WorkbenchesInstanceName  = "default"
	WorkbenchesKind          = "Workbenches"
)

// WorkbenchesSpec defines the desired state of Workbenches.
type WorkbenchesSpec struct {
	// managementState indicates whether this component should be managed by the operator.
	// Valid values are "Managed" and "Removed".
	// +kubebuilder:default=Managed
	// +kubebuilder:validation:Enum=Managed;Removed
	ManagementState string `json:"managementState,omitempty"`

	// workbenchNamespace is the namespace where workbenches (Notebooks) are deployed.
	// This field is immutable after initial creation.
	// +kubebuilder:validation:Pattern="^([a-z0-9]([-a-z0-9]*[a-z0-9])?)?$"
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="workbenchNamespace is immutable"
	WorkbenchNamespace string `json:"workbenchNamespace,omitempty"`

	// gatewayDomain is the domain for the data science gateway.
	// Projected by the orchestrator from the platform GatewayConfig.
	// +kubebuilder:validation:MaxLength=253
	GatewayDomain string `json:"gatewayDomain,omitempty"`

	// platform identifies the platform type (OpenDataHub, SelfManagedRhoai).
	// Projected by the orchestrator.
	// +kubebuilder:validation:Enum=OpenDataHub;SelfManagedRhoai
	Platform string `json:"platform,omitempty"`

	// mlflowEnabled indicates whether the MLflow integration is active.
	// Projected by the orchestrator from DSC MLflowOperator state.
	MLflowEnabled bool `json:"mlflowEnabled,omitempty"`
}

// ComponentRelease tracks release metadata for a deployed component.
type ComponentRelease struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	RepoURL string `json:"repoUrl,omitempty"`
}

// WorkbenchesStatus defines the observed state of Workbenches.
type WorkbenchesStatus struct {
	// conditions represent the latest available observations of the component's state.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// releases tracks the deployed component versions.
	Releases []ComponentRelease `json:"releases,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase is the overall phase of the component ("Ready" or "Not Ready").
	Phase string `json:"phase,omitempty"`

	// workbenchNamespace reflects the active workbench namespace.
	WorkbenchNamespace string `json:"workbenchNamespace,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="Workbenches name must be default"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Ready"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,description="Reason"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,description="Phase"

// Workbenches is the Schema for the workbenches API.
type Workbenches struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkbenchesSpec   `json:"spec,omitempty"`
	Status WorkbenchesStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkbenchesList contains a list of Workbenches.
type WorkbenchesList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Workbenches `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workbenches{}, &WorkbenchesList{})
}
