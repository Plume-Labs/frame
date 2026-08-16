# NodeTuning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Frame a `NodeTuning` CRD, a node agent, and a controller that
converges low-level node settings and orchestrates the disruptive restarts they
need, behind per-node human approval.

**Architecture:** A cluster-scoped `NodeTuning` selects nodes and declares the
settings tuned cannot own (KSM, cpu-manager policy, MIG label) plus which tuned
profile the node runs. A privileged DaemonSet agent applies node-local state and
reports *measured* state — never what it wrote. A controller in
`frame-controller-manager` diffs desired against observed, and for anything
needing a restart it stops at `RebootPending` until a per-node annotation
approves that exact generation, then does cordon → drain → detached restart →
verify → uncordon, one node at a time.

**Tech Stack:** Go 1.26, kubebuilder v4 (multigroup), controller-runtime v0.23.3,
envtest, Ginkgo/Gomega, Kind e2e, tuned (in the agent image).

**Spec:** `docs/superpowers/specs/2026-08-16-frame-node-tuning-operator-design.md`

## Global Constraints

- API group `frame.plume-labs.io`, version **`v1beta1`**. V1 ships on `v1beta1`;
  there is no `v1`.
- `NodeTuning` is **cluster-scoped**.
- Module path `github.com/rmocq/frame`; `PROJECT` has `multigroup: true`.
- Every new CRD must exist in **both** `config/crd/bases/` and
  `charts/frame/files/crds/` — `hack/helm-parity.sh` is what guards this.
- ClusterRole rules are maintained in **two** hand-kept copies:
  `config/rbac/role.yaml` and the chart's equivalent. There is no sync target.
- `.status.storedVersions` is append-only; do not add a second served version.
- The agent may restart only units on a compile-time allowlist. The allowlist is
  **not** a CRD field.
- `ksm.enabled` defaults to **false**. Page merging across tenants is a
  side channel; it stays opt-in.
- Tests: `make test` (envtest). E2E: `make test-e2e` (Kind), not in CI.

---

### Task 1: The `NodeTuning` API type

**Files:**
- Create: `api/frame/v1beta1/nodetuning_types.go`
- Modify: `api/frame/v1beta1/zz_generated.deepcopy.go` (generated)
- Test: `internal/controller/frame/nodetuning_v1beta1_schema_test.go`

**Interfaces:**
- Produces: `NodeTuning`, `NodeTuningSpec`, `NodeTuningStatus`,
  `NodeTuningNodeStatus`, `KSMSpec`, `ObservedTuning`, `ObservedKSM`, and the
  phase/realization string constants used by every later task.

- [ ] **Step 1: Write the failing schema test**

```go
package frame

import (
	"testing"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func TestNodeTuningKSMDefaultsOff(t *testing.T) {
	// Page merging across tenants is a side channel. A NodeTuning that says
	// nothing about KSM must not turn it on.
	var nt framev1beta1.NodeTuning
	if nt.Spec.KSM != nil {
		t.Fatalf("KSM must be nil unless declared, got %+v", nt.Spec.KSM)
	}
}

func TestNodeTuningPhaseConstantsAreDistinct(t *testing.T) {
	seen := map[framev1beta1.NodeTuningPhase]bool{}
	for _, p := range []framev1beta1.NodeTuningPhase{
		framev1beta1.PhaseInSync, framev1beta1.PhaseDrifted,
		framev1beta1.PhaseRebootPending, framev1beta1.PhaseApplying,
		framev1beta1.PhaseFailed,
	} {
		if seen[p] {
			t.Fatalf("duplicate phase constant %q", p)
		}
		seen[p] = true
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/controller/frame/ -run TestNodeTuning`
Expected: FAIL — `undefined: framev1beta1.NodeTuning`

- [ ] **Step 3: Write the types**

