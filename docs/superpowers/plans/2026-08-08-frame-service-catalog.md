# FrameService Service Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the service catalog's model plus its first type, so an operator can declare an inference server as a Kubernetes resource and a workload can mount its credentials.

**Architecture:** One generic `FrameService` CRD in a new API group, `services.plume-labs.io/v1alpha1`. One controller, which delegates to a Go `Provider` chosen by `spec.type`. Providers register a JSON Schema for their parameters — enforced by the validating webhook, not by the CRD — and a `Size` function that derives resource requests from those parameters. The inference provider owns its workload directly: a Deployment running llama.cpp, a Service, and a Secret.

**Tech Stack:** Go 1.26.1, Kubebuilder v4.14.0 (`/home/rmocq/bin/kubebuilder`), controller-runtime, Ginkgo v2 + Gomega with envtest, Kind for e2e, Prometheus client for metrics.

**Design source:** `docs/superpowers/specs/2026-08-08-frame-service-catalog-design.md`. Read it before Task 3 — the tasks implement it, they do not restate it.

## Global Constraints

- API group: `services.plume-labs.io`, version `v1alpha1`. Domain is `plume-labs.io`, project repo is `github.com/rmocq/frame`.
- Never hand-edit `config/crd/bases/*`, `config/rbac/role.yaml`, `config/webhook/manifests.yaml`, `**/zz_generated.*.go`, or `PROJECT`. Regenerate with `make manifests generate`.
- Never delete a `// +kubebuilder:scaffold:*` marker.
- Scaffold with `kubebuilder create api` / `kubebuilder create webhook`, never by hand-creating the files.
- `make test` must pass at the end of every task. Envtest coverage on `internal/controller` is gated at ≥45% in CI; it currently sits at 46.5% and must not drop below the gate.
- Log messages: capital first letter, no trailing period, past tense, name the object type — `log.Info("Created Deployment", "name", d.Name)`.
- Use `metav1.Condition` for status, never bespoke string fields. Every controller sets a `Ready` condition.
- Every reconcile is idempotent and re-fetches before updating.
- `make lint` is currently red with 21 pre-existing issues. Do not fix them here, and do not add new ones: check with `make lint 2>&1 | grep <your-file>`.
- The cluster's only GPU is a Tesla P4: compute capability 6.1, **7680 MiB**, one card, on node `neura-k3s-w2`. llama.cpp is the only viable backend. Never introduce vLLM or KubeAI.
- There is **no `plan` field** and no `ServicePlan` resource. Size is derived. There is **no `nodeName` field**. Frame decides placement.

---

## File Structure

**Task 1 moves existing files.** After it, the layout is:

| Path | Responsibility |
|---|---|
| `api/frame/v1alpha1/` | The seven existing CRD types (moved from `api/v1alpha1/`) |
| `internal/controller/frame/` | The six existing controllers (moved from `internal/controller/`) |
| `internal/webhook/frame/v1alpha1/` | The seven existing webhooks (moved from `internal/webhook/v1alpha1/`) |

**New files:**

| Path | Responsibility |
|---|---|
| `api/services/v1alpha1/frameservice_types.go` | The `FrameService` schema — envelope only, no provider knowledge |
| `internal/services/provider/provider.go` | The `Provider` interface, `Sizing`, `Binding`, `Result` types |
| `internal/services/provider/registry.go` | Type-string → provider lookup, and the closed list of valid types |
| `internal/services/provider/inference/catalog.go` | Model metadata: layers, KV heads, head dim, weight bytes |
| `internal/services/provider/inference/inference.go` | The inference provider: schema, `Size`, `Reconcile`, `Bind` |
| `internal/controller/services/frameservice_controller.go` | Reconcile loop, finalizer, status, deletion policy |
| `internal/controller/services/binding.go` | Secret creation and projection into other namespaces |
| `internal/controller/services/metrics.go` | Prometheus counters for the catalog |
| `internal/webhook/services/v1alpha1/frameservice_webhook.go` | Validation dispatching to the provider's schema and `Size` |
| `test/e2e/e2e_test.go` | One more spec in the existing `CRD reconciliation` context |

The provider package does not import the controller package; the controller imports providers only through the registry. That is what keeps a new type from touching the controller.

---

### Task 1: Convert the project to the multi-group layout

A second API group cannot share `api/v1alpha1/` — the package registers one `GroupVersion` with its `SchemeBuilder`, so two groups in it cannot both be registered. Kubebuilder's multi-group layout is the supported way out, and every later task assumes it. This task changes no behaviour: it is green-to-green.

**Files:**
- Modify: `PROJECT` (via the CLI only)
- Move: `api/v1alpha1/` → `api/frame/v1alpha1/`
- Move: `internal/controller/*.go` → `internal/controller/frame/`
- Move: `internal/webhook/v1alpha1/` → `internal/webhook/frame/v1alpha1/`
- Modify: `cmd/main.go`, every moved file's imports, both envtest suites' relative paths

- [ ] **Step 1: Confirm the suite is green before touching anything**

Run: `make test`
Expected: PASS, with `internal/controller` at 46.5% coverage. Write the number down; Step 9 compares against it.

- [ ] **Step 2: Switch the layout**

```bash
/home/rmocq/bin/kubebuilder edit --multigroup=true
```

Expected: `PROJECT` gains `multigroup: true`. No files move — the CLI only records the layout.

- [ ] **Step 3: Move the API types**

```bash
mkdir -p api/frame
git mv api/v1alpha1 api/frame/v1alpha1
```

- [ ] **Step 4: Move the controllers**

```bash
mkdir -p internal/controller/frame
git mv internal/controller/*.go internal/controller/frame/
```

- [ ] **Step 5: Move the webhooks**

```bash
mkdir -p internal/webhook/frame
git mv internal/webhook/v1alpha1 internal/webhook/frame/v1alpha1
```

- [ ] **Step 6: Repoint the import paths**

Two import paths changed. Rewrite both across the tree:

```bash
grep -rl 'github.com/rmocq/frame/api/v1alpha1' --include='*.go' . \
  | xargs sed -i 's|github.com/rmocq/frame/api/v1alpha1|github.com/rmocq/frame/api/frame/v1alpha1|g'
grep -rl 'github.com/rmocq/frame/internal/controller"' --include='*.go' . \
  | xargs sed -i 's|github.com/rmocq/frame/internal/controller"|github.com/rmocq/frame/internal/controller/frame"|g'
grep -rl 'github.com/rmocq/frame/internal/webhook/v1alpha1' --include='*.go' . \
  | xargs sed -i 's|github.com/rmocq/frame/internal/webhook/v1alpha1|github.com/rmocq/frame/internal/webhook/frame/v1alpha1|g'
```

`cmd/main.go` imports the controller package under an alias; check what it now reads and make the alias still name the package correctly:

Run: `grep -n 'internal/controller\|internal/webhook' cmd/main.go`

- [ ] **Step 7: Fix the envtest relative paths**

Both suites walk up to the repo root with `..` segments, and both moved one directory deeper.

In `internal/controller/frame/suite_test.go`, every `filepath.Join("..", "..", …)` becomes `filepath.Join("..", "..", "..", …)` — there are two: `CRDDirectoryPaths` and `basePath` for the envtest binaries.

