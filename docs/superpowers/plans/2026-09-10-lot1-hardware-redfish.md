# Lot 1 — hardware over Redfish: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a signed-in person can see a physical server's inventory, sensors and event log in the Frame console, and can power it on, shut it down or restart it — under their own identity, with the write recorded.

**Architecture:** a new `FrameMachine` CRD (`v1beta1` only, no conversion webhook) is reconciled by a controller that polls the machine's BMC over Redfish and writes inventory, sensors and the recent event log into `.status`. The console reads the CRD through the existing `frame-uiproxy` path and writes `spec.powerRequest` to act. No new network path between browser and apiserver.

**Tech Stack:** Go 1.26.1, controller-runtime, kubebuilder markers, CEL validation, envtest + ginkgo/gomega; React 19 + Vite + vitest for the console. **No new Go or npm dependency** — see Global Constraints.

**Spec:** `docs/superpowers/specs/2026-09-10-lot1-hardware-redfish-design.md`

## Global Constraints

- **`v1beta1` is frozen.** `FrameMachine` is a *new* kind and therefore unconstrained by the freeze, but it must ship **without a conversion webhook** and with **no `v1alpha1` counterpart**. Do not add it to `config/crd/patches/` and do not create `api/frame/v1alpha1/framemachine_types.go`. `FrameTask` is the precedent.
- **No new dependency, Go or npm.** The Redfish client is hand-written on `net/http` and `encoding/json`. Do not add `gofish` or any other Redfish library.
- **Keep files under 500 lines.** `src/lib/frame-sdk.ts` is already ~3480 lines and over this rule; the client added in Task 8 must be thin (target under 130 lines) and all pure logic goes in `src/lib/machines.ts` instead.
- **Never run `bin/crd-render/`'s output against the cluster.** It switches CRD conversion to a webhook pointing at a service that does not exist. `make test` invoking `crd-render` is fine; applying its output is not.
- **Never use bare `git stash` / `git stash pop`.** The stash stack is shared across worktrees and other sessions use it. Set work aside with a WIP commit instead.
- **Never commit secrets, credentials or `.env` files.** BMC credentials live in a Secret created out of band; sample manifests carry placeholder values only.
- **Never add a `Co-Authored-By` trailer to commits.**
- **RBAC tiers are `admin`, `editor`, `viewer`.** There is no "operator" tier: `frame:operators` is a *group* and it binds to the **editor** ClusterRole. Aggregated roles select on the `rbac.frame.plume-labs.io/tier` label.
- **`X-Frame-Action` is bounded to 200 characters at construction.** `FrameTaskSpec.Action` caps there and an overflow silently drops the audit record rather than failing the write.
- The repo's tests run with `make test` (Go) and `npm test` (console, `vitest run`). `npm run build` must stay clean.

---

### Task 1: The `FrameMachine` API type

**Files:**
- Create: `api/frame/v1beta1/framemachine_types.go`
- Create: `internal/controller/frame/framemachine_v1beta1_schema_test.go`
- Modify: `config/crd/kustomization.yaml` (add the generated CRD to `resources`, before the `+kubebuilder:scaffold:crdkustomizeresource` marker — **do not** add a `patches/` entry)
- Generated (by `make manifests generate`): `api/frame/v1beta1/zz_generated.deepcopy.go`, `config/crd/bases/frame.plume-labs.io_framemachines.yaml`

**Interfaces:**
- Consumes: nothing.
- Produces: the Go types every later task uses — `FrameMachine`, `FrameMachineSpec`, `BMCSpec`, `BMCTLSSpec`, `PowerRequestSpec`, `PowerAction`, `FrameMachineStatus`, `MachineInventory`, `ProcessorInfo`, `MemoryModuleInfo`, `DriveInfo`, `NetworkAdapterInfo`, `MachineSensors`, `TemperatureReading`, `FanReading`, `PowerSupplyReading`, `EventLogEntry`.

- [ ] **Step 1: Write the type file**

Create `api/frame/v1beta1/framemachine_types.go`. Copy the Apache licence header verbatim from the top of `api/frame/v1beta1/frametask_types.go`, then:

```go
package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PowerAction is a request to change the machine's power state, clear its
// event log, or set its identification LED.
// +kubebuilder:validation:Enum=On;GracefulShutdown;ForceOff;ForceRestart;ClearSEL;IndicatorLedOn;IndicatorLedOff
type PowerAction string

const (
	PowerActionOn              PowerAction = "On"
	PowerActionGracefulShutdown PowerAction = "GracefulShutdown"
	PowerActionForceOff        PowerAction = "ForceOff"
	PowerActionForceRestart    PowerAction = "ForceRestart"
	PowerActionClearSEL        PowerAction = "ClearSEL"
	PowerActionIndicatorLedOn  PowerAction = "IndicatorLedOn"
	PowerActionIndicatorLedOff PowerAction = "IndicatorLedOff"
)

// BMCTLSSpec says how the controller verifies the BMC's certificate. There is
// deliberately no permissive default: an iLO4 ships a self-signed certificate,
// so a machine registered without a choice here sits Reachable=false with a
// TLS error until someone makes one. The bypass is a value in the spec,
// visible in `kubectl get -o yaml` and in review, rather than a default buried
// in the client.
type BMCTLSSpec struct {
	// InsecureSkipVerify disables certificate verification entirely.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// CABundleRef names a ConfigMap in the same namespace holding the CA that
	// signed the BMC's certificate under the key `ca.crt`.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	CABundleRef string `json:"caBundleRef,omitempty"`
}

// BMCSpec locates the machine's baseboard management controller.
//
// +kubebuilder:validation:XValidation:rule="self.tls.insecureSkipVerify == true || has(self.tls.caBundleRef)",message="bmc.tls must set either insecureSkipVerify or caBundleRef"
type BMCSpec struct {
	// Address is the IP of the management port.
	//
	// It is an IP literal and not a hostname on purpose. The controller
	// connects to this address carrying the credentials named below, which
	// makes this field a server-side request forgery primitive; requiring a
	// literal takes name resolution out of that path. Loopback is the
	// operator's own pod and link-local is where cloud metadata services
	// live, so both are refused.
	//
	// MaxLength is what lets the CEL cost estimator bound isIP(), the same
	// reason FrameNodeSpec.IP carries one. 45 matches that field.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="isIP(self)",message="bmc.address must be a valid IP address"
	// +kubebuilder:validation:XValidation:rule="!cidr('127.0.0.0/8').containsIP(self)",message="bmc.address must not be a loopback address"
	// +kubebuilder:validation:XValidation:rule="!cidr('169.254.0.0/16').containsIP(self)",message="bmc.address must not be a link-local address"
	Address string `json:"address"`

	// CredentialsRef names a Secret in the same namespace holding the keys
	// `username` and `password`. Only the controller reads it; no console
	// tier is granted `secrets`.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	CredentialsRef string `json:"credentialsRef"`

	// TLS is required, and one of its two fields must be set.
	// +kubebuilder:validation:Required
	TLS BMCTLSSpec `json:"tls"`
}

// PowerRequestSpec is an action, not a desired state.
//
// A controller holding "desired: On" would turn a machine back on after
// someone pressed its physical power button — it would fight the person
// standing at the rack — and could not express a restart at all, since the
// state before and after is identical. The controller acts only when
// RequestedAt is later than status.lastPowerActionAt. The same pattern is
// already in this codebase: lot 2 restarts a Deployment by writing
// restartedAt.
type PowerRequestSpec struct {
	// +kubebuilder:validation:Required
	Action PowerAction `json:"action"`

	// +kubebuilder:validation:Required
	RequestedAt metav1.Time `json:"requestedAt"`
}

// FrameMachineSpec defines the desired state of FrameMachine.
type FrameMachineSpec struct {
	// +kubebuilder:validation:Required
	BMC BMCSpec `json:"bmc"`

	// +optional
	PowerRequest *PowerRequestSpec `json:"powerRequest,omitempty"`

	// NodeRef names the Kubernetes node this chassis carries, when it carries
	// one. It names a Kubernetes node and not a FrameNode deliberately:
	// FrameNode's provisioning flow is Talos-shaped and the estate moved to a
	// Debian-based image (see docs/provisioning.md), whereas a Kubernetes node
	// name is true regardless of how the machine was installed.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	NodeRef string `json:"nodeRef,omitempty"`
}

// ProcessorInfo describes one installed CPU.
type ProcessorInfo struct {
	Socket string `json:"socket,omitempty"`
	Model  string `json:"model,omitempty"`
	Cores  int32  `json:"cores,omitempty"`
	Threads int32 `json:"threads,omitempty"`
}

// MemoryModuleInfo describes one installed DIMM and where it sits.
type MemoryModuleInfo struct {
	Slot         string `json:"slot,omitempty"`
	SizeMiB      int32  `json:"sizeMiB,omitempty"`
	Type         string `json:"type,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
}

