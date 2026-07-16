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

package tls

import (
	"context"
	"fmt"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// RBAC for webhook TLS auto-configuration.
// clusterissuers stay on the ClusterRole; namespaced certificates use
// config/rbac/webhook_tls_role.yaml (Role + RoleBinding).
// Service/MWC verbs are already granted by the Workbenches reconciler markers.
// +kubebuilder:rbac:groups=cert-manager.io,resources=clusterissuers,verbs=get;create;patch

const (
	openShiftServingCertAnnotation = "service.beta.openshift.io/serving-cert-secret-name"
	openShiftInjectCABundle        = "service.beta.openshift.io/inject-cabundle"
	certManagerInjectCAFrom        = "cert-manager.io/inject-ca-from"
)

var (
	ensureLog = logf.Log.WithName("webhook-tls")

	clusterIssuerGVK = schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "ClusterIssuer",
	}
	certificateGVK = schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	}
)

// Configure detects the TLS provider, ensures required resources/annotations, and
// reports whether webhooks should remain enabled (false when Provider is None).
// Missing Service/MWC is logged and treated as success so startup can proceed;
// the periodic ensurer retries until those objects exist.
func Configure(ctx context.Context, cfg *rest.Config, cli client.Client) (enableWebhooks bool, err error) {
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false, fmt.Errorf("create discovery client: %w", err)
	}

	names, err := DefaultNames()
	if err != nil {
		return false, err
	}

	provider, err := Detect(disco)
	if err != nil {
		return false, err
	}

	ensureLog.Info("detected webhook TLS provider", "provider", provider)

	if err := Ensure(ctx, cli, provider, names); err != nil {
		return false, err
	}

	return provider != ProviderNone, nil
}

// Ensure applies provider-specific TLS configuration for webhook serving certs.
// It is idempotent. NotFound for Service/MWC is logged and ignored (retry later).
func Ensure(ctx context.Context, cli client.Client, provider Provider, names Names) error {
	switch provider {
	case ProviderOpenShift:
		return ensureOpenShift(ctx, cli, names)
	case ProviderCertManager:
		return ensureCertManager(ctx, cli, names)
	case ProviderNone:
		return ensureNone(ctx, cli, names)
	default:
		return fmt.Errorf("unknown TLS provider %q", provider)
	}
}

func ensureOpenShift(ctx context.Context, cli client.Client, names Names) error {
	if err := patchServiceAnnotation(ctx, cli, names.Namespace, names.ServiceName,
		openShiftServingCertAnnotation, names.CertSecret); err != nil {
		return err
	}

	return patchMWCAnnotation(ctx, cli, names.MutatingWebhookName,
		openShiftInjectCABundle, "true")
}

func ensureCertManager(ctx context.Context, cli client.Client, names Names) error {
	if err := ensureClusterIssuer(ctx, cli, names); err != nil {
		return err
	}

	if err := ensureCertificate(ctx, cli, names); err != nil {
		return err
	}

	injectFrom := fmt.Sprintf("%s/%s", names.Namespace, names.CertificateName)
	return patchMWCAnnotation(ctx, cli, names.MutatingWebhookName,
		certManagerInjectCAFrom, injectFrom)
}

func ensureNone(ctx context.Context, cli client.Client, names Names) error {
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
	err := cli.Get(ctx, types.NamespacedName{Name: names.MutatingWebhookName}, mwc)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get MutatingWebhookConfiguration %s: %w", names.MutatingWebhookName, err)
	}

	if err := cli.Delete(ctx, mwc); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete MutatingWebhookConfiguration %s: %w", names.MutatingWebhookName, err)
	}

	return nil
}

func patchServiceAnnotation(ctx context.Context, cli client.Client, namespace, name, key, value string) error {
	svc := &corev1.Service{}
	if err := cli.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, svc); err != nil {
		if apierrors.IsNotFound(err) {
			ensureLog.Info("webhook Service not found; will retry", "namespace", namespace, "name", name)
			return nil
		}
		return fmt.Errorf("get Service %s/%s: %w", namespace, name, err)
	}

	if svc.Annotations != nil && svc.Annotations[key] == value {
		return nil
	}

	original := svc.DeepCopy()
	if svc.Annotations == nil {
		svc.Annotations = map[string]string{}
	}
	svc.Annotations[key] = value

	if err := cli.Patch(ctx, svc, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch Service %s/%s annotations: %w", namespace, name, err)
	}

	return nil
}

