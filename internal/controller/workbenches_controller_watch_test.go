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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	componentsv1alpha1 "github.com/opendatahub-io/workbenches-operator/api/v1alpha1"
	"github.com/opendatahub-io/workbenches-operator/internal/metadata"
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
			old:  deploymentWithLabel("opendatahub", "deploy-a", false, 1, 1),
			new:  deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1),
			want: true,
		},
		{
			name: "label removed",
			old:  deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1),
			new:  deploymentWithLabel("opendatahub", "deploy-a", false, 1, 1),
			want: true,
		},
		{
			name: "ready replicas changed",
			old:  deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1),
			new:  deploymentWithLabel("opendatahub", "deploy-a", true, 0, 1),
			want: true,
		},
		{
			name: "desired replicas changed",
			old:  deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1),
			new:  deploymentWithLabel("opendatahub", "deploy-a", true, 1, 2),
			want: true,
		},
		{
			name: "unrelated update without label",
			old:  deploymentWithLabel("opendatahub", "deploy-a", false, 1, 1),
			new:  deploymentWithLabel("opendatahub", "deploy-a", false, 0, 0),
			want: false,
		},
		{
			name: "no availability change",
			old:  deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1),
			new:  deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1),
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

func TestMapComponentDeploymentToWorkbenches(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := componentsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	wb := &componentsv1alpha1.Workbenches{
		ObjectMeta: metav1.ObjectMeta{Name: componentsv1alpha1.WorkbenchesInstanceName},
		Spec: componentsv1alpha1.WorkbenchesSpec{
			WorkbenchNamespace: "opendatahub",
		},
	}

	reconciler := &WorkbenchesReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(wb).Build(),
	}

	tests := []struct {
		name     string
		deploy   *appsv1.Deployment
		wantSize int
	}{
		{
			name:     "labeled deployment in workbench namespace",
			deploy:   deploymentWithLabel("opendatahub", "notebook-controller", true, 1, 1),
			wantSize: 1,
		},
		{
			name:     "labeled deployment in other namespace",
			deploy:   deploymentWithLabel("other-ns", "notebook-controller", true, 1, 1),
			wantSize: 0,
		},
		{
			name:     "unlabeled deployment in workbench namespace",
			deploy:   deploymentWithLabel("opendatahub", "notebook-controller", false, 1, 1),
			wantSize: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := reconciler.mapComponentDeploymentToWorkbenches(context.Background(), tt.deploy)
			if len(got) != tt.wantSize {
				t.Fatalf("mapComponentDeploymentToWorkbenches() len = %d, want %d", len(got), tt.wantSize)
			}
		})
	}
}

func TestDeploymentAvailabilityChangedPredicateCreateDeleteGeneric(t *testing.T) {
	t.Parallel()

	predicate := deploymentAvailabilityChangedPredicate{}
	labeled := deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1)
	unlabeled := deploymentWithLabel("opendatahub", "deploy-b", false, 1, 1)

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
	labeled := deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1)

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

	if !hasComponentLabel(deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1)) {
		t.Fatal("hasComponentLabel() = false, want true")
	}
	if hasComponentLabel(deploymentWithLabel("opendatahub", "deploy-a", false, 1, 1)) {
		t.Fatal("hasComponentLabel() = true, want false")
	}

	nilReplicas := deploymentWithLabel("opendatahub", "deploy-a", true, 1, 1)
	nilReplicas.Spec.Replicas = nil
	if got := deploymentDesiredReplicas(nilReplicas); got != 1 {
		t.Fatalf("deploymentDesiredReplicas(nil) = %d, want 1", got)
	}

	withReplicas := deploymentWithLabel("opendatahub", "deploy-a", true, 1, 3)
	if got := deploymentDesiredReplicas(withReplicas); got != 3 {
		t.Fatalf("deploymentDesiredReplicas(3) = %d, want 3", got)
	}
}

func TestMapComponentDeploymentToWorkbenchesEdgeCases(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := componentsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	reconciler := &WorkbenchesReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
	}

	if got := reconciler.mapComponentDeploymentToWorkbenches(
		context.Background(),
		&componentsv1alpha1.Workbenches{},
	); len(got) != 0 {
		t.Fatalf("mapComponentDeploymentToWorkbenches(invalid type) len = %d, want 0", len(got))
	}

	if got := reconciler.mapComponentDeploymentToWorkbenches(
		context.Background(),
		deploymentWithLabel("opendatahub", "notebook-controller", true, 1, 1),
	); len(got) != 0 {
		t.Fatalf("mapComponentDeploymentToWorkbenches(missing CR) len = %d, want 0", len(got))
	}
}

func deploymentWithLabel(namespace, name string, labeled bool, readyReplicas, specReplicas int32) *appsv1.Deployment {
	labels := map[string]string{}
	if labeled {
		labels[metadata.ComponentLabelKey] = metadata.LabelTrue
	}

	replicas := specReplicas

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
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