// DriveInfo describes one drive the BMC can see.
type DriveInfo struct {
	Name     string `json:"name,omitempty"`
	Model    string `json:"model,omitempty"`
	SizeGB   int32  `json:"sizeGB,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Health   string `json:"health,omitempty"`
}

// NetworkAdapterInfo describes one network port.
type NetworkAdapterInfo struct {
	Name   string `json:"name,omitempty"`
	MAC    string `json:"mac,omitempty"`
	Status string `json:"status,omitempty"`
}

// MachineInventory is what the machine is made of.
type MachineInventory struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	SerialNumber string `json:"serialNumber,omitempty"`
	BIOSVersion  string `json:"biosVersion,omitempty"`
	BMCFirmware  string `json:"bmcFirmware,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=8
	Processors []ProcessorInfo `json:"processors,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=48
	MemoryModules []MemoryModuleInfo `json:"memoryModules,omitempty"`

	TotalMemoryGiB int32 `json:"totalMemoryGiB,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=32
	Drives []DriveInfo `json:"drives,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=16
	NetworkAdapters []NetworkAdapterInfo `json:"networkAdapters,omitempty"`
}

// TemperatureReading is one temperature sensor.
type TemperatureReading struct {
	Name          string `json:"name,omitempty"`
	Celsius       int32  `json:"celsius,omitempty"`
	UpperCritical *int32 `json:"upperCritical,omitempty"`
	Health        string `json:"health,omitempty"`
}

// FanReading is one fan. Units is what the BMC reported the reading in —
// iLO4 commonly says Percent where other vendors say RPM, so the number is
// meaningless without it.
type FanReading struct {
	Name    string `json:"name,omitempty"`
	Reading int32  `json:"reading,omitempty"`
	Units   string `json:"units,omitempty"`
	Health  string `json:"health,omitempty"`
}

// PowerSupplyReading is one PSU.
type PowerSupplyReading struct {
	Name                 string `json:"name,omitempty"`
	Health               string `json:"health,omitempty"`
	State                string `json:"state,omitempty"`
	LastPowerOutputWatts int32  `json:"lastPowerOutputWatts,omitempty"`
}

// MachineSensors is what the machine currently reads.
type MachineSensors struct {
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Temperatures []TemperatureReading `json:"temperatures,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=32
	Fans []FanReading `json:"fans,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=8
	PowerSupplies []PowerSupplyReading `json:"powerSupplies,omitempty"`

	PowerConsumedWatts int32 `json:"powerConsumedWatts,omitempty"`
}

// EventLogEntry is one line of the machine's event log.
type EventLogEntry struct {
	ID       string      `json:"id,omitempty"`
	Severity string      `json:"severity,omitempty"`
	Message  string      `json:"message,omitempty"`
	Created  metav1.Time `json:"created,omitempty"`
}

// FrameMachineStatus defines the observed state of FrameMachine.
//
// No status.phase: the Ready-equivalent condition is Reachable, and its reason
// carries why — Probed when it succeeded, and one of TLSError, AuthFailed,
// Timeout, Unsupported, CredentialsUnavailable or ProbeFailed when it did not.
type FrameMachineStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	PowerState string `json:"powerState,omitempty"`

	// +optional
	IndicatorLED string `json:"indicatorLED,omitempty"`

	// +optional
	Inventory *MachineInventory `json:"inventory,omitempty"`

	// +optional
	Sensors *MachineSensors `json:"sensors,omitempty"`

	// EventLog holds the most recent entries only. etcd is not a log store: a
	// machine up for years can hold thousands, and twenty-five is enough to
	// answer "why did it reboot", which is the question the log is for.
	// +optional
	// +kubebuilder:validation:MaxItems=25
	EventLog []EventLogEntry `json:"eventLog,omitempty"`

	// EventLogCounts is the count per severity across the whole log, not just
	// the retained entries.
	// +optional
	EventLogCounts map[string]int32 `json:"eventLogCounts,omitempty"`

	// +optional
	EventLogTotal int32 `json:"eventLogTotal,omitempty"`

	// LastProbeAt is when the values above were read. Every panel renders it:
	// a sensor value with no timestamp is a lie the moment the BMC stops
	// answering.
	// +optional
	LastProbeAt *metav1.Time `json:"lastProbeAt,omitempty"`

	// +optional
	LastPowerAction string `json:"lastPowerAction,omitempty"`

	// LastPowerActionAt is the guard: a powerRequest is executed only when its
	// requestedAt is strictly later than this.
	// +optional
	LastPowerActionAt *metav1.Time `json:"lastPowerActionAt,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fm
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=".spec.bmc.address"
// +kubebuilder:printcolumn:name="Power",type=string,JSONPath=".status.powerState"
// +kubebuilder:printcolumn:name="Reachable",type=string,JSONPath=`.status.conditions[?(@.type=="Reachable")].status`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=".status.inventory.model"
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=".spec.nodeRef"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameMachine is the Schema for the framemachines API.
type FrameMachine struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec FrameMachineSpec `json:"spec,omitempty"`

	// +optional
	Status FrameMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameMachineList contains a list of FrameMachine.
type FrameMachineList struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard list metadata
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []FrameMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameMachine{}, &FrameMachineList{})
}
```

- [ ] **Step 2: Generate deepcopy and the CRD manifest**

Run: `make manifests generate`
Expected: `api/frame/v1beta1/zz_generated.deepcopy.go` gains `FrameMachine*` methods, and `config/crd/bases/frame.plume-labs.io_framemachines.yaml` appears.

- [ ] **Step 3: Register the CRD in kustomize**

In `config/crd/kustomization.yaml`, add this line to `resources`, immediately before the `# +kubebuilder:scaffold:crdkustomizeresource` marker:

```yaml
- bases/frame.plume-labs.io_framemachines.yaml
```

Add **nothing** to the `patches:` list. FrameMachine has no conversion webhook, by design.

- [ ] **Step 4: Write the failing schema test**

Create `internal/controller/frame/framemachine_v1beta1_schema_test.go`. Copy the Apache licence header verbatim from `internal/controller/frame/nodetuning_v1beta1_schema_test.go`, then:

```go
package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs go through the real apiserver (k8sClient / envtest, wired in
// suite_test.go), because the thing under test is the CEL rule the apiserver
// compiles — not a Go zero-value fact. A struct-literal assertion would pass
// unchanged if the marker were deleted.
var _ = Describe("FrameMachine v1beta1 schema", func() {
	newMachine := func(name, address string) *framev1beta1.FrameMachine {
		return &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameMachineSpec{
				BMC: framev1beta1.BMCSpec{
					Address:        address,
					CredentialsRef: "ilo-credentials",
					TLS:            framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
				},
			},
		}
	}

	It("admits a routable IPv4 management address", func() {
		fm := newMachine("fm-ok", "192.168.2.50")
		Expect(k8sClient.Create(context.Background(), fm)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), fm)).To(Succeed())
	})

	It("refuses a hostname", func() {
		err := k8sClient.Create(context.Background(), newMachine("fm-host", "ilo.example.com"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must be a valid IP address"))
	})

	It("refuses a loopback address, so the controller cannot be aimed at its own pod", func() {
		err := k8sClient.Create(context.Background(), newMachine("fm-loop", "127.0.0.1"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must not be a loopback address"))
	})

	It("refuses a link-local address, where metadata services live", func() {
		err := k8sClient.Create(context.Background(), newMachine("fm-ll", "169.254.169.254"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must not be a link-local address"))
	})

	It("refuses a TLS block that chooses neither verification nor bypass", func() {
		fm := newMachine("fm-tls", "192.168.2.51")
		fm.Spec.BMC.TLS = framev1beta1.BMCTLSSpec{}
		err := k8sClient.Create(context.Background(), fm)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("insecureSkipVerify or caBundleRef"))
	})

	It("accepts a CA bundle as the other way to satisfy the TLS rule", func() {
		fm := newMachine("fm-ca", "192.168.2.52")
		fm.Spec.BMC.TLS = framev1beta1.BMCTLSSpec{CABundleRef: "ilo-ca"}
		Expect(k8sClient.Create(context.Background(), fm)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), fm)).To(Succeed())
	})

	It("caps the retained event log at 25 entries", func() {
		fm := newMachine("fm-log", "192.168.2.53")
		Expect(k8sClient.Create(context.Background(), fm)).To(Succeed())
		entries := make([]framev1beta1.EventLogEntry, 0, 26)
		for i := 0; i < 26; i++ {
			entries = append(entries, framev1beta1.EventLogEntry{ID: "e", Severity: "OK", Message: "m"})
		}
		fm.Status.EventLog = entries
		err := k8sClient.Status().Update(context.Background(), fm)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must have at most 25 items"))
		Expect(k8sClient.Delete(context.Background(), fm)).To(Succeed())
	})
})
```

- [ ] **Step 5: Run the schema test and watch it fail for the right reason**

Run: `make test`
Expected: the FrameMachine specs fail because the CRD is not installed in envtest yet, or because the type does not compile. A failure naming a *missing* kind is correct at this point; a failure naming a CEL compile error is not — see Step 6.

- [ ] **Step 6: Verify the CEL rules compile against the apiserver**

The `cidr(...).containsIP(...)` functions come from Kubernetes' IP/CIDR CEL library, available from 1.31; this cluster runs 1.36 and the repo already uses `isIP()`. If envtest rejects the rule with a **compilation** error rather than a validation failure, replace the two `cidr(...)` rules with these, which use only string functions and cannot fail to compile:

```go
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('127.')",message="bmc.address must not be a loopback address"
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('169.254.')",message="bmc.address must not be a link-local address"
```

If you make this substitution, say so in your report — it is a deviation from the plan and the reviewer needs to know the bound became coarser.

- [ ] **Step 7: Make the test pass**

Run: `make test`
Expected: all seven FrameMachine schema specs pass. Every other suite still passes.

- [ ] **Step 8: Commit**

```bash
git add api/frame/v1beta1/framemachine_types.go \
        api/frame/v1beta1/zz_generated.deepcopy.go \
        config/crd/bases/frame.plume-labs.io_framemachines.yaml \
        config/crd/kustomization.yaml \
        internal/controller/frame/framemachine_v1beta1_schema_test.go
git commit -m "feat(api): add FrameMachine, a v1beta1-only kind for a BMC-managed chassis

The address is bounded to a routable IP literal by CEL: the controller
connects to it carrying a Secret, so a hostname would put name resolution in
that path, and loopback and link-local are refused outright."
```

---

### Task 2: RBAC roles and the sample manifest

**Files:**
- Create: `config/rbac/framemachine_admin_role.yaml`, `config/rbac/framemachine_editor_role.yaml`, `config/rbac/framemachine_viewer_role.yaml`
- Create: `config/samples/frame_v1beta1_framemachine.yaml`
- Modify: `config/rbac/kustomization.yaml`, `config/samples/kustomization.yaml`, `config/rbac/role.yaml`

**Interfaces:**
- Consumes: the `framemachines` resource name from Task 1.
- Produces: the manager's own permission to read `framemachines`, write their status, and read `secrets` and `configmaps` — which Task 4's controller needs.

- [ ] **Step 1: Write the three tier roles**

Create `config/rbac/framemachine_viewer_role.yaml`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/name: frame
    app.kubernetes.io/managed-by: kustomize
  name: framemachine-viewer-role
rules:
- apiGroups:
  - frame.plume-labs.io
  resources:
  - framemachines
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - frame.plume-labs.io
  resources:
  - framemachines/status
  verbs:
  - get
```

Create `config/rbac/framemachine_editor_role.yaml` — the same document with `name: framemachine-editor-role` and these verbs on `framemachines`: `create`, `delete`, `get`, `list`, `patch`, `update`, `watch`; keep the `framemachines/status` rule at `get`.

Create `config/rbac/framemachine_admin_role.yaml` — the same document with `name: framemachine-admin-role`, the editor's verbs on `framemachines`, and `framemachines/status` at `get`, `patch`, `update`.

- [ ] **Step 2: Register them**

In `config/rbac/kustomization.yaml`, add to `resources`, after the `frametask_viewer_role.yaml` line:

```yaml
- framemachine_admin_role.yaml
- framemachine_editor_role.yaml
- framemachine_viewer_role.yaml
```

- [ ] **Step 3: Write the sample**

Create `config/samples/frame_v1beta1_framemachine.yaml`:

```yaml
apiVersion: frame.plume-labs.io/v1beta1
kind: FrameMachine
metadata:
  name: ml350-g9
  namespace: default
  labels:
    app.kubernetes.io/name: frame
