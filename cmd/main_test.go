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

package main

import (
	"context"
	"errors"
	"os"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/workbenches-operator/internal/platform"
	"github.com/opendatahub-io/workbenches-operator/internal/tlsconfig"
)

func TestResolveApplicationsNamespace(t *testing.T) {
	tests := []struct {
		name   string
		env    string
		setEnv bool
		want   string
	}{
		{
			name:   "unset falls back to default",
			setEnv: false,
			want:   platform.DefaultNotebooksNamespaceODH,
		},
		{
			name:   "empty falls back to default",
			env:    "",
			setEnv: true,
			want:   platform.DefaultNotebooksNamespaceODH,
		},
		{
			name:   "invalid DNS label falls back to default",
			env:    "Invalid_Namespace",
			setEnv: true,
			want:   platform.DefaultNotebooksNamespaceODH,
		},
		{
			name:   "valid namespace is kept",
			env:    "opendatahub",
			setEnv: true,
			want:   "opendatahub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			if tt.setEnv {
				t.Setenv("APPLICATIONS_NAMESPACE", tt.env)
			} else {
				_ = os.Unsetenv("APPLICATIONS_NAMESPACE")
			}
			g.Expect(resolveApplicationsNamespace()).To(Equal(tt.want))
		})
	}
}

func TestConfigureWebhookServingCerts(t *testing.T) {
	// Not parallel: mutates package-level webhookTLSConfigure.
	t.Run("disabled is a no-op", func(t *testing.T) {
		g := NewWithT(t)
		enabled, err := configureWebhookServingCerts(nil, nil, false)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(enabled).To(BeFalse())
	})

	t.Run("provider unavailable disables webhooks", func(t *testing.T) {
		g := NewWithT(t)
		orig := webhookTLSConfigure
		t.Cleanup(func() { webhookTLSConfigure = orig })
		webhookTLSConfigure = func(context.Context, *rest.Config, client.Client) (bool, error) {
			return false, nil
		}

		enabled, err := configureWebhookServingCerts(&rest.Config{}, nil, true)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(enabled).To(BeFalse())
	})

	t.Run("configure error is returned", func(t *testing.T) {
		g := NewWithT(t)
		orig := webhookTLSConfigure
		t.Cleanup(func() { webhookTLSConfigure = orig })
		webhookTLSConfigure = func(context.Context, *rest.Config, client.Client) (bool, error) {
			return false, errors.New("boom")
		}

		enabled, err := configureWebhookServingCerts(&rest.Config{}, nil, true)
		g.Expect(err).To(MatchError("boom"))
		g.Expect(enabled).To(BeFalse())
	})

	t.Run("successful configure keeps webhooks enabled", func(t *testing.T) {
		g := NewWithT(t)
		orig := webhookTLSConfigure
		t.Cleanup(func() { webhookTLSConfigure = orig })
		webhookTLSConfigure = func(context.Context, *rest.Config, client.Client) (bool, error) {
			return true, nil
		}

		enabled, err := configureWebhookServingCerts(&rest.Config{}, nil, true)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(enabled).To(BeTrue())
	})
}

func TestLogTLSBootstrapResult(t *testing.T) {
	t.Parallel()
	// Smoke-call the logging helper for both branches.
	logTLSBootstrapResult(&tlsconfig.BootstrapResult{HasOpenShiftConfigAPI: false})
	logTLSBootstrapResult(&tlsconfig.BootstrapResult{
		HasOpenShiftConfigAPI: true,
		UnsupportedCiphers:    []string{"TLS_FAKE"},
	})
}

func TestRegisterTLSProfileWatcherSkipped(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	err := registerTLSProfileWatcher(nil, &tlsconfig.BootstrapResult{HasOpenShiftConfigAPI: false}, func() {})
	g.Expect(err).NotTo(HaveOccurred())
}