In `internal/webhook/frame/v1alpha1/webhook_suite_test.go` the paths already carry three `..`; they now need four. There are three: `CRDDirectoryPaths`, the webhook `Paths`, and `basePath`.

- [ ] **Step 8: Regenerate**

```bash
make manifests generate
```

Expected: `config/crd/bases/*` unchanged in content (the group and kinds did not change), `zz_generated.deepcopy.go` regenerated under its new path.

- [ ] **Step 9: Verify nothing changed but the layout**

Run: `make test`
Expected: PASS, `internal/controller` coverage within a rounding error of the Step 1 number. A drop means a test file was left behind.

Run: `git status --short`
Expected: only renames (`R`) plus the import edits, `PROJECT`, and regenerated files. No deletions.

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "refactor: move to the kubebuilder multi-group layout

A second API group cannot live in api/v1alpha1: the package registers one
GroupVersion with its SchemeBuilder, so two groups cannot both be registered
from it. The service catalog needs services.plume-labs.io, so the
existing group moves under api/frame/ and the controllers and webhooks
follow.

Behaviour is unchanged — the CRDs, their manifests and their tests are
identical, only their paths moved."
```

---

### Task 2: Scaffold the FrameService API

**Files:**
- Create: `api/services/v1alpha1/frameservice_types.go` (scaffolded, then written)
- Create: `internal/controller/services/frameservice_controller.go` (scaffolded, left as a stub until Task 6)
- Modify: `cmd/main.go` (scaffold marker), `PROJECT` (via CLI)

**Interfaces:**
- Produces: `servicesv1alpha1.FrameService`, `.Spec` (`Type`, `Parameters`, `ServiceClass`, `Binding`, `DeletionPolicy`), `.Status` (`Phase`, `Conditions`, `Binding`, `Sizing`, `Provisioned`, `ObservedGeneration`) — every later task builds on these names.

- [ ] **Step 1: Scaffold**

```bash
/home/rmocq/bin/kubebuilder create api --group services --version v1alpha1 --kind FrameService \
  --resource --controller