spec:
  bmc:
    # The IP of the management port. A hostname is refused: see the field's
    # doc comment on why.
    address: 192.168.2.60
    # A Secret in this namespace with the keys `username` and `password`.
    # Never commit real credentials — create it with `kubectl create secret`.
    credentialsRef: ml350-g9-ilo
    tls:
      # An iLO4 ships a self-signed certificate. Choosing the bypass here is
      # deliberate and visible; the alternative is caBundleRef.
      insecureSkipVerify: true
  nodeRef: ""
```

Add `- frame_v1beta1_framemachine.yaml` to `resources` in `config/samples/kustomization.yaml`, before the scaffold marker.

- [ ] **Step 4: Grant the manager what the controller will need**

The manager's aggregate role is generated from `+kubebuilder:rbac` markers. Add these above the `Reconcile` method you will create in Task 4 — put them in the type file for now so `make manifests` picks them up, then move them onto the reconciler in Task 4:

```go
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
```

Run: `make manifests`
Expected: `config/rbac/role.yaml` gains `framemachines` rules. It already carries `secrets` and `configmaps` for other controllers; confirm rather than duplicate.

- [ ] **Step 5: Prove the manifests still render**

Run: `kustomize build config/default > /dev/null && echo RENDER_OK`
Expected: `RENDER_OK`, no error. (If `kustomize` is not on PATH, use `make manifests` plus `bin/kustomize build config/default > /dev/null`.)

- [ ] **Step 6: Commit**

```bash
git add config/rbac/ config/samples/
git commit -m "feat(rbac): tier roles, manager grants and a sample for FrameMachine

The sample's credentialsRef names a Secret that is never committed; the TLS
block chooses the bypass explicitly rather than defaulting to it."
```

---

### Task 3: The Redfish client

**Files:**
- Create: `internal/redfish/client.go`
- Create: `internal/redfish/types.go`
- Create: `internal/redfish/client_test.go`
- Create: `internal/redfish/testdata/ilo4/` (recorded JSON fixtures)

**Interfaces:**
- Consumes: nothing from earlier tasks — this package must not import `api/frame/v1beta1`. Keeping it free of the API types is what lets it be tested and reasoned about on its own.
- Produces:

```go
package redfish

type Client interface {
	Probe(ctx context.Context) (*Snapshot, error)
	Reset(ctx context.Context, resetType string) error
	ClearLog(ctx context.Context) error
	SetIndicatorLED(ctx context.Context, on bool) error
}

func New(baseURL, username, password string, tlsConfig *tls.Config) Client
```

and the value types `Snapshot`, `Inventory`, `Processor`, `MemoryModule`, `Drive`, `NetworkAdapter`, `Sensors`, `Temperature`, `Fan`, `PowerSupply`, `LogEntry`, plus the sentinel errors `ErrAuth`, `ErrTLS`, `ErrUnsupported`.

- [ ] **Step 1: Write the value types**

Create `internal/redfish/types.go` with the Apache licence header copied from `internal/controller/frame/talos_client.go`, then:

```go
package redfish

import (
	"errors"
	"time"
)

// These errors are what the controller turns into a Reachable condition
// reason, so they are part of this package's contract, not an implementation
// detail.
var (
	ErrAuth        = errors.New("redfish: authentication rejected")
	ErrTLS         = errors.New("redfish: TLS verification failed")
	ErrUnsupported = errors.New("redfish: service does not expose the expected resources")
)

type Processor struct {
	Socket  string
	Model   string
	Cores   int32
	Threads int32
}

type MemoryModule struct {
	Slot         string
	SizeMiB      int32
	Type         string
	Manufacturer string
}

type Drive struct {
	Name     string
	Model    string
	SizeGB   int32
	Protocol string
	Health   string
}

type NetworkAdapter struct {
	Name   string
	MAC    string
	Status string
}

type Inventory struct {
	Manufacturer    string
	Model           string
	SerialNumber    string
	BIOSVersion     string
	BMCFirmware     string
	Processors      []Processor
	MemoryModules   []MemoryModule
	TotalMemoryGiB  int32
	Drives          []Drive
	NetworkAdapters []NetworkAdapter
}

type Temperature struct {
	Name          string
	Celsius       int32
	UpperCritical *int32
	Health        string
}

type Fan struct {
	Name    string
	Reading int32
	Units   string
	Health  string
}

type PowerSupply struct {
	Name                 string
	Health               string
	State                string
	LastPowerOutputWatts int32
}

type Sensors struct {
	Temperatures       []Temperature
	Fans               []Fan
	PowerSupplies      []PowerSupply
	PowerConsumedWatts int32
}

type LogEntry struct {
	ID       string
	Severity string
	Message  string
	Created  time.Time
}