```go
package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type NodeTuningPhase string

const (
	PhaseInSync        NodeTuningPhase = "InSync"
	PhaseDrifted       NodeTuningPhase = "Drifted"
	PhaseRebootPending NodeTuningPhase = "RebootPending"
	PhaseApplying      NodeTuningPhase = "Applying"
	PhaseFailed        NodeTuningPhase = "Failed"
)

type Realization string

const (
	// RealizationEffective means the setting is live in the running unit, so
	// containers created from now on get it.
	RealizationEffective Realization = "Effective"
	// RealizationFullyRealized additionally means every container on the node
	// was created after the change. For KSM this is where most of the benefit
	// actually is.
	RealizationFullyRealized Realization = "FullyRealized"
	RealizationPending       Realization = "Pending"
)

// ApprovalAnnotation carries the generation a human approved. A generation
// rather than a boolean, so approving one change never silently authorizes the
// next one.
const ApprovalAnnotation = "frame.plume-labs.io/tuning-approved"

type KSMSpec struct {
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`
	// +kubebuilder:validation:Minimum=1
	PagesToScan *int32 `json:"pagesToScan,omitempty"`
	// +kubebuilder:validation:Minimum=1
	SleepMillisecs   *int32 `json:"sleepMillisecs,omitempty"`
	MergeAcrossNodes *bool  `json:"mergeAcrossNodes,omitempty"`
}

type NodeTuningSpec struct {
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
	// TunedProfile owns sysctls, kernel modules, governor and scheduler. Frame
	// declares which profile runs and never restates its contents: tuned
	// re-applies at boot and on every change, so a second writer has no owner.
	TunedProfile string `json:"tunedProfile,omitempty"`
	KSM          *KSMSpec `json:"ksm,omitempty"`
	// +kubebuilder:validation:Enum=none;static
	CPUManagerPolicy string `json:"cpuManagerPolicy,omitempty"`
	MIGProfile       string `json:"migProfile,omitempty"`
}

type ObservedKSM struct {
	MemoryKSM     bool  `json:"memoryKSM"`
	GeneralProfit int64 `json:"generalProfit"`
	PagesSharing  int64 `json:"pagesSharing"`
}

type ObservedTuning struct {
	KSM              *ObservedKSM `json:"ksm,omitempty"`
	TunedProfile     string       `json:"tunedProfile,omitempty"`
	CPUManagerPolicy string       `json:"cpuManagerPolicy,omitempty"`
}

type NodeTuningNodeStatus struct {
	Name              string          `json:"name"`
	Phase             NodeTuningPhase `json:"phase,omitempty"`
	AppliedGeneration int64           `json:"appliedGeneration,omitempty"`
	Realization       Realization     `json:"realization,omitempty"`
	Observed          ObservedTuning  `json:"observed,omitempty"`
	Message           string          `json:"message,omitempty"`
}

