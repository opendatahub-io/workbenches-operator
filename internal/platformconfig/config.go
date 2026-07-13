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

// Package platformconfig reads platform-managed configuration for module controllers.
package platformconfig

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
)

const (
	// ConfigMapName is the platform-managed ConfigMap for the workbenches module.
	ConfigMapName = "odh-workbenches-config"

	// VersionDataKey is the ConfigMap data key for the platform version handshake.
	VersionDataKey = "platformVersion"

	// ReleaseName is the status.releases entry name for the platform version handshake.
	ReleaseName = "platform"
)

var errApplicationsNamespaceNotConfigured = errors.New("applications namespace is not configured")

// ReadPlatformVersion returns data.platformVersion from odh-workbenches-config.
// A missing ConfigMap or key yields an empty string without error.
func ReadPlatformVersion(ctx context.Context, c client.Reader, namespace string) (string, error) {
	if namespace == "" {
		return "", errApplicationsNamespaceNotConfigured
	}

	cm := &corev1.ConfigMap{}

	err := c.Get(ctx, client.ObjectKey{Name: ConfigMapName, Namespace: namespace}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}

		return "", fmt.Errorf("reading ConfigMap %s/%s: %w", namespace, ConfigMapName, err)
	}

	if cm.Data == nil {
		return "", nil
	}

	return strings.TrimSpace(cm.Data[VersionDataKey]), nil
}

// GetPlatformRelease returns the platform handshake entry from status.releases.
func GetPlatformRelease(releases []componentsv1alpha1.ComponentRelease) componentsv1alpha1.ComponentRelease {
	for _, release := range releases {
		if release.Name == ReleaseName {
			return release
		}
	}

	return componentsv1alpha1.ComponentRelease{}
}

// SetPlatformRelease records the reconciled platform version in status.releases.
func SetPlatformRelease(releases *[]componentsv1alpha1.ComponentRelease, version string) {
	version = strings.TrimSpace(version)
	if version == "" {
		return
	}

	for i, release := range *releases {
		if release.Name == ReleaseName {
			(*releases)[i].Version = version

			return
		}
	}

	*releases = append(*releases, componentsv1alpha1.ComponentRelease{
		Name:    ReleaseName,
		Version: version,
	})
}

// MergeComponentReleases combines upstream component releases with the platform handshake entry.
func MergeComponentReleases(
	componentReleases []componentsv1alpha1.ComponentRelease,
	platformRelease componentsv1alpha1.ComponentRelease,
) []componentsv1alpha1.ComponentRelease {
	merged := make([]componentsv1alpha1.ComponentRelease, 0, len(componentReleases)+1)

	for _, release := range componentReleases {
		if release.Name == ReleaseName {
			continue
		}

		merged = append(merged, release)
	}

	if platformRelease.Name == ReleaseName && strings.TrimSpace(platformRelease.Version) != "" {
		merged = append(merged, platformRelease)
	}

	return merged
}

// HandshakeComplete reports whether the module has recorded the target platform version.
func HandshakeComplete(platformVersion string, releases []componentsv1alpha1.ComponentRelease) bool {
	platformVersion = strings.TrimSpace(platformVersion)
	if platformVersion == "" {
		return false
	}

	return GetPlatformRelease(releases).Version == platformVersion
}
