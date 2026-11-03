package v1alpha1

import (
	"testing"
)

func TestIngressConfigStatusDeepCopy(t *testing.T) {
	var nilStatus *IngressConfigStatus
	if got := nilStatus.DeepCopy(); got != nil {
		t.Fatalf("DeepCopy of nil IngressConfigStatus = %v, want nil", got)
	}

	in := &IngressConfigStatus{Hash: "abc123"}
	out := in.DeepCopy()
	if out == nil || out == in {
		t.Fatal("DeepCopy must return a new non-nil IngressConfigStatus")
	}
	if *out != *in {
		t.Fatalf("DeepCopy = %+v, want %+v", *out, *in)
	}

	out.Hash = "deadbeef"
	if in.Hash != "abc123" {
		t.Fatalf("DeepCopy must be independent: original mutated to %s", in.Hash)
	}
}
