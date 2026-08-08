package inference

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// TestSizeGuardsTheCeilingDivisionNotJustTheMultiplication pins the exact
// point where ctx*kvBytesPerToken(), plus the ceiling division's rounding
// term (bytesPerMiB-1), stops fitting in an int64. A guard that only bounds
// the multiplication (ctx <= math.MaxInt64/kvBytesPerToken()) still admits a
// band of ctx values above the true boundary whose sum with (bytesPerMiB-1)
// overflows on its own, wrapping kvMiB negative and letting Size report a
// fit that does not exist.
//
// This is a white-box test (package inference, not inference_test) because
// the boundary is derived from the catalog's own llama-3.1-8b-instruct entry
// via its real kvBytesPerToken(), not hardcoded: it tracks that entry if its
// layer count, KV head count or head dim ever changes, rather than rotting
// into a magic number the moment someone edits the catalog.
func TestSizeGuardsTheCeilingDivisionNotJustTheMultiplication(t *testing.T) {
	m, ok := catalog["llama-3.1-8b-instruct"]
	if !ok {
		t.Fatal("catalog missing llama-3.1-8b-instruct")
	}
	perToken := m.kvBytesPerToken()

	// The largest ctx for which ctx*perToken + (bytesPerMiB-1) still fits an
	// int64 — the true safety boundary the guard must enforce, computed the
	// same way production code does.
	maxSafeCtx := (int64(math.MaxInt64) - (bytesPerMiB - 1)) / perToken

	const p4MiB = 7680 // the cluster's Tesla P4.
	// nil client, nil apiReader: this test only calls Size, which never
	// dereferences either.
	p := New(p4MiB, nil, nil)

	t.Run("largest context the guard's arithmetic must still accept", func(t *testing.T) {
		_, err := p.Size(map[string]string{
			"model":         "llama-3.1-8b-instruct",
			"contextLength": strconv.FormatInt(maxSafeCtx, 10),
		})
		if err == nil {
			t.Fatal("Size accepted a context length no real GPU could ever hold")
		}
		// No real GPU has room for this many tokens, so it must still be
		// refused — but by the ordinary too-big-for-the-card check, not by
		// the overflow guard. If the guard's own arithmetic wrongly overflows
		// at this exact value, it refuses here instead, and the message
		// changes from the total-vs-capacity accounting to this one.
		if strings.Contains(err.Error(), "too large to size") {
			t.Fatalf("overflow guard rejected a context length its own arithmetic must still accept: %v", err)
		}
	})

	t.Run("smallest context the guard must refuse", func(t *testing.T) {
		_, err := p.Size(map[string]string{
			"model":         "llama-3.1-8b-instruct",
			"contextLength": strconv.FormatInt(maxSafeCtx+1, 10),
		})
		if err == nil {
			t.Fatal("Size accepted a context length that overflows the ceiling division")
		}
		if !strings.Contains(err.Error(), "too large to size") {
			t.Fatalf("expected the overflow guard to refuse contextLength %d, got: %v", maxSafeCtx+1, err)
		}
	})
}
