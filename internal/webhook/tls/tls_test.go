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

package tls_test

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	webhooktls "github.com/opendatahub-io/workbenches-operator/internal/webhook/tls"
)

func TestDetect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		groups []*metav1.APIResourceList
		want   webhooktls.Provider
	}{
		{
			name: "OpenShift when route.openshift.io present",
			groups: []*metav1.APIResourceList{
				{GroupVersion: "route.openshift.io/v1"},
			},
			want: webhooktls.ProviderOpenShift,
		},
		{
			name: "OpenShift when security.openshift.io present",
			groups: []*metav1.APIResourceList{
				{GroupVersion: "security.openshift.io/v1"},
			},
			want: webhooktls.ProviderOpenShift,
		},
		{
			name: "OpenShift preferred over cert-manager",
			groups: []*metav1.APIResourceList{
				{GroupVersion: "route.openshift.io/v1"},
				{GroupVersion: "cert-manager.io/v1"},
			},
			want: webhooktls.ProviderOpenShift,
		},
		{
			name: "CertManager when cert-manager.io present",
			groups: []*metav1.APIResourceList{
				{GroupVersion: "cert-manager.io/v1"},
			},
			want: webhooktls.ProviderCertManager,
		},
		{
			name:   "None when neither present",
			groups: []*metav1.APIResourceList{},
			want:   webhooktls.ProviderNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			disco := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
			disco.Resources = tt.groups

			got, err := webhooktls.Detect(disco)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got).To(Equal(tt.want))
		})
	}
}

// partialDiscovery returns a fixed APIGroupList (and optional error) from ServerGroups,
// simulating partial discovery failures from unhealthy APIServices.
type partialDiscovery struct {
	fakediscovery.FakeDiscovery

	groups *metav1.APIGroupList
	err    error
}

func (d *partialDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	return d.groups, d.err
}

func TestDetectPartialDiscovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		groups  *metav1.APIGroupList
		err     error
		want    webhooktls.Provider
		wantErr bool
	}{
		{
			name: "uses OpenShift from partial list despite error",
			groups: &metav1.APIGroupList{
				Groups: []metav1.APIGroup{{Name: "route.openshift.io"}},
			},
			err:  errors.New("partial discovery failure"),
			want: webhooktls.ProviderOpenShift,
		},
		{
			name: "uses CertManager from partial list despite error",
			groups: &metav1.APIGroupList{
				Groups: []metav1.APIGroup{{Name: "cert-manager.io"}},
			},
			err:  errors.New("partial discovery failure"),
			want: webhooktls.ProviderCertManager,
		},
		{
			name: "errors when partial list has no provider",
			groups: &metav1.APIGroupList{
				Groups: []metav1.APIGroup{{Name: "apps"}},
			},
			err:     errors.New("partial discovery failure"),
			want:    webhooktls.ProviderNone,
			wantErr: true,
		},
		{
			name:    "errors when groups is nil",
			groups:  nil,
			err:     errors.New("discovery unavailable"),
			want:    webhooktls.ProviderNone,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)

			disco := &partialDiscovery{
				FakeDiscovery: fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}},
				groups:        tt.groups,
				err:           tt.err,
			}

			got, err := webhooktls.Detect(disco)
			if tt.wantErr {
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).NotTo(HaveOccurred())
			}
			g.Expect(got).To(Equal(tt.want))
		})
	}
}

func TestDefaultNamesRequiresNamespace(t *testing.T) {
	// Not parallel: mutates process environment.
	t.Setenv("OPERATOR_NAMESPACE", "")
	t.Setenv("POD_NAMESPACE", "")
	// In-cluster path won't exist in unit tests.

	g := NewWithT(t)
	_, err := webhooktls.DefaultNames()
	g.Expect(err).To(MatchError(ContainSubstring("unable to resolve operator namespace")))
}

func TestDefaultNamesUsesOperatorNamespace(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", "from-env")
	t.Setenv("POD_NAMESPACE", "ignored")

	g := NewWithT(t)
	names, err := webhooktls.DefaultNames()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(names.Namespace).To(Equal("from-env"))
}

