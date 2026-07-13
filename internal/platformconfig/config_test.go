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

package platformconfig

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
)

func TestReadPlatformVersion(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName,
			Namespace: "opendatahub",
		},
		Data: map[string]string{
			VersionDataKey: " 2.20.0 ",
		},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	got, err := ReadPlatformVersion(context.Background(), cli, "opendatahub")
	if err != nil {
		t.Fatalf("ReadPlatformVersion() error = %v", err)
	}

	if got != "2.20.0" {
		t.Fatalf("ReadPlatformVersion() = %q, want %q", got, "2.20.0")
	}
}

func TestReadPlatformVersionMissingConfigMap(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).Build()

	got, err := ReadPlatformVersion(context.Background(), cli, "opendatahub")
	if err != nil {
		t.Fatalf("ReadPlatformVersion() error = %v", err)
	}

	if got != "" {
		t.Fatalf("ReadPlatformVersion() = %q, want empty", got)
	}
}

func TestPlatformReleaseHelpers(t *testing.T) {
	t.Parallel()

	releases := []componentsv1alpha1.ComponentRelease{
		{Name: "Kubeflow Notebook Controller", Version: "1.10.0"},
	}

	SetPlatformRelease(&releases, "2.20.0")

	if !HandshakeComplete("2.20.0", releases) {
		t.Fatal("HandshakeComplete() = false, want true")
	}

	merged := MergeComponentReleases(releases, GetPlatformRelease(releases))
	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}

	SetPlatformRelease(&releases, "2.21.0")
	if HandshakeComplete("2.20.0", releases) {
		t.Fatal("HandshakeComplete() = true before handshake advanced")
	}
}
