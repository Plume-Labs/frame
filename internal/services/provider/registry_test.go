package provider_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rmocq/frame/internal/services/provider"
)

// stub is the smallest thing that satisfies Provider, so registry behaviour can
// be tested without dragging a real service type into it.
type stub struct{ typeName string }

func (s stub) Type() string                                    { return s.typeName }
func (s stub) ParameterSchema() *provider.Schema               { return &provider.Schema{} }
func (s stub) Size(map[string]string) (provider.Sizing, error) { return provider.Sizing{}, nil }

func TestRegistryFindsARegisteredProvider(t *testing.T) {
	r := provider.NewRegistry(stub{typeName: "inference"})

	got, err := r.Get("inference")
	if err != nil {
		t.Fatalf("Get(inference) returned %v, want nil", err)
	}
	if got.Type() != "inference" {
		t.Fatalf("Get(inference) returned provider %q", got.Type())
	}
}

func TestRegistryRejectsAnUnknownType(t *testing.T) {
	r := provider.NewRegistry(stub{typeName: "inference"})

	_, err := r.Get("infrence")
	if !errors.Is(err, provider.ErrUnknownType) {
		t.Fatalf("Get(typo) returned %v, want ErrUnknownType", err)
	}
	// The message has to name the alternatives: this error reaches an operator
	// through kubectl, and "unknown type" alone tells them nothing.
	if got := err.Error(); !strings.Contains(got, "inference") {
		t.Fatalf("error %q does not list the valid types", got)
	}
}

func TestRegistryListsItsTypesInOrder(t *testing.T) {
	r := provider.NewRegistry(stub{typeName: "queue"}, stub{typeName: "database"})

	// Sorted, so the webhook's error messages and the CRD's docs do not shuffle
	// with registration order.
	want := []string{"database", "queue"}
	got := r.Types()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Types() = %v, want %v", got, want)
	}
}