// Snapshot is one complete read of a machine. Probe returns it whole so a
// reconcile is one call rather than seven, and so a partial failure is a
// failure of the snapshot rather than a half-written status.
type Snapshot struct {
	PowerState   string
	IndicatorLED string
	Inventory    Inventory
	Sensors      Sensors
	Log          []LogEntry
	LogTotal     int
	LogCounts    map[string]int
}
```

- [ ] **Step 2: Record the fixtures**

Create these files under `internal/redfish/testdata/ilo4/`. They are shaped the way an iLO4 answers — an older Redfish, with `Oem/Hp` blocks and fields that other implementations would carry elsewhere. Note that `Fans` report `Units: "Percent"`, which is why `Fan.Units` exists.

`service_root.json`:
```json
{
  "@odata.type": "#ServiceRoot.v1_0_0.ServiceRoot",
  "Id": "RootService",
  "RedfishVersion": "1.0.0",
  "Systems": { "@odata.id": "/redfish/v1/Systems/" },
  "Chassis": { "@odata.id": "/redfish/v1/Chassis/" },
  "Managers": { "@odata.id": "/redfish/v1/Managers/" }
}
```

`systems.json`:
```json
{
  "@odata.type": "#ComputerSystemCollection.ComputerSystemCollection",
  "Members@odata.count": 1,
  "Members": [ { "@odata.id": "/redfish/v1/Systems/1/" } ]
}
```

`system_1.json`:
```json
{
  "@odata.type": "#ComputerSystem.1.0.1.ComputerSystem",
  "Id": "1",
  "Manufacturer": "HPE",
  "Model": "ProLiant ML350 Gen9",
  "SerialNumber": "CZJ1234567",
  "PowerState": "On",
  "IndicatorLED": "Off",
  "BiosVersion": "P92 v2.76",
  "MemorySummary": { "TotalSystemMemoryGiB": 64 },
  "ProcessorSummary": { "Count": 2, "Model": "Intel(R) Xeon(R) CPU E5-2650 v4" },
  "Actions": {
    "#ComputerSystem.Reset": {
      "target": "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/"
    }
  },
  "LogServices": { "@odata.id": "/redfish/v1/Systems/1/LogServices/" }
}
```

`chassis.json`:
```json
{
  "Members@odata.count": 1,
  "Members": [ { "@odata.id": "/redfish/v1/Chassis/1/" } ]
}
```

`chassis_1_thermal.json`:
```json
{
  "@odata.type": "#Thermal.1.0.0.Thermal",
  "Temperatures": [
    { "Name": "01-Inlet Ambient", "ReadingCelsius": 22, "UpperThresholdCritical": 42, "Status": { "Health": "OK" } },
    { "Name": "04-P1 DIMM 1-6", "ReadingCelsius": 31, "Status": { "Health": "OK" } },
    { "Name": "02-CPU 1", "ReadingCelsius": 40, "UpperThresholdCritical": 70, "Status": { "Health": "OK" } }
  ],
  "Fans": [
    { "FanName": "Fan 1", "CurrentReading": 23, "Units": "Percent", "Status": { "Health": "OK" } },
    { "FanName": "Fan 2", "CurrentReading": 23, "Units": "Percent", "Status": { "Health": "OK" } }
  ]
}
```

`chassis_1_power.json`:
```json
{
  "@odata.type": "#Power.1.0.0.Power",
  "PowerControl": [ { "PowerConsumedWatts": 118 } ],
  "PowerSupplies": [
    { "Name": "HpeServerPowerSupply", "Status": { "Health": "OK", "State": "Enabled" }, "LastPowerOutputWatts": 60 },
    { "Name": "HpeServerPowerSupply", "Status": { "Health": "OK", "State": "Enabled" }, "LastPowerOutputWatts": 58 }
  ]
}
```

`log_entries.json`:
```json
{
  "Members@odata.count": 3,
  "Members": [
    { "Id": "1", "Severity": "OK", "Created": "2026-08-30T09:11:00Z", "Message": "System Power Restored" },
    { "Id": "2", "Severity": "Warning", "Created": "2026-09-02T04:02:11Z", "Message": "Corrected Memory Error threshold exceeded (Processor 1, DIMM 3)" },
    { "Id": "3", "Severity": "Critical", "Created": "2026-09-05T18:44:02Z", "Message": "Fan 3 Failed" }
  ]
}
```

`managers_1.json`:
```json
{
  "Id": "1",
  "FirmwareVersion": "iLO 4 v2.82",
  "Model": "iLO 4"
}
```

Also record a **degraded** fixture, because absent fields are the named risk. `system_1_sparse.json` — an iLO4 whose firmware omits half of what the rich one carries:
```json
{
  "Id": "1",
  "Model": "ProLiant ML350 Gen9",
  "PowerState": "Off"
}
```

- [ ] **Step 3: Write the failing tests**

Create `internal/redfish/client_test.go` with the licence header, then:

```go
package redfish

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// serveFixtures maps Redfish paths onto the recorded files. Anything not
// mapped 404s, which is how the sparse case is expressed: a firmware that
// does not expose a resource simply does not answer for it.
func serveFixtures(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, file := range routes {
		body, err := os.ReadFile(filepath.Join("testdata", "ilo4", file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fullRoutes() map[string]string {
	return map[string]string{
		"/redfish/v1/":                              "service_root.json",
		"/redfish/v1/Systems/":                      "systems.json",
		"/redfish/v1/Systems/1/":                    "system_1.json",
		"/redfish/v1/Chassis/":                      "chassis.json",
		"/redfish/v1/Chassis/1/Thermal/":            "chassis_1_thermal.json",
		"/redfish/v1/Chassis/1/Power/":              "chassis_1_power.json",
		"/redfish/v1/Systems/1/LogServices/IML/Entries/": "log_entries.json",
		"/redfish/v1/Managers/1/":                   "managers_1.json",
	}
}

func insecureClient(url string) Client {
	return New(url, "admin", "secret", &tls.Config{InsecureSkipVerify: true})
}

func TestProbeReadsInventorySensorsAndLog(t *testing.T) {
	srv := serveFixtures(t, fullRoutes())
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if snap.PowerState != "On" {
		t.Errorf("PowerState = %q, want On", snap.PowerState)
	}
	if snap.Inventory.Model != "ProLiant ML350 Gen9" {
		t.Errorf("Model = %q", snap.Inventory.Model)
	}
	if snap.Inventory.SerialNumber != "CZJ1234567" {
		t.Errorf("SerialNumber = %q", snap.Inventory.SerialNumber)
	}
	if snap.Inventory.BMCFirmware != "iLO 4 v2.82" {
		t.Errorf("BMCFirmware = %q, want the Managers value", snap.Inventory.BMCFirmware)
	}
	if snap.Inventory.TotalMemoryGiB != 64 {
		t.Errorf("TotalMemoryGiB = %d, want 64", snap.Inventory.TotalMemoryGiB)
	}
	if got := len(snap.Sensors.Temperatures); got != 3 {
		t.Errorf("temperatures = %d, want 3", got)
	}
	if snap.Sensors.PowerConsumedWatts != 118 {
		t.Errorf("PowerConsumedWatts = %d, want 118", snap.Sensors.PowerConsumedWatts)
	}
	if got := len(snap.Sensors.PowerSupplies); got != 2 {
		t.Errorf("power supplies = %d, want 2", got)
	}
}

// The fan reading is a percentage on this hardware and an RPM figure on
// others. A client that dropped Units would render "23" as a fan speed.
func TestProbeKeepsTheFanUnits(t *testing.T) {
	srv := serveFixtures(t, fullRoutes())
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(snap.Sensors.Fans) == 0 {
		t.Fatal("no fans")
	}
	if snap.Sensors.Fans[0].Units != "Percent" {
		t.Errorf("Units = %q, want Percent", snap.Sensors.Fans[0].Units)
	}
	if snap.Sensors.Fans[0].Reading != 23 {
		t.Errorf("Reading = %d, want 23", snap.Sensors.Fans[0].Reading)
	}
}

func TestProbeCountsTheWholeLogNotJustWhatItReturns(t *testing.T) {
	srv := serveFixtures(t, fullRoutes())
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if snap.LogTotal != 3 {
		t.Errorf("LogTotal = %d, want 3", snap.LogTotal)
	}
	if snap.LogCounts["Critical"] != 1 || snap.LogCounts["Warning"] != 1 || snap.LogCounts["OK"] != 1 {
		t.Errorf("LogCounts = %v", snap.LogCounts)
	}
}

// The named risk in the spec: iLO4 firmware revisions omit fields and whole
// resources. A probe against a sparse service must return what it found, not
// an error and not a panic.
func TestProbeToleratesAFirmwareThatOmitsResources(t *testing.T) {
	srv := serveFixtures(t, map[string]string{
		"/redfish/v1/":           "service_root.json",
		"/redfish/v1/Systems/":   "systems.json",
		"/redfish/v1/Systems/1/": "system_1_sparse.json",
		"/redfish/v1/Chassis/":   "chassis.json",
	})
	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe on a sparse service: %v", err)
	}
	if snap.PowerState != "Off" {
		t.Errorf("PowerState = %q, want Off", snap.PowerState)
	}
	if snap.Inventory.SerialNumber != "" {
		t.Errorf("SerialNumber = %q, want empty on a sparse service", snap.Inventory.SerialNumber)
	}
	if len(snap.Sensors.Temperatures) != 0 {
		t.Errorf("temperatures = %d, want none", len(snap.Sensors.Temperatures))
	}
}

func TestProbeReportsAuthFailureDistinctly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	_, err := insecureClient(srv.URL).Probe(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

// Verification is on unless the spec turns it off, and a self-signed BMC
// certificate must surface as ErrTLS rather than as a generic dial failure —
// the controller renders the two differently.
func TestProbeReportsTLSFailureDistinctly(t *testing.T) {
	srv := serveFixtures(t, fullRoutes())
	verifying := New(srv.URL, "admin", "secret", &tls.Config{})
	_, err := verifying.Probe(context.Background())
	if !errors.Is(err, ErrTLS) {
		t.Fatalf("err = %v, want ErrTLS", err)
	}
}

func TestResetPostsTheActionTarget(t *testing.T) {
	var got struct {
		ResetType string `json:"ResetType"`
	}
	posted := make(chan string, 1)

	routes := fullRoutes()
	mux := http.NewServeMux()
	for path, file := range routes {
		body, err := os.ReadFile(filepath.Join("testdata", "ilo4", file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	}
	mux.HandleFunc("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		posted <- got.ResetType
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	if err := insecureClient(srv.URL).Reset(context.Background(), "GracefulShutdown"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if v := <-posted; v != "GracefulShutdown" {
		t.Errorf("ResetType = %q, want GracefulShutdown", v)
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./internal/redfish/...`
Expected: compilation failure — `New` and `Client` do not exist yet.

- [ ] **Step 5: Implement the client**

Create `internal/redfish/client.go` with the licence header. Requirements the tests above pin down:

- `New(baseURL, username, password string, tlsConfig *tls.Config) Client` returns an unexported struct holding an `*http.Client` whose `Transport` carries `tlsConfig` and whose `Timeout` is 20 seconds. An iLO4 is slow; 20s is generous without being a hang.
- Every request uses HTTP Basic auth. iLO4 supports it on Redfish and it avoids a session lifecycle this lot does not need.
- A `get(ctx, path string, out any) error` helper: builds `baseURL + path`, sets `Accept: application/json`, and maps failures — a `tls.CertificateVerificationError` or an `x509.UnknownAuthorityError` in the chain to `ErrTLS` (use `errors.As`), HTTP 401 or 403 to `ErrAuth`, HTTP 404 to a sentinel the caller treats as "absent", anything else to a wrapped error.
- `Probe` walks: `/redfish/v1/` → `Systems/` → first member → the system document; `Chassis/` → first member → `Thermal/` and `Power/`; `Managers/1/` for the BMC firmware; and the log entries at `/redfish/v1/Systems/{id}/LogServices/IML/Entries/`. **A 404 on any resource after the system document is not an error** — it leaves that part of the snapshot zero-valued. Only a failure to read the service root or the system document fails the probe.
- Decode into structs whose optional numbers are pointers (`*int32`), so an absent `UpperThresholdCritical` is distinguishable from zero. Copy pointer values into the exported types only when non-nil.
- `LogCounts` is built by counting `Severity` across every member; `LogTotal` is `Members@odata.count` when present and `len(Members)` otherwise. Return at most the newest 25 entries in `Log`, newest first, sorted by `Created` descending.
- `Reset(ctx, resetType)` POSTs `{"ResetType": resetType}` to the `#ComputerSystem.Reset` action target read from the system document. If the document has no such action, return `ErrUnsupported`.
- `ClearLog(ctx)` POSTs `{"Action": "LogService.ClearLog"}` to `/redfish/v1/Systems/{id}/LogServices/IML/Actions/LogService.ClearLog/`.
- `SetIndicatorLED(ctx, on)` PATCHes `{"IndicatorLED": "Lit"}` or `{"IndicatorLED": "Off"}` to the system document.

Keep `client.go` under 500 lines. If it approaches that, split the decoding into `internal/redfish/decode.go`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/redfish/... -v`
Expected: all eight tests pass.

- [ ] **Step 7: Prove the sparse test is discriminating**

Temporarily change `Probe` so that a 404 on the Thermal resource returns an error instead of leaving the sensors empty.
Run: `go test ./internal/redfish/... -run TestProbeTolerates`
Expected: FAIL. Restore the code and confirm it passes again. Record both outputs in your report — a tolerance test that cannot fail proves nothing.

- [ ] **Step 8: Commit**

```bash
git add internal/redfish/
git commit -m "feat(redfish): a hand-written client for the seven resources this lot needs

No dependency added: the value a Redfish library brings is breadth across
vendors, and what this lot needs instead is tolerance of the fields iLO4
omits, which pointer decoding gives directly. A 404 after the system document
leaves that part of the snapshot empty rather than failing the probe."
```

---

### Task 4: The controller — polling and the Reachable condition

**Files:**
- Create: `internal/controller/frame/framemachine_controller.go`
- Create: `internal/controller/frame/framemachine_controller_test.go`
- Modify: `cmd/main.go` (register the reconciler alongside the others, around line 313)
- Modify: `api/frame/v1beta1/framemachine_types.go` (move the `+kubebuilder:rbac` markers added in Task 2 onto the reconciler)

**Interfaces:**
- Consumes: `framev1beta1.FrameMachine` and its status types (Task 1); `redfish.Client`, `redfish.Snapshot`, `redfish.ErrAuth`, `redfish.ErrTLS`, `redfish.ErrUnsupported` (Task 3).
- Produces:

```go
type FrameMachineReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// NewClient builds the Redfish client for a machine. Tests replace it.
	NewClient func(ctx context.Context, kube client.Client, namespace string, bmc framev1beta1.BMCSpec) (redfish.Client, error)
}

func (r *FrameMachineReconciler) SetupWithManager(mgr ctrl.Manager) error
```

and, in the same package, `buildRedfishClient(ctx, kube, namespace, bmc)` — the default `NewClient`, mirroring `buildTalosClient` in `talos_client.go`.

- [ ] **Step 1: Write the failing tests**

Create `internal/controller/frame/framemachine_controller_test.go` with the licence header. These are envtest specs in the existing `controller` package, so `k8sClient` and `ctx` come from `suite_test.go`.

```go
package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/redfish"
)

// fakeRedfish is the seam the whole controller is tested through: no network,
// no fixtures, just the two outcomes the controller has to tell apart.
type fakeRedfish struct {
	snapshot *redfish.Snapshot
	probeErr error

	resets   []string
	clearLog int
	ledCalls []bool
}

func (f *fakeRedfish) Probe(context.Context) (*redfish.Snapshot, error) {
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	return f.snapshot, nil
}
func (f *fakeRedfish) Reset(_ context.Context, t string) error { f.resets = append(f.resets, t); return nil }
func (f *fakeRedfish) ClearLog(context.Context) error          { f.clearLog++; return nil }
func (f *fakeRedfish) SetIndicatorLED(_ context.Context, on bool) error {
	f.ledCalls = append(f.ledCalls, on)
	return nil
}

var _ = Describe("FrameMachine controller", func() {
	var (
		fake *fakeRedfish
		r    *FrameMachineReconciler
	)

	newSecret := func(name string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			StringData: map[string]string{"username": "admin", "password": "secret"},
		}
	}

	newMachine := func(name string) *framev1beta1.FrameMachine {
		return &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameMachineSpec{
				BMC: framev1beta1.BMCSpec{
					Address:        "192.168.2.60",
					CredentialsRef: name + "-creds",
					TLS:            framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
				},
			},
		}
	}

	BeforeEach(func() {
		fake = &fakeRedfish{snapshot: &redfish.Snapshot{
			PowerState:   "On",
			IndicatorLED: "Off",
			Inventory: redfish.Inventory{
				Model:          "ProLiant ML350 Gen9",
				SerialNumber:   "CZJ1234567",
				TotalMemoryGiB: 64,
			},
			Sensors: redfish.Sensors{
				Temperatures:       []redfish.Temperature{{Name: "01-Inlet Ambient", Celsius: 22, Health: "OK"}},
				PowerConsumedWatts: 118,
			},
			Log:       []redfish.LogEntry{{ID: "3", Severity: "Critical", Message: "Fan 3 Failed"}},
			LogTotal:  3,
			LogCounts: map[string]int{"Critical": 1, "Warning": 1, "OK": 1},
		}}
		r = &FrameMachineReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			NewClient: func(context.Context, client.Client, string, framev1beta1.BMCSpec) (redfish.Client, error) {
				return fake, nil
			},
		}
	})

	It("writes what it read, with the time it read it", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-ok-creds"))).To(Succeed())
		fm := newMachine("fm-ok")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())

		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-ok", Namespace: "default"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(60 * time.Second))

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "fm-ok", Namespace: "default"}, &got)).To(Succeed())
		Expect(got.Status.PowerState).To(Equal("On"))
		Expect(got.Status.Inventory).NotTo(BeNil())
		Expect(got.Status.Inventory.SerialNumber).To(Equal("CZJ1234567"))
		Expect(got.Status.Sensors.PowerConsumedWatts).To(BeNumerically("==", 118))
		Expect(got.Status.EventLogTotal).To(BeNumerically("==", 3))
		Expect(got.Status.EventLogCounts).To(HaveKeyWithValue("Critical", int32(1)))
		Expect(got.Status.LastProbeAt).NotTo(BeNil())
		Expect(meta.IsStatusConditionTrue(got.Status.Conditions, "Reachable")).To(BeTrue())
	})

	It("keeps the last reading when a probe fails, and says why", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-flap-creds"))).To(Succeed())
		fm := newMachine("fm-flap")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-flap", Namespace: "default"}}

		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		fake.probeErr = redfish.ErrTLS
		res, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(5 * time.Minute))

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		cond := meta.FindStatusCondition(got.Status.Conditions, "Reachable")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("TLSError"))
		// The last good reading survives, because the screen shows it beside
		// its age rather than showing nothing.
		Expect(got.Status.Inventory).NotTo(BeNil())
		Expect(got.Status.Inventory.SerialNumber).To(Equal("CZJ1234567"))
	})

	It("distinguishes an authentication failure from a TLS one", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-auth-creds"))).To(Succeed())
		Expect(k8sClient.Create(ctx, newMachine("fm-auth"))).To(Succeed())
		fake.probeErr = redfish.ErrAuth

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-auth", Namespace: "default"}}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(meta.FindStatusCondition(got.Status.Conditions, "Reachable").Reason).To(Equal("AuthFailed"))
	})

	It("reports a missing credentials Secret without contacting anything", func() {
		Expect(k8sClient.Create(ctx, newMachine("fm-nosecret"))).To(Succeed())
		r.NewClient = func(context.Context, client.Client, string, framev1beta1.BMCSpec) (redfish.Client, error) {
			return nil, errors.New("secrets \"fm-nosecret-creds\" not found")
		}

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-nosecret", Namespace: "default"}}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		cond := meta.FindStatusCondition(got.Status.Conditions, "Reachable")
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("CredentialsUnavailable"))
	})

	It("truncates the retained log to the schema's cap", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-long-creds"))).To(Succeed())
		Expect(k8sClient.Create(ctx, newMachine("fm-long"))).To(Succeed())
		entries := make([]redfish.LogEntry, 0, 40)
		for i := 0; i < 40; i++ {
			entries = append(entries, redfish.LogEntry{ID: "e", Severity: "OK", Message: "m"})
		}
		fake.snapshot.Log = entries

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-long", Namespace: "default"}}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(len(got.Status.EventLog)).To(Equal(25))
	})
})
```

The import block above is incomplete on purpose only in that it omits what
your editor will add for you; the specs need `"errors"`,
`"k8s.io/apimachinery/pkg/api/meta"` and
`"sigs.k8s.io/controller-runtime/pkg/client"` alongside what is listed.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `make test`
Expected: compilation failure — `FrameMachineReconciler` does not exist.

- [ ] **Step 3: Implement the reconciler**

Create `internal/controller/frame/framemachine_controller.go` with the licence header and the `+kubebuilder:rbac` markers moved off the type file. Behaviour, in order:

1. `Get` the `FrameMachine`; on `IsNotFound`, return without error.
2. Build the Redfish client through `r.NewClient`. On error, set `Reachable=False` with reason `CredentialsUnavailable`, message from the error, patch status, and return `ctrl.Result{RequeueAfter: 5 * time.Minute}, nil`.
3. Execute a pending `spec.powerRequest` — Task 5 adds this; leave a call to a `r.runPowerRequest(ctx, fm, rc)` that returns `(bool, error)` and, for now, always returns `(false, nil)`.
4. `Probe`. On error, map the sentinel to a reason — `redfish.ErrTLS` → `TLSError`, `redfish.ErrAuth` → `AuthFailed`, `redfish.ErrUnsupported` → `Unsupported`, `context.DeadlineExceeded` or a `net.Error` with `Timeout()` → `Timeout`, anything else → `ProbeFailed`. Set `Reachable=False` with that reason. **Do not clear the previously stored inventory, sensors or log**: the screen shows the last reading beside its age. Patch status, return `RequeueAfter: 5 * time.Minute`.
5. On success, map the snapshot onto the status types, truncate `EventLog` to 25 entries, convert `LogCounts` from `map[string]int` to `map[string]int32`, set `LastProbeAt` to `metav1.Now()`, set `Reachable=True` with reason `Probed`, set `ObservedGeneration` to `fm.Generation`, patch status, and return `RequeueAfter: 60 * time.Second`.

Use `setCondition` from `internal/controller/frame/helpers.go` — the repo has one and there is a regression test guarding it (`setcondition_regression_test.go`). Read it before writing conditions by hand.

Status writes go through `r.Status().Patch(ctx, fm, patch)` with `patch := client.MergeFrom(fm.DeepCopy())` taken before mutation, matching `frameresourcequota_controller.go`.

`SetupWithManager` watches `&framev1beta1.FrameMachine{}` and names the controller `framemachine`.

- [ ] **Step 4: Write `buildRedfishClient`**

In the same file (or `internal/controller/frame/redfish_client.go` if `framemachine_controller.go` is nearing 400 lines):

```go
// buildRedfishClient mirrors buildTalosClient: it is the one place the
// controller reads a credential, so the Secret never travels further.
func buildRedfishClient(ctx context.Context, kube client.Client, namespace string, bmc framev1beta1.BMCSpec) (redfish.Client, error) {
	var sec corev1.Secret
	if err := kube.Get(ctx, types.NamespacedName{Name: bmc.CredentialsRef, Namespace: namespace}, &sec); err != nil {
		return nil, err
	}
	user := string(sec.Data["username"])
	pass := string(sec.Data["password"])
	if user == "" || pass == "" {
		return nil, fmt.Errorf("secret %s/%s must carry both username and password", namespace, bmc.CredentialsRef)
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case bmc.TLS.InsecureSkipVerify:
		tlsCfg.InsecureSkipVerify = true
	case bmc.TLS.CABundleRef != "":
		var cm corev1.ConfigMap
		if err := kube.Get(ctx, types.NamespacedName{Name: bmc.TLS.CABundleRef, Namespace: namespace}, &cm); err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cm.Data["ca.crt"])) {
			return nil, fmt.Errorf("configmap %s/%s has no usable ca.crt", namespace, bmc.TLS.CABundleRef)
		}
		tlsCfg.RootCAs = pool
	}

	return redfish.New("https://"+bmc.Address, user, pass, tlsCfg), nil
}
```

- [ ] **Step 5: Register the controller**

In `cmd/main.go`, beside the other reconcilers (the `NodeTuningReconciler` block is around line 313), add:

```go
	if err := (&controller.FrameMachineReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor("framemachine-controller"),
		NewClient: controller.BuildRedfishClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "FrameMachine")
		os.Exit(1)
	}