func testNames() webhooktls.Names {
	return webhooktls.Names{
		Namespace:           "workbenches-operator-system",
		ServiceName:         webhooktls.DefaultWebhookServiceName,
		CertSecret:          webhooktls.DefaultWebhookCertSecret,
		CertificateName:     webhooktls.DefaultWebhookCertificate,
		IssuerName:          webhooktls.DefaultWebhookIssuer,
		MutatingWebhookName: webhooktls.DefaultMutatingWebhookName,
	}
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	g := NewWithT(t)
	g.Expect(clientgoscheme.AddToScheme(s)).To(Succeed())
	return s
}

func TestEnsureOpenShift(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	names := testNames()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.ServiceName,
			Namespace: names.Namespace,
		},
	}
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: names.MutatingWebhookName,
		},
	}

	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(svc, mwc).Build()

	g.Expect(webhooktls.Ensure(context.Background(), cli, webhooktls.ProviderOpenShift, names)).To(Succeed())

	gotSvc := &corev1.Service{}
	g.Expect(cli.Get(context.Background(), types.NamespacedName{
		Namespace: names.Namespace,
		Name:      names.ServiceName,
	}, gotSvc)).To(Succeed())
	g.Expect(gotSvc.Annotations).To(HaveKeyWithValue(
		"service.beta.openshift.io/serving-cert-secret-name",
		names.CertSecret,
	))

	gotMWC := &admissionregistrationv1.MutatingWebhookConfiguration{}
	g.Expect(cli.Get(context.Background(), types.NamespacedName{Name: names.MutatingWebhookName}, gotMWC)).To(Succeed())
	g.Expect(gotMWC.Annotations).To(HaveKeyWithValue(
		"service.beta.openshift.io/inject-cabundle",
		"true",
	))

	// Idempotent.
	g.Expect(webhooktls.Ensure(context.Background(), cli, webhooktls.ProviderOpenShift, names)).To(Succeed())
}

func TestEnsureCertManager(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	names := testNames()
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: names.MutatingWebhookName,
			UID:  "test-mwc-uid",
		},
	}

	s := newScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(mwc).
		Build()

	g.Expect(webhooktls.Ensure(context.Background(), cli, webhooktls.ProviderCertManager, names)).To(Succeed())

	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cert-manager.io", Version: "v1", Kind: "ClusterIssuer",
	})
	g.Expect(cli.Get(context.Background(), types.NamespacedName{Name: names.IssuerName}, issuer)).To(Succeed())
	selfSigned, found, err := unstructured.NestedMap(issuer.Object, "spec", "selfSigned")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(selfSigned).NotTo(BeNil())
	g.Expect(issuer.GetOwnerReferences()).To(HaveLen(1))
	g.Expect(issuer.GetOwnerReferences()[0].Kind).To(Equal("MutatingWebhookConfiguration"))
	g.Expect(issuer.GetOwnerReferences()[0].Name).To(Equal(names.MutatingWebhookName))
	g.Expect(issuer.GetOwnerReferences()[0].UID).To(Equal(types.UID("test-mwc-uid")))

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cert-manager.io", Version: "v1", Kind: "Certificate",
	})
	g.Expect(cli.Get(context.Background(), types.NamespacedName{
		Namespace: names.Namespace,
		Name:      names.CertificateName,
	}, cert)).To(Succeed())

	secretName, found, err := unstructured.NestedString(cert.Object, "spec", "secretName")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(secretName).To(Equal(names.CertSecret))

	dnsNames, found, err := unstructured.NestedSlice(cert.Object, "spec", "dnsNames")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(dnsNames).To(Equal([]any{
		names.ServiceName + "." + names.Namespace + ".svc",
		names.ServiceName + "." + names.Namespace + ".svc.cluster.local",
	}))

	issuerRef, found, err := unstructured.NestedMap(cert.Object, "spec", "issuerRef")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(issuerRef).To(Equal(map[string]any{
		"group": "cert-manager.io",
		"kind":  "ClusterIssuer",
		"name":  names.IssuerName,
	}))
	g.Expect(cert.GetOwnerReferences()).To(HaveLen(1))
	g.Expect(cert.GetOwnerReferences()[0].Kind).To(Equal("ClusterIssuer"))
	g.Expect(cert.GetOwnerReferences()[0].Name).To(Equal(names.IssuerName))
	g.Expect(cert.GetOwnerReferences()[0].UID).To(Equal(issuer.GetUID()))

	gotMWC := &admissionregistrationv1.MutatingWebhookConfiguration{}
	g.Expect(cli.Get(context.Background(), types.NamespacedName{Name: names.MutatingWebhookName}, gotMWC)).To(Succeed())
	g.Expect(gotMWC.Annotations).To(HaveKeyWithValue(
		"cert-manager.io/inject-ca-from",
		names.Namespace+"/"+names.CertificateName,
	))

	// Idempotent.
	g.Expect(webhooktls.Ensure(context.Background(), cli, webhooktls.ProviderCertManager, names)).To(Succeed())
}

