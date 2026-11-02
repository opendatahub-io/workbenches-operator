package v1alpha1

import (
	"testing"
)

func TestIngressConfigStatusDeepCopy(t *testing.T) {
	var nilStatus *IngressConfigStatus
	if got := nilStatus.DeepCopy(); got != nil {
		t.Fatalf("DeepCopy of nil IngressConfigStatus = %v, want nil", got)
	}

	in := &IngressConfigStatus{Hash: "abc123", DeploymentGeneration: 7}
	out := in.DeepCopy()
	if out == nil || out == in {
		t.Fatal("DeepCopy must return a new non-nil IngressConfigStatus")
	}
	if *out != *in {
		t.Fatalf("DeepCopy = %+v, want %+v", *out, *in)
	}

	out.DeploymentGeneration = 9
	if in.DeploymentGeneration != 7 {
		t.Fatalf("DeepCopy must be independent: original mutated to %d", in.DeploymentGeneration)
	}
}
