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

// Package releases loads upstream component release metadata for status reporting.
package releases

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
)

const (
	// ComponentMetadataFilename is the upstream manifest file that declares
	// installed component versions. See opendatahub-operator releases action.
	ComponentMetadataFilename = "component_metadata.yaml"

	// metadataRelativePath is the only upstream manifest root that ships
	// component_metadata.yaml. ODH-specific overlays (odh-notebook-controller,
	// notebooks) are internal packaging and do not publish meaningful upstream
	// release metadata for platform aggregation.
	metadataRelativePath = "workbenches/kf-notebook-controller"
)

type metadataDocument struct {
	Releases []componentsv1alpha1.ComponentRelease `yaml:"releases"`
}

// CollectWorkbenchesReleases reads the upstream component_metadata.yaml baked
// into the operator image and returns release entries for status.releases.
//
// A missing metadata file yields an empty slice. Parse errors fail the call.
func CollectWorkbenchesReleases(manifestsBasePath string) ([]componentsv1alpha1.ComponentRelease, error) {
	path := filepath.Join(manifestsBasePath, metadataRelativePath, ComponentMetadataFilename)

	return loadMetadataFile(path)
}

func loadMetadataFile(path string) ([]componentsv1alpha1.ComponentRelease, error) {
	data, err := os.ReadFile(path) //nolint:gosec // reading baked-in manifests from a known path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("reading metadata file %s: %w", path, err)
	}

	var doc metadataDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unmarshaling metadata file %s: %w", path, err)
	}

	releases := make([]componentsv1alpha1.ComponentRelease, 0, len(doc.Releases))
	for _, item := range doc.Releases {
		name := strings.TrimSpace(item.Name)
		version := strings.TrimSpace(item.Version)
		if name == "" || version == "" {
			continue
		}

		releases = append(releases, componentsv1alpha1.ComponentRelease{
			Name:    name,
			Version: version,
			RepoURL: item.RepoURL,
		})
	}

	return releases, nil
}
