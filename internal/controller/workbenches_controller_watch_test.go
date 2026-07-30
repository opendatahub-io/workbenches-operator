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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
	"github.com/opendatahub-io/workbenches-operator/internal/gvk"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
	"github.com/opendatahub-io/workbenches-operator/internal/platform"
	"github.com/opendatahub-io/workbenches-operator/internal/platformconfig"
)

func TestDeploymentAvailabilityChangedPredicateUpdate(t *testing.T) {
	t.Parallel()

	predicate := deploymentAvailabilityChangedPredicate{}

	tests := []struct {
		name string
		old  *appsv1.Deployment
		new  *appsv1.Deployment
		want bool
	}{
		{
			name: "label added",
			old:  deploymentWithLabel("deploy-a", false, 1, 1),
			new:  deploymentWithLabel("deploy-a", true, 1, 1),
			want: true,
		},
		{
			name: "label removed",
			old:  deploymentWithLabel("deploy-a", true, 1, 1),
			new:  deploymentWithLabel("deploy-a", false, 1, 1),
			want: true,
		},
		{
			name: "ready replicas changed",
			old:  deploymentWithLabel("deploy-a", true, 1, 1),
			new:  deploymentWithLabel("deploy-a", true, 0, 1),
			want: true,
		},
		{
			name: "desired replicas changed",
			old:  deploymentWithLabel("deploy-a", true, 1, 1),
			new:  deploymentWithLabel("deploy-a", true, 1, 2),
			want: true,
		},
		{
			name: "unrelated update without label",
			old:  deploymentWithLabel("deploy-a", false, 1, 1),
			new:  deploymentWithLabel("deploy-a", false, 0, 0),
			want: false,
		},
		{
			name: "no availability change",
			old:  deploymentWithLabel("deploy-a", true, 1, 1),
			new:  deploymentWithLabel("deploy-a", true, 1, 1),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := predicate.Update(event.UpdateEvent{ObjectOld: tt.old, ObjectNew: tt.new})
			if got != tt.want {
				t.Fatalf("Update() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeploymentAvailabilityChangedPredicateCreateDeleteGeneric(t *testing.T) {
	t.Parallel()

	predicate := deploymentAvailabilityChangedPredicate{}
	labeled := deploymentWithLabel("deploy-a", true, 1, 1)
	unlabeled := deploymentWithLabel("deploy-b", false, 1, 1)

	if !predicate.Create(event.CreateEvent{Object: labeled}) {
		t.Fatal("Create() = false, want true for labeled deployment")
	}
	if predicate.Create(event.CreateEvent{Object: unlabeled}) {
		t.Fatal("Create() = true, want false for unlabeled deployment")
	}
	if !predicate.Delete(event.DeleteEvent{Object: labeled}) {
		t.Fatal("Delete() = false, want true for labeled deployment")
	}
	if predicate.Delete(event.DeleteEvent{Object: unlabeled}) {
		t.Fatal("Delete() = true, want false for unlabeled deployment")
	}
	if predicate.Generic(event.GenericEvent{Object: labeled}) {
		t.Fatal("Generic() = true, want false")
	}
}

func TestDeploymentAvailabilityChangedPredicateUpdateInvalidType(t *testing.T) {
	t.Parallel()

	predicate := deploymentAvailabilityChangedPredicate{}
	labeled := deploymentWithLabel("deploy-a", true, 1, 1)

	oldObj := &componentsv1alpha1.Workbenches{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{metadata.ComponentLabelKey: metadata.LabelTrue},
		},
	}

	if !predicate.Update(event.UpdateEvent{
		ObjectOld: oldObj,
		ObjectNew: labeled,
	}) {
		t.Fatal("Update() = false, want true when old object is not a Deployment")
	}
}

func TestHasComponentLabelAndDesiredReplicas(t *testing.T) {
	t.Parallel()

	if !hasComponentLabel(deploymentWithLabel("deploy-a", true, 1, 1)) {
		t.Fatal("hasComponentLabel() = false, want true")
	}
	if hasComponentLabel(deploymentWithLabel("deploy-a", false, 1, 1)) {
		t.Fatal("hasComponentLabel() = true, want false")
	}

	nilReplicas := deploymentWithLabel("deploy-a", true, 1, 1)
	nilReplicas.Spec.Replicas = nil
	if got := deploymentDesiredReplicas(nilReplicas); got != 1 {
		t.Fatalf("deploymentDesiredReplicas(nil) = %d, want 1", got)
	}

	withReplicas := deploymentWithLabel("deploy-a", true, 1, 3)
	if got := deploymentDesiredReplicas(withReplicas); got != 3 {
		t.Fatalf("deploymentDesiredReplicas(3) = %d, want 3", got)
	}
}

func TestMapPlatformConfigToWorkbenches(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := componentsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(corev1) error = %v", err)
	}

	t.Run("unset apps ns accepts either default before CR exists", func(t *testing.T) {
		t.Parallel()

		r := &WorkbenchesReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		}

		for _, ns := range []string{
			platform.DefaultApplicationsNamespaceODH,
			platform.DefaultApplicationsNamespaceRHOAI,
		} {
			got := r.mapPlatformConfigToWorkbenches(context.Background(), &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      platformconfig.ConfigMapName,
					Namespace: ns,
				},
			})
			if len(got) != 1 {
				t.Fatalf("namespace %s: len = %d, want 1", ns, len(got))
			}
		}
	})

	t.Run("CR platform selects resolved apps namespace", func(t *testing.T) {
		t.Parallel()

		wb := &componentsv1alpha1.Workbenches{
			ObjectMeta: metav1.ObjectMeta{Name: componentsv1alpha1.WorkbenchesInstanceName},
			Spec: componentsv1alpha1.WorkbenchesSpec{
				Platform: "SelfManagedRhoai",
			},
		}
		r := &WorkbenchesReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(wb).Build(),
		}

		if got := r.mapPlatformConfigToWorkbenches(context.Background(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      platformconfig.ConfigMapName,
				Namespace: platform.DefaultApplicationsNamespaceODH,
			},
		}); len(got) != 0 {
			t.Fatalf("wrong apps ns: len = %d, want 0", len(got))
		}

		if got := r.mapPlatformConfigToWorkbenches(context.Background(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      platformconfig.ConfigMapName,
				Namespace: platform.DefaultApplicationsNamespaceRHOAI,
			},
		}); len(got) != 1 {
			t.Fatalf("rhoai apps ns: len = %d, want 1", len(got))
		}
	})
}