```

Expected: `api/services/v1alpha1/frameservice_types.go` and `internal/controller/services/frameservice_controller.go` created, `PROJECT` updated, `cmd/main.go` gains a registration at the scaffold marker.

- [ ] **Step 2: Write the types**

Replace the scaffolded `FrameServiceSpec` and `FrameServiceStatus` in `api/services/v1alpha1/frameservice_types.go`:

```go
// FrameServiceSpec is the envelope. Everything type-specific lives in
// Parameters, which the provider owns and the webhook validates.
type FrameServiceSpec struct {
	// Type selects the provider. The valid set is closed and enforced by the
	// webhook against the provider registry, so a typo is refused at admission
	// rather than leaving an instance Pending forever.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	// Parameters are provider-owned and validated at admission against the
	// JSON Schema that provider registers — not by this CRD's own OpenAPI.
	// They are deliberately outside the API compatibility guarantee: a breaking
	// parameter change ships as a new Type value rather than redefining this one.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// ServiceClass is the tier the instance's workloads run at, so the existing
	// FrameResourceQuota and SchedulingPolicy apply to it like any other
	// workload. It never names a node: Frame decides placement.
	// +optional
	// +kubebuilder:validation:Enum=HIGH;MEDIUM;LOW
	// +kubebuilder:default=MEDIUM
	ServiceClass string `json:"serviceClass,omitempty"`

	// +optional
	Binding BindingSpec `json:"binding,omitempty"`

	// DeletionPolicy decides what happens to the instance's data when this
	// object is deleted. Retain is the default because the failure modes are
	// not symmetric: a retained volume costs disk and is visible, a deleted one
	// costs the data at the moment someone meant to redeploy.
	// +optional
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

type BindingSpec struct {
	// SecretName defaults to the FrameService's own name.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SecretName string `json:"secretName,omitempty"`

	// ProjectTo copies the credentials Secret into these namespaces. Opt-in and
	// explicit: a catalog that writes Secrets into namespaces nobody listed is a
	// cross-tenant leak dressed as convenience.
	// +optional
	ProjectTo []string `json:"projectTo,omitempty"`
}

// Sizing is what the provider derived from the parameters. It is reported
// rather than requested — nothing in the spec states it, and an operator has to
// be able to see what an instance costs.
type Sizing struct {
	// +optional
	GPU string `json:"gpu,omitempty"`
	// +optional
	GPUMemory string `json:"gpuMemory,omitempty"`
	// +optional
	CPU string `json:"cpu,omitempty"`
	// +optional
	Memory string `json:"memory,omitempty"`
}

type BindingStatus struct {
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
	// Endpoint is what a consumer connects to. Never contains credentials.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

// ProvisionedRef names one object the provider created, so kubectl describe
// explains an instance without anyone knowing the provider's internals.
type ProvisionedRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

type FrameServiceStatus struct {
	// +optional
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Degraded;Deleting
	Phase string `json:"phase,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	Binding BindingStatus `json:"binding,omitempty"`
	// +optional
	Sizing Sizing `json:"sizing,omitempty"`
	// +optional
	Provisioned []ProvisionedRef `json:"provisioned,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}
```

Add the printer columns above the `FrameService` struct, alongside the existing `+kubebuilder:object:root=true` and `+kubebuilder:subresource:status`:

```go
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.binding.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
```

Add the `corev1` import: `corev1 "k8s.io/api/core/v1"`.

- [ ] **Step 3: Leave the controller a no-op for now**

The scaffolded `Reconcile` returns `ctrl.Result{}, nil`. Leave it. Task 6 replaces it, and a controller that does nothing cannot break the suite in between.

- [ ] **Step 4: Generate and verify the CRD**

```bash
make manifests generate
```

Run: `grep -c 'deletionPolicy\|serviceClass\|projectTo' config/crd/bases/services.plume-labs.io_frameservices.yaml`
Expected: at least 3 — the fields reached the schema.

- [ ] **Step 5: Run the suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(api): add the FrameService envelope

One generic resource for the whole catalog: a type selecting a provider, a
provider-owned parameter map, and the service class, binding and deletion
policy that apply whatever the type is.

No plan field and no nodeName. Size is derived from the parameters and
reported in status.sizing; placement is the scheduler's."
```

---

### Task 3: The provider interface and registry

Pure Go, no Kubernetes API calls, no envtest. It is the seam that keeps a new service type from touching the controller.

**Files:**
- Create: `internal/services/provider/provider.go`
- Create: `internal/services/provider/registry.go`
- Test: `internal/services/provider/registry_test.go`

**Interfaces:**
- Consumes: `servicesv1alpha1.FrameService` from Task 2.
- Produces: `provider.Provider`, `provider.Sizing`, `provider.Binding`, `provider.Result`, `provider.Registry`, `provider.NewRegistry(...Provider) *Registry`, `(*Registry).Get(string) (Provider, error)`, `(*Registry).Types() []string`, `provider.ErrUnknownType`.

- [ ] **Step 1: Write the failing test**

`internal/services/provider/registry_test.go`:

```go
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
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/services/provider/...`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the interface**

`internal/services/provider/provider.go`:

```go
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
```

- [ ] **Step 4: Write the registry**

`internal/services/provider/registry.go`:

```go
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
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/services/provider/...`
Expected: PASS, 3 tests.

- [ ] **Step 6: Promote apiextensions-apiserver to a direct dependency**

Run: `go mod tidy`
Expected: `k8s.io/apiextensions-apiserver` moves out of the `// indirect` block in `go.mod`. Confirm with `grep apiextensions go.mod`.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(services): add the provider interface and registry

The seam the catalog turns on. The controller depends on this package and
never on an individual provider, so adding a service type does not touch the
reconcile loop.

Provider covers validation and sizing; Provisioner adds the workload. They
are split so the webhook can depend on the smaller half, and so a test can
register a schema-only stub without a workload behind it.

Unknown types fail with the valid ones listed: the error surfaces through
kubectl, where 'unknown service type' on its own tells an operator nothing."
```

---

### Task 4: The inference model catalog and Size

The load-bearing arithmetic. Everything else in the inference provider is plumbing; this is the part that decides whether an instance can exist on this hardware.

**Files:**
- Create: `internal/services/provider/inference/catalog.go`
- Create: `internal/services/provider/inference/inference.go` (schema and `Size` only)
- Test: `internal/services/provider/inference/inference_test.go`

**Interfaces:**
- Consumes: `provider.Provider`, `provider.Sizing`, `provider.Schema` from Task 3.
- Produces: `inference.New(gpuMemoryMiB int64) *Provider`, satisfying `provider.Provider` with `Type() == "inference"`.

- [ ] **Step 1: Write the failing test**

`internal/services/provider/inference/inference_test.go`:

```go
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
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/services/provider/inference/...`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the model catalog**

`internal/services/provider/inference/catalog.go`:

```go
package inference

import "sort"

// model is what sizing needs to know about a set of weights. The provider has
// to know the models it serves in order to size them; there is no way to derive
// a KV cache footprint from a name.
type model struct {
	// WeightsMiB is the on-GPU size of the quantised weights this provider
	// serves. Q4_K_M throughout: it is what fits on Pascal-class hardware.
	WeightsMiB int64
	// Layers, KVHeads and HeadDim give the KV cache per token:
	//   2 (K and V) x Layers x KVHeads x HeadDim x 2 bytes (f16)
	Layers  int64
	KVHeads int64
	HeadDim int64
}

// kvBytesPerToken is the cache one token occupies, in bytes.
func (m model) kvBytesPerToken() int64 {
	const kvAndV = 2
	const bytesPerElementF16 = 2
	return kvAndV * m.Layers * m.KVHeads * m.HeadDim * bytesPerElementF16
}

// catalog is the set of models this provider can serve. Adding one is a code
// change on purpose: the numbers below decide whether an instance is admitted,
// and a wrong one turns into a crash loop on a shared GPU.
var catalog = map[string]model{
	// Llama 3.1 8B Instruct, Q4_K_M. 128Ki of KV cache per token.
	"llama-3.1-8b-instruct": {WeightsMiB: 4690, Layers: 32, KVHeads: 8, HeadDim: 128},
	// Llama 3.1 70B Instruct, Q4_K_M. Listed so the refusal names a real model
	// rather than an unknown one — it cannot fit a Pascal card, and saying so
	// is more useful than pretending it does not exist.
	"llama-3.1-70b-instruct": {WeightsMiB: 40000, Layers: 80, KVHeads: 8, HeadDim: 128},
	// Qwen2.5 7B Instruct, Q4_K_M.
	"qwen2.5-7b-instruct": {WeightsMiB: 4400, Layers: 28, KVHeads: 4, HeadDim: 128},
}

// knownModels lists the catalog, sorted, for error messages.
func knownModels() []string {
	out := make([]string, 0, len(catalog))
	for name := range catalog {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Write the provider's schema and Size**

`internal/services/provider/inference/inference.go`:

```go
// Package inference provisions llama.cpp model servers.
//
// llama.cpp is the only backend this package supports, and that is a hardware
// fact rather than a preference: the cluster's Tesla P4 is Pascal, compute
// capability 6.1, and vLLM and KubeAI both need sm_7.0 or newer. The choice is
// internal to this provider, so a newer card means a new implementation behind
// the same spec.type, not an API change.
package inference

import (
	"fmt"
	"strconv"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/rmocq/frame/internal/services/provider"
)

const (
	// defaultContextLength is what llama.cpp itself defaults to.
	defaultContextLength = 4096
	// bytesPerMiB converts the KV cache arithmetic into the unit the catalog
	// and the resource requests both speak.
	bytesPerMiB = 1024 * 1024
)

// Provider serves models with llama.cpp.
type Provider struct {
	// gpuMemoryMiB is the memory of the card an instance will land on. Injected
	// rather than probed so the arithmetic is testable and so a heterogeneous
	// cluster can size against the smallest card rather than the largest.
	gpuMemoryMiB int64
}

// New builds the provider for a card of the given size.
func New(gpuMemoryMiB int64) *Provider {
	return &Provider{gpuMemoryMiB: gpuMemoryMiB}
}

func (p *Provider) Type() string { return "inference" }

func (p *Provider) ParameterSchema() *provider.Schema {
	return &apiextensionsv1.JSONSchemaProps{
		Type:     "object",
		Required: []string{"model"},
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"model": {
				Type:        "string",
				Description: "Model to serve. Must be one of: " + strings.Join(knownModels(), ", "),
				Enum:        enumOf(knownModels()),
			},
			"contextLength": {
				Type: "string",
				Description: fmt.Sprintf(
					"Context window in tokens, as a string. Defaults to %d. Sized against the "+
						"GPU: an oversized window is refused here rather than exhausting the KV "+
						"cache at runtime.", defaultContextLength),
				Pattern: `^[0-9]+$`,
			},
		},
	}
}

func enumOf(values []string) []apiextensionsv1.JSON {
	out := make([]apiextensionsv1.JSON, 0, len(values))
	for _, v := range values {
		out = append(out, apiextensionsv1.JSON{Raw: []byte(strconv.Quote(v))})
	}
	return out
}

// Size derives the footprint from the model and the requested context window,
// and refuses anything that will not fit the card.
func (p *Provider) Size(params map[string]string) (provider.Sizing, error) {
	name := params["model"]
	m, ok := catalog[name]
	if !ok {
		return provider.Sizing{}, fmt.Errorf(
			"parameters.model %q is not a model this provider serves: known models are %s",
			name, strings.Join(knownModels(), ", "))
	}

	ctx := int64(defaultContextLength)
	if raw, set := params["contextLength"]; set && raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			return provider.Sizing{}, fmt.Errorf(
				"parameters.contextLength %q is not a positive whole number of tokens", raw)
		}
		ctx = parsed
	}

	// The weights are the floor: no context window makes an oversized model fit,
	// so say that rather than blaming the context the operator can change.
	if m.WeightsMiB >= p.gpuMemoryMiB {
		return provider.Sizing{}, fmt.Errorf(
			"model %q needs %dMi for weights alone, and the GPU has %dMi",
			name, m.WeightsMiB, p.gpuMemoryMiB)
	}

	kvMiB := (m.kvBytesPerToken()*ctx + bytesPerMiB - 1) / bytesPerMiB
	totalMiB := m.WeightsMiB + kvMiB
	if totalMiB > p.gpuMemoryMiB {
		return provider.Sizing{}, fmt.Errorf(
			"model %q at contextLength %d needs %dMi (%dMi weights + %dMi KV cache) "+
				"and the GPU has %dMi: lower contextLength or pick a smaller model",
			name, ctx, totalMiB, m.WeightsMiB, kvMiB, p.gpuMemoryMiB)
	}

	return provider.Sizing{
		GPU:       "1",
		GPUMemory: fmt.Sprintf("%dMi", totalMiB),
		// Host resources scale off the model rather than being guessed: llama.cpp
		// needs enough RAM to load and mmap the weights before they reach the GPU.
		CPU:    "4",
		Memory: fmt.Sprintf("%dMi", m.WeightsMiB*2),
	}, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/services/provider/inference/... -v`
Expected: PASS, 6 tests. If `TestSizeFitsAnEightBillionModelAtEightThousandContext` reports a GPUMemory other than `5720Mi`, the catalog numbers and the test disagree — fix the catalog, not the test, and check the arithmetic: 8 KV heads × 128 head dim × 32 layers × 2 × 2 bytes = 131072 bytes per token, × 8192 tokens = 1024Mi, + 4690Mi weights = 5714Mi. Round the catalog's `WeightsMiB` to make the total land where the test says, and adjust the spec's worked example if you change it.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(inference): derive an instance's footprint from its model

The arithmetic that decides whether an inference instance can exist on this
hardware. Weights come from a small in-repo catalog — there is no way to
derive a KV cache footprint from a model name — and the cache is computed
from the requested context window: 2 x layers x kv heads x head dim x 2
bytes, per token.

Refusals carry the numbers. An operator who asks Llama 3.1 8B for 32768
tokens is told it needs 8792Mi against a 7680Mi card and that lowering the
context is the fix, rather than watching a pod crash-loop on KV exhaustion.
A model whose weights alone exceed the card is refused on the weights, since
no context window would have saved it.

The card size is injected rather than probed, so the arithmetic is testable
and a heterogeneous cluster can size against its smallest card."
```

---

### Task 5: The validating webhook

Where the generic CRD earns back what typed CRDs would have given: mistakes refused by `kubectl apply`, with the field named.

**Files:**
- Create: `internal/webhook/services/v1alpha1/frameservice_webhook.go` (scaffolded, then written)
- Test: `internal/webhook/services/v1alpha1/frameservice_webhook_test.go`
- Modify: `cmd/main.go` (registration)

**Interfaces:**
- Consumes: `provider.Registry` (Task 3), `inference.New` (Task 4).
- Produces: `webhook.FrameServiceCustomValidator{Registry: *provider.Registry}`.

- [ ] **Step 1: Scaffold the webhook**

```bash
/home/rmocq/bin/kubebuilder create webhook --group services --version v1alpha1 --kind FrameService \
  --programmatic-validation
```

Expected: `internal/webhook/services/v1alpha1/frameservice_webhook.go` and a `_test.go`, plus `config/webhook/manifests.yaml` regenerated.

- [ ] **Step 2: Write the failing test**

Replace the scaffolded test body in `internal/webhook/services/v1alpha1/frameservice_webhook_test.go` with:

```go
var _ = Describe("FrameService Webhook", func() {
	var (
		obj       *servicesv1alpha1.FrameService
		oldObj    *servicesv1alpha1.FrameService
		validator FrameServiceCustomValidator
	)

	BeforeEach(func() {
		obj = &servicesv1alpha1.FrameService{}
		oldObj = &servicesv1alpha1.FrameService{}
		validator = FrameServiceCustomValidator{
			Registry: provider.NewRegistry(inference.New(7680)),
		}
	})

	valid := func() servicesv1alpha1.FrameServiceSpec {
		return servicesv1alpha1.FrameServiceSpec{
			Type: "inference",
			Parameters: map[string]string{
				"model":         "llama-3.1-8b-instruct",
				"contextLength": "8192",
			},
			ServiceClass: "HIGH",
		}
	}

	It("admits a service that fits", func() {
		obj.Spec = valid()
		Expect(validator.ValidateCreate(ctx, obj)).To(BeNil())
	})

	It("refuses a type no provider answers to, and names the ones that exist", func() {
		obj.Spec = valid()
		obj.Spec.Type = "infrence"
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("inference"))
	})

	It("refuses parameters the provider's schema rejects", func() {
		obj.Spec = valid()
		delete(obj.Spec.Parameters, "model")
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("model"))
	})

	It("refuses an instance that cannot fit, at admission rather than at runtime", func() {
		obj.Spec = valid()
		obj.Spec.Parameters["contextLength"] = "32768"
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("7680Mi"))
	})

	It("applies the same rules on update", func() {
		oldObj.Spec = valid()
		obj.Spec = valid()
		obj.Spec.Parameters["contextLength"] = "32768"
		_, err := validator.ValidateUpdate(ctx, oldObj, obj)
		Expect(err).To(HaveOccurred())
	})

	It("refuses to change the type of an existing service", func() {
		// The provisioned workload belongs to the old provider; switching type
		// would orphan it with nothing left that knows how to clean it up.
		oldObj.Spec = valid()
		obj.Spec = valid()
		obj.Spec.Type = "database"
		_, err := validator.ValidateUpdate(ctx, oldObj, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})

	It("allows deletion", func() {
		obj.Spec = valid()
		Expect(validator.ValidateDelete(ctx, obj)).Error().NotTo(HaveOccurred())
	})
})
```

Add imports: `servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"`, `"github.com/rmocq/frame/internal/services/provider"`, `"github.com/rmocq/frame/internal/services/provider/inference"`.

- [ ] **Step 3: Run it and watch it fail**

Run: `go test ./internal/webhook/services/... -v`
Expected: FAIL — `Registry` is not a field of the scaffolded validator.

- [ ] **Step 4: Write the validator**

In `internal/webhook/services/v1alpha1/frameservice_webhook.go`, replace the scaffolded validator:

```go
// FrameServiceCustomValidator enforces what the CRD cannot.
//
// spec.parameters is a free-form map by design, so the apiserver's own schema
// cannot check it. Rather than let every mistake surface ten seconds later as a
// degraded status, this validator dispatches on spec.type to the schema the
// provider registers, and runs the provider's Size so an instance that will
// never fit is refused by kubectl rather than admitted and left Pending.
type FrameServiceCustomValidator struct {
	Registry *provider.Registry
}

func (v *FrameServiceCustomValidator) ValidateCreate(
	_ context.Context, svc *servicesv1alpha1.FrameService,
) (admission.Warnings, error) {
	return nil, v.validate(svc)
}

func (v *FrameServiceCustomValidator) ValidateUpdate(
	_ context.Context, oldObj, newObj *servicesv1alpha1.FrameService,
) (admission.Warnings, error) {
	// The provisioned workload belongs to the provider that made it. Switching
	// type would orphan it: the new provider does not recognise it, and the old
	// one is no longer consulted.
	if oldObj.Spec.Type != "" && oldObj.Spec.Type != newObj.Spec.Type {
		return nil, fmt.Errorf(
			"spec.type is immutable: it is %q and cannot become %q. Delete the service and create a new one",
			oldObj.Spec.Type, newObj.Spec.Type)
	}
	return nil, v.validate(newObj)
}

func (v *FrameServiceCustomValidator) ValidateDelete(
	_ context.Context, _ *servicesv1alpha1.FrameService,
) (admission.Warnings, error) {
	return nil, nil
}

func (v *FrameServiceCustomValidator) validate(svc *servicesv1alpha1.FrameService) error {
	p, err := v.Registry.Get(svc.Spec.Type)
	if err != nil {
		return fmt.Errorf("spec.type: %w", err)
	}

	if err := validateAgainstSchema(p.ParameterSchema(), svc.Spec.Parameters); err != nil {
		return fmt.Errorf("spec.parameters: %w", err)
	}

	if _, err := p.Size(svc.Spec.Parameters); err != nil {
		return fmt.Errorf("spec.parameters: %w", err)
	}

	for _, ns := range svc.Spec.Binding.ProjectTo {
		if ns == "" {
			return fmt.Errorf("spec.binding.projectTo contains an empty namespace")
		}
	}
	return nil
}

// validateAgainstSchema checks a parameter map against a provider's schema.
//
// Parameters are map[string]string, so the schema's job here is presence,
// allowed values and shape — not types. Running the full JSON Schema validator
// would mean converting the map to unstructured JSON on every admission for
// checks this covers directly.
func validateAgainstSchema(schema *provider.Schema, params map[string]string) error {
	for _, required := range schema.Required {
		if params[required] == "" {
			return fmt.Errorf("%s is required", required)
		}
	}

	for key, value := range params {
		prop, known := schema.Properties[key]
		if !known {
			return fmt.Errorf("%s is not a parameter this type accepts", key)
		}
		if len(prop.Enum) > 0 && !enumAllows(prop.Enum, value) {
			return fmt.Errorf("%s: %q is not one of the accepted values", key, value)
		}
		if prop.Pattern != "" {
			ok, err := regexp.MatchString(prop.Pattern, value)
			if err != nil || !ok {
				return fmt.Errorf("%s: %q does not match %s", key, value, prop.Pattern)
			}
		}
	}
	return nil
}

func enumAllows(enum []apiextensionsv1.JSON, value string) bool {
	quoted := strconv.Quote(value)
	for _, allowed := range enum {
		if string(allowed.Raw) == quoted {
			return true
		}
	}
	return false
}
```

Update `SetupFrameServiceWebhookWithManager` to take the registry and pass it into the validator.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/webhook/services/... -v`
Expected: PASS, 7 specs.

- [ ] **Step 6: Wire it into the manager**

In `cmd/main.go`, build the registry once and hand it to the webhook setup:

```go
serviceRegistry := provider.NewRegistry(inference.New(inferenceGPUMemoryMiB))
if err := serviceswebhook.SetupFrameServiceWebhookWithManager(mgr, serviceRegistry); err != nil {
	setupLog.Error(err, "unable to create webhook", "webhook", "FrameService")
	os.Exit(1)
}
```

Add a flag next to the existing ones so the card size is configurable rather than compiled in:

```go
flag.Int64Var(&inferenceGPUMemoryMiB, "inference-gpu-memory-mib", 7680,
	"Usable GPU memory per card, in MiB, that inference instances are sized against. "+
		"Defaults to the Tesla P4's 7680.")
```

- [ ] **Step 7: Run the whole suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat(services): validate provider parameters at admission

The generic CRD's one real cost is that the apiserver cannot check
spec.parameters. This is where that is paid back: the webhook dispatches on
spec.type to the schema the provider registers, then runs the provider's Size,
so a bad parameter or an instance that will never fit is refused by kubectl
apply with the field named — not admitted and left degraded ten seconds later.

spec.type is immutable. The provisioned workload belongs to the provider that
made it; switching type would orphan it, since the new provider does not
recognise it and the old one is no longer consulted.

The card size is a flag rather than a constant, defaulting to the Tesla P4's
7680MiB, so sizing does not have to be recompiled for different hardware."
```

---

### Task 6: The controller

**Files:**
- Modify: `internal/controller/services/frameservice_controller.go`
- Create: `internal/controller/services/metrics.go`
- Test: `internal/controller/services/frameservice_controller_test.go`
- Create: `internal/controller/services/suite_test.go` (envtest bootstrap for this package)

**Interfaces:**
- Consumes: `provider.Registry`, `provider.Provisioner`, `provider.Result` (Task 3).
- Produces: `FrameServiceReconciler{Client, Scheme, Recorder, Registry}`, `frameServiceFinalizer` const.

- [ ] **Step 1: Copy the envtest bootstrap**

`internal/controller/services/suite_test.go` is `internal/controller/frame/suite_test.go` with the package renamed to `services`. Copy it and change the package clause. The `..` depth is the same — both sit three levels below the root after Task 1.

- [ ] **Step 2: Write the failing test**

`internal/controller/services/frameservice_controller_test.go`:

```go
package services

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider"
)

// fakeProvisioner records what the controller asked of it, so the tests assert
// the reconcile loop's behaviour rather than any real provider's.
type fakeProvisioner struct {
	result   provider.Result
	err      error
	binding  provider.Binding
	reconciles int
}

func (f *fakeProvisioner) Type() string                      { return "fake" }
func (f *fakeProvisioner) ParameterSchema() *provider.Schema { return &provider.Schema{Type: "object"} }
func (f *fakeProvisioner) Size(map[string]string) (provider.Sizing, error) {
	return provider.Sizing{GPU: "1", GPUMemory: "512Mi", CPU: "1", Memory: "1Gi"}, nil
}
func (f *fakeProvisioner) Reconcile(context.Context, *servicesv1alpha1.FrameService) (provider.Result, error) {
	f.reconciles++
	return f.result, f.err
}
func (f *fakeProvisioner) Bind(context.Context, *servicesv1alpha1.FrameService) (provider.Binding, error) {
	return f.binding, nil
}

var _ = Describe("FrameService Controller", func() {
	const name = "test-svc"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	var svc *servicesv1alpha1.FrameService
	var fake *fakeProvisioner

	BeforeEach(func() {
		fake = &fakeProvisioner{
			result:  provider.Result{Ready: true, Reason: "Provisioned", Message: "Serving"},
			binding: provider.Binding{Endpoint: "http://test-svc.default.svc:8080"},
		}
		svc = &servicesv1alpha1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       servicesv1alpha1.FrameServiceSpec{Type: "fake", DeletionPolicy: "Retain"},
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &servicesv1alpha1.FrameService{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
	})

	r := func() *FrameServiceReconciler {
		return &FrameServiceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(100),
			Registry: provider.NewRegistry(fake),
		}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds its finalizer before doing anything else", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(svc, frameServiceFinalizer)).To(BeTrue())
		// The provider must not run before the finalizer is durable: a crash in
		// between would leave a provisioned instance with nothing tracking it.
		Expect(fake.reconciles).To(Equal(0))
	})

	It("reports Ready and publishes the endpoint and the derived sizing", func() {
		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Status.Phase).To(Equal("Ready"))
		Expect(svc.Status.Binding.Endpoint).To(Equal("http://test-svc.default.svc:8080"))
		Expect(svc.Status.Sizing.GPUMemory).To(Equal("512Mi"))
		Expect(readyCondition(svc).Status).To(Equal(metav1.ConditionTrue))
		Expect(readyCondition(svc).Reason).To(Equal("Provisioned"))
	})

	It("reports Degraded without wedging when the provider cannot finish", func() {
		fake.result = provider.Result{Ready: false, Reason: "OperatorMissing", Message: "postgresqls CRD absent"}
		_, _ = r().Reconcile(ctx, req)
		res, err := r().Reconcile(ctx, req)

		// Degrading is not an error: returning one would back off and hide the
		// reason behind controller-runtime's retry logging.
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Status.Phase).To(Equal("Degraded"))
		Expect(readyCondition(svc).Reason).To(Equal("OperatorMissing"))
	})

	It("refuses to reconcile an unknown type, and says so in status", func() {
		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		svc.Spec.Type = "nonexistent"
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())

		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Status.Phase).To(Equal("Degraded"))
		Expect(readyCondition(svc).Reason).To(Equal("UnknownType"))
	})

	It("releases its finalizer on delete", func() {
		_, _ = r().Reconcile(ctx, req)
		Expect(k8sClient.Delete(ctx, svc)).To(Succeed())

		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &servicesv1alpha1.FrameService{}))
		}, "5s").Should(BeTrue())
	})
})

func readyCondition(svc *servicesv1alpha1.FrameService) metav1.Condition {
	for _, c := range svc.Status.Conditions {
		if c.Type == "Ready" {
			return c
		}
	}
	return metav1.Condition{}
}
```

Add the `apierrors "k8s.io/apimachinery/pkg/api/errors"` import.

- [ ] **Step 3: Run it and watch it fail**

Run: `make test`
Expected: FAIL — `FrameServiceReconciler` has no `Registry` field and `frameServiceFinalizer` does not exist.

- [ ] **Step 4: Write the controller**

Replace `internal/controller/services/frameservice_controller.go`'s reconcile:

```go
const frameServiceFinalizer = "services.plume-labs.io/finalizer"

// degradedRequeue is how long a degraded instance waits before the controller
// looks again. Long enough not to hammer a missing operator, short enough that
// installing one is noticed without a restart.
const degradedRequeue = 2 * time.Minute

type FrameServiceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Registry *provider.Registry
}

// +kubebuilder:rbac:groups=services.plume-labs.io,resources=frameservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=services.plume-labs.io,resources=frameservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=services.plume-labs.io,resources=frameservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *FrameServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var svc servicesv1alpha1.FrameService
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !svc.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &svc)
	}

	// The finalizer lands before the provider runs. A crash in between would
	// otherwise leave a provisioned instance with nothing tracking it.
	if !controllerutil.ContainsFinalizer(&svc, frameServiceFinalizer) {
		controllerutil.AddFinalizer(&svc, frameServiceFinalizer)
		return ctrl.Result{}, r.Update(ctx, &svc)
	}

	p, err := r.Registry.Get(svc.Spec.Type)
	if err != nil {
		// A type the webhook would have refused can still reach here: the object
		// may predate the provider being removed. Report it rather than retrying
		// something no amount of retrying fixes.
		r.Recorder.Event(&svc, corev1.EventTypeWarning, "UnknownType", err.Error())
		frameServiceUnknownType.Inc()
		return ctrl.Result{}, r.setStatus(ctx, &svc, "Degraded", metav1.ConditionFalse,
			"UnknownType", err.Error(), nil, nil)
	}

	prov, ok := p.(provider.Provisioner)
	if !ok {
		msg := fmt.Sprintf("provider %q can validate but cannot provision", svc.Spec.Type)
		return ctrl.Result{}, r.setStatus(ctx, &svc, "Degraded", metav1.ConditionFalse,
			"NotProvisionable", msg, nil, nil)
	}

	sizing, err := prov.Size(svc.Spec.Parameters)
	if err != nil {
		return ctrl.Result{}, r.setStatus(ctx, &svc, "Degraded", metav1.ConditionFalse,
			"SizeRefused", err.Error(), nil, nil)
	}

	result, err := prov.Reconcile(ctx, &svc)
	if err != nil {
		r.Recorder.Event(&svc, corev1.EventTypeWarning, "ProvisionFailed", err.Error())
		frameServiceProvisionFailed.Inc()
		return ctrl.Result{}, fmt.Errorf("provisioning %s: %w", svc.Spec.Type, err)
	}

	if !result.Ready {
		// Degrading is not an error. Returning one would back off and bury the
		// reason in controller-runtime's retry logging instead of status.
		return ctrl.Result{RequeueAfter: degradedRequeue}, r.setStatus(ctx, &svc,
			"Degraded", metav1.ConditionFalse, result.Reason, result.Message,
			&sizing, result.Provisioned)
	}

	binding, err := prov.Bind(ctx, &svc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("binding %s: %w", svc.Name, err)
	}

	// The endpoint is publishable on its own — it carries no credential. The
	// Secret that carries the credentials is Task 8's, and status.binding.secretRef
	// stays empty until then rather than naming an object that does not exist.
	svc.Status.Binding = servicesv1alpha1.BindingStatus{Endpoint: binding.Endpoint}
	frameServiceReady.Inc()
	log.Info("Reconciled FrameService", "type", svc.Spec.Type, "endpoint", binding.Endpoint)
	return ctrl.Result{}, r.setStatus(ctx, &svc, "Ready", metav1.ConditionTrue,
		result.Reason, result.Message, &sizing, result.Provisioned)
}
```

Write `setStatus` to patch status and set the `Ready` condition, `reconcileDelete` to honour `spec.deletionPolicy` and then remove the finalizer, and `SetupWithManager` to own `Deployment`, `Service` and `Secret`.

This task publishes the endpoint and leaves `status.binding.secretRef` empty. Do not stub a binding function: an empty `secretRef` is honest about a Secret that does not exist yet, where a stub returning a name for a missing object is a lie the next reader has to discover. Task 8 fills it in.

- [ ] **Step 5: Write the metrics**

`internal/controller/services/metrics.go`, following the existing counter style:

```go
var (
	frameServiceReady = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_frameservice_ready_total",
		Help: "Total number of FrameService reconciles that reached Ready.",
	})
	frameServiceProvisionFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_frameservice_provision_failed_total",
		Help: "Total number of FrameService provisioning attempts that returned an error.",
	})
	frameServiceUnknownType = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_frameservice_unknown_type_total",
		Help: "Total number of FrameService reconciles for a type no provider answers to.",
	})
)

func init() {
	metrics.Registry.MustRegister(
		frameServiceReady, frameServiceProvisionFailed, frameServiceUnknownType)
}
```

- [ ] **Step 6: Run the tests**

Run: `make test`
Expected: PASS, 5 new specs in `internal/controller/services`.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(services): reconcile a FrameService through its provider

The loop the whole catalog runs on: resolve the provider, size the instance,
let the provider build it, publish the endpoint and the derived footprint.

The finalizer lands before the provider is ever called. A crash in between
would otherwise leave a provisioned instance with nothing tracking it.

Degrading is not an error. A provider that cannot finish — a missing backing
operator, a size that no longer fits — sets the reason in status and requeues,
rather than returning an error that would back off and bury the reason in
controller-runtime's retry logging. Only a genuine failure returns one.

An unknown type is reported rather than retried: an object can outlive the
provider that served it, and no amount of requeuing brings one back."
```

---

### Task 7: The inference provider's workload

**Files:**
- Modify: `internal/services/provider/inference/inference.go`
- Test: `internal/services/provider/inference/reconcile_test.go`

**Interfaces:**
- Consumes: `provider.Provisioner` (Task 3), `Size` (Task 4).
- Produces: `inference.Provider` now satisfies `provider.Provisioner`. Requires `inference.New` to take a client: `New(gpuMemoryMiB int64, c client.Client) *Provider`. **Update Task 5's `cmd/main.go` wiring and the webhook test's `inference.New(7680)` calls to pass `nil` — the webhook only needs `Size`.**

- [ ] **Step 1: Write the failing test**

`internal/services/provider/inference/reconcile_test.go` uses a fake client rather than envtest — the provider only creates objects, and a fake client makes the assertions about *what* it creates direct:

```go
func TestReconcileCreatesADeploymentSizedFromTheModel(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = servicesv1alpha1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	p := inference.New(7680, c)
	svc := &servicesv1alpha1.FrameService{
		ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "research"},
		Spec: servicesv1alpha1.FrameServiceSpec{
			Type:         "inference",
			ServiceClass: "HIGH",
			Parameters: map[string]string{
				"model":         "llama-3.1-8b-instruct",
				"contextLength": "8192",
			},
		},
	}

	result, err := p.Reconcile(context.Background(), svc)
	if err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}
	if len(result.Provisioned) != 2 {
		t.Fatalf("Provisioned = %v, want a Deployment and a Service", result.Provisioned)
	}

	var d appsv1.Deployment
	if err := c.Get(context.Background(),
		types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}

	container := d.Spec.Template.Spec.Containers[0]
	// The GPU is requested as a resource so the scheduler finds the card: this
	// is what stands in for a nodeName, and it keeps working when a second card
	// arrives.
	if container.Resources.Limits["nvidia.com/gpu"] != resource.MustParse("1") {
		t.Fatalf("no GPU requested: %v", container.Resources.Limits)
	}
	// The context window reaches llama.cpp, and it is the one that was sized.
	if !argsContain(container.Args, "-c", "8192") {
		t.Fatalf("args %v do not carry the sized context length", container.Args)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	// … same setup …
	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	var list appsv1.DeploymentList
	_ = c.List(ctx, &list)
	if len(list.Items) != 1 {
		t.Fatalf("reconciling twice created %d Deployments", len(list.Items))
	}
}

func TestBindReturnsTheClusterEndpoint(t *testing.T) {
	// … same setup, after a Reconcile …
	b, err := p.Bind(ctx, svc)
	if err != nil {
		t.Fatalf("Bind returned %v", err)
	}
	if b.Endpoint != "http://llama.research.svc.cluster.local:8080" {
		t.Fatalf("Endpoint = %q", b.Endpoint)
	}
	// The endpoint is published in status, so it must never carry a credential.
	if strings.Contains(b.Endpoint, "token") || strings.Contains(b.Endpoint, "@") {
		t.Fatalf("endpoint %q looks like it carries a credential", b.Endpoint)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/services/provider/inference/...`
Expected: FAIL — `New` takes one argument and there is no `Reconcile`.

- [ ] **Step 3: Implement Reconcile and Bind**

Add to `inference.go`: the client field on `Provider`, and `Reconcile` creating a Deployment and a Service via `controllerutil.CreateOrUpdate`, with

- image `ghcr.io/ggml-org/llama.cpp:server-cuda` and args `--host 0.0.0.0 --port 8080 -m <model path> -c <contextLength>`
- `nvidia.com/gpu: 1` in limits, and the CPU and memory from `Size`
- `nodeSelector` from `spec.serviceClass` via the label the FrameNode controller already writes: `frame.plume-labs.io/service-class`
- `controllerutil.SetControllerReference(svc, obj, scheme)` on both, so deleting the FrameService garbage-collects them

and `Bind` returning `http://<name>.<namespace>.svc.cluster.local:8080` with `Data` carrying the endpoint under key `endpoint`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/provider/inference/... -v`
Expected: PASS, 9 tests.

- [ ] **Step 5: Fix the callers of New**

Run: `grep -rn 'inference.New(' --include='*.go' .`
Update each to the two-argument form. The webhook only needs `Size`, so it passes `nil`; add a comment there saying why that is safe.

- [ ] **Step 6: Run the whole suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(inference): stand up the llama.cpp server

Reconcile creates the Deployment and Service, both owner-referenced to the
FrameService so deleting one garbage-collects them. Resources come from Size,
so the context window the operator asked for is the one llama.cpp is started
with and the one the memory was computed against.

The GPU is requested as nvidia.com/gpu rather than pinned with a nodeName.
The scheduler finds the only card on its own, and the same spec keeps working
the day a second one arrives."
```

---

### Task 8: Credential binding and projection

**Files:**
- Create: `internal/controller/services/binding.go`
- Test: `internal/controller/services/binding_test.go`
- Modify: `internal/controller/services/frameservice_controller.go` (call `reconcileBinding` and fill in `status.binding.secretRef`, which Task 6 deliberately left empty)

**Interfaces:**
- Consumes: `provider.Binding` (Task 3).
- Produces: `(*FrameServiceReconciler).reconcileBinding(ctx, svc, binding) (string, error)`.

- [ ] **Step 1: Write the failing test**

```go
It("writes the credentials Secret beside the service", func() { /* assert Secret exists with the binding data */ })

It("projects the Secret only into the namespaces that were listed", func() {
	// Two namespaces exist; only one is in projectTo. The other must stay empty:
	// a catalog that writes Secrets into namespaces nobody listed is a
	// cross-tenant leak dressed as convenience.
})

It("never overwrites a Secret it does not own", func() {
	// A Secret of the same name, created by someone else, is left untouched and
	// the FrameService goes Degraded with BindingConflict.
})

It("removes a projected Secret when its namespace leaves projectTo", func() {
	// Otherwise revoking access would silently leave the credentials behind.
})
```

Write each of these out in full against the envtest client, following the Task 6 test file's structure.

- [ ] **Step 2: Run it and watch it fail**

Run: `make test`
Expected: FAIL — `reconcileBinding` does not exist yet.

- [ ] **Step 3: Implement**

`binding.go` creates the Secret in the service's namespace with an owner reference, then reconciles the projected copies. Ownership is tracked with the label `frame.plume-labs.io/owned-by: <namespace>.<name>`; a Secret without that label is never written to, and a Secret carrying it whose namespace has left `projectTo` is deleted.

Projected copies cannot use an owner reference — owner references do not cross namespaces — so deletion is handled explicitly in `reconcileDelete`. Write that down in a comment, since it is the sort of thing a later reader assumes was an oversight.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(services): deliver credentials, and only where they were asked for

The Secret lands beside the FrameService with an owner reference.
spec.binding.projectTo copies it elsewhere, opt-in and explicit: a catalog
that writes Secrets into namespaces nobody listed is a cross-tenant leak
dressed as convenience.

The controller only ever writes Secrets carrying its own ownership label, so
it can never overwrite one someone else created under the same name — that
case degrades the service instead. A namespace removed from projectTo has its
copy deleted, because revoking access has to actually revoke it.

Projected copies get no owner reference: owner references do not cross
namespaces. Their deletion is explicit in reconcileDelete."
```

---

### Task 9: End-to-end on Kind

**Files:**
- Modify: `test/e2e/e2e_test.go` (one more spec in the existing `CRD reconciliation` context)
- Modify: `internal/services/provider/registry.go` if a test-only provider needs registering

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Write the spec**

Add to the `CRD reconciliation` context, following the pattern the six existing CRD specs use:

```go
It("provisions a FrameService through its provider", func() {
	applyCR(fmt.Sprintf(`
apiVersion: services.plume-labs.io/v1alpha1
kind: FrameService
metadata:
  name: e2e-inference
  namespace: %s
spec:
  type: inference
  serviceClass: HIGH
  parameters:
    model: llama-3.1-8b-instruct
    contextLength: "4096"
`, crNamespace))

	By("checking the Deployment was created and sized")
	Eventually(func(g Gomega) {
		out, err := kubectlGet(g, "deployment", "e2e-inference", crNamespace,
			`{.spec.template.spec.containers[0].resources.limits.nvidia\.com/gpu}`)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(Equal("1"))
	}).Should(Succeed())

	By("checking the credentials Secret exists")
	Eventually(func(g Gomega) {
		_, err := kubectlGet(g, "secret", "e2e-inference", crNamespace, "{.metadata.name}")
		g.Expect(err).NotTo(HaveOccurred())
	}).Should(Succeed())
})

It("refuses a FrameService that cannot fit the card", func() {
	// The refusal is the feature: it happens at admission, so there is no
	// object to inspect afterwards and no pod to crash-loop.
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`
apiVersion: services.plume-labs.io/v1alpha1
kind: FrameService
metadata:
  name: e2e-too-big
  namespace: %s
spec:
  type: inference
  parameters:
    model: llama-3.1-8b-instruct
    contextLength: "32768"
`, crNamespace))
	out, err := utils.Run(cmd)
	Expect(err).To(HaveOccurred(), "A service that cannot fit must be refused")
	Expect(out).To(ContainSubstring("7680Mi"))
})
```

Note: the pod will not become Ready in Kind — there is no GPU and the image is not pulled. The spec asserts what Frame did, not what llama.cpp did. Say so in a comment.

- [ ] **Step 2: Add `frameservices` to the teardown list**

`frameKinds` in `test/e2e/e2e_test.go` drives the finalizer release before the manager is undeployed. A FrameService carries a finalizer, so omitting it hangs the namespace deletion.

```go
var frameKinds = []string{
	"framejobs", "framenodes", "frameresourcequotas", "schedulingpolicies",
	"talosmachineconfigs", "talosupgrades", "frameusers", "frameservices",
}
```

- [ ] **Step 3: Run it**

```bash
./bin/kind delete cluster --name frame-test-e2e   # only if one is left over from a failed run
make test-e2e KIND=$PWD/bin/kind
```

Expected: `SUCCESS! -- 14 Passed | 0 Failed`.

If the run fails, delete the cluster before retrying: `make test-e2e` skips creation when a cluster already exists, and a failed run never reaches its cleanup, so the next one inherits the last one's leftovers.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "test(e2e): prove a FrameService reaches the cluster

The catalog gets the same treatment the frame.plume-labs.io CRDs now have: a
spec is admitted, the controller turns it into a Deployment and a Secret, and
status reports it.

A second spec asserts the refusal, which is as much a feature as the
provisioning: a service that cannot fit the card is rejected by kubectl apply
with the numbers, so there is no object to inspect afterwards and no pod to
crash-loop.

The pod never becomes Ready in Kind — no GPU, and the image is not pulled.
What is asserted is what Frame did, not what llama.cpp did."
```

---

### Task 10: Documentation

**Files:**
- Modify: `docs/crd-reference.md`, `docs/architecture.md`, `docs/README.md`, `README.md`, `docs/roadmap.md`
- Create: `config/samples/services_v1alpha1_frameservice.yaml`
- Modify: `config/samples/kustomization.yaml`

- [ ] **Step 1: Write the sample**

A valid FrameService that fits the card, with comments explaining that size is derived and placement is the scheduler's. Register it in `config/samples/kustomization.yaml`.

- [ ] **Step 2: Document the CRD**

Add a `FrameService` section to `docs/crd-reference.md`, in the style of the existing entries: spec, status, webhook, and the parameter schema for the inference type. State plainly that `parameters` is outside the API compatibility guarantee.

- [ ] **Step 3: Correct the counts, again**

`docs/architecture.md`, `docs/README.md` and `README.md` say "seven CRDs" and describe a single group. There are now eight across two groups, and the SDK covers six of the eight. Fix each, and check the operator/controller counts separately — as before, not every number moves the same way.

Run: `grep -rn "seven CRD\|seven \`frame\|all seven" README.md docs/`

- [ ] **Step 4: Tick the roadmap**

In `docs/roadmap.md`, mark S1's first bullet done and record what implementing it proved about the core API — the three hypotheses under "Where this may force the core API to change" in the spec are now answerable, and Phase B is unblocked or has new work, depending on the answers.

- [ ] **Step 5: Verify everything one last time**

```bash
make test && make test-e2e KIND=$PWD/bin/kind && npx tsc --noEmit && npm run build
```

Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "docs: document the service catalog

FrameService gets a reference entry and a sample that fits the card. The CRD
counts move from seven to eight across two groups — and, as when FrameUser was
documented, not every count moves the same way: the SDK covers six of the
eight, and FrameService's provider does the work a controller does for the
others.

The roadmap records what implementing S1 proved about the core API, which is
the whole reason it came before the freeze."
```

---

## Self-Review

**Spec coverage.** Every section of the design maps to a task: the CRD envelope to Task 2, the provider model to Task 3, the inference provider to Tasks 4 and 7, sizing to Task 4, placement to Task 7 (resource requests, no nodeName), admission validation to Task 5, binding and projection to Task 8, deletion policy to Task 6, testing to Tasks 6–9. The multi-group conversion (Task 1) is not in the spec — it is a prerequisite the spec did not name, and it belongs here rather than there.

**Not covered, deliberately.** The spec's "Where this may force the core API to change" is a set of hypotheses to answer by implementing, not work to schedule; Task 10 Step 4 records the answers. Database, queue and VM types are out of scope per the spec.

**Known gap.** Task 7 changes `inference.New`'s signature after Task 5 has already called it. That is called out in Task 7's Interfaces block and Step 5 rather than hidden — splitting the provider across two tasks is what makes each independently testable, and the alternative was one task large enough that a reviewer could not reject half of it.