func patchMWCAnnotation(ctx context.Context, cli client.Client, name, key, value string) error {
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
	if err := cli.Get(ctx, types.NamespacedName{Name: name}, mwc); err != nil {
		if apierrors.IsNotFound(err) {
			ensureLog.Info("MutatingWebhookConfiguration not found; will retry", "name", name)
			return nil
		}
		return fmt.Errorf("get MutatingWebhookConfiguration %s: %w", name, err)
	}

	if mwc.Annotations != nil && mwc.Annotations[key] == value {
		return nil
	}

	original := mwc.DeepCopy()
	if mwc.Annotations == nil {
		mwc.Annotations = map[string]string{}
	}
	mwc.Annotations[key] = value

	if err := cli.Patch(ctx, mwc, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch MutatingWebhookConfiguration %s annotations: %w", name, err)
	}

	return nil
}

func ensureClusterIssuer(ctx context.Context, cli client.Client, names Names) error {
	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(clusterIssuerGVK)

	err := cli.Get(ctx, types.NamespacedName{Name: names.IssuerName}, issuer)
	if err == nil {
		return reconcileOwnerReference(ctx, cli, issuer, names.MutatingWebhookName, mutatingWebhookOwnerRef)
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get ClusterIssuer %s: %w", names.IssuerName, err)
	}

	issuer = &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(clusterIssuerGVK)
	issuer.SetName(names.IssuerName)
	issuer.Object["spec"] = map[string]any{
		"selfSigned": map[string]any{},
	}

	// ClusterIssuer is cluster-scoped; own it from the MutatingWebhookConfiguration
	// (also cluster-scoped) so GC cleans it up when the webhook config is removed.
	if owner, ok, ownerErr := mutatingWebhookOwnerRef(ctx, cli, names.MutatingWebhookName); ownerErr != nil {
		return ownerErr
	} else if ok {
		issuer.SetOwnerReferences([]metav1.OwnerReference{owner})
	}

	if err := cli.Create(ctx, issuer); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Another replica created it; still reconcile ownership.
			existing := &unstructured.Unstructured{}
			existing.SetGroupVersionKind(clusterIssuerGVK)
			if getErr := cli.Get(ctx, types.NamespacedName{Name: names.IssuerName}, existing); getErr != nil {
				return fmt.Errorf("get ClusterIssuer %s after AlreadyExists: %w", names.IssuerName, getErr)
			}
			return reconcileOwnerReference(ctx, cli, existing, names.MutatingWebhookName, mutatingWebhookOwnerRef)
		}
		return fmt.Errorf("create ClusterIssuer %s: %w", names.IssuerName, err)
	}

	return nil
}

func ensureCertificate(ctx context.Context, cli client.Client, names Names) error {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)

	err := cli.Get(ctx, types.NamespacedName{Namespace: names.Namespace, Name: names.CertificateName}, cert)
	if err == nil {
		return reconcileOwnerReference(ctx, cli, cert, names.IssuerName, clusterIssuerOwnerRef)
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get Certificate %s/%s: %w", names.Namespace, names.CertificateName, err)
	}

	dnsNames := []any{
		fmt.Sprintf("%s.%s.svc", names.ServiceName, names.Namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", names.ServiceName, names.Namespace),
	}

	cert = &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName(names.CertificateName)
	cert.SetNamespace(names.Namespace)
	cert.Object["spec"] = map[string]any{
		"dnsNames":   dnsNames,
		"secretName": names.CertSecret,
		"issuerRef": map[string]any{
			"group": "cert-manager.io",
			"kind":  "ClusterIssuer",
			"name":  names.IssuerName,
		},
	}

	// Namespaced Certificate can be owned by the cluster-scoped ClusterIssuer.
	if owner, ok, ownerErr := clusterIssuerOwnerRef(ctx, cli, names.IssuerName); ownerErr != nil {
		return ownerErr
	} else if ok {
		cert.SetOwnerReferences([]metav1.OwnerReference{owner})
	}

	if err := cli.Create(ctx, cert); err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing := &unstructured.Unstructured{}
			existing.SetGroupVersionKind(certificateGVK)
			if getErr := cli.Get(ctx, types.NamespacedName{
				Namespace: names.Namespace,
				Name:      names.CertificateName,
			}, existing); getErr != nil {
				return fmt.Errorf("get Certificate %s/%s after AlreadyExists: %w", names.Namespace, names.CertificateName, getErr)
			}
			return reconcileOwnerReference(ctx, cli, existing, names.IssuerName, clusterIssuerOwnerRef)
		}
		return fmt.Errorf("create Certificate %s/%s: %w", names.Namespace, names.CertificateName, err)
	}

	return nil
}

