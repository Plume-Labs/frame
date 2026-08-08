package provider_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/rmocq/frame/internal/services/provider"
)

// stub is the smallest thing that satisfies Provider, so registry behaviour can
// be tested without dragging a real service type into it. schema is settable
// so the registration-time schema check can be exercised; a nil schema
// defaults to the empty one every prior test relied on.
type stub struct {
	typeName string
	schema   *provider.Schema
}

func (s stub) Type() string { return s.typeName }

func (s stub) ParameterSchema() *provider.Schema {
	if s.schema != nil {
		return s.schema
	}
	return &provider.Schema{}
}

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

func TestRegistryAcceptsASchemaWithinTheEnforcedSubset(t *testing.T) {
	schema := &provider.Schema{
		Type:     "object",
		Required: []string{"model"},
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"model": {
				Type:        "string",
				Description: "Model to serve.",
				Enum:        []apiextensionsv1.JSON{{Raw: []byte(`"a"`)}},
			},
			"contextLength": {
				Type:    "string",
				Pattern: `^[0-9]+$`,
			},
		},
	}

	// Must not panic: every field set here is one validateAgainstSchema enforces.
	provider.NewRegistry(stub{typeName: "inference", schema: schema})
}

func TestRegistryPanicsOnAPropertyConstraintTheWebhookIgnores(t *testing.T) {
	minLength := int64(8)
	schema := &provider.Schema{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"password": {
				Type:      "string",
				MinLength: &minLength,
			},
		},
	}

	msg := panicMessage(t, func() { provider.NewRegistry(stub{typeName: "vault", schema: schema}) })
	if !strings.Contains(msg, "password") {
		t.Fatalf("panic message %q does not name the property", msg)
	}
	if !strings.Contains(msg, "MinLength") {
		t.Fatalf("panic message %q does not name the offending field", msg)
	}
}

func TestRegistryPanicsOnAdditionalProperties(t *testing.T) {
	schema := &provider.Schema{
		Type:                 "object",
		AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{Allows: true},
	}

	// The webhook refuses unknown parameter keys unconditionally, so a
	// provider setting this would believe it had opened the parameter set
	// while being silently overruled — the same class of gap as MinLength.
	msg := panicMessage(t, func() { provider.NewRegistry(stub{typeName: "vault", schema: schema}) })
	if !strings.Contains(msg, "AdditionalProperties") {
		t.Fatalf("panic message %q does not name AdditionalProperties", msg)
	}
}

// panicMessage runs fn, fails the test if it does not panic, and returns the
// recovered value's message.
func panicMessage(t *testing.T, fn func()) (msg string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic, got none")
		}
		msg = fmt.Sprint(r)
	}()
	fn()
	return ""
}
