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
	//
	// Only a subset of JSONSchemaProps is enforced: on the root schema, Type
	// and Required; on each entry of Properties, Type, Description, Enum and
	// Pattern. Setting anything else — MinLength, Minimum, Maximum,
	// MultipleOf, Format, AdditionalProperties, nested Properties, or any
	// other JSON Schema field — is refused at registry construction with a
	// panic naming the property and the field, rather than being silently
	// accepted and never checked at admission.
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

	// Reconcile builds or converges the instance and reports what exists.
	//
	// Ownership is a requirement, not a suggestion, because it is what gives
	// spec.deletionPolicy its meaning: the controller itself has no idea what
	// kind of objects a given provider creates, so it cannot enumerate them at
	// delete time. It can only rely on Kubernetes garbage collection acting on
	// whatever ownership Reconcile set up.
	//
	//   - Every object that exposes the instance — a Service, a credentials
	//     Secret, anything a consumer talks to or reads — MUST be created with
	//     controllerutil.SetControllerReference against the FrameService.
	//     Garbage collection then removes it the moment the FrameService is
	//     deleted, regardless of deletionPolicy: exposure never survives its
	//     FrameService.
	//   - Every object that holds the instance's data — a PersistentVolumeClaim,
	//     a delegating operator's own CR — MUST NOT carry that owner reference.
	//     That is what lets deletionPolicy: Retain (the default) keep it after
	//     the FrameService and its exposing objects are gone, and it is why a
	//     provider author who gets this backwards leaks nothing: forgetting the
	//     owner reference on an exposing object is the failure mode that leaves
	//     an orphaned Service or Secret behind on every delete, silent until
	//     someone finds it.
	//
	// Objects the provider wants deleted outright under deletionPolicy: Delete
	// — typically the data objects above, which by design outlive owner-reference
	// GC — belong in Result.Provisioned: it is the only handle the generic
	// reconcileDelete has on them, since Provisioner exposes no teardown hook.
	Reconcile(ctx context.Context, svc *servicesv1alpha1.FrameService) (Result, error)

	// Bind returns what a consumer needs to reach the instance once Reconcile
	// reports Ready.
	Bind(ctx context.Context, svc *servicesv1alpha1.FrameService) (Binding, error)
}
