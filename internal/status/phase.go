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

// Package status provides helpers for Workbenches ModuleStatus reporting.
package status

// ModuleStatus phase values per the platform ModuleStatus specification.
const (
	PhasePending      = "Pending"
	PhaseInitializing = "Initializing"
	PhaseReady        = "Ready"
	PhaseUpgrading    = "Upgrading"
	PhaseDegraded     = "Degraded"
	PhaseFailed       = "Failed"
)

// PhaseContext captures reconcile inputs used to derive status.phase.
type PhaseContext struct {
	// PreviousObservedGeneration is status.observedGeneration before this reconcile.
	PreviousObservedGeneration int64
	// Generation is the CR metadata.generation for this reconcile.
	Generation int64
	// WasReady is true when Ready=True before this reconcile started.
	WasReady bool
	// Ready is true when Ready=True after conditions were updated.
	Ready bool
	// Degraded is true when Degraded=True after conditions were updated.
	Degraded bool
	// Failed is true for unrecoverable reconcile errors (InvalidSpec, apply failures, etc.).
	Failed bool
	// Removed is true when managementState is Removed and cleanup succeeded.
	Removed bool
	// ProvisioningSucceeded is true when manifests were applied successfully.
	ProvisioningSucceeded bool
}

// ComputePhase derives the ModuleStatus phase from the current reconcile context.
//
// Priority (highest first): Failed, Ready, Upgrading, Degraded, Pending, Initializing.
func ComputePhase(ctx PhaseContext) string {
	// Removed components surface as Failed per the ModuleStatus contract (no separate Removed phase).
	if ctx.Failed || ctx.Removed {
		return PhaseFailed
	}

	if ctx.Ready {
		return PhaseReady
	}

	if ctx.WasReady &&
		ctx.Generation > ctx.PreviousObservedGeneration &&
		ctx.PreviousObservedGeneration > 0 {
		return PhaseUpgrading
	}

	if ctx.Degraded {
		return PhaseDegraded
	}

	if ctx.PreviousObservedGeneration == 0 && !ctx.ProvisioningSucceeded {
		return PhasePending
	}

	return PhaseInitializing
}
