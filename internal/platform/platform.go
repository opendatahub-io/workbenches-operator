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

// Package platform provides platform type constants and helpers.
package platform

// Platform type constants matching the orchestrator's platform identity values.
const (
	OpenDataHub      = "OpenDataHub"
	SelfManagedRhoai = "SelfManagedRhoai"
)

// Default notebook namespace per platform.
const (
	DefaultNotebooksNamespaceODH   = "opendatahub"
	DefaultNotebooksNamespaceRHOAI = "rhods-notebooks"
)

// IsValid reports whether platformType is a recognized platform value.
func IsValid(platformType string) bool {
	switch platformType {
	case OpenDataHub, SelfManagedRhoai:
		return true
	default:
		return false
	}
}

// SectionTitle returns the UI section title based on platform type.
func SectionTitle(platformType string) string {
	titles := map[string]string{
		SelfManagedRhoai: "OpenShift Self Managed Services",
		OpenDataHub:      "OpenShift Open Data Hub",
	}

	if title, ok := titles[platformType]; ok {
		return title
	}

	return titles[OpenDataHub]
}

// DefaultNotebooksNamespace returns the default workbench namespace for the given platform.
func DefaultNotebooksNamespace(platformType string) string {
	switch platformType {
	case SelfManagedRhoai:
		return DefaultNotebooksNamespaceRHOAI
	default:
		return DefaultNotebooksNamespaceODH
	}
}
