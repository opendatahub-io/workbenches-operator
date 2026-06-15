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

package releases

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMetadataFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ComponentMetadataFilename)

	content := `releases:
  - name: Kubeflow Notebook Controller
    version: 1.10.0
    repoUrl: https://github.com/kubeflow/kubeflow
  - name: Empty Version Component
    version: "   "
    repoUrl: https://example.com/empty
  - name: "   "
    version: 2.0.0
    repoUrl: https://example.com/empty-name
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	got, err := loadMetadataFile(path)
	if err != nil {
		t.Fatalf("loadMetadataFile() error = %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("len(releases) = %d, want 1", len(got))
	}

	if got[0].Name != "Kubeflow Notebook Controller" {
		t.Fatalf("name = %q", got[0].Name)
	}

	if got[0].Version != "1.10.0" {
		t.Fatalf("version = %q", got[0].Version)
	}

	if got[0].RepoURL != "https://github.com/kubeflow/kubeflow" {
		t.Fatalf("repoUrl = %q", got[0].RepoURL)
	}
}

func TestLoadMetadataFileMissing(t *testing.T) {
	got, err := loadMetadataFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("loadMetadataFile() error = %v", err)
	}

	if got != nil {
		t.Fatalf("releases = %#v, want nil", got)
	}
}

func TestCollectWorkbenchesReleases(t *testing.T) {
	base := t.TempDir()

	writeMetadata(t, base, "workbenches/kf-notebook-controller", `releases:
  - name: Kubeflow Notebook Controller
    version: 1.10.0
    repoUrl: https://github.com/kubeflow/kubeflow
`)

	got, err := CollectWorkbenchesReleases(base)
	if err != nil {
		t.Fatalf("CollectWorkbenchesReleases() error = %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("len(releases) = %d, want 1", len(got))
	}
}

func TestCollectWorkbenchesReleasesEmptyWhenNoFiles(t *testing.T) {
	got, err := CollectWorkbenchesReleases(t.TempDir())
	if err != nil {
		t.Fatalf("CollectWorkbenchesReleases() error = %v", err)
	}

	if got != nil {
		t.Fatalf("releases = %#v, want nil", got)
	}
}

func writeMetadata(t *testing.T, base, root, content string) {
	t.Helper()

	dir := filepath.Join(base, root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	path := filepath.Join(dir, ComponentMetadataFilename)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