func TestMapImageStreamToWorkbenches(t *testing.T) {
	t.Parallel()

	r := &WorkbenchesReconciler{}

	imageStream := &unstructured.Unstructured{}
	imageStream.SetName("jupyter-minimal-notebook")
	imageStream.SetNamespace("opendatahub")
	imageStream.SetGroupVersionKind(gvk.ImageStream)

	reqs := r.mapImageStreamToWorkbenches(context.Background(), imageStream)
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	if reqs[0].Name != componentsv1alpha1.WorkbenchesInstanceName {
		t.Fatalf("request name = %q, want %q", reqs[0].Name, componentsv1alpha1.WorkbenchesInstanceName)
	}

	// ODH watch-all is filtered here to managed part-of; mapper still always enqueues.
	reqs = r.mapImageStreamToWorkbenches(context.Background(), nil)
	if len(reqs) != 1 {
		t.Fatalf("nil object: got %d requests, want 1", len(reqs))
	}
}

func TestShouldWatchImageStreams(t *testing.T) {
	t.Parallel()

	emptyMapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{})
	watch, err := shouldWatchImageStreams(emptyMapper)
	if err != nil {
		t.Fatalf("empty mapper error = %v", err)
	}
	if watch {
		t.Fatal("empty mapper: watch = true, want false")
	}

	withIS := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.ImageStream.GroupVersion()})
	withIS.Add(gvk.ImageStream, meta.RESTScopeNamespace)
	watch, err = shouldWatchImageStreams(withIS)
	if err != nil {
		t.Fatalf("mapper with ImageStream error = %v", err)
	}
	if !watch {
		t.Fatal("mapper with ImageStream: watch = false, want true")
	}
}

func deploymentWithLabel(name string, labeled bool, readyReplicas, specReplicas int32) *appsv1.Deployment {
	labels := map[string]string{}
	if labeled {
		labels[metadata.ComponentLabelKey] = metadata.LabelTrue
	}

	replicas := specReplicas

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "opendatahub",
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:     readyReplicas,
			AvailableReplicas: readyReplicas,
		},
	}
}
