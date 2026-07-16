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
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const defaultEnsureInterval = 30 * time.Second

// ensurer periodically re-applies Ensure for a provider detected once at startup.
// Re-detection is unnecessary: API groups do not change without an API server (and
// typically operator) restart. The loop exists to retry Service/MWC annotation
// when those objects appear after the first Configure attempt.
type ensurer struct {
	cli      client.Client
	names    Names
	provider Provider
	interval time.Duration
}

// NewEnsurer returns a manager.Runnable that re-asserts webhook TLS configuration.
// It detects the provider once up front, then only re-runs Ensure on each interval.
func NewEnsurer(cfg *rest.Config, cli client.Client) (manager.Runnable, error) {
	names, err := DefaultNames()
	if err != nil {
		return nil, err
	}

	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create discovery client: %w", err)
	}

	provider, err := Detect(disco)
	if err != nil {
		return nil, err
	}

	return &ensurer{
		cli:      cli,
		names:    names,
		provider: provider,
		interval: defaultEnsureInterval,
	}, nil
}

// Start runs Ensure immediately, then on every interval until ctx is cancelled.
func (e *ensurer) Start(ctx context.Context) error {
	ensureLog.Info("starting webhook TLS ensurer", "provider", e.provider)

	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	for {
		if err := Ensure(ctx, e.cli, e.provider, e.names); err != nil {
			ensureLog.Error(err, "periodic webhook TLS ensure failed", "provider", e.provider)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
