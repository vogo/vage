package interrupt

import (
	"context"
	"testing"
)

func TestApprovedExecute_RoundTrip(t *testing.T) {
	if IsApprovedExecute(context.Background()) {
		t.Fatal("plain context must not be approved")
	}
	if IsApprovedExecute(nil) {
		t.Fatal("nil context must not be approved")
	}

	ctx := WithApprovedExecute(context.Background())
	if !IsApprovedExecute(ctx) {
		t.Fatal("WithApprovedExecute context must report true")
	}

	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if !IsApprovedExecute(child) {
		t.Fatal("derived context must keep the approved mark")
	}
}