```

This requires `buildRedfishClient` to be exported as `BuildRedfishClient`. Export it and keep the doc comment.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `make test`
Expected: the five FrameMachine controller specs pass, and every other suite still passes.

- [ ] **Step 7: Prove the "keeps the last reading" test is discriminating**

Temporarily change the failure branch so it writes `fm.Status.Inventory = nil` before patching.
Run: `make test`
Expected: "keeps the last reading when a probe fails" FAILS. Restore and confirm it passes. Record both outputs in your report.

- [ ] **Step 8: Commit**

```bash
git add internal/controller/frame/framemachine_controller.go \
        internal/controller/frame/framemachine_controller_test.go \
        internal/controller/frame/redfish_client.go \
        api/frame/v1beta1/framemachine_types.go \
        cmd/main.go config/rbac/role.yaml
git commit -m "feat(controller): poll a machine's BMC and record what it read, and when

A failed probe keeps the previous reading and says why it is stale, because
the screen shows a value beside its age rather than showing nothing. TLS,
auth, timeout and unsupported are four different reasons on the Reachable
condition, not one."
```

---

### Task 5: Power actions and the timestamp guard

**Files:**
- Modify: `internal/controller/frame/framemachine_controller.go` (replace the `runPowerRequest` stub from Task 4)
- Modify: `internal/controller/frame/framemachine_controller_test.go` (add the specs below)

**Interfaces:**
- Consumes: `framev1beta1.PowerRequestSpec`, `framev1beta1.PowerAction` (Task 1); `redfish.Client.Reset/ClearLog/SetIndicatorLED` (Task 3); `FrameMachineReconciler` (Task 4).
- Produces: nothing new — it completes `Reconcile`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/controller/frame/framemachine_controller_test.go`, inside the same `Describe`:

```go
	It("executes a request whose timestamp is newer than the last action", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-pwr-creds"))).To(Succeed())
		fm := newMachine("fm-pwr")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-pwr", Namespace: "default"}}

		fm.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionGracefulShutdown,
			RequestedAt: metav1.Now(),
		}
		Expect(k8sClient.Update(ctx, fm)).To(Succeed())

		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.resets).To(Equal([]string{"GracefulShutdown"}))

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(got.Status.LastPowerAction).To(Equal("GracefulShutdown"))
		Expect(got.Status.LastPowerActionAt).NotTo(BeNil())
	})

	// The guard, and the reason the field is a timestamp rather than a desired
	// state: a second reconcile of the same object must not shut the machine
	// down twice.
	It("does not repeat an action on the next reconcile", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-once-creds"))).To(Succeed())
		fm := newMachine("fm-once")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-once", Namespace: "default"}}

		fm.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionForceRestart,
			RequestedAt: metav1.Now(),
		}
		Expect(k8sClient.Update(ctx, fm)).To(Succeed())

		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(fake.resets).To(HaveLen(1))
	})

	It("ignores a request older than the last action it performed", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-stale-creds"))).To(Succeed())
		fm := newMachine("fm-stale")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-stale", Namespace: "default"}}

		now := metav1.Now()
		earlier := metav1.NewTime(now.Add(-time.Hour))

		var stored framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &stored)).To(Succeed())
		stored.Status.LastPowerActionAt = &now
		Expect(k8sClient.Status().Update(ctx, &stored)).To(Succeed())

		Expect(k8sClient.Get(ctx, req.NamespacedName, &stored)).To(Succeed())
		stored.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionForceOff,
			RequestedAt: earlier,
		}
		Expect(k8sClient.Update(ctx, &stored)).To(Succeed())

		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.resets).To(BeEmpty())
	})

	It("routes the non-power actions to their own calls", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-led-creds"))).To(Succeed())
		fm := newMachine("fm-led")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-led", Namespace: "default"}}

		fm.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionIndicatorLedOn,
			RequestedAt: metav1.Now(),
		}
		Expect(k8sClient.Update(ctx, fm)).To(Succeed())
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.ledCalls).To(Equal([]bool{true}))
		Expect(fake.resets).To(BeEmpty())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		got.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionClearSEL,
			RequestedAt: metav1.NewTime(time.Now().Add(time.Second)),
		}
		Expect(k8sClient.Update(ctx, &got)).To(Succeed())
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.clearLog).To(Equal(1))
	})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `make test`
Expected: the four new specs fail — `fake.resets` stays empty, because `runPowerRequest` is still the stub.

- [ ] **Step 3: Implement `runPowerRequest`**

```go
// runPowerRequest executes spec.powerRequest at most once. The guard is the
// timestamp: acting on "newer than the last action" rather than on a desired
// state is what stops the controller re-asserting a power state against
// someone who pressed the physical button, and is what makes a restart
// expressible at all — the state before and after is identical. The same
// pattern restarts a Deployment in lot 2, by writing restartedAt.
//
// Returns true when it acted, so Reconcile knows the status carries a change
// even if the probe that follows fails.
func (r *FrameMachineReconciler) runPowerRequest(ctx context.Context, fm *framev1beta1.FrameMachine, rc redfish.Client) (bool, error) {
	req := fm.Spec.PowerRequest
	if req == nil {
		return false, nil
	}
	if last := fm.Status.LastPowerActionAt; last != nil && !req.RequestedAt.Time.After(last.Time) {
		return false, nil
	}

	var err error
	switch req.Action {
	case framev1beta1.PowerActionOn:
		err = rc.Reset(ctx, "On")
	case framev1beta1.PowerActionGracefulShutdown:
		err = rc.Reset(ctx, "GracefulShutdown")
	case framev1beta1.PowerActionForceOff:
		err = rc.Reset(ctx, "ForceOff")
	case framev1beta1.PowerActionForceRestart:
		err = rc.Reset(ctx, "ForceRestart")
	case framev1beta1.PowerActionClearSEL:
		err = rc.ClearLog(ctx)
	case framev1beta1.PowerActionIndicatorLedOn:
		err = rc.SetIndicatorLED(ctx, true)
	case framev1beta1.PowerActionIndicatorLedOff:
		err = rc.SetIndicatorLED(ctx, false)
	default:
		err = fmt.Errorf("unknown power action %q", req.Action)
	}

	// The timestamp advances whether or not the action succeeded. A failed
	// action that left the timestamp behind would be retried on every
	// reconcile, once a minute, forever — and "force off, repeatedly, until
	// it works" is not a behaviour anyone asked for. The failure is reported
	// on the condition instead.
	now := metav1.Now()
	fm.Status.LastPowerAction = string(req.Action)
	fm.Status.LastPowerActionAt = &now

	if err != nil {
		r.Recorder.Event(fm, corev1.EventTypeWarning, "PowerActionFailed",
			fmt.Sprintf("%s: %v", req.Action, err))
		return true, nil
	}
	r.Recorder.Event(fm, corev1.EventTypeNormal, "PowerAction", string(req.Action))
	return true, nil
}
```

Wire it into `Reconcile` in place of the Task 4 stub, before the probe, and make sure the status patch that follows carries `LastPowerAction`/`LastPowerActionAt` even when the probe then fails.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test`
Expected: all nine FrameMachine controller specs pass.

- [ ] **Step 5: Prove the guard is discriminating**

Temporarily remove the `if last := ...` early return.
Run: `make test`
Expected: "does not repeat an action on the next reconcile" FAILS with `fake.resets` holding three entries. Restore and confirm. Record both outputs.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/frame/
git commit -m "feat(controller): execute a power request once, guarded by its timestamp

