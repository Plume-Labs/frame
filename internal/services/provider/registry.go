package provider

import (
	"errors"
	"fmt"
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

// NewRegistry builds the registry. A duplicate type is a programming error, so
// it panics rather than silently letting one provider shadow another.
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{byType: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		if _, dup := r.byType[p.Type()]; dup {
			panic(fmt.Sprintf("two providers registered for type %q", p.Type()))
		}
		r.byType[p.Type()] = p
		r.types = append(r.types, p.Type())
	}
	// Sorted so error messages and generated docs do not shuffle with
	// registration order.
	sort.Strings(r.types)
	return r
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
