package orchestrator

import (
	"context"
	"testing"
)

func TestCancelRegistry(t *testing.T) {
	reg := newCancelRegistry()
	_, cancel := context.WithCancel(context.Background())
	cancelled := false
	reg.register("run1", func() { cancelled = true; cancel() })

	if !reg.cancel("run1") {
		t.Fatal("cancel(run1) = false, want true")
	}
	if !cancelled {
		t.Error("registered cancel func was not invoked")
	}

	reg.deregister("run1")
	if reg.cancel("run1") {
		t.Error("cancel after deregister = true, want false")
	}
	if reg.cancel("nope") {
		t.Error("cancel(unknown) = true, want false")
	}
}