type ownerRefFunc func(context.Context, client.Client, string) (metav1.OwnerReference, bool, error)

// reconcileOwnerReference attaches the desired controller owner once the owner
// object exists. Used to repair resources created during the MWC/issuer startup race.
func reconcileOwnerReference(
	ctx context.Context,
	cli client.Client,
	obj *unstructured.Unstructured,
	ownerName string,
	resolve ownerRefFunc,
) error {
	owner, ok, err := resolve(ctx, cli, ownerName)
	if err != nil || !ok {
		return err
	}
	if ownerRefMatches(obj.GetOwnerReferences(), owner) {
		return nil
	}

	original := obj.DeepCopy()
	obj.SetOwnerReferences(upsertOwnerReference(obj.GetOwnerReferences(), owner))
	if err := cli.Patch(ctx, obj, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch %s/%s owner references: %w", obj.GetKind(), obj.GetName(), err)
	}
	return nil
}

func upsertOwnerReference(refs []metav1.OwnerReference, owner metav1.OwnerReference) []metav1.OwnerReference {
	out := make([]metav1.OwnerReference, 0, len(refs)+1)
	replaced := false
	for _, ref := range refs {
		if ref.Kind == owner.Kind && ref.Name == owner.Name && ref.APIVersion == owner.APIVersion {
			out = append(out, owner)
			replaced = true
			continue
		}
		out = append(out, ref)
	}
	if !replaced {
		out = append(out, owner)
	}
	return out
}

func ownerRefMatches(refs []metav1.OwnerReference, want metav1.OwnerReference) bool {
	for _, ref := range refs {
		if ref.UID == want.UID && ref.Kind == want.Kind && ref.Name == want.Name && ref.APIVersion == want.APIVersion {
			return true
		}
	}
	return false
}

func mutatingWebhookOwnerRef(ctx context.Context, cli client.Client, name string) (metav1.OwnerReference, bool, error) {
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
	if err := cli.Get(ctx, types.NamespacedName{Name: name}, mwc); err != nil {
		if apierrors.IsNotFound(err) {
			ensureLog.Info("MutatingWebhookConfiguration not found; ClusterIssuer owner deferred", "name", name)
			return metav1.OwnerReference{}, false, nil
		}
		return metav1.OwnerReference{}, false, fmt.Errorf("get MutatingWebhookConfiguration %s for owner ref: %w", name, err)
	}

	controller := true
	blockOwnerDeletion := true
	return metav1.OwnerReference{
		APIVersion:         admissionregistrationv1.SchemeGroupVersion.String(),
		Kind:               "MutatingWebhookConfiguration",
		Name:               mwc.Name,
		UID:                mwc.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}, true, nil
}

func clusterIssuerOwnerRef(ctx context.Context, cli client.Client, name string) (metav1.OwnerReference, bool, error) {
	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(clusterIssuerGVK)
	if err := cli.Get(ctx, types.NamespacedName{Name: name}, issuer); err != nil {
		if apierrors.IsNotFound(err) {
			ensureLog.Info("ClusterIssuer not found; Certificate owner deferred", "name", name)
			return metav1.OwnerReference{}, false, nil
		}
		return metav1.OwnerReference{}, false, fmt.Errorf("get ClusterIssuer %s for owner ref: %w", name, err)
	}

	controller := true
	blockOwnerDeletion := true
	return metav1.OwnerReference{
		APIVersion:         clusterIssuerGVK.GroupVersion().String(),
		Kind:               clusterIssuerGVK.Kind,
		Name:               issuer.GetName(),
		UID:                issuer.GetUID(),
		Controller:         &controller,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}, true, nil
}
