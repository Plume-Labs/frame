package inference_test

import (
	"strings"
	"testing"

	"github.com/rmocq/frame/internal/services/provider/inference"
)

// The cluster's Tesla P4 reports 7680 MiB.
const p4MiB = 7680

func TestSizeFitsAnEightBillionModelAtEightThousandContext(t *testing.T) {
	p := inference.New(p4MiB)

	got, err := p.Size(map[string]string{
		"model":         "llama-3.1-8b-instruct",
		"contextLength": "8192",
	})
	if err != nil {
		t.Fatalf("Size returned %v, want nil", err)
	}

	// 4.58Gi of Q4_K_M weights + 8192 tokens x 128Ki of KV cache = 5720Mi.
	if got.GPUMemory != "5720Mi" {
		t.Fatalf("GPUMemory = %q, want 5720Mi", got.GPUMemory)
	}
	if got.GPU != "1" {
		t.Fatalf("GPU = %q, want 1", got.GPU)
	}
}

func TestSizeRefusesTheSameModelAtThirtyTwoThousandContext(t *testing.T) {
	p := inference.New(p4MiB)

	_, err := p.Size(map[string]string{
		"model":         "llama-3.1-8b-instruct",
		"contextLength": "32768",
	})
	if err == nil {
		t.Fatal("Size accepted 32768 context on a 7680MiB card, want a refusal")
	}
	// The refusal reaches an operator through kubectl apply, so it has to carry
	// the numbers, not just the verdict.
	for _, want := range []string{"8792Mi", "7680Mi", "contextLength"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestSizeRefusesAModelTooBigForTheCardAtAnyContext(t *testing.T) {
	p := inference.New(p4MiB)

	_, err := p.Size(map[string]string{
		"model":         "llama-3.1-70b-instruct",
		"contextLength": "512",
	})
	if err == nil {
		t.Fatal("Size accepted a 70B model on a 7680MiB card")
	}
	if !strings.Contains(err.Error(), "weights") {
		t.Fatalf("error %q does not say the weights alone do not fit", err.Error())
	}
}

func TestSizeRefusesAnUnknownModel(t *testing.T) {
	p := inference.New(p4MiB)

	_, err := p.Size(map[string]string{"model": "gpt-9", "contextLength": "1024"})
	if err == nil {
		t.Fatal("Size accepted an unknown model")
	}
	// Naming the known models is the difference between a dead end and a fix.
	if !strings.Contains(err.Error(), "llama-3.1-8b-instruct") {
		t.Fatalf("error %q does not list the known models", err.Error())
	}
}

func TestSizeRefusesANonNumericContextLength(t *testing.T) {
	p := inference.New(p4MiB)

	_, err := p.Size(map[string]string{
		"model":         "llama-3.1-8b-instruct",
		"contextLength": "lots",
	})
	if err == nil {
		t.Fatal("Size accepted a non-numeric contextLength")
	}
}

// TestSizeRefusesAContextLengthThatWouldOverflowTheKVMultiplication guards
// the fix for a request whose contextLength is large enough that
// ctx*kvBytesPerToken wraps int64: for llama-3.1-8b-instruct's 131072
// bytes/token, that is anything above roughly 7.04e13 tokens. Before the
// fix, the wrapped (and possibly negative) product could make an impossible
// request look like it fit within the card's memory. A digit-only string is
// all it takes to reach this from an admission request, so Size must refuse
// it rather than silently wrap.
func TestSizeRefusesAContextLengthThatWouldOverflowTheKVMultiplication(t *testing.T) {
	p := inference.New(p4MiB)

	_, err := p.Size(map[string]string{
		"model":         "llama-3.1-8b-instruct",
		"contextLength": "100000000000000",
	})
	if err == nil {
		t.Fatal("Size accepted a contextLength that overflows the KV cache multiplication")
	}
}

func TestTypeAndSchema(t *testing.T) {
	p := inference.New(p4MiB)

	if p.Type() != "inference" {
		t.Fatalf("Type() = %q, want inference", p.Type())
	}
	schema := p.ParameterSchema()
	if _, ok := schema.Properties["model"]; !ok {
		t.Fatal("schema has no model property")
	}
	if len(schema.Required) == 0 || schema.Required[0] != "model" {
		t.Fatalf("schema.Required = %v, want model first", schema.Required)
	}
}