The timestamp advances even when the action fails: leaving it behind would
retry a force-off every sixty seconds forever. The failure goes on the
condition and an Event instead."
```

---

### Task 6: The console tiers get the new kind

**Files:**
- Modify: `deploy/kubernetes/base/rbac.yaml`
- Modify: `deploy/kubernetes/base/rbac_manifest_test.go` if one exists; otherwise create `deploy/kubernetes/base/framemachine_rbac_test.go` following the manifest-test pattern lot 2 added

**Interfaces:**
- Consumes: the `framemachines` resource name (Task 1).
- Produces: the grants the console's viewer and admin tiers need.

- [ ] **Step 1: Find the manifest-test pattern**

Run: `ls deploy/kubernetes/base/*_test.go; grep -rln "rbac.frame.plume-labs.io/tier" --include="*_test.go" .`
Read whatever it finds. Lot 2 added a test proving a grant and its guard render together; this task's test follows the same shape. If no such test exists in this directory, put the new one in the package the search finds.

- [ ] **Step 2: Write the failing test**

The test must assert, by parsing `deploy/kubernetes/base/rbac.yaml`:

- the ClusterRole labelled `rbac.frame.plume-labs.io/tier: viewer` has a rule whose `apiGroups` contains `frame.plume-labs.io`, whose `resources` contains `framemachines`, and whose `verbs` contain `get`, `list` and `watch`
- **no** rule in the `viewer` or `editor` ClusterRole grants any verb on `framemachines` beyond those three
- the ClusterRole labelled `rbac.frame.plume-labs.io/tier: admin` grants `patch` on `framemachines`
- **no** tier at all grants any verb on `secrets`

That last assertion is the discriminating one: it fails the moment somebody adds a Secret grant to make a registration form work, which is exactly the change the spec says must be a decision rather than a slip.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./deploy/... -run FrameMachine -v` (adjust the package path to where you put the test)
Expected: FAIL — no `framemachines` rule exists yet.

- [ ] **Step 4: Add the rules**

In `deploy/kubernetes/base/rbac.yaml`, in the ClusterRole carrying `rbac.frame.plume-labs.io/tier: viewer` (around line 25), add to its `rules`:

```yaml
# Lot 1. The Hardware screen reads a chassis's inventory, sensors and event
# log; all three live in FrameMachine's status, which the operator fills by
# polling the BMC. Call sites: src/components/hardware/HardwareView.tsx and
# the MachineClient in src/lib/frame-sdk.ts.
- apiGroups: ["frame.plume-labs.io"]
  resources: ["framemachines"]
  verbs: ["get", "list", "watch"]
```

In the ClusterRole carrying `rbac.frame.plume-labs.io/tier: admin` (around line 361), add:

```yaml
# Lot 1. spec.powerRequest is the only writable field the console touches, and
# patch is the only verb that reaches it. Admin and not editor: restarting a
# Deployment is bounded by an update strategy, powering off a chassis is
# bounded by nothing, and the chassis may be carrying the cluster that hosts
# the console making the request.
# Call site: the power dialog in src/components/hardware/MachineActions.tsx.
- apiGroups: ["frame.plume-labs.io"]
  resources: ["framemachines"]
  verbs: ["patch"]
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./deploy/... -run FrameMachine -v`
Expected: PASS.

- [ ] **Step 6: Prove the Secret assertion is discriminating**

Temporarily add a `secrets: ["get"]` rule to the viewer ClusterRole.
Run: the same command.
Expected: FAIL, naming the Secret grant. Remove it and confirm PASS. Record both outputs.

- [ ] **Step 7: Commit**

```bash
git add deploy/kubernetes/base/
git commit -m "feat(rbac): the console tiers gain framemachines, read for all and patch for admin

Power is admin and not editor, and no tier gains secrets — the test asserts
the second, so a registration form that needs Secret access has to be a
decision rather than a slip."
```

---

### Task 7: The console's pure logic

**Files:**
- Create: `src/lib/machines.ts`
- Create: `src/lib/machines.test.ts`

**Interfaces:**
- Consumes: nothing from earlier tasks (this file is pure TypeScript with no network).
- Produces:

```ts
export type SensorSeverity = 'ok' | 'warning' | 'critical' | 'unknown'
export interface Machine { name: string; namespace: string; address: string; nodeRef: string; powerState: string; indicatorLED: string; reachable: boolean; reachableReason: string; reachableMessage: string; lastProbeAt: string | null; inventory: MachineInventory | null; sensors: MachineSensors | null; eventLog: MachineEvent[]; eventLogCounts: Record<string, number>; eventLogTotal: number; lastPowerAction: string; lastPowerActionAt: string | null }
export function toMachine(cr: MachineCR): Machine
export function temperatureSeverity(t: { celsius: number; upperCritical?: number | null; health?: string }): SensorSeverity
export function eventSeverity(raw: string): SensorSeverity
export function stalenessLabel(lastProbeAt: string | null, now: Date): string
export function isStale(lastProbeAt: string | null, now: Date): boolean
export function powerActionLabel(action: string, machineName: string): string
```

- [ ] **Step 1: Write the failing tests**

Create `src/lib/machines.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import {
  eventSeverity,
  isStale,
  powerActionLabel,
  stalenessLabel,
  temperatureSeverity,
  toMachine,
} from './machines'

describe('temperatureSeverity', () => {
  it('is critical at or above the reported upper critical threshold', () => {
    expect(temperatureSeverity({ celsius: 70, upperCritical: 70 })).toBe('critical')
    expect(temperatureSeverity({ celsius: 71, upperCritical: 70 })).toBe('critical')
  })

  it('warns within five degrees of the threshold, so the screen is useful before the alarm', () => {
    expect(temperatureSeverity({ celsius: 66, upperCritical: 70 })).toBe('warning')
    expect(temperatureSeverity({ celsius: 65, upperCritical: 70 })).toBe('warning')
  })

  it('is ok further below', () => {
    expect(temperatureSeverity({ celsius: 64, upperCritical: 70 })).toBe('ok')
    expect(temperatureSeverity({ celsius: 22, upperCritical: 42 })).toBe('ok')
  })

  // iLO4 omits the threshold on most DIMM sensors. Inventing one would be a
  // guess rendered as a fact, so the reported health is the fallback and
  // absence of both is 'unknown', not 'ok'.
  it('falls back to the reported health when there is no threshold', () => {
    expect(temperatureSeverity({ celsius: 31, health: 'OK' })).toBe('ok')
    expect(temperatureSeverity({ celsius: 31, health: 'Warning' })).toBe('warning')
    expect(temperatureSeverity({ celsius: 31, health: 'Critical' })).toBe('critical')
    expect(temperatureSeverity({ celsius: 31 })).toBe('unknown')
  })
})

describe('eventSeverity', () => {
  it('maps the vocabulary the BMC actually uses', () => {
    expect(eventSeverity('OK')).toBe('ok')
    expect(eventSeverity('Warning')).toBe('warning')
    expect(eventSeverity('Critical')).toBe('critical')
  })

  it('is case-insensitive, because firmware revisions disagree on it', () => {
    expect(eventSeverity('critical')).toBe('critical')
    expect(eventSeverity('WARNING')).toBe('warning')
  })

  it('does not silently downgrade a severity it has never seen', () => {
    expect(eventSeverity('Fatal')).toBe('unknown')
    expect(eventSeverity('')).toBe('unknown')
  })
})

describe('staleness', () => {
  const now = new Date('2026-09-10T12:00:00Z')

  it('is not stale within two polling intervals', () => {
    expect(isStale('2026-09-10T11:59:30Z', now)).toBe(false)
    expect(isStale('2026-09-10T11:58:05Z', now)).toBe(false)
  })

  it('is stale beyond them', () => {
    expect(isStale('2026-09-10T11:57:00Z', now)).toBe(true)
  })

  // A machine that has never been probed has no reading to be stale about,
  // and rendering "0 s ago" would be a lie about a value that does not exist.
  it('treats never-probed as stale', () => {
    expect(isStale(null, now)).toBe(true)
    expect(stalenessLabel(null, now)).toBe('jamais relevé')
  })

  it('says how old the reading is in words', () => {
    expect(stalenessLabel('2026-09-10T11:59:30Z', now)).toBe('il y a 30 s')
    expect(stalenessLabel('2026-09-10T11:46:00Z', now)).toBe('il y a 14 min')
    expect(stalenessLabel('2026-09-10T09:00:00Z', now)).toBe('il y a 3 h')
  })
})

describe('powerActionLabel', () => {
  it('names the action and the machine, in that order', () => {
    expect(powerActionLabel('GracefulShutdown', 'ml350-g9')).toBe(
      'power: GracefulShutdown ml350-g9',
    )
  })

  // FrameTaskSpec.Action caps at 200 characters and an overflow does not fail
  // the write — it drops the audit record. The machine name is what gets cut,
  // because the action is the part that cannot be reconstructed.
  it('truncates to 200 characters rather than losing the record', () => {
    const long = 'm'.repeat(400)
    const label = powerActionLabel('ForceOff', long)
    expect(label.length).toBe(200)
    expect(label.startsWith('power: ForceOff ')).toBe(true)
  })
})

describe('toMachine', () => {
  it('reads the Reachable condition into three flat fields', () => {
    const m = toMachine({
      metadata: { name: 'ml350-g9', namespace: 'default' },
      spec: { bmc: { address: '192.168.2.60' }, nodeRef: 'w2' },
      status: {
        powerState: 'On',
        lastProbeAt: '2026-09-10T11:59:30Z',
        conditions: [
          { type: 'Reachable', status: 'False', reason: 'TLSError', message: 'x509: certificate signed by unknown authority' },
        ],
      },
    })
    expect(m.reachable).toBe(false)
    expect(m.reachableReason).toBe('TLSError')
    expect(m.reachableMessage).toContain('unknown authority')
    expect(m.address).toBe('192.168.2.60')
    expect(m.nodeRef).toBe('w2')
  })

  it('survives a machine that has never been probed', () => {
    const m = toMachine({
      metadata: { name: 'fresh', namespace: 'default' },
      spec: { bmc: { address: '192.168.2.61' } },
    })
    expect(m.reachable).toBe(false)
    expect(m.lastProbeAt).toBeNull()
    expect(m.inventory).toBeNull()
    expect(m.sensors).toBeNull()
    expect(m.eventLog).toEqual([])
    expect(m.eventLogTotal).toBe(0)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npm test -- machines`
Expected: FAIL — `src/lib/machines.ts` does not exist.

- [ ] **Step 3: Implement the module**

Create `src/lib/machines.ts`. Requirements the tests pin down:

- `temperatureSeverity`: `critical` when `celsius >= upperCritical`; `warning` when `celsius >= upperCritical - 5`; `ok` below that. With no `upperCritical`, map `health` case-insensitively (`ok`/`warning`/`critical`), and return `unknown` when neither is present.
- `eventSeverity`: case-insensitive map of `ok`, `warning`, `critical`; everything else `unknown`.
- `isStale(lastProbeAt, now)`: `true` when `lastProbeAt` is null, or when `now - lastProbeAt > 150_000` ms — two polling intervals plus a half, so a single slow reconcile does not grey the panel.
- `stalenessLabel`: `'jamais relevé'` for null; `il y a N s` under 60 s; `il y a N min` under 60 min; `il y a N h` beyond. Floor the division.
- `powerActionLabel(action, machineName)`: `` `power: ${action} ${machineName}` `` truncated to 200 characters, cutting the machine name.
- `toMachine`: flatten the CR. `reachable` is `status.conditions` finding `type === 'Reachable'` with `status === 'True'`; absent means `false`. Everything nullable stays null rather than becoming a zero value, so the screen can tell "not read" from "read as zero".

Export a `MachineCR` interface describing the fields read above, with everything below `metadata` optional.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `npm test -- machines`
Expected: all specs pass.

- [ ] **Step 5: Prove the threshold test is discriminating**

Temporarily change the warning band from `- 5` to `- 0`.
Run: `npm test -- machines`
Expected: the "warns within five degrees" spec FAILS. Restore and confirm. Record both outputs.

- [ ] **Step 6: Run the whole console suite and the build**

Run: `npm test && npm run build`
Expected: every existing spec still passes; the build is clean.

- [ ] **Step 7: Commit**

```bash
git add src/lib/machines.ts src/lib/machines.test.ts
git commit -m "feat(ui): the hardware screen's pure logic, where vitest can reach it

Absence is preserved rather than defaulted: a sensor with neither threshold
nor health is 'unknown', not 'ok', and a machine never probed reads null
rather than zero. The action label truncates at 200 characters because
FrameTask drops a longer one without failing the write."
```

---

### Task 8: The SDK client

**Files:**
- Modify: `src/lib/frame-sdk.ts`
- Modify: `src/lib/frame-sdk.test.ts` if it exists; otherwise add the specs to `src/lib/machines.test.ts`

**Interfaces:**
- Consumes: `toMachine`, `powerActionLabel`, `Machine`, `MachineCR` from `./machines` (Task 7); the exported `frameListPath(plural, ns?)` already in `frame-sdk.ts`.
- Produces, on the `FrameClient` returned by `createFrameClient`:

```ts
machines: {
  list(): Promise<Machine[]>
  watchPath(): string
  power(machine: Machine, action: string): Promise<void>
}
```

- [ ] **Step 1: Find the existing client and header conventions**

Run: `grep -n "X-Frame-Action" src/lib/frame-sdk.ts | head`
Read every hit. The header is set on writes and its value must already be bounded; reuse that path rather than inventing a second one. If the existing call sites build the value inline, use `powerActionLabel` from Task 7 for the new one and note the inconsistency in your report.

- [ ] **Step 2: Write the failing test**

Add to `src/lib/machines.test.ts`:

```ts
import { machinesPath, powerPatchBody } from './frame-sdk'

describe('the machines client', () => {
  it('reads framemachines from the frame API group', () => {
    expect(machinesPath()).toBe('/apis/frame.plume-labs.io/v1beta1/framemachines')
  })

  // The write is a timestamp, not a desired state — a body carrying only the
  // action would make a second press a no-op, and a restart inexpressible.
  it('builds a patch carrying both the action and the moment it was asked for', () => {
    const before = Date.now()
    const body = powerPatchBody('ForceRestart')
    const at = Date.parse(body.spec.powerRequest.requestedAt)
    expect(body.spec.powerRequest.action).toBe('ForceRestart')
    expect(at).toBeGreaterThanOrEqual(before)
    expect(at).toBeLessThanOrEqual(Date.now())
  })
})
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `npm test -- machines`
Expected: FAIL — `machinesPath` and `powerPatchBody` are not exported.

- [ ] **Step 4: Implement**

In `src/lib/frame-sdk.ts`, near the other exported path helpers (around line 676):

```ts
export function machinesPath(ns?: string): string {
  return frameListPath('framemachines', ns)
}

/**
 * The power write is an action stamped with the moment it was requested, not a
 * desired state. A body carrying only the action would make a second press a
 * no-op and a restart inexpressible; the controller acts only when this
 * timestamp is newer than status.lastPowerActionAt.
 */
export function powerPatchBody(action: string): {
  spec: { powerRequest: { action: string; requestedAt: string } }
} {
  return { spec: { powerRequest: { action, requestedAt: new Date().toISOString() } } }
}
```

Then add a `MachineClient` class beside the other clients, keeping it under 130 lines:

- `list()` — `k8sFetch<ListResponse<MachineCR>>(machinesPath())`, mapped through `toMachine`, sorted by name.
- `watchPath()` — returns `machinesPath()`, for `useLiveResource`.
- `power(machine, action)` — a `PATCH` with `Content-Type: application/merge-patch+json`, body `powerPatchBody(action)`, and the `X-Frame-Action` header set to `powerActionLabel(action, machine.name)`, against `` `${machinesPath(machine.namespace)}/${machine.name}` ``.

Expose it on the object `createFrameClient` returns, as `machines`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `npm test && npm run build`
Expected: all specs pass, build clean.

- [ ] **Step 6: Commit**

```bash
git add src/lib/frame-sdk.ts src/lib/machines.test.ts
git commit -m "feat(ui): a machines client that writes power as a stamped action