func TestEnsureCertManagerAttachesOwnerAfterMWCAppears(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	names := testNames()
	s := newScheme(t)
	cli := fake.NewClientBuilder().WithScheme(s).Build()

	// First ensure: no MWC yet → ClusterIssuer created without owner.
	g.Expect(webhooktls.Ensure(context.Background(), cli, webhooktls.ProviderCertManager, names)).To(Succeed())

	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cert-manager.io", Version: "v1", Kind: "ClusterIssuer",
	})
	g.Expect(cli.Get(context.Background(), types.NamespacedName{Name: names.IssuerName}, issuer)).To(Succeed())
	g.Expect(issuer.GetOwnerReferences()).To(BeEmpty())

	// MWC appears later (chart/platform install race).
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: names.MutatingWebhookName,
			UID:  "late-mwc-uid",
		},
	}
	g.Expect(cli.Create(context.Background(), mwc)).To(Succeed())

	// Subsequent ensure must attach the deferred owner reference.
	g.Expect(webhooktls.Ensure(context.Background(), cli, webhooktls.ProviderCertManager, names)).To(Succeed())

	g.Expect(cli.Get(context.Background(), types.NamespacedName{Name: names.IssuerName}, issuer)).To(Succeed())
	g.Expect(issuer.GetOwnerReferences()).To(HaveLen(1))
	g.Expect(issuer.GetOwnerReferences()[0].Kind).To(Equal("MutatingWebhookConfiguration"))
	g.Expect(issuer.GetOwnerReferences()[0].Name).To(Equal(names.MutatingWebhookName))
	g.Expect(issuer.GetOwnerReferences()[0].UID).To(Equal(types.UID("late-mwc-uid")))

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cert-manager.io", Version: "v1", Kind: "Certificate",
	})
	g.Expect(cli.Get(context.Background(), types.NamespacedName{
		Namespace: names.Namespace,
		Name:      names.CertificateName,
	}, cert)).To(Succeed())
	g.Expect(cert.GetOwnerReferences()).To(HaveLen(1))
	g.Expect(cert.GetOwnerReferences()[0].Kind).To(Equal("ClusterIssuer"))
	g.Expect(cert.GetOwnerReferences()[0].Name).To(Equal(names.IssuerName))
	g.Expect(cert.GetOwnerReferences()[0].UID).To(Equal(issuer.GetUID()))
}

func TestEnsureNoneDeletesMWC(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	names := testNames()
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: names.MutatingWebhookName,
		},
	}

	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(mwc).Build()

	g.Expect(webhooktls.Ensure(context.Background(), cli, webhooktls.ProviderNone, names)).To(Succeed())

	got := &admissionregistrationv1.MutatingWebhookConfiguration{}
	err := cli.Get(context.Background(), types.NamespacedName{Name: names.MutatingWebhookName}, got)
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())

	// Idempotent when already gone.
	g.Expect(webhooktls.Ensure(context.Background(), cli, webhooktls.ProviderNone, names)).To(Succeed())
}

func TestEnsureOpenShiftMissingResources(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	// Service and MWC absent — should log and succeed so startup can continue.
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	g.Expect(webhooktls.Ensure(context.Background(), cli, webhooktls.ProviderOpenShift, testNames())).To(Succeed())
}