type NodeTuningStatus struct {
	ObservedGeneration int64                  `json:"observedGeneration,omitempty"`
	Nodes              []NodeTuningNodeStatus `json:"nodes,omitempty"`
	Conditions         []metav1.Condition     `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type NodeTuning struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NodeTuningSpec   `json:"spec,omitempty"`
	Status            NodeTuningStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NodeTuningList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeTuning `json:"items"`
}

func init() { SchemeBuilder.Register(&NodeTuning{}, &NodeTuningList{}) }
```

- [ ] **Step 4: Generate and verify**

Run: `make generate manifests && go test ./internal/controller/frame/ -run TestNodeTuning`
Expected: PASS, and `config/crd/bases/frame.plume-labs.io_nodetunings.yaml` exists.

- [ ] **Step 5: Mirror the CRD into the chart**

```bash
cp config/crd/bases/frame.plume-labs.io_nodetunings.yaml charts/frame/files/crds/
./hack/helm-parity.sh
```
Expected: helm-parity reports no drift.

- [ ] **Step 6: Commit**

```bash
git add api/frame/v1beta1/nodetuning_types.go config/crd/bases charts/frame/files/crds internal/controller/frame/nodetuning_v1beta1_schema_test.go
git commit -m "feat(api): NodeTuning type, cluster-scoped, KSM off by default"
```

---

### Task 2: Agent — observe only

**Files:**
- Create: `cmd/agent/main.go`, `internal/agent/observe.go`
- Create: `Dockerfile.agent`
- Test: `internal/agent/observe_test.go`

**Interfaces:**
- Consumes: `framev1beta1.ObservedTuning`, `ObservedKSM` from Task 1.
- Produces: `agent.Observe(root string) (framev1beta1.ObservedTuning, error)`,
  reading under `root` so tests can point it at a fixture tree instead of `/`.

- [ ] **Step 1: Write the failing test**

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// The bug this whole design exists to catch: the drop-in is on disk while
// systemd still reports the old value. Observing the file is not observing the
// effect, so Observe must read what systemd reports, not what we wrote.
func TestObserveReportsSystemdNotTheFile(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/systemd/system/k3s-agent.service.d/10-ksm.conf"),
		"[Service]\nMemoryKSM=yes\n")
	mustWrite(t, filepath.Join(root, "run/frame-agent/memory-ksm"), "no\n")

	got, err := Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.KSM == nil || got.KSM.MemoryKSM {
		t.Fatalf("drop-in present but systemd says no: want MemoryKSM=false, got %+v", got.KSM)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/agent/ -run TestObserve`
Expected: FAIL — `undefined: Observe`

- [ ] **Step 3: Implement `Observe`**

`Observe` reads, under `root`: `run/frame-agent/memory-ksm` (written by the
agent from `systemctl show <unit> -p MemoryKSM --value`),
`sys/kernel/mm/ksm/general_profit`, `sys/kernel/mm/ksm/pages_sharing`, and
`etc/tuned/active_profile`. A missing file yields the zero value and no error —
an unconfigured node is not a broken one.

- [ ] **Step 4: Run the test**

Run: `go test ./internal/agent/...`
Expected: PASS

- [ ] **Step 5: Agent main loop and image**

`cmd/agent/main.go` resolves its node from the downward-API `NODE_NAME`, calls
`Observe` every 30s, and patches the matching entry of the `NodeTuning` status.
`Dockerfile.agent` installs `tuned` and copies the binary.

- [ ] **Step 6: Commit**

```bash
git add cmd/agent internal/agent Dockerfile.agent
git commit -m "feat(agent): observe measured node state, never what we wrote"
```

---

### Task 3: Agent — apply

**Files:**
- Create: `internal/agent/apply.go`
- Test: `internal/agent/apply_test.go`

**Interfaces:**
- Produces: `agent.Apply(root string, spec framev1beta1.NodeTuningSpec) (needsRestart bool, err error)`
  and `agent.IsRestartable(unit string) bool`, the compile-time allowlist Task 6
  calls before scheduling any restart.

- [ ] **Step 1: Write the failing tests**

```go
// Writing the drop-in is not enough — it only takes effect on the next unit
// start, so Apply must say so rather than let a caller assume it is live.
func TestApplyKSMRequestsRestart(t *testing.T) {
	root := t.TempDir()
	yes := true
	needsRestart, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: yes},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !needsRestart {
		t.Fatal("enabling KSM writes a systemd drop-in; that needs a restart")
	}
}

// The scanner knobs are live immediately, so changing only those must NOT
// demand a disruptive restart.
func TestApplyScannerKnobsAloneNeedNoRestart(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/systemd/system/k3s-agent.service.d/10-ksm.conf"),
		"[Service]\nMemoryKSM=yes\n")
	four := int32(4000)
	needsRestart, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: true, PagesToScan: &four},
	})
	if err != nil {
		t.Fatal(err)
	}
	if needsRestart {
		t.Fatal("scanner knobs apply live; demanding a restart would drain a node for nothing")
	}
}
```

```go
// The unit allowlist is the whole security boundary of the agent: once a
// restart target is attacker- or typo-controlled, "restart a unit" is
// arbitrary root. It must be compile-time, never reachable from the CRD.
func TestRestartableUnitsAreAllowlisted(t *testing.T) {
	for _, u := range []string{"k3s", "k3s-agent", "kubelet", "containerd"} {
		if !IsRestartable(u) {
			t.Errorf("%s should be restartable", u)
		}
	}
	for _, u := range []string{"sshd", "systemd-networkd", "nftables", "../k3s", ""} {
		if IsRestartable(u) {
			t.Errorf("%s must never be restartable", u)
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/agent/ -run 'TestApply|TestRestartable'`
Expected: FAIL — `undefined: Apply`, `undefined: IsRestartable`

- [ ] **Step 3: Implement `Apply`**

Writes the drop-in only when its content would change; writes sysfs knobs
individually and tolerates a refusal (the kernel returns EBUSY on
`merge_across_nodes` once anything is merged, and a strict writer would fail on
every run after the first); sets `tuned-adm profile` when it differs; sets the
MIG label. Returns `needsRestart` when the drop-in or cpu-manager policy
changed.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent
git commit -m "feat(agent): apply node-local settings, distinguishing live knobs from restart-gated ones"
```

---

### Task 4: Controller — diff and report, no disruption

**Files:**
- Create: `internal/controller/frame/nodetuning_controller.go`
- Modify: `cmd/main.go` (register alongside the other controllers)
- Modify: `config/rbac/role.yaml` and the chart's ClusterRole copy
- Test: `internal/controller/frame/nodetuning_controller_test.go`

**Interfaces:**
- Produces: `NodeTuningReconciler` with `SetupWithManager(mgr)`.

- [ ] **Step 1: Write the failing tests (envtest)**

```go
// Overlapping selectors are a configuration error, not a merge: silently
// picking a winner makes the effective configuration unpredictable.
It("marks a node Failed when two NodeTunings select it", func() {
	createNodeTuning("a", map[string]string{"role": "worker"})
	createNodeTuning("b", map[string]string{"role": "worker"})
	Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseFailed))
	Expect(nodeStatusMessage("a", "node-1")).To(ContainSubstring("b"))
})

It("reports Drifted when observed does not match spec", func() {
	createNodeTuning("a", map[string]string{"role": "worker"})
	setObserved("a", "node-1", framev1beta1.ObservedTuning{KSM: &framev1beta1.ObservedKSM{MemoryKSM: false}})
	Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseDrifted))
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `make test`
Expected: FAIL — reconciler does not exist

- [ ] **Step 3: Implement the reconciler**

Lists nodes by selector; for each, finds all `NodeTuning` objects selecting it
and reports `Failed` naming the others if more than one; otherwise diffs spec
against `status.nodes[].observed` and writes `InSync` or `Drifted`. It performs
no disruptive action in this task.

- [ ] **Step 4: RBAC and registration**

Add `nodetunings` and `nodetunings/status` rules plus `nodes` get/list/watch to
**both** ClusterRole copies, then register in `cmd/main.go` next to the existing
`SetupWithManager` calls.

- [ ] **Step 5: Run the tests**

Run: `make test && ./hack/helm-parity.sh`
Expected: PASS, no chart drift.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/frame cmd/main.go config/rbac charts
git commit -m "feat(controller): NodeTuning diffs desired against observed"
```

---

### Task 5: Approval gate

**Files:**
- Modify: `internal/controller/frame/nodetuning_controller.go`
- Test: `internal/controller/frame/nodetuning_controller_test.go`

- [ ] **Step 1: Write the failing tests**

```go
It("waits at RebootPending until the node is approved", func() {
	setObservedNeedingRestart("a", "node-1")
	Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseRebootPending))
	Consistently(nodeCordoned("node-1"), "3s").Should(BeFalse())
})

// A stale approval must not carry: approving generation 4 says nothing about 5.
It("ignores an approval for an older generation", func() {
	annotateNode("node-1", framev1beta1.ApprovalAnnotation, "4")
	bumpSpecToGeneration(5)
	Consistently(nodeStatusPhase("a", "node-1"), "3s").Should(Equal(framev1beta1.PhaseRebootPending))
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `make test`
Expected: FAIL — the controller proceeds without approval

- [ ] **Step 3: Implement the gate**

When `needsRestart`, set `RebootPending` and return. Proceed only when the
node's `ApprovalAnnotation` parses to an integer equal to the `NodeTuning`'s
current `metadata.generation`.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/controller/frame
git commit -m "feat(controller): gate restarts on a per-node, per-generation approval"
```

---

### Task 6: Rolling restart

**Files:**
- Modify: `internal/controller/frame/nodetuning_controller.go`
- Create: `internal/controller/frame/nodetuning_rollout.go`
- Test: `internal/controller/frame/nodetuning_rollout_test.go`

- [ ] **Step 1: Write the failing tests**

```go
It("refuses to start on a node while another is not Ready", func() {
	markNodeNotReady("node-2")
	approve("node-1")
	Consistently(nodeCordoned("node-1"), "3s").Should(BeFalse())
	Expect(nodeStatusMessage("a", "node-1")).To(ContainSubstring("node-2"))
})

// A rollout that keeps going through failures turns one broken node into an
// outage. A partially tuned cluster is recoverable; this is not.
It("halts the campaign and leaves the node cordoned on failure", func() {
	approve("node-1"); failRestart("node-1")
	Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseFailed))
	Expect(nodeCordoned("node-1")()).To(BeTrue())
	approve("node-2")
	Consistently(nodeCordoned("node-2"), "3s").Should(BeFalse())
})
```

- [ ] **Step 2: Run to verify they fail**

Run: `make test`
Expected: FAIL — no rollout logic

- [ ] **Step 3: Implement the rollout**

Cordon → evict respecting PodDisruptionBudgets → ask the agent to schedule a
detached restart → wait → uncordon. Verification compares the unit's
`ActiveEnterTimestamp` before and after, **not** node readiness: k3s-agent
returns in seconds while a node only reports `NotReady` after roughly forty
seconds of missed lease, so waiting for a flap reports failure on success. Node
readiness is still required before uncordoning. Every probe in the wait loop
tolerates failure — restarting k3s on the server node takes the apiserver down
for seconds, so the controller's own reads are expected to fail mid-wait.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/controller/frame
git commit -m "feat(controller): roll restarts one node at a time, halting on failure"
```

---

### Task 7: Realization

**Files:**
- Modify: `internal/controller/frame/nodetuning_controller.go`
- Test: `internal/controller/frame/nodetuning_controller_test.go`

- [ ] **Step 1: Write the failing test**

```go
// A node can be correctly configured and still deduplicate almost nothing
// while its long-lived pods predate the restart. Reporting that as "applied"
// is the illusion this field exists to prevent.
It("reports Effective, not FullyRealized, when pods predate the restart", func() {
	restartAt := metav1.Now()
	createPodOnNode("node-1", "old", metav1.NewTime(restartAt.Add(-time.Hour)))
	completeRestart("node-1", restartAt)
	Eventually(nodeRealization("a", "node-1")).Should(Equal(framev1beta1.RealizationEffective))
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `make test`
Expected: FAIL — realization is never set

- [ ] **Step 3: Implement**

After a successful restart set `Effective`. Promote to `FullyRealized` when no
pod on the node has a `startTime` earlier than the restart. A drain produces
this for free, so the two usually converge.

- [ ] **Step 4: Run the test**

Run: `make test`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/controller/frame
git commit -m "feat(controller): distinguish Effective from FullyRealized"
```

---

### Task 8: Deploy the orphans, ship the agent

**Files:**
- Modify: `deploy/kubernetes/base/kustomization.yaml`
- Create: `deploy/kubernetes/base/node-tuning-agent/daemonset.yaml`
- Delete: `deploy/kubernetes/base/ksm-tuner/`
- Test: `test/e2e/nodetuning_test.go`

- [ ] **Step 1: Wire the never-deployed manifests in**

`nfd` and `nvidia-mps` are ordinary DaemonSets with no drift problem; their
only defect is that `base/kustomization.yaml` never referenced them, so they
were never deployed. Add them as resources. `ksm-tuner` is superseded by the
agent and is removed.

- [ ] **Step 2: Write the e2e test**

```go
// The founding bug, end to end: a drop-in on disk while systemd still reports
// the old value must surface as Drifted, not as InSync.
func TestAgentReportsDropInNotYetLoaded(t *testing.T) {
	applyNodeTuning(t, `
apiVersion: frame.plume-labs.io/v1beta1
kind: NodeTuning
metadata: { name: e2e }
spec:
  nodeSelector: { matchLabels: { "kubernetes.io/os": linux } }
  ksm: { enabled: true }
`)
	// Write the drop-in behind the agent's back and do NOT reload systemd, so
	// the file says yes while the unit still says no.
	execOnNode(t, "kind-worker", `mkdir -p /etc/systemd/system/kubelet.service.d && `+
		`printf '[Service]\nMemoryKSM=yes\n' > /etc/systemd/system/kubelet.service.d/10-ksm.conf`)

	phase := eventuallyNodePhase(t, "e2e", "kind-worker", 90*time.Second)
	if phase != string(framev1beta1.PhaseRebootPending) &&
		phase != string(framev1beta1.PhaseDrifted) {
		t.Fatalf("file on disk must not read as applied; got phase %q", phase)
	}
	obs := nodeObserved(t, "e2e", "kind-worker")
	if obs.KSM == nil || obs.KSM.MemoryKSM {
		t.Fatalf("agent must report what systemd says, not the file: %+v", obs.KSM)
	}
}
```

- [ ] **Step 3: Run it**

Run: `make test-e2e`
Expected: PASS. Note `make test-e2e` is not in CI, so this guards only when run
by hand.

- [ ] **Step 4: Commit**

```bash
git add deploy/kubernetes/base test/e2e
git commit -m "feat(deploy): ship the agent, deploy nfd and mps at last, drop ksm-tuner"
```