The patch carries requestedAt, so a second press is a second action rather
than a no-op, and the X-Frame-Action header goes through the bounded label."
```

---

### Task 9: The Hardware screen — list, inventory, sensors

**Files:**
- Create: `src/components/hardware/HardwareView.tsx`
- Create: `src/components/hardware/MachineDetail.tsx`
- Modify: `src/App.tsx`

**Interfaces:**
- Consumes: `createFrameClient().machines` (Task 8); `temperatureSeverity`, `stalenessLabel`, `isStale`, `Machine` (Task 7); `useLiveResource` from `@/hooks/useLiveResource`.
- Produces: the `HardwareView` export `App.tsx` lazy-loads, and the `MachineDetail` component Task 10 adds a third tab to.

- [ ] **Step 1: Read the pattern this screen copies**

Run: `ls src/components/workloads/ && sed -n '1,60p' src/components/workloads/WorkloadsView.tsx`
The Hardware screen is the same shape: a list on the left, a detail panel on the right, tabs inside the panel. Follow its imports, its use of `useLiveResource`, and its loading and empty states rather than inventing new ones.

- [ ] **Step 2: Build the screen**

`HardwareView.tsx` renders the machine list: name, model from `inventory.model`, power state, and a freshness marker. `MachineDetail.tsx` renders the panel with two tabs for now — *Inventaire* and *Capteurs*.

Three requirements that are not cosmetic:

- **Every panel renders `stalenessLabel(machine.lastProbeAt, new Date())`.** When `isStale(...)` is true, grey the panel body and show the label prominently. A sensor value with no timestamp is a lie the moment the BMC stops answering, and this is the screen someone reads during an incident.
- **When `machine.reachable` is false, show `reachableReason` and `reachableMessage`.** `TLSError` on a fresh iLO4 is the expected first state and the message tells the person exactly what to fix; hiding it behind "unavailable" wastes their afternoon.
- **A machine with `inventory === null` shows "jamais relevé", not an empty table.** The two are different states and the screen must not conflate them.

Colour temperatures by `temperatureSeverity`. Render fans as `${reading} ${units}` — never the bare number, since 23 means 23% here and 23 RPM elsewhere.

- [ ] **Step 3: Wire it into the navigation**

In `src/App.tsx`:

1. Beside the other lazy imports (around line 25):

```tsx
const HardwareView = lazy(() => import('@/components/hardware/HardwareView').then((m) => ({ default: m.HardwareView })))
```

2. In the `Compute` NAV group (around line 191), after the existing `nodes` item, add a sibling item:

```tsx
      {
        id: 'hardware',
        label: 'Hardware',
        icon: <Server />,
        description: 'Physical chassis over Redfish: inventory, sensors, event log and power',
        tabs: [{ id: 'hardware', label: 'Machines' }],
      },
```

Import `Server` from `lucide-react` alongside the other icons if it is not already imported.

3. In `renderTab`, beside `case 'racks':`, add:

```tsx
      case 'hardware':
        return <HardwareView />
```

- [ ] **Step 4: Verify the build and the suite**

Run: `npm run build && npm test`
Expected: build clean, every existing spec still passing.

- [ ] **Step 5: Verify the nav table and renderTab agree**

`src/App.tsx` carries a comment (around line 92) saying the NAV table and `renderTab` check each other, and that a typo in NAV fails to render. Run whatever test enforces that:

Run: `npm test -- App` — if no such test exists, confirm by `grep -n "NAV" src/*.test.ts src/**/*.test.ts` and say so in your report.

- [ ] **Step 6: Commit**

```bash
git add src/components/hardware/ src/App.tsx
git commit -m "feat(ui): the Hardware screen — machine list, inventory and sensors

Every panel renders how old its reading is, and greys itself past two polling
intervals. A fan is rendered with its units because 23 means 23% here and
23 RPM elsewhere."
```

---

### Task 10: The event log and the power dialogs

**Files:**
- Create: `src/components/hardware/MachineEventLog.tsx`
- Create: `src/components/hardware/MachineActions.tsx`
- Modify: `src/components/hardware/MachineDetail.tsx`

**Interfaces:**
- Consumes: `eventSeverity`, `Machine` (Task 7); `createFrameClient().machines.power` (Task 8); `MachineDetail` (Task 9).
- Produces: nothing later tasks consume.

- [ ] **Step 1: Build the event log tab**

`MachineEventLog.tsx` renders `machine.eventLog` newest first, coloured by `eventSeverity`, with the per-severity counts and `eventLogTotal` above it. It must say, in words, that only the most recent 25 entries are retained and the counts cover the whole log — otherwise a person reading "Critical: 4" against three visible lines will conclude the screen is broken.

Add it to `MachineDetail.tsx` as the third tab, *Journal*.

- [ ] **Step 2: Build the actions**

`MachineActions.tsx` renders the buttons — allumer, extinction propre, extinction forcée, redémarrage forcé, LED d'identification, effacer le journal — each opening a confirmation dialog before calling `client.machines.power(machine, action)`.

**The confirmation names the consequence, not the request.** When `machine.nodeRef` is non-empty, the dialog must read what runs on that node before asking, and say so: "Extinction propre de `ml350-g9` — cette machine porte le nœud `w2`, avec 14 pods." Fetch the pod count with the existing SDK — `grep -n "fieldSelector=spec.nodeName" src/lib/frame-sdk.ts` will find the path if one exists; if none does, count from the pod list the workloads client already fetches, and say in your report which you used.

When `nodeRef` is empty, say that plainly instead: "cette machine ne porte aucun nœud du cluster".

Disable every button when the signed-in person is not admin. The console already knows the tier — `grep -rn "tier\|isAdmin\|groups" src/lib/frame-config.ts src/lib/frame-sdk.ts | head` will find how; follow it rather than adding a second notion of who is admin.

- [ ] **Step 3: Verify the build and the suite**

Run: `npm run build && npm test`
Expected: build clean, all specs pass.

- [ ] **Step 4: Commit**

```bash
git add src/components/hardware/
git commit -m "feat(ui): the event log tab and the power dialogs

The confirmation names what the machine carries, not just what was asked:
powering off a chassis that holds a cluster node is a different act from
powering off a spare, and the person clicking must be told which one it is."
```

---

### Task 11: Documentation

**Files:**
- Modify: `docs/deployment.md`
- Modify: `docs/crd-reference.md`
- Modify: `docs/runbook.md`

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Write the registration section**

In `docs/deployment.md`, add a section **Registering a machine's BMC**. It must contain the exact commands, and state plainly that this is a `kubectl` step because no console tier holds `secrets`:

```bash
# The credentials never enter the repo and never enter the console.
kubectl create secret generic ml350-g9-ilo \
  --namespace default \
  --from-literal=username='<iLO user>' \
  --from-literal=password='<iLO password>'

kubectl apply -f - <<'EOF'
apiVersion: frame.plume-labs.io/v1beta1
kind: FrameMachine
metadata:
  name: ml350-g9
  namespace: default
spec:
  bmc:
    address: 192.168.2.60
    credentialsRef: ml350-g9-ilo
    tls:
      insecureSkipVerify: true
EOF

kubectl get framemachine ml350-g9 -o wide
```

State the two prerequisites: the operator must reach the address at layer 3 (free on a shared LOM port, a route on a separate management VLAN), and an iLO4 needs firmware 2.30 or later for Redfish.

- [ ] **Step 2: Write the end-to-end check, labelled unexecuted**

In the same file, add a subsection **The check that has never once been run**, matching how lots 0c and 2 label theirs. Twelve numbered steps: register the machine; watch `Reachable` go true; read the model and serial on the screen; compare a temperature against the iLO's own web UI; find the machine's last power-on in the event log; light the identification LED and confirm it physically; put it out; shut the machine down gracefully from the console; confirm a `FrameTask` naming you and the action exists; power it back on; confirm the event log gained an entry; confirm the screen greys itself when you unplug the management port.

Mark the whole section **not executed**, and say why: no BMC was reachable when this lot was written.

- [ ] **Step 3: Add the CRD reference entry**

In `docs/crd-reference.md`, add `FrameMachine` to whatever table lists the kinds, noting it has a controller, has **no** webhook and **no** `v1alpha1`, and pointing at the spec.

- [ ] **Step 4: Add the runbook note**

In `docs/runbook.md`, add a short entry: what `Reachable=False` means per reason (`TLSError`, `AuthFailed`, `Timeout`, `Unsupported`, `CredentialsUnavailable`, `ProbeFailed`), and that a stale reading is shown deliberately rather than hidden.

- [ ] **Step 5: Verify every path and command you wrote**

Run each `kubectl` command's `--dry-run=client` form, and check that every file path named in the docs exists:

Run: `kubectl apply --dry-run=client -f config/samples/frame_v1beta1_framemachine.yaml`
Expected: `framemachine.frame.plume-labs.io/ml350-g9 created (dry run)` — or, if no cluster is reachable, `--dry-run=client` still validates the shape locally.

- [ ] **Step 6: Commit**

```bash
git add docs/
git commit -m "docs(hardware): registering a BMC, and the check nobody has run

Registration is a kubectl step and the document says why: no console tier
holds secrets, and a form that needed one would be a decision rather than a
convenience."
```

---

## Self-review

**Spec coverage.** Each section of the spec maps to a task: the object and its CEL bounds → Task 1; credentials, the SSRF bound and TLS → Tasks 1 and 4; the controller, polling and staleness → Task 4; power as a timestamp → Task 5; authorization and the tiers → Task 6; the screen → Tasks 9 and 10; the pure logic vitest can reach → Task 7; registration as a `kubectl` step and the unexecuted check → Task 11. The spec's rejected alternatives need no task.

**Known deviations, decided rather than discovered.**

- **No `gofish`.** The spec says the client sits behind a Go interface; it does not say the implementation must be a library. Seven endpoints against one firmware family is not where a broad vendor-abstraction library pays for itself, and the named risk — fields iLO4 omits — is answered by pointer decoding directly. Cost if wrong: another firmware answers differently and we own the fix rather than pulling an upgrade.
- **`src/lib/frame-sdk.ts` grows again**, against the 500-line rule it already breaks at ~3480. Task 8 caps the addition at 130 lines and puts every testable behaviour in `src/lib/machines.ts`. Extracting `WorkloadClient` and `MachineClient` into their own modules is recorded as owed work, not done here — the same deferral lot 2 made.
- **`.tsx` carries no test coverage**, because vitest runs `include: ['src/**/*.test.ts']`. Tasks 9 and 10 are therefore reviewed by reading. This is stated in the spec and is not a gap this plan can close without changing the test configuration, which is out of scope.
