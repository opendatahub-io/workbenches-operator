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

package status

import "testing"

func TestComputePhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  PhaseContext
		want string
	}{
		{
			name: "failed reconcile error",
			ctx: PhaseContext{
				Failed: true,
			},
			want: PhaseFailed,
		},
		{
			name: "removed component",
			ctx: PhaseContext{
				Removed: true,
			},
			want: PhaseFailed,
		},
		{
			name: "fully ready",
			ctx: PhaseContext{
				Ready:                      true,
				ProvisioningSucceeded:      true,
				PreviousObservedGeneration: 1,
				Generation:                 1,
			},
			want: PhaseReady,
		},
		{
			name: "spec change while previously ready",
			ctx: PhaseContext{
				WasReady:                   true,
				Degraded:                   true,
				PreviousObservedGeneration: 1,
				Generation:                 2,
				ProvisioningSucceeded:      true,
			},
			want: PhaseUpgrading,
		},
		{
			name: "degraded after regression",
			ctx: PhaseContext{
				Degraded:                   true,
				ProvisioningSucceeded:      true,
				PreviousObservedGeneration: 2,
				Generation:                 2,
			},
			want: PhaseDegraded,
		},
		{
			name: "recovered from degraded",
			ctx: PhaseContext{
				Ready:                      true,
				ProvisioningSucceeded:      true,
				PreviousObservedGeneration: 2,
				Generation:                 2,
			},
			want: PhaseReady,
		},
		{
			name: "first reconcile before provisioning",
			ctx: PhaseContext{
				PreviousObservedGeneration: 0,
				Generation:                 1,
			},
			want: PhasePending,
		},
		{
			name: "waiting for deployments on first install",
			ctx: PhaseContext{
				PreviousObservedGeneration: 0,
				Generation:                 1,
				ProvisioningSucceeded:      true,
			},
			want: PhaseInitializing,
		},
		{
			name: "waiting for deployments after prior observe",
			ctx: PhaseContext{
				PreviousObservedGeneration: 1,
				Generation:                 1,
				ProvisioningSucceeded:      true,
			},
			want: PhaseInitializing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ComputePhase(tt.ctx); got != tt.want {
				t.Fatalf("ComputePhase() = %q, want %q", got, tt.want)
			}
		})
	}
}
