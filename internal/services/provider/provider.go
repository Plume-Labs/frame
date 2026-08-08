// Package provider is the seam between the FrameService controller and the
// service types it can provision. The controller knows this package; it never
// knows an individual provider, which is what lets a new type land without
// touching the reconcile loop.
package provider

import (
	"context"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
)

// Schema is the JSON Schema a provider validates its parameters against.
type Schema = apiextensionsv1.JSONSchemaProps

// Sizing is the resource footprint a provider derived from an instance's
// parameters. Quantities are strings so they can be surfaced in status without
// the caller reparsing them.
type Sizing struct {
	GPU       string
	GPUMemory string
	CPU       string
	Memory    string
}

// Binding is what a consumer needs to reach an instance.
type Binding struct {
	// Data becomes the credentials Secret.
	Data map[string][]byte
	// Endpoint is safe to publish in status: it carries no credential.
	Endpoint string
}

// Result is what one reconcile pass achieved.
type Result struct {
	// Ready is true once the instance is serving.
	Ready bool
	// Reason and Message go straight into the Ready condition.
	Reason  string
	Message string
	// Provisioned lists the objects that now exist, for status.
	Provisioned []servicesv1alpha1.ProvisionedRef
}

// Provider provisions one service type.
type Provider interface {
	// Type is the spec.type value this provider answers to.
	Type() string

	// ParameterSchema is what the webhook validates spec.parameters against.
	// The CRD cannot: parameters are a free-form map by design.
	ParameterSchema() *Schema

	// Size derives the resources this instance needs from its parameters. It
	// runs at admission as well as during reconcile, so an instance that cannot
	// fit is refused by kubectl with the numbers rather than admitted and left
	// Pending against a cluster that will never have room.
	Size(params map[string]string) (Sizing, error)
}

// Provisioner is a Provider that also creates and binds the workload. It is
// separate from Provider so the webhook can depend on validation and sizing
// alone, and so a test can register a schema-only stub.
type Provisioner interface {
	Provider
	Reconcile(ctx context.Context, svc *servicesv1alpha1.FrameService) (Result, error)
	Bind(ctx context.Context, svc *servicesv1alpha1.FrameService) (Binding, error)
}
