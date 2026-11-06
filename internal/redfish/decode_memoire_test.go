package redfish

import (
	"encoding/json"
	"math"
	"testing"
)

// iDRAC 7 (R320, firmware 2.65) reports TotalSystemMemoryGiB as a fraction;
// an integer field made the whole system document fail to decode.
func TestComputerSystemDecodesFractionalMemory(t *testing.T) {
	var sys computerSystemJSON
	body := []byte(`{"MemorySummary":{"TotalSystemMemoryGiB":59.6046720}}`)
	if err := json.Unmarshal(body, &sys); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := int32(math.Round(sys.MemorySummary.TotalSystemMemoryGiB)); got != 60 {
		t.Fatalf("TotalMemoryGiB = %d, want 60", got)
	}
}
