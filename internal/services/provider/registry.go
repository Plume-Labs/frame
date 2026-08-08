package provider

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ErrUnknownType is returned for a spec.type no provider answers to.
var ErrUnknownType = errors.New("unknown service type")

// Registry maps spec.type to its provider. It is built once at start-up and
// read concurrently, so it is never mutated after construction.
type Registry struct {
	byType map[string]Provider
	types  []string
}

// enforcedRootFields and enforcedPropertyFields name the only JSONSchemaProps
// fields the webhook's validateAgainstSchema actually inspects (Properties is
// the structural field that carries the per-property schemas themselves, not
// a constraint in its own right). Every other field — MinLength, Maximum,
// AdditionalProperties, and the rest of JSON Schema — would be accepted by a
// provider and silently never checked at admission, so checkSchemaIsEnforceable
// refuses it at registration instead.
var (
	enforcedRootFields     = map[string]bool{"Type": true, "Required": true, "Properties": true}
	enforcedPropertyFields = map[string]bool{"Type": true, "Description": true, "Enum": true, "Pattern": true}
)

// NewRegistry builds the registry. A duplicate type, and now a provider whose
// schema sets a constraint the webhook cannot enforce, are both programming
// errors discovered here rather than reachable from cluster input, so both
// panic: a panic at boot is far kinder than a constraint that quietly does
// nothing for a year.
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{byType: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		if _, dup := r.byType[p.Type()]; dup {
			panic(fmt.Sprintf("two providers registered for type %q", p.Type()))
		}
		checkSchemaIsEnforceable(p.Type(), p.ParameterSchema())
		r.byType[p.Type()] = p
		r.types = append(r.types, p.Type())
	}
	// Sorted so error messages and generated docs do not shuffle with
	// registration order.
	sort.Strings(r.types)
	return r
}

// checkSchemaIsEnforceable panics if a provider's ParameterSchema sets a
// JSONSchemaProps field outside the subset validateAgainstSchema enforces.
// See Provider.ParameterSchema for the supported subset and the rationale.
func checkSchemaIsEnforceable(providerType string, schema *Schema) {
	checkNoUnenforcedFields(providerType, "", schema, enforcedRootFields)

	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic, so a panic message does not shuffle between runs.

	for _, name := range names {
		prop := schema.Properties[name]
		checkNoUnenforcedFields(providerType, name, &prop, enforcedPropertyFields)
	}
}

// checkNoUnenforcedFields walks every field of a JSONSchemaProps by reflection
// and panics on the first one set outside allowed. Reflection, rather than a
// hand-picked list of "known-dangerous" fields, is what makes the gap
// unrepresentable: a JSON Schema field added later is rejected by default
// instead of silently joining the ignored set.
func checkNoUnenforcedFields(providerType, propertyName string, schema *Schema, allowed map[string]bool) {
	v := reflect.ValueOf(*schema)
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		if allowed[field.Name] || v.Field(i).IsZero() {
			continue
		}
		if propertyName == "" {
			panic(fmt.Sprintf(
				"provider %q: schema sets %s, which the webhook's validateAgainstSchema does not "+
					"enforce; the supported schema is type and required, plus type, description, enum "+
					"and pattern per property — remove %s or enforce it in validateAgainstSchema first",
				providerType, field.Name, field.Name))
		}
		panic(fmt.Sprintf(
			"provider %q: parameter %q sets %s, which the webhook's validateAgainstSchema does not "+
				"enforce; the supported per-property fields are type, description, enum and pattern — "+
				"remove %s or enforce it in validateAgainstSchema first",
			providerType, propertyName, field.Name, field.Name))
	}
}

// Get returns the provider for a type, or an error naming the valid ones. The
// error reaches an operator through kubectl, so it has to be actionable.
func (r *Registry) Get(t string) (Provider, error) {
	p, ok := r.byType[t]
	if !ok {
		return nil, fmt.Errorf("%w %q: valid types are %s",
			ErrUnknownType, t, strings.Join(r.types, ", "))
	}
	return p, nil
}

// Types lists every registered type, sorted.
func (r *Registry) Types() []string {
	out := make([]string, len(r.types))
	copy(out, r.types)
	return out
}
