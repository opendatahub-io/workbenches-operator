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
	"errors"
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/discovery"
)

// Provider identifies how webhook serving certificates are provisioned.
type Provider string

const (
	// ProviderOpenShift uses OpenShift service-CA annotations.
	ProviderOpenShift Provider = "OpenShift"
	// ProviderCertManager uses cert-manager Certificate + ClusterIssuer resources.
	ProviderCertManager Provider = "CertManager"
	// ProviderNone means no TLS provider is available; webhooks should be disabled.
	ProviderNone Provider = "None"
)

const (
	DefaultWebhookServiceName  = "workbenches-operator-webhook-service"
	DefaultWebhookCertSecret   = "workbenches-operator-controller-webhook-cert"
	DefaultWebhookCertificate  = "workbenches-operator-webhook-cert"
	DefaultWebhookIssuer       = "workbenches-operator-selfsigned-issuer"
	DefaultMutatingWebhookName = "workbenches-operator-mutating-webhook-configuration"
	inClusterNamespacePath     = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	openShiftRouteGroup        = "route.openshift.io"
	openShiftSecurityGroup     = "security.openshift.io"
	certManagerGroup           = "cert-manager.io"
)

// Names holds the webhook TLS-related resource names and target namespace.
type Names struct {
	Namespace           string
	ServiceName         string
	CertSecret          string
	CertificateName     string
	IssuerName          string
	MutatingWebhookName string
}

// DefaultNames returns chart namePrefix defaults with the resolved operator namespace.
func DefaultNames() (Names, error) {
	ns, err := resolveNamespace()
	if err != nil {
		return Names{}, err
	}

	return Names{
		Namespace:           ns,
		ServiceName:         DefaultWebhookServiceName,
		CertSecret:          DefaultWebhookCertSecret,
		CertificateName:     DefaultWebhookCertificate,
		IssuerName:          DefaultWebhookIssuer,
		MutatingWebhookName: DefaultMutatingWebhookName,
	}, nil
}

func resolveNamespace() (string, error) {
	if data, err := os.ReadFile(inClusterNamespacePath); err == nil {
		if ns := strings.TrimSpace(string(data)); ns != "" {
			return ns, nil
		}
	}
	if ns := os.Getenv("OPERATOR_NAMESPACE"); ns != "" {
		return ns, nil
	}
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns, nil
	}
	return "", errors.New("unable to resolve operator namespace: set OPERATOR_NAMESPACE or POD_NAMESPACE, or run in-cluster with a service account namespace file")
}

// Detect inspects the cluster API groups and returns the preferred TLS provider.
// Preference order: OpenShift → cert-manager → None.
func Detect(disco discovery.DiscoveryInterface) (Provider, error) {
	// ServerGroups can return a partial group list alongside an error when some
	// APIServices are unhealthy. Prefer a detected provider from that list over
	// failing startup (or incorrectly choosing ProviderNone).
	groups, err := disco.ServerGroups()
	if groups == nil {
		return ProviderNone, fmt.Errorf("discover API groups: %w", err)
	}

	hasOpenShift := false
	hasCertManager := false

	for i := range groups.Groups {
		name := groups.Groups[i].Name
		switch name {
		case openShiftRouteGroup, openShiftSecurityGroup:
			hasOpenShift = true
		case certManagerGroup:
			hasCertManager = true
		}
	}

	if hasOpenShift {
		return ProviderOpenShift, nil
	}
	if hasCertManager {
		return ProviderCertManager, nil
	}

	if err != nil {
		return ProviderNone, fmt.Errorf("discover API groups (partial): %w", err)
	}

	return ProviderNone, nil
}
