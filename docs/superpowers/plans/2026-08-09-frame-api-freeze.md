# Frame API Freeze — `v1beta1` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `frame.plume-labs.io/v1beta1` and `services.plume-labs.io/v1beta1` as the storage version for all eight kinds, with `v1alpha1` still served through a conversion webhook, so that Frame has a frozen API with a real deprecation path and every stored object survives the move.

**Architecture:** Two API versions per kind. `v1beta1` is the **hub** (`conversion.Hub`, `+kubebuilder:storageversion`); `v1alpha1` is a **spoke** (`conversion.Convertible`, `deprecated: true`). One conversion webhook, served by the existing manager on `/convert`, with cert-manager injecting the CA into each CRD's `spec.conversion.webhook.clientConfig.caBundle`. Controllers, admission webhooks and authd are rewritten against the hub types; the apiserver's default `matchPolicy: Equivalent` converts admission requests arriving at `v1alpha1` to the hub before the webhook sees them, so admission webhooks are registered on `v1beta1` only.

**Tech Stack:** Go 1.26.1, Kubebuilder v4.14.0 (`/home/rmocq/bin/kubebuilder`), controller-gen v0.20.1 (`bin/controller-gen`), controller-runtime v0.23.3, `k8s.io/*` v0.35.0, Ginkgo v2 + Gomega with envtest, Kind for e2e, Helm 3, kustomize v5.8.1, TypeScript/React for `src/`.

**Design source:** `docs/superpowers/specs/2026-08-09-frame-api-freeze-inventory.md` — a 31-decision inventory (14 irreversible, 8 one-way, 9 additive) ending in a "Decisions taken" section. **Read §2 (ground truth), §6 (sequencing) and the "Decisions taken" section before Task 12.** The tasks implement the inventory; they do not restate its reasoning.

---

## Global Constraints

These are binding. Three of them are the repository owner's own decisions and are **not open for re-argument** — implement them as written.

### Owner decisions (binding, verbatim from the inventory's "Decisions taken")

1. **F2 — conditions only, everywhere.** `status.phase` goes. The convention argument wins over the sunk cost: three printer columns and two SDK mappers are a day's work, and a `phase` field removed after the freeze needs a third version. The consequence F3 names stands and must be handled — conversion cannot reconstruct `phase` from conditions, so `v1alpha1`'s `phase` becomes a one-way projection *out* of conditions, computed, never stored.

2. **F8 — delete the constraint.** The GPU/`LOW` rule couples two orthogonal properties and rests on a hardware fact with an expiry date. It currently constrains nothing, so deleting it removes no protection anyone has. The type comment and the webhook branch both go; the security review's finding is closed by removal rather than by enforcement.

3. **F10 — derive priority from `serviceClass`.** The premise is accepted: a service instance's tier is its urgency, so `serviceClass` maps to a `PriorityClass` exactly as `FrameJob.spec.priority` already does. This is irreversible and knowingly gives an existing field a second meaning. Recorded here so that a later reader finds the reasoning rather than rediscovering the overloading and assuming it was an accident: if a HIGH-tier instance ever needs to be evicted before a MEDIUM one, that is a `v1beta2` problem, and this paragraph is the evidence that the case was considered and rejected rather than missed.

### Mechanical constraints

4. **No new object-level CEL.** CRD validation ratcheting is per-schema-node: a rule attached to `spec` re-runs whenever *any part of* `spec` changes, so an over-strict `spec`-level `XValidation` permanently freezes an existing object — it can never be edited again, in any way. Fix Round 1 had to back exactly one of these out (the GPU/`LOW` rule). **No task in this plan adds a `spec`-level `XValidation`.** The four inherited object-level rules (FrameNode network-once-disk, FrameResourceQuota at-least-one-limit, SchedulingPolicy preemption/priorityClass, TalosMachineConfig configPatch-oneof) carry over to `v1beta1` verbatim and become permanent; the inventory verified with server-side dry-run patches on `quota-high` (`maxJobs`) and `neura-default` (`queueWeight`) that none of them strands a stored object. Any task that nonetheless needs an object-level rule must justify it in its own text and name the stored objects it was checked against.

5. **Sweep the whole tree for every field you touch.** A previous "remove unused fields" pass missed two *writers*, in `test/e2e/e2e_test.go` and `deploy/samples/test-cluster/workloads.yaml`. Every task that renames, removes or re-types a field runs its sweep over `api/`, `internal/`, `cmd/`, `test/`, `config/`, `charts/`, `deploy/`, `src/` (the SDK at `src/lib/frame-sdk.ts` and the views under `src/components/`) and `docs/` — not just `internal/`.

6. **Ground truth: what is actually stored** (live k3s test cluster, `KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml`):

   | Kind | Stored objects | What constrains a change |
   |---|---|---|
   | FrameJob | **2** | both `gpuCount: 0`, both `namespace: default` in namespace `default`; one `serviceClass: LOW`; condition type `Submitted` on both |
   | FrameNode | **3** | `rack-01`, `local`; all have a non-empty `serviceClass` (none uses `""`) |
   | FrameResourceQuota | **3** | `generation: 3`, `Ready` at `observedGeneration: 2` |
   | SchedulingPolicy | **1** | `preemption: true` + `priorityClass: neura-high` |
   | FrameUser | **0** | nothing to convert — changes here are free |
   | TalosMachineConfig | **0** | nothing to convert — changes here are free |
   | TalosUpgrade | **0** | nothing to convert — changes here are free |
   | FrameService | **0** | nothing to convert — changes here are free |

   Every task states which side of this table it falls on.

7. **`status.storedVersions` only grows.** A version cannot be dropped from a CRD while it appears in `.status.storedVersions`, and it stays there until every stored object has been rewritten at the new storage version. Task 21 owns the migration and the assertion.

8. **Never hand-edit** `config/crd/bases/*`, `config/rbac/role.yaml`, `config/webhook/manifests.yaml`, `**/zz_generated.*.go`, `charts/frame/files/crds/*`, or `PROJECT`. Regenerate with `make manifests generate`. Never delete a `// +kubebuilder:scaffold:*` marker. Scaffold with `kubebuilder create api` / `kubebuilder create webhook`, never by hand-creating the files.

9. **`make test` must pass at the end of every task.** Envtest coverage on `internal/controller` is gated at ≥45% in CI. `make lint` must not gain new findings: check with `make lint 2>&1 | grep <your-file>`.

10. **Log messages:** capital first letter, no trailing period, past tense, name the object type — `log.Info("Created Deployment", "name", d.Name)`.

11. **Every reconcile is idempotent and re-fetches before updating.** Use `metav1.Condition` for status, never bespoke string fields.

12. **Commit path-explicitly.** `deploy/kubernetes/overlays/development/kustomization.yaml` is uncommitted and unrelated to this work. **Never `git add -A`** — every commit in this plan names its paths.

---

## File Structure

**Existing, modified:**

| Path | What changes |
|---|---|
| `api/frame/v1alpha1/*_types.go` | become the spoke: `ConvertTo`/`ConvertFrom`, `deprecated` markers, `observedGeneration` added (Task 6) |
| `api/services/v1alpha1/frameservice_types.go` | same, for `FrameService` |
| `internal/controller/frame/*.go` | retyped to `v1beta1`; phase reads become condition reads |
| `internal/controller/services/*.go` | retyped to `v1beta1`; `status.phase` writes removed |
| `internal/webhook/frame/v1alpha1/` | **moves** to `internal/webhook/frame/v1beta1/` |
| `internal/webhook/services/v1alpha1/` | **moves** to `internal/webhook/services/v1beta1/` |
| `internal/authd/` | retyped to `v1beta1`; `PasswordHash` moves `Spec` → `Status` |
| `internal/services/provider/inference/inference.go` | sets `PriorityClassName` (Task 5) |
| `config/crd/kustomization.yaml` | eight conversion patches registered, `configurations:` uncommented |
| `config/default/kustomization.yaml` | the two commented conversion CA-injection replacement blocks turned on |
| `charts/frame/templates/crds.yaml` | injects `spec.conversion` + the cert-manager annotation |
| `charts/frame/templates/webhookconfigurations.yaml` | webhook names/versions move to `v1beta1` |
| `charts/frame/templates/_helpers.tpl` | `frame.tierRoleCRDs` gains `frameuser` |
| `hack/helm-parity.sh` | stops skipping CRDs wholesale (Task 10) |
| `Makefile` | `crd-render` target (Task 11) |
| `PROJECT` | `v1beta1` resources, `conversion: true` |
| `src/lib/frame-sdk.ts` | `VERSION = 'v1beta1'`, phase mappers rewritten onto conditions |
| `test/e2e/e2e_test.go` | conversion spec, storage-migration spec |
| `deploy/jobs/pipeline-parallelism.yaml`, `deploy/jobs/speculative-decoding.yaml` | `InferenceRoute` stanzas removed |
| `deploy/samples/test-cluster/workloads.yaml`, `config/samples/*` | `apiVersion` bumped, removed fields dropped |
| `docs/crd-reference.md`, `docs/upgrading.md`, `docs/roadmap.md` | the freeze written down |

**New:**

| Path | Responsibility |
|---|---|
| `internal/scheduling/priority.go` | `PriorityClassForJobPriority`, `PriorityClassForServiceClass` — the one mapping both consumers share |
| `api/frame/v1beta1/` | the seven `frame.plume-labs.io` hub types + `serviceclass.go` |
| `api/services/v1beta1/` | the `FrameService` hub type |
| `api/frame/v1alpha1/conversion.go` | `ConvertTo`/`ConvertFrom` for the seven |
| `api/frame/v1alpha1/phase.go` | the one-way `status.phase` projection out of conditions |
| `api/frame/v1alpha1/conversion_test.go` | fuzzed round-trip, both directions |
| `api/services/v1alpha1/conversion.go`, `phase.go`, `conversion_test.go` | same, for `FrameService` |
| `config/crd/patches/webhook_in_<plural>.yaml` × 8 | the kustomize conversion stanza |
| `internal/controller/frame/conversion_envtest_test.go` | F14 layer 2: write `v1alpha1`, read `v1beta1`, against a real apiserver |
| `config/rbac/frameuser_{admin,editor,viewer}_role.yaml` | the missing tier |
| `bin/crd-render/` (gitignored) | kustomize-rendered CRDs the envtest suites load |

---

## Task order and why

The inventory's §6 is the sequencing authority. Restated as this plan's parts:

- **Part 0 (Tasks 1–9)** — behaviour and additive changes that touch no CRD *shape*, plus the two additive status fields. They land **before** the conversion change for three reasons: F12 must precede F4 (F4 removes `""` from FrameNode's `serviceClass` enum; F12 stops writing empty label values — reversed, a stored `serviceClass: ""` converts into a `v1beta1` object whose enum forbids the value it holds, surfacing as a node-patch failure rather than a validation error); F10 must precede F4 so the semantics F4 freezes are the final ones; and Tasks 6 and 7 add `status.observedGeneration` and `status.used` to **`v1alpha1`**, which is what makes the `v1beta1 → v1alpha1 → v1beta1` direction lossless by construction and removes any need for a conversion annotation escape hatch (see Task 18).
- **Part 1 (Tasks 10–11)** — conversion plumbing that must exist before any two-version CRD does. F13's Helm divergence is decided here, not discovered later.
- **Part 2 (Tasks 12–22)** — **the one conversion-compatible change. These eleven tasks land together, on one branch, as one merge.** Anything that alters the shape or meaning of a field has to be in the change that introduces `v1beta1`, because `v1beta1` is the only artefact that can carry it.
- **Part 3 (Tasks 23–24)** — RBAC and docs, which depend on F11's outcome but do not gate it.

---

# Part 0 — Before the freeze

Conversion-neutral. Each task here is independently shippable and independently revertible.

---

### Task 1: Delete the GPU / `serviceClass: LOW` constraint (F8)

**Owner decision, binding.** The rule is enforced only for the three pipelines in `knownPipelines`, so it has never applied to `training` — this project's own sample and e2e pipeline. It is documented in the type comment and called out in the security review, and the owner ruled that it goes rather than gets repaired.

**Live objects:** both stored FrameJobs have `gpuCount: 0`, so neither exercises the rule. This change **cannot** reject anything that is currently accepted — it only stops rejecting.

**Files:**
- Modify: `internal/webhook/frame/v1alpha1/framejob_webhook.go`
- Modify: `internal/webhook/frame/v1alpha1/framejob_webhook_test.go`
- Modify: `api/frame/v1alpha1/framejob_types.go` (the doc comment on `FrameJobSpec`)
- Modify: `docs/roadmap.md` (the Phase B pre-freeze paragraph that defers this)

**Interfaces:**
- Consumes: nothing.
- Produces: `validateFrameJob(job *framev1alpha1.FrameJob) (admission.Warnings, error)` keeps its signature but now only ever returns a warning, never an error. Task 20 moves this function to `internal/webhook/frame/v1beta1/framejob_webhook.go` unchanged.

- [ ] **Step 1: Sweep for every writer of the constraint's inputs**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn 'serviceClass\|ServiceClass' --include='*.go' --include='*.ts' --include='*.tsx' --include='*.yaml' --include='*.md' \
  api internal cmd test config charts deploy src docs | grep -v node_modules | grep -v zz_generated
```

Expected: the four writers the inventory names — `config/samples/frame_v1alpha1_framejob.yaml` (`training`/`HIGH`/2 GPUs), `test/e2e/e2e_test.go:502-506` (`training`/`HIGH`/2 GPUs), `deploy/samples/test-cluster/workloads.yaml:129,134` (both `gpuCount: 0`) — plus the enum declarations. None of them is affected by *removing* a rejection.

- [ ] **Step 2: Delete the check**

In `internal/webhook/frame/v1alpha1/framejob_webhook.go`, replace `validateFrameJob` with:

```go
// validateFrameJob warns when the pipeline is not one Frame knows about. It
// deliberately does not hard-reject: `pipeline` names an Argo
// WorkflowTemplate that lives in the cluster and that Frame does not own, so
// enumerating other people's templates is not Frame's business (F9).
//
// The GPU/serviceClass:LOW rule that used to live here is gone (F8). It
// coupled two orthogonal properties — how much hardware a job wants, and how
// preemptible it is — and rested on a hardware fact with an expiry date (one
// Pascal P4). It was also only ever reachable for the three pipelines in
// knownPipelines, so it constrained nothing anyone had hit. Scheduling
// priority is SchedulingPolicy's and the frame-* PriorityClasses' job.
func validateFrameJob(job *framev1alpha1.FrameJob) (admission.Warnings, error) {
	if !knownPipelines[job.Spec.Pipeline] {
		known := make([]string, 0, len(knownPipelines))
		for k := range knownPipelines {
			known = append(known, k)
		}
		sort.Strings(known)
		return admission.Warnings{fmt.Sprintf("pipeline %q not in known list %v; ensure WorkflowTemplate exists", job.Spec.Pipeline, known)}, nil
	}
	return nil, nil
}
```

Add `"sort"` to the imports. (The map iteration order was nondeterministic, which made the warning text unstable across calls; sorting it costs nothing and makes the test below assertable.)

- [ ] **Step 3: Replace the test**

In `internal/webhook/frame/v1alpha1/framejob_webhook_test.go`, delete whichever `It`/subtest asserts the GPU/LOW rejection and add:

```go
func TestValidateFrameJobAdmitsGPUsAtLowServiceClass(t *testing.T) {
	// F8: the constraint was deleted deliberately. This test is the guard
	// against it being "restored" by someone reading the old doc comment in a
	// git blame.
	job := &framev1alpha1.FrameJob{
		Spec: framev1alpha1.FrameJobSpec{
			Pipeline:     "neura-training-dag",
			ServiceClass: "LOW",
			GPUCount:     2,
		},
	}
	warnings, err := validateFrameJob(job)
	if err != nil {
		t.Fatalf("expected a GPU job at LOW to be admitted, got error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for a known pipeline, got %v", warnings)
	}
}

func TestValidateFrameJobWarnsOnUnknownPipeline(t *testing.T) {
	job := &framev1alpha1.FrameJob{
		Spec: framev1alpha1.FrameJobSpec{Pipeline: "training", ServiceClass: "LOW", GPUCount: 2},
	}
	warnings, err := validateFrameJob(job)
	if err != nil {
		t.Fatalf("an unknown pipeline must warn, not reject: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %v", warnings)
	}
	if !strings.Contains(warnings[0], `pipeline "training" not in known list`) {
		t.Fatalf("warning did not name the pipeline: %q", warnings[0])
	}
}
```

Add `"strings"` and `"testing"` to the test file's imports if absent.

- [ ] **Step 4: Strip the deferral from the type doc comment**

In `api/frame/v1alpha1/framejob_types.go`, replace the whole comment block above `type FrameJobSpec struct` (currently lines 26–37, the paragraph beginning "The GPU/serviceClass:LOW conflict the webhook enforces") with:

```go
// FrameJobSpec defines the desired state of FrameJob.
//
// There is deliberately no rule coupling gpuCount to serviceClass. One was
// once enforced in the validating webhook for three pipeline names and
// nowhere else; it was removed in the v1beta1 freeze (F8) because it tied
// how much hardware a job wants to how preemptible it is, two orthogonal
// properties. Scheduling priority is spec.priority's, projected onto a
// frame-* PriorityClass by the controller.
type FrameJobSpec struct {
```

- [ ] **Step 5: Update the roadmap paragraph**

In `docs/roadmap.md`, in the "Pre-freeze cleanup" paragraph under Phase B, replace the sentence `This constraint's real state — it silently doesn't apply to most pipelines today — is now Phase B's to resolve, not this pass's.` with:

```
Phase B resolved it by deleting the constraint outright (F8) rather than
repairing the bypass — see docs/superpowers/specs/2026-08-09-frame-api-freeze-inventory.md.
```

- [ ] **Step 6: Test cycle**

```bash
cd /home/rmocq/Neura/.externals/frame
go test ./internal/webhook/frame/... -run 'FrameJob' -v
make test
make lint 2>&1 | grep framejob_webhook || echo "no new lint findings"
```

Expected: the two new tests pass; `make test` PASS; no new lint findings.

- [ ] **Step 7: Commit**

```bash
git add internal/webhook/frame/v1alpha1/framejob_webhook.go \
        internal/webhook/frame/v1alpha1/framejob_webhook_test.go \
        api/frame/v1alpha1/framejob_types.go \
        docs/roadmap.md
git commit -m "feat(api)!: delete the FrameJob GPU/serviceClass:LOW constraint

The rule only ever ran for the three pipelines in knownPipelines, so it
never applied to 'training' — this project's own sample and e2e pipeline.
Verified live: training+LOW+2 GPUs was admitted, neura-training-dag+LOW+2
GPUs was denied.

It is deleted rather than repaired (F8, owner decision): it couples how
much hardware a job wants to how preemptible it is, two orthogonal
properties, and the only thing that made it defensible was a hardware fact
with an expiry date. Scheduling priority is SchedulingPolicy's and the
frame-* PriorityClasses'.

Both stored FrameJobs have gpuCount: 0, so nothing that was accepted
before is rejected now — this only stops rejecting."
```

---

### Task 2: Make condition writes carry reason and message changes

`internal/controller/frame/helpers.go:27-38`'s `setCondition` replaces an existing condition **only when its `Status` differs**:

```go
for i, existing := range *conditions {
    if existing.Type == c.Type {
        if existing.Status != c.Status {
            (*conditions)[i] = c
        }
        return
    }
}
```

So a FrameNode moving `Provisioning → Degraded → Offline` — all `Ready=False` — never updates the condition's `Reason` or `Message` after the first write. Today that is a cosmetic staleness bug. From Task 18 onwards it is a **correctness** bug: `v1alpha1`'s `status.phase` is projected out of `Ready.Reason`, so a frozen reason freezes the projected phase. This task must land before Tasks 3 and 18.

**Live objects:** all three FrameNodes and all three FrameResourceQuotas carry conditions written through this function. Replacing it changes no schema and no condition *type* — only whether a reason/message update reaches the object.

**Files:**
- Modify: `internal/controller/frame/helpers.go`
- Modify: `internal/controller/frame/framejob_controller.go`, `framenode_controller.go`, `frameresourcequota_controller.go`, `schedulingpolicy_controller.go`, `talosmachineconfig_controller.go`, `talosupgrade_controller.go`
- Create: `internal/controller/frame/helpers_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `setCondition` is **deleted**. Call sites use `meta.SetStatusCondition(conditions *[]metav1.Condition, newCondition metav1.Condition)` from `k8s.io/apimachinery/pkg/api/meta` directly. `conditionStatus(ok bool) metav1.ConditionStatus` and `const conditionTypeReady = "Ready"` in `internal/controller/frame/helpers.go` are unchanged and every later task relies on both names.
- Produces: `func readyReason(conditions []metav1.Condition) string` in `internal/controller/frame/helpers.go` — returns the `Ready` condition's `Reason`, or `""` if there is no `Ready` condition. Tasks 3 and 13 use it as the controllers' state-machine input once `status.phase` is gone.

- [ ] **Step 1: Rewrite the helper file**

Replace the body of `internal/controller/frame/helpers.go` below the licence header with:

```go
package controller

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// conditionTypeReady is the standard status.conditions[].type this package's
// controllers use to report overall reconcile health.
const conditionTypeReady = "Ready"

// readyReason is the Reason of the Ready condition, or "" when there is none.
//
// This is the controllers' state-machine input now that status.phase is gone
// (F2). It is also exactly what api/frame/v1alpha1's conversion projects back
// out as the legacy phase, so a reason that stops tracking reality here shows
// up as a wrong phase to every v1alpha1 client.
func readyReason(conditions []metav1.Condition) string {
	if c := meta.FindStatusCondition(conditions, conditionTypeReady); c != nil {
		return c.Reason
	}
	return ""
}

func conditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}
```

`setCondition` is gone. `meta.SetStatusCondition` replaces it: it updates `Reason`, `Message` and `ObservedGeneration` on every call and only moves `LastTransitionTime` when `Status` actually changes — which is the behaviour the old helper was trying and failing to express.

- [ ] **Step 2: Repoint the call sites**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rln 'setCondition(' --include='*.go' internal/ \
  | xargs sed -i 's/setCondition(&\(.*\)\.Status\.Conditions,/meta.SetStatusCondition(\&\1.Status.Conditions,/g'
grep -rn 'setCondition(' --include='*.go' internal/ || echo "no callers left"
```

Then add `"k8s.io/apimachinery/pkg/api/meta"` to the import block of every file the sed touched, and run `make fmt`. `internal/controller/services/` already uses `meta.SetStatusCondition` and needs no change.

- [ ] **Step 3: Drop `LastTransitionTime` from the call sites that set it**

`meta.SetStatusCondition` fills `LastTransitionTime` itself and ignores a supplied one only when the status is unchanged. Search for any caller that sets it explicitly:

```bash
grep -rn 'LastTransitionTime' --include='*.go' internal/ api/ | grep -v zz_generated
```

Expected after Step 1: nothing in `internal/controller/frame/`. If a controller sets it, delete that line — the helper owned it before and `meta.SetStatusCondition` owns it now.

- [ ] **Step 4: Add the regression test**

Create `internal/controller/frame/helpers_test.go`:

```go
package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The old setCondition replaced an existing condition only when Status
// differed, so a FrameNode going Provisioning -> Degraded -> Offline (all
// Ready=False) kept the first Reason forever. That is what projects the
// legacy v1alpha1 status.phase, so a frozen Reason is a frozen phase.
func TestReasonUpdateReachesTheConditionEvenWhenStatusIsUnchanged(t *testing.T) {
	var conds []metav1.Condition

	meta.SetStatusCondition(&conds, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Provisioning",
		Message:            "Waiting to apply config",
		ObservedGeneration: 1,
	})
	if got := readyReason(conds); got != "Provisioning" {
		t.Fatalf("readyReason = %q, want Provisioning", got)
	}

	meta.SetStatusCondition(&conds, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Offline",
		Message:            "Node reports NotReady",
		ObservedGeneration: 2,
	})
	if got := readyReason(conds); got != "Offline" {
		t.Fatalf("readyReason = %q after a reason-only change, want Offline", got)
	}
	if len(conds) != 1 {
		t.Fatalf("expected one Ready condition, got %d", len(conds))
	}
	if conds[0].ObservedGeneration != 2 {
		t.Fatalf("observedGeneration = %d, want 2", conds[0].ObservedGeneration)
	}
	if conds[0].Message != "Node reports NotReady" {
		t.Fatalf("message = %q, want the second message", conds[0].Message)
	}
}

func TestReadyReasonIsEmptyWithoutAReadyCondition(t *testing.T) {
	conds := []metav1.Condition{{
		Type:   "Submitted",
		Status: metav1.ConditionTrue,
		Reason: "WorkflowCreated",
	}}
	if got := readyReason(conds); got != "" {
		t.Fatalf("readyReason = %q, want empty", got)
	}
}
```

- [ ] **Step 5: Test cycle**

```bash
go test ./internal/controller/frame/... -run 'TestReasonUpdate|TestReadyReason' -v
make test
make lint 2>&1 | grep -E 'helpers|controller/frame' || echo "no new lint findings"
```

Expected: both tests pass; `make test` PASS with `internal/controller` coverage at or above its previous value.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/frame/helpers.go internal/controller/frame/helpers_test.go \
        internal/controller/frame/framejob_controller.go \
        internal/controller/frame/framenode_controller.go \
        internal/controller/frame/frameresourcequota_controller.go \
        internal/controller/frame/schedulingpolicy_controller.go \
        internal/controller/frame/talosmachineconfig_controller.go \
        internal/controller/frame/talosupgrade_controller.go
git commit -m "fix(controller): let a condition's reason and message change

setCondition replaced an existing condition only when its Status differed,
so a FrameNode moving Provisioning -> Degraded -> Offline (all Ready=False)
kept the first reason and message forever.

Cosmetic today; load-bearing from the freeze on, because v1alpha1's
status.phase becomes a projection out of Ready.Reason and a frozen reason
would be a frozen phase. meta.SetStatusCondition does the right thing:
reason, message and observedGeneration always update, LastTransitionTime
moves only on a real status transition.

Adds readyReason(), the controllers' state-machine input once status.phase
is gone."
```

---

### Task 3: Make `FrameJob`'s condition a real `Ready` (F3)

`framejob_controller.go:105-115` sets a condition of type `Submitted` once, when the ArgoWorkflow is created, and never touches the conditions again; the later phase transitions at `:124-147` update `status.phase` only. Both live FrameJobs show it: `phase: Completed`, condition `Submitted=True reason=WorkflowCreated`. Every other condition-bearing kind uses `conditionTypeReady`.

F3 option 1: rename **and** make it track the real state. Option 2 (rename without fixing the update) is explicitly worse than doing nothing and must not be done. This task is a prerequisite for F2's phase projection: after it, `Ready.Reason` carries the FrameJob phase token, which is the only thing conversion can reconstruct `status.phase` from.

**Live objects:** the two stored FrameJobs carry a `Submitted` condition. After this task their next reconcile adds a `Ready` condition alongside it; the stale `Submitted` condition is left in place rather than deleted, because deleting a condition type from another controller's write path is not something a status patch can do cleanly and both objects are terminal. Task 24 documents `Submitted` as removed from the contract.

**Files:**
- Modify: `internal/controller/frame/framejob_controller.go`
- Modify: `internal/controller/frame/framejob_controller_test.go`
- Modify: `docs/crd-reference.md` (the FrameJob section's condition sentence)

**Interfaces:**
- Consumes: `conditionTypeReady`, `readyReason`, `conditionStatus` from Task 2.
- Produces: in `internal/controller/frame/framejob_controller.go` —
  - `const jobPhaseSubmitted = "Submitted"`, `jobPhaseRunning`, `jobPhaseSuspended`, `jobPhaseCompleted`, `jobPhaseFailed` (the five reason tokens; `jobPhaseRunning`, `jobPhaseCompleted`, `jobPhaseFailed` already exist)
  - `func setJobReady(job *framev1alpha1.FrameJob, phase, message string)` — writes the `Ready` condition with `Reason: phase`
  - The invariant every later task depends on: **`Ready.Reason` on a FrameJob is always one of the five phase tokens, and `Ready.Status` is `True` only for `Completed`.**

- [ ] **Step 1: Add the phase constants and the setter**

In `internal/controller/frame/framejob_controller.go`, extend the existing phase const block (currently `jobPhaseCompleted`/`jobPhaseRunning`/`jobPhaseFailed` at lines 45-50):

```go
// FrameJob phases. They are the Reason on the Ready condition, and the value
// api/frame/v1alpha1's conversion projects back out as the legacy
// status.phase. Do not write a reason here that is not one of these.
const (
	jobPhaseSubmitted = "Submitted"
	jobPhaseRunning   = "Running"
	jobPhaseSuspended = "Suspended"
	jobPhaseCompleted = "Completed"
	jobPhaseFailed    = "Failed"
)

// setJobReady writes the one condition a FrameJob carries. Ready is True only
// on Completed: a job that failed an hour ago must not read as healthy, which
// is precisely what the old write-once Submitted=True condition did (F3).
func setJobReady(job *framev1alpha1.FrameJob, phase, message string) {
	meta.SetStatusCondition(&job.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             conditionStatus(phase == jobPhaseCompleted),
		Reason:             phase,
		Message:            message,
		ObservedGeneration: job.Generation,
	})
}
```

Add `"k8s.io/apimachinery/pkg/api/meta"` to the imports.

- [ ] **Step 2: Write `Ready` on creation instead of `Submitted`**

In `Reconcile`, replace the block at the workflow-creation branch (currently lines 104-116) with:

```go
		patch := client.MergeFrom(job.DeepCopy())
		job.Status.Phase = jobPhaseSubmitted
		job.Status.ArgoWorkflowName = job.Name
		now := metav1.Now()
		job.Status.StartTime = &now
		setJobReady(&job, jobPhaseSubmitted, fmt.Sprintf("ArgoWorkflow %s/%s created", ns, job.Name))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Patch(ctx, &job, patch)
```

(`job.Status.Phase` still exists in `v1alpha1` at this point and is still written — Task 12 removes the field from the hub type and Task 20 removes this line. Writing both here keeps this task green on its own.)

- [ ] **Step 3: Write `Ready` on every phase transition**

Replace the transition block (currently lines 124-147) with:

```go
	phase := workflowPhase(existing, job.Spec.Suspended)
	if readyReason(job.Status.Conditions) != phase || job.Status.Phase != phase {
		patch := client.MergeFrom(job.DeepCopy())
		job.Status.Phase = phase
		job.Status.Message = workflowMessage(existing)
		if phase == jobPhaseCompleted || phase == jobPhaseFailed {
			now := metav1.Now()
			job.Status.CompletionTime = &now
		}
		setJobReady(&job, phase, job.Status.Message)
		if err := r.Status().Patch(ctx, &job, patch); err != nil {
			return ctrl.Result{}, err
		}
		eventType := corev1.EventTypeNormal
		if phase == jobPhaseFailed {
			eventType = corev1.EventTypeWarning
		}
		r.Recorder.Event(&job, eventType, "Phase"+phase, fmt.Sprintf("Job phase changed to %s", phase))
		switch phase {
		case jobPhaseCompleted:
			frameJobCompleted.Inc()
		case jobPhaseFailed:
			frameJobFailed.Inc()
		}
	}
```

The guard now reads the condition as well as the field, so the transition still fires for the two stored FrameJobs whose `phase` already matches but whose `Ready` condition does not exist yet.

- [ ] **Step 4: Make `workflowPhase` return the constants**

In `workflowPhase` (lines 242-257) replace the bare `"Suspended"` and `"Submitted"` string literals with `jobPhaseSuspended` and `jobPhaseSubmitted`. No behaviour change; it makes the "reason is always one of the five" invariant checkable by grep.

- [ ] **Step 5: Add the controller tests**

Append to `internal/controller/frame/framejob_controller_test.go`, inside the existing `Describe`:

```go
	It("writes a Ready condition that tracks the workflow, not a write-once Submitted", func() {
		ctx := context.Background()
		name := "cond-tracking"

		job := &framev1alpha1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       framev1alpha1.FrameJobSpec{Pipeline: "neura-training-dag", Namespace: "default"},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, job)
		})

		reconciler := &FrameJobReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		key := types.NamespacedName{Name: name, Namespace: "default"}

		// Pass 1 adds the finalizer, pass 2 creates the Workflow.
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		fetched := &framev1alpha1.FrameJob{}
		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		ready := meta.FindStatusCondition(fetched.Status.Conditions, conditionTypeReady)
		Expect(ready).NotTo(BeNil(), "a FrameJob must carry a Ready condition")
		Expect(ready.Reason).To(Equal(jobPhaseSubmitted))
		Expect(ready.Status).To(Equal(metav1.ConditionFalse), "Submitted is not Ready")
		Expect(meta.FindStatusCondition(fetched.Status.Conditions, "Submitted")).
			To(BeNil(), "the Submitted condition type is gone (F3)")

		// Drive the backing Workflow to Failed and reconcile again.
		wf := &unstructured.Unstructured{}
		wf.SetGroupVersionKind(argoWorkflowGVK)
		Expect(k8sClient.Get(ctx, key, wf)).To(Succeed())
		Expect(unstructured.SetNestedField(wf.Object, "Failed", "status", "phase")).To(Succeed())
		Expect(k8sClient.Update(ctx, wf)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		ready = meta.FindStatusCondition(fetched.Status.Conditions, conditionTypeReady)
		Expect(ready.Reason).To(Equal(jobPhaseFailed),
			"the Ready condition must follow the workflow, not stay at its first value")
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
	})
```

Ensure the test file imports `"k8s.io/apimachinery/pkg/api/meta"`, `"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"`, `"k8s.io/client-go/tools/record"`, `"k8s.io/apimachinery/pkg/types"` and `ctrl "sigs.k8s.io/controller-runtime"`.

- [ ] **Step 6: Update the CRD reference**

In `docs/crd-reference.md`, in the `## FrameJob` section, replace the sentence describing the `Submitted` condition with:

```
**Conditions:** `Ready` only. Its `reason` is the job phase — one of
`Submitted`, `Running`, `Suspended`, `Completed`, `Failed` — and its `status`
is `True` only for `Completed`. The `Submitted` condition type this kind used
to write once and never update is gone (F3).
```

- [ ] **Step 7: Test cycle**

```bash
go test ./internal/controller/frame/... -v -ginkgo.focus='Ready condition that tracks'
make test
make lint 2>&1 | grep framejob_controller || echo "no new lint findings"
```

- [ ] **Step 8: Commit**

```bash
git add internal/controller/frame/framejob_controller.go \
        internal/controller/frame/framejob_controller_test.go \
        docs/crd-reference.md
git commit -m "fix(controller): give FrameJob a Ready condition that tracks the job

The Submitted condition was written once, when the ArgoWorkflow was
created, and never touched again — so a generic 'is this healthy' client
found no Ready condition at all and a Submitted=True condition on a job
that failed an hour ago. Both live FrameJobs show exactly that.

Ready now carries the phase as its reason and is True only on Completed.
The Submitted condition type is gone rather than renamed-and-left-stale:
a permanently-True Ready on a failed job would be worse than an honestly
named Submitted."
```

---

### Task 4: Rename the node-label projection and stop writing empty values (F12)

`framenode_controller.go:221-232` writes four labels onto the corresponding `corev1.Node`, unconditionally and including empty values, and `reconcileDelete` at `:271-275` removes them. Two problems the freeze must settle:

- **`topology.kubernetes.io/rack` is an invented key under a Kubernetes-reserved prefix.** The well-known keys there are `zone` and `region`; `rack` is not one. Frame already owns `frame.plume-labs.io/` and uses it for three other keys.
- **Empty values are written**, so "unset" and "explicitly empty" are indistinguishable to a selector.

And one thing the freeze must **document rather than change**: `frame.plume-labs.io/service-class` means two different things depending on what it is attached to. On a `Node` it is the tier of hardware the FrameNode controller classified (`framenode_controller.go:226`), read by the inference provider's `NodeSelector` (`inference.go:51,541-543`) and by the FrameJob controller's Workflow labels (`framejob_controller.go:196`). On a `Namespace` it selects which namespaces a `FrameResourceQuota` projects into (`frameresourcequota_controller.go:76,146`). Renaming either breaks the other's readers silently, so both stay — the overloading is recorded, not fixed.

**This task must land before Task 13** (F4 drops `""` from FrameNode's `serviceClass` enum). Reversed, a FrameNode holding `serviceClass: ""` would convert into a `v1beta1` object whose enum forbids the value it holds, and the label projection is what would surface it — as a node-patch failure, not a validation error.

**Live objects:** three FrameNodes, on one test cluster, all with non-empty `serviceClass`; racks `rack-01` and `local`. The rename costs a one-time relabel of three nodes, done in Step 5. After a `v1.0.0` chart ships it would cost a migration note and a broken selector for anyone who wrote one.

**Files:**
- Modify: `internal/controller/frame/framenode_controller.go`
- Modify: `internal/controller/frame/framenode_controller_test.go`
- Modify: `test/e2e/e2e_test.go` (the FrameNode spec asserts on the labels)
- Modify: `docs/crd-reference.md` (new "Node labels Frame writes" section)

**Interfaces:**
- Consumes: nothing.
- Produces: in `internal/controller/frame/framenode_controller.go` —
  - `const nodeLabelRack = "frame.plume-labs.io/rack"`
  - `const nodeLabelZone = "topology.kubernetes.io/zone"`
  - `const nodeLabelServiceClass = "frame.plume-labs.io/service-class"`
  - `const nodeLabelRole = "frame.plume-labs.io/role"`
  - `const nodeLabelRDMA = "frame.plume-labs.io/rdma"`
  - `var frameNodeLabels = []string{nodeLabelRack, nodeLabelZone, nodeLabelServiceClass, nodeLabelRole, nodeLabelRDMA}`
  - `func applyNodeLabel(labels map[string]string, key, value string)` — sets the key when `value != ""`, deletes it otherwise.

- [ ] **Step 1: Sweep for every reader and writer of the four keys**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn 'topology.kubernetes.io/rack\|topology.kubernetes.io/zone\|frame.plume-labs.io/service-class\|frame.plume-labs.io/role\|frame.plume-labs.io/rdma' \
  --include='*.go' --include='*.ts' --include='*.tsx' --include='*.yaml' --include='*.yml' --include='*.md' --include='*.tpl' \
  api internal cmd test config charts deploy src docs hack | grep -v node_modules
```

Expected hits, all of which must be accounted for before Step 2:
- `internal/controller/frame/framenode_controller.go` — the writer and the delete path
- `internal/controller/frame/frameresourcequota_controller.go:76,146` — `frame.plume-labs.io/service-class` **on Namespaces**, a different meaning; leave alone
- `internal/controller/frame/framejob_controller.go:196` — the same key as a Workflow label; leave alone
- `internal/services/provider/inference/inference.go:51` — `serviceClassLabel`, the `NodeSelector`; leave alone
- `test/e2e/e2e_test.go` — the FrameNode spec and the `BeforeAll` namespace label
- any `deploy/` or `docs/` mention of `topology.kubernetes.io/rack`

- [ ] **Step 2: Add the constants and the setter**

In `internal/controller/frame/framenode_controller.go`, add above `reconcileOnline`:

```go
// The labels Frame projects from a FrameNode onto its corev1.Node. These are
// API, not an implementation detail: the inference provider's NodeSelector
// and the FrameJob controller's Workflow labels both read
// nodeLabelServiceClass, so renaming one of these silently unschedules
// everything that selects on it (F12).
//
// rack is under frame.plume-labs.io, not topology.kubernetes.io: the
// well-known keys in the kubernetes.io namespace are zone and region, and
// rack is not one of them — that prefix is reserved for upstream.
//
// A caution for anyone reading nodeLabelServiceClass and generalising: the
// same key means something else on a Namespace, where
// FrameResourceQuota uses it to select which namespaces a quota projects
// into. Two meanings, one key, distinguished only by what it is attached to.
const (
	nodeLabelRack         = "frame.plume-labs.io/rack"
	nodeLabelZone         = "topology.kubernetes.io/zone"
	nodeLabelServiceClass = "frame.plume-labs.io/service-class"
	nodeLabelRole         = "frame.plume-labs.io/role"
	nodeLabelRDMA         = "frame.plume-labs.io/rdma"
)

// frameNodeLabels is every key reconcileDelete strips. Keeping the list in
// one place is what stops a fifth label being added to the write path and
// forgotten in the delete path.
var frameNodeLabels = []string{
	nodeLabelRack, nodeLabelZone, nodeLabelServiceClass, nodeLabelRole, nodeLabelRDMA,
}

// applyNodeLabel sets key when value is non-empty and removes it otherwise.
// Writing an empty value is legal but makes "unclassified" and "explicitly
// empty" indistinguishable to a selector, which is not a distinction Frame
// wants to have to explain.
func applyNodeLabel(labels map[string]string, key, value string) {
	if value == "" {
		delete(labels, key)
		return
	}
	labels[key] = value
}
```

- [ ] **Step 3: Rewrite the write path**

In `reconcileOnline`, replace lines 220-233 with:

```go
	base := node.DeepCopy()
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	applyNodeLabel(node.Labels, nodeLabelRack, fn.Spec.Rack)
	applyNodeLabel(node.Labels, nodeLabelZone, fn.Spec.Zone)
	applyNodeLabel(node.Labels, nodeLabelServiceClass, fn.Spec.ServiceClass)
	applyNodeLabel(node.Labels, nodeLabelRole, fn.Spec.Role)
	rdma := ""
	if fn.Spec.RDMAInterface != "" {
		rdma = "true"
	}
	applyNodeLabel(node.Labels, nodeLabelRDMA, rdma)
	// The old topology.kubernetes.io/rack key is removed here as well as in
	// reconcileDelete, so an existing node relabels itself on its next
	// reconcile rather than carrying both keys until someone deletes the
	// FrameNode.
	delete(node.Labels, "topology.kubernetes.io/rack")
	if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching node labels: %w", err)
	}
```

> `client.MergeFrom` produces a JSON merge patch, in which a deleted map key becomes an explicit `null` — so `delete()` on the mutated copy really does remove the label from the Node, it does not merely omit it.

- [ ] **Step 4: Rewrite the delete path**

Replace lines 268-279 of `reconcileDelete` with:

```go
	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err == nil {
		base := node.DeepCopy()
		for _, key := range frameNodeLabels {
			delete(node.Labels, key)
		}
		// The pre-freeze key, for nodes labelled before F12.
		delete(node.Labels, "topology.kubernetes.io/rack")
		if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}
```

- [ ] **Step 5: Relabel the three live nodes**

Run once, against the test cluster, after the operator carrying this change is deployed. Until then the controller does it itself on the next reconcile (Step 3's `delete`), so this is belt-and-braces for nodes whose FrameNode is not reconciling:

```bash
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml
for n in $(kubectl get nodes -o name); do
  rack=$(kubectl get "$n" -o jsonpath='{.metadata.labels.topology\.kubernetes\.io/rack}')
  if [ -n "$rack" ]; then
    kubectl label --overwrite "$n" "frame.plume-labs.io/rack=$rack"
    kubectl label "$n" 'topology.kubernetes.io/rack-'
  fi
done
kubectl get nodes -L frame.plume-labs.io/rack,topology.kubernetes.io/zone,frame.plume-labs.io/service-class
```

Expected: three nodes, `frame.plume-labs.io/rack` populated (`rack-01`, `local`), `topology.kubernetes.io/rack` gone.

- [ ] **Step 6: Add the controller test**

Append to `internal/controller/frame/framenode_controller_test.go`, inside the existing `Describe`:

```go
	It("projects the frame-prefixed rack label and skips empty values", func() {
		ctx := context.Background()

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "label-projection-node",
				Labels: map[string]string{"topology.kubernetes.io/rack": "stale"},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		fn := &framev1alpha1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: "label-projection", Namespace: "default"},
			Spec: framev1alpha1.FrameNodeSpec{
				IP:           "127.0.0.1",
				Role:         "worker",
				Hostname:     "label-projection-node",
				Rack:         "rack-07",
				ServiceClass: "HIGH",
				// Zone and RDMAInterface deliberately left empty.
			},
		}
		Expect(k8sClient.Create(ctx, fn)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, fn) })

		reconciler := &FrameNodeReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		key := types.NamespacedName{Name: "label-projection", Namespace: "default"}
		_, err := reconciler.reconcileOnline(ctx, fn)
		Expect(err).NotTo(HaveOccurred())
		_ = key

		fetched := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "label-projection-node"}, fetched)).To(Succeed())
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelRack, "rack-07"))
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelServiceClass, "HIGH"))
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelRole, "worker"))
		Expect(fetched.Labels).NotTo(HaveKey(nodeLabelZone), "an empty zone must not be written")
		Expect(fetched.Labels).NotTo(HaveKey(nodeLabelRDMA), "no RDMA interface, no label")
		Expect(fetched.Labels).NotTo(HaveKey("topology.kubernetes.io/rack"),
			"the reserved-prefix key must be cleaned up on reconcile")
	})
```

- [ ] **Step 7: Update the e2e spec**

In `test/e2e/e2e_test.go`, in the `syncs a FrameNode onto the real Kubernetes Node` spec, after the phase assertions add:

```go
			By("checking the node carries the frame-prefixed rack label and no reserved-prefix one")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "node", nodeName,
					"-o", `jsonpath={.metadata.labels.frame\.plume-labs\.io/rack}`))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("rack-e2e"))

				out, err = utils.Run(exec.Command("kubectl", "get", "node", nodeName,
					"-o", `jsonpath={.metadata.labels.topology\.kubernetes\.io/rack}`))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(BeEmpty(), "topology.kubernetes.io/ is reserved for upstream keys")
			}).Should(Succeed())
```

- [ ] **Step 8: Document the label block**

Add to `docs/crd-reference.md`, immediately after the `## FrameNode` section:

```markdown
### Node labels Frame writes

The FrameNode controller projects five labels onto the corresponding
`corev1.Node`, and strips them when the FrameNode is deleted. **These are
API.** Two other components select on them, so renaming one unschedules
running workloads at runtime with no admission-time error to warn anyone.

| Key | Source | Read by |
|---|---|---|
| `frame.plume-labs.io/rack` | `spec.rack` | operators; topology-aware placement |
| `topology.kubernetes.io/zone` | `spec.zone` | Kubernetes' own well-known zone key |
| `frame.plume-labs.io/service-class` | `spec.serviceClass` | the inference provider's `NodeSelector`; the FrameJob controller's Workflow labels |
| `frame.plume-labs.io/role` | `spec.role` | operators |
| `frame.plume-labs.io/rdma` | `"true"` when `spec.rdmaInterface` is set | operators |

Empty values are not written: a label that is absent means "unclassified",
and there is no separate "explicitly empty" state.

`rack` lives under `frame.plume-labs.io/`, not `topology.kubernetes.io/`.
The well-known keys in the `kubernetes.io` namespace are `zone` and `region`;
`rack` is not one of them and that prefix is reserved for upstream use. Frame
wrote `topology.kubernetes.io/rack` before `v1beta1`; the controller removes
that key on every reconcile so an existing node relabels itself.

**One key, two meanings.** `frame.plume-labs.io/service-class` on a **Node**
is the tier of hardware the FrameNode controller classified. The same key on
a **Namespace** selects which namespaces a `FrameResourceQuota` projects
into. They are unrelated; the shared key is historical and is frozen as-is
because renaming either breaks the other's readers silently.
```

- [ ] **Step 9: Test cycle**

```bash
go test ./internal/controller/frame/... -v -ginkgo.focus='frame-prefixed rack label'
make test
make lint 2>&1 | grep framenode_controller || echo "no new lint findings"
```

Expected: PASS. Do not run `make test-e2e` here unless a Kind cluster is available; Task 21 owns the e2e run.

- [ ] **Step 10: Commit**

```bash
git add internal/controller/frame/framenode_controller.go \
        internal/controller/frame/framenode_controller_test.go \
        test/e2e/e2e_test.go docs/crd-reference.md
git commit -m "feat(controller)!: move the rack label off the reserved prefix

topology.kubernetes.io/rack is an invented key under a namespace reserved
for upstream — the well-known keys there are zone and region. It becomes
frame.plume-labs.io/rack, which Frame already owns and already uses for
three other keys. The controller deletes the old key on every reconcile so
existing nodes relabel themselves; three nodes on one test cluster is the
whole cost, and after a published chart it would be a broken selector with
no admission-time error.

Empty values are no longer written, so an absent label means unclassified
and there is no indistinguishable 'explicitly empty'.

Also documents the five keys as API in docs/crd-reference.md, including the
fact that frame.plume-labs.io/service-class means one thing on a Node and
another on a Namespace. Both stay: renaming either breaks the other's
readers silently."
```

---

### Task 5: Derive `FrameService`'s scheduling priority from `serviceClass` (F10)

**Owner decision, binding, and a Phase B deliverable rather than an option** (roadmap `docs/roadmap.md:121`, proven by S1). `FrameJob` has the lever — `jobPriorityClass` (`framejob_controller.go:226-240`) maps `spec.priority` onto `frame-critical`/`frame-high`/`frame-medium`/`frame-low`, and `SchedulingPolicy`'s `reconcilePriorityClass` creates those objects. The inference provider sets no `priorityClassName` anywhere, so an instance always runs at the implicit default of 0 with no field an operator could set.

`spec.serviceClass` today means "which `FrameResourceQuota` and node pool this instance's workloads belong to". After this task it *also* means "how preemptible this instance is". That second meaning is deliberate and is the owner decision recorded in Global Constraints.

**This task must land before Task 17** so the semantics F4 freezes are the final ones.

**Live objects:** zero FrameServices exist anywhere, so this is free — no running instance changes priority under anyone.

**Files:**
- Create: `internal/scheduling/priority.go`
- Create: `internal/scheduling/priority_test.go`
- Modify: `internal/controller/frame/framejob_controller.go` (delete `jobPriorityClass`, call the shared function)
- Modify: `internal/services/provider/inference/inference.go`
- Modify: `internal/services/provider/inference/reconcile_test.go`
- Modify: `docs/crd-reference.md` (FrameService section)

**Interfaces:**
- Consumes: nothing.
- Produces: package `github.com/rmocq/frame/internal/scheduling` with exactly two exported functions:
  - `func PriorityClassForJobPriority(priority string) string` — `critical|high|medium|low` → `frame-critical|frame-high|frame-medium|frame-low`, `""` for anything else.
  - `func PriorityClassForServiceClass(serviceClass string) string` — `HIGH|MEDIUM|LOW` → `frame-high|frame-medium|frame-low`, `""` for anything else.
  Tasks 12, 17 and 20 both import this package; nothing else may re-derive the mapping.

- [ ] **Step 1: Create the shared mapping**

```bash
mkdir -p internal/scheduling
```

`internal/scheduling/priority.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package scheduling holds the one mapping from a Frame tier onto the
// frame-* PriorityClass objects SchedulingPolicy's controller creates.
//
// It lives outside internal/controller so that both the FrameJob controller
// and the inference provider can import it without the provider depending on
// a controller package — the provider is deliberately reachable only through
// the registry.
package scheduling

// The PriorityClass names SchedulingPolicy's reconcilePriorityClass creates.
// Nothing else may name a PriorityClass: Frame owns placement, and a spec
// field naming an arbitrary (possibly system) PriorityClass would break that.
const (
	PriorityClassCritical = "frame-critical"
	PriorityClassHigh     = "frame-high"
	PriorityClassMedium   = "frame-medium"
	PriorityClassLow      = "frame-low"
)

// PriorityClassForJobPriority maps FrameJob.spec.priority onto a
// Frame-managed PriorityClass. An unrecognised value — including the empty
// string — yields "", meaning "set no priorityClassName", which leaves the
// workload at the cluster's implicit default.
func PriorityClassForJobPriority(priority string) string {
	switch priority {
	case "critical":
		return PriorityClassCritical
	case "high":
		return PriorityClassHigh
	case "medium":
		return PriorityClassMedium
	case "low":
		return PriorityClassLow
	default:
		return ""
	}
}

// PriorityClassForServiceClass maps FrameService.spec.serviceClass onto a
// Frame-managed PriorityClass.
//
// A FrameService has no separate spec.priority, unlike a FrameJob, and that
// is deliberate (F10). A job's resource tier and its scheduling urgency are
// separable — a HIGH-tier nightly batch can legitimately be low-priority. A
// long-lived service instance has no such separation: its tier is its
// urgency, for its whole lifetime. Adding spec.priority would duplicate
// serviceClass with no case where the two would differ, and then they could
// disagree.
//
// There is no critical tier: serviceClass has three values and inventing a
// fourth here would put an instance above every job on the cluster.
func PriorityClassForServiceClass(serviceClass string) string {
	switch serviceClass {
	case "HIGH":
		return PriorityClassHigh
	case "MEDIUM":
		return PriorityClassMedium
	case "LOW":
		return PriorityClassLow
	default:
		return ""
	}
}
```

- [ ] **Step 2: Test the mapping**

`internal/scheduling/priority_test.go`:

```go
package scheduling

import "testing"

func TestPriorityClassForJobPriority(t *testing.T) {
	cases := map[string]string{
		"critical": PriorityClassCritical,
		"high":     PriorityClassHigh,
		"medium":   PriorityClassMedium,
		"low":      PriorityClassLow,
		"":         "",
		"HIGH":     "", // the job enum is lower-case; the service enum is upper
		"nonsense": "",
	}
	for in, want := range cases {
		if got := PriorityClassForJobPriority(in); got != want {
			t.Errorf("PriorityClassForJobPriority(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPriorityClassForServiceClass(t *testing.T) {
	cases := map[string]string{
		"HIGH":     PriorityClassHigh,
		"MEDIUM":   PriorityClassMedium,
		"LOW":      PriorityClassLow,
		"":         "",
		"high":     "", // the service enum is upper-case
		"CRITICAL": "", // deliberately not a serviceClass value
	}
	for in, want := range cases {
		if got := PriorityClassForServiceClass(in); got != want {
			t.Errorf("PriorityClassForServiceClass(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 3: Hoist the FrameJob mapping**

In `internal/controller/frame/framejob_controller.go`, delete the whole `jobPriorityClass` function (lines 226-240) and change its one call site in `buildWorkflow` (line 208) to:

```go
	if pc := scheduling.PriorityClassForJobPriority(job.Spec.Priority); pc != "" {
		spec["priorityClassName"] = pc
	}
```

Add the import `"github.com/rmocq/frame/internal/scheduling"`.

- [ ] **Step 4: Set the priority class on the inference Deployment**

In `internal/services/provider/inference/inference.go`, inside the `CreateOrUpdate` mutate function for the Deployment, immediately after the `NodeSelector` assignment (currently lines 541-543), add:

```go
			// Scheduling priority is derived from serviceClass, not from a
			// field of its own (F10): a long-lived instance's tier is its
			// urgency. The mapping is shared with the FrameJob controller so
			// the two cannot drift, and it only ever names a PriorityClass
			// SchedulingPolicy's controller created — never a system one.
			//
			// PriorityClassName is a scalar on the PodSpec, so setContainer's
			// partial-update discipline does not apply: it is assigned
			// wholesale. An unrecognised serviceClass yields "", which leaves
			// the pod at the cluster's implicit default rather than failing.
			deployment.Spec.Template.Spec.PriorityClassName =
				scheduling.PriorityClassForServiceClass(svc.Spec.ServiceClass)
```

Add the import `"github.com/rmocq/frame/internal/scheduling"`.

- [ ] **Step 5: Test the provider change**

Append to `internal/services/provider/inference/reconcile_test.go`, alongside the existing Deployment assertions:

```go
func TestReconcileSetsPriorityClassFromServiceClass(t *testing.T) {
	for serviceClass, want := range map[string]string{
		"HIGH":   "frame-high",
		"MEDIUM": "frame-medium",
		"LOW":    "frame-low",
	} {
		t.Run(serviceClass, func(t *testing.T) {
			// newTestProvider and newTestService are the helpers this file
			// already uses for every other Deployment assertion; reuse them
			// rather than building a second fixture shape.
			p, c := newTestProvider(t)
			svc := newTestService()
			svc.Spec.ServiceClass = serviceClass

			if _, err := p.Reconcile(context.Background(), svc); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			var d appsv1.Deployment
			key := types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace}
			if err := c.Get(context.Background(), key, &d); err != nil {
				t.Fatalf("get Deployment: %v", err)
			}
			if got := d.Spec.Template.Spec.PriorityClassName; got != want {
				t.Fatalf("priorityClassName = %q, want %q", got, want)
			}
		})
	}
}
```

> If `newTestProvider` / `newTestService` are named differently in that file, use whatever the neighbouring Deployment tests use — do not introduce a second fixture helper. Confirm with `grep -n 'func new.*Test\|func setup' internal/services/provider/inference/reconcile_test.go`.

- [ ] **Step 6: Document the derivation**

In `docs/crd-reference.md`, in the `## FrameService` section, add after the `serviceClass` description:

```
`serviceClass` carries two meanings, deliberately. It selects the node pool
and the `FrameResourceQuota` the instance's workloads belong to, **and** it
determines the instance's scheduling priority: `HIGH`/`MEDIUM`/`LOW` map onto
the `frame-high`/`frame-medium`/`frame-low` PriorityClasses that
`SchedulingPolicy`'s controller creates. There is no `spec.priority` on a
FrameService and no `spec.priorityClassName`: a long-lived instance's tier is
its urgency, and letting a user name an arbitrary PriorityClass would break
the invariant that Frame owns placement. If a HIGH-tier instance ever needs
to be evicted before a MEDIUM one, that is a `v1beta2` problem (F10).
```

- [ ] **Step 7: Test cycle**

```bash
go test ./internal/scheduling/... -v
go test ./internal/services/provider/inference/... -run PriorityClass -v
make test
make lint 2>&1 | grep -E 'scheduling|inference|framejob_controller' || echo "no new lint findings"
```

Expected: PASS. `make test` must show `internal/controller` coverage unchanged — this task moves a function out of that package but its one caller is still covered by the existing `buildWorkflow` tests.

- [ ] **Step 8: Commit**

```bash
git add internal/scheduling/priority.go internal/scheduling/priority_test.go \
        internal/controller/frame/framejob_controller.go \
        internal/services/provider/inference/inference.go \
        internal/services/provider/inference/reconcile_test.go \
        docs/crd-reference.md
git commit -m "feat(services): schedule FrameService instances by their serviceClass

The inference provider set no priorityClassName at all, so every instance
ran at the implicit default of 0 with no field an operator could change.
It now derives one from spec.serviceClass through the same mapping the
FrameJob controller uses, hoisted into internal/scheduling so the two
cannot drift.

This knowingly gives serviceClass a second meaning — tier and urgency —
which is the owner's decision (F10): a long-lived instance's tier is its
urgency, unlike a job's, where a HIGH-tier nightly batch can legitimately
be low-priority. No spec.priority and no spec.priorityClassName: the first
would duplicate serviceClass with no case where they differ, the second
would let a user name a PriorityClass Frame did not create.

Zero FrameServices exist anywhere, so nothing running changes priority."
```

---
### Task 6: Add top-level `status.observedGeneration` to the seven kinds that lack it (R1)

Only `FrameService` has it (`frameservice_types.go:144`, written at `frameservice_controller.go:255`). The other seven set `ObservedGeneration` per-condition only. On the live cluster right now all three `FrameResourceQuota` objects are at `metadata.generation: 3` with their `Ready` condition at `observedGeneration: 2` — the controller has not reconciled the current spec, and to learn that a client must know a `Ready` condition exists, find it by type, and compare. `status.observedGeneration` answers "has the controller seen this spec yet" without knowing anything about the kind's condition vocabulary.

The inventory files this as Tier 3, additive, "do it but do not let it gate the conversion change". **This plan does the opposite of letting it wait**, for a sequencing reason the inventory does not spell out: adding it to `v1alpha1` **now** means the `v1beta1` types have no field `v1alpha1` lacks, which makes the `v1beta1 → v1alpha1 → v1beta1` round trip lossless by construction and removes any need for the annotation escape hatch F14 point 2 describes. Task 18 depends on that.

**Live objects:** three FrameResourceQuotas, three FrameNodes, two FrameJobs, one SchedulingPolicy. A new optional status field is invisible to all of them until their next reconcile, at which point it appears. Nothing is rejected.

**Files:**
- Modify: `api/frame/v1alpha1/framejob_types.go`, `framenode_types.go`, `frameresourcequota_types.go`, `schedulingpolicy_types.go`, `talosmachineconfig_types.go`, `talosupgrade_types.go`, `frameuser_types.go`
- Modify: `internal/controller/frame/framejob_controller.go`, `framenode_controller.go`, `frameresourcequota_controller.go`, `schedulingpolicy_controller.go`, `talosmachineconfig_controller.go`, `talosupgrade_controller.go`
- Modify: `internal/controller/frame/frameresourcequota_controller_test.go`
- Modify: `docs/crd-reference.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `ObservedGeneration int64` with json tag `observedGeneration,omitempty` on all seven `*Status` structs in `api/frame/v1alpha1`. Tasks 12–16 carry the identical field onto the `v1beta1` types, and Task 18's conversion copies it straight across.
- Produces: nothing new in Go API surface — the controllers assign the field inline.

- [ ] **Step 1: Add the field to all seven status structs**

To each of `FrameJobStatus`, `FrameNodeStatus`, `FrameResourceQuotaStatus`, `SchedulingPolicyStatus`, `TalosMachineConfigStatus`, `TalosUpgradeStatus`, `FrameUserStatus`, add — as the **first** field of the struct, so it reads first in `kubectl explain`:

```go
	// ObservedGeneration is the metadata.generation this status was computed
	// from. A client can compare it to metadata.generation to tell whether
	// the controller has seen the current spec yet, without knowing anything
	// about this kind's condition vocabulary.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
```

`FrameUser` has no controller (`PROJECT:87-97` has no `controller: true`), so nothing writes its copy. Add it anyway: the field is part of the frozen shape, and a future controller must not have to add a status field to a frozen version.

- [ ] **Step 2: Write it from every controller that writes status**

In each of the six controllers, in **every** place that takes a status patch, set the field beside the existing condition write. Concretely:

`framejob_controller.go` — in both status-patch blocks (the workflow-creation branch and the phase-transition branch), add before the `setJobReady` call:

```go
		job.Status.ObservedGeneration = job.Generation
```

`framenode_controller.go` — in `setPhase` and in `reconcileOnline`, add before the condition write:

```go
	fn.Status.ObservedGeneration = fn.Generation
```

and in the discovery branch (around line 110, where `fn.Status.Phase = nodePhaseDiscovered` is set) add the same line.

`frameresourcequota_controller.go` — before the `meta.SetStatusCondition` call:

```go
	frq.Status.ObservedGeneration = frq.Generation
```

`schedulingpolicy_controller.go`, `talosmachineconfig_controller.go`, `talosupgrade_controller.go` — the same, using each file's own receiver variable, immediately before its condition write. Find them with:

```bash
grep -n 'meta.SetStatusCondition' internal/controller/frame/*.go
```

Every hit must have an `ObservedGeneration` assignment on the line above it, inside the same patch block.

- [ ] **Step 3: Regenerate**

```bash
make manifests generate
grep -c 'observedGeneration' config/crd/bases/frame.plume-labs.io_*.yaml
```

Expected: a non-zero count for all seven files (each will show at least two — the per-condition one and the new top-level one).

- [ ] **Step 4: Add the test**

Append to `internal/controller/frame/frameresourcequota_controller_test.go`:

```go
	It("records the generation its status was computed from", func() {
		ctx := context.Background()
		frq := &framev1alpha1.FrameResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: "obsgen", Namespace: "default"},
			Spec:       framev1alpha1.FrameResourceQuotaSpec{ServiceClass: "HIGH", MaxJobs: 5},
		}
		Expect(k8sClient.Create(ctx, frq)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, frq) })

		reconciler := &FrameResourceQuotaReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		key := types.NamespacedName{Name: "obsgen", Namespace: "default"}
		// Pass 1 adds the finalizer, pass 2 reconciles.
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		fetched := &framev1alpha1.FrameResourceQuota{}
		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		Expect(fetched.Status.ObservedGeneration).To(Equal(fetched.Generation),
			"status.observedGeneration must track metadata.generation")

		// Change the spec and confirm the field moves with it.
		fetched.Spec.MaxJobs = 9
		Expect(k8sClient.Update(ctx, fetched)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		Expect(fetched.Status.ObservedGeneration).To(Equal(fetched.Generation))
	})
```

- [ ] **Step 5: Document it**

In `docs/crd-reference.md`, add a short paragraph immediately after the intro block (before `## FrameNode`):

```markdown
### `status.observedGeneration`

Every kind carries a top-level `status.observedGeneration`: the
`metadata.generation` its status was computed from. Compare it to
`metadata.generation` to tell whether the controller has seen the current
spec. Conditions carry their own per-condition `observedGeneration` as well;
the top-level field is the one a client can read without knowing which
condition types this kind writes. `FrameUser` has the field and no writer —
it has no controller.
```

- [ ] **Step 6: Test cycle**

```bash
make manifests generate
go test ./internal/controller/frame/... -v -ginkgo.focus='generation its status was computed from'
make test
git status --short -- charts/frame/files/crds/
```

Expected: PASS; `make manifests` has also re-run `helm-sync-crds` (it is wired in at `Makefile:53`), so the chart's CRD copies show as modified — that is correct and must be committed.

- [ ] **Step 7: Commit**

```bash
git add api/frame/v1alpha1/ internal/controller/frame/ config/crd/bases/ charts/frame/files/crds/ docs/crd-reference.md
git commit -m "feat(api): add top-level status.observedGeneration to the seven kinds

Only FrameService had it. To tell whether a controller has seen the current
spec, a client had to know a Ready condition exists, find it by type, and
compare its observedGeneration — and a kind whose controller writes several
conditions with different values gave no answer at all. On the live cluster
all three FrameResourceQuotas sit at generation 3 with Ready at
observedGeneration 2, which is exactly the state that was unreadable.

Additive, so no conversion consequence — and doing it before the freeze is
what leaves v1beta1 with no field v1alpha1 lacks, which is what makes the
v1beta1 -> v1alpha1 -> v1beta1 round trip lossless without an annotation
escape hatch."
```

---

### Task 7: Project real quota usage into `FrameResourceQuotaStatus` (R5, controller half)

`crToQuota` in the SDK hardcodes `usedCPU: '0'`, `usedMemory: '0Gi'`, `usedGPUs: 0` (`frame-sdk.ts:785-787`), never wired to anything. The `corev1.ResourceQuota` the controller already creates in each matching namespace carries a real `status.used`; nothing reads it back. The inventory's recommendation is to project it for real rather than delete the display.

Like Task 6, this lands in `v1alpha1` **before** the freeze so `v1beta1` gains no field `v1alpha1` lacks.

**Live objects:** three FrameResourceQuotas, currently projecting into namespaces labelled `frame.plume-labs.io/service-class`. Adding a status field changes nothing they hold.

**Files:**
- Modify: `api/frame/v1alpha1/frameresourcequota_types.go`
- Modify: `internal/controller/frame/frameresourcequota_controller.go`
- Modify: `internal/controller/frame/frameresourcequota_controller_test.go`
- Modify: `config/rbac/` via markers (regenerated)
- Modify: `docs/crd-reference.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `FrameResourceQuotaStatus.Used corev1.ResourceList` (json `used,omitempty`) and `FrameResourceQuotaStatus.Namespaces int32` (json `namespaces,omitempty`). Task 9 (SDK) reads `status.used` by the same key names the controller writes. Task 14 carries both fields onto `v1beta1` unchanged.
- Produces: `func sumQuotaUsage(quotas []corev1.ResourceQuota) corev1.ResourceList` in `internal/controller/frame/frameresourcequota_controller.go`.

- [ ] **Step 1: Add the status fields**

In `api/frame/v1alpha1/frameresourcequota_types.go`, add to `FrameResourceQuotaStatus` (after the `ObservedGeneration` field Task 6 added):

```go
	// Used is the sum of status.used across every corev1.ResourceQuota this
	// object projects into. The keys are the ones buildResourceList writes:
	// limits.cpu, limits.memory, requests.nvidia.com/gpu and
	// count/framejobs.frame.plume-labs.io. Absent until at least one
	// projected quota reports usage.
	// +optional
	Used corev1.ResourceList `json:"used,omitempty"`

	// Namespaces is how many namespaces this quota currently projects into.
	// It is what makes Used interpretable: a zero Used with zero namespaces
	// means "nothing selected this quota", which is a different problem from
	// "selected, and idle".
	// +optional
	Namespaces int32 `json:"namespaces,omitempty"`
```

Add `corev1 "k8s.io/api/core/v1"` to the file's imports.

- [ ] **Step 2: Read the projected quotas back**

In `internal/controller/frame/frameresourcequota_controller.go`, the reconcile loop already iterates `nsList.Items` and calls `CreateOrUpdate` on a `corev1.ResourceQuota` named `quotaName` in each. Collect them as it goes: replace the loop body's tail and the status block with:

```go
	projected := make([]corev1.ResourceQuota, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		quota := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name:      quotaName,
				Namespace: ns.Name,
			},
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, quota, func() error {
			quota.Spec.Hard = hard
			return nil
		}); err != nil {
			log.Error(err, "Failed to reconcile ResourceQuota", "namespace", ns.Name)
			return ctrl.Result{}, err
		}
		// CreateOrUpdate returns the object as written, which carries the
		// status the apiserver last computed. Reading it here rather than
		// issuing a second Get keeps this to one round trip per namespace.
		projected = append(projected, *quota)
	}

	patch := client.MergeFrom(frq.DeepCopy())
	frq.Status.ObservedGeneration = frq.Generation
	frq.Status.Namespaces = int32(len(nsList.Items))
	frq.Status.Used = sumQuotaUsage(projected)
	meta.SetStatusCondition(&frq.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("Applied to %d namespaces", len(nsList.Items)),
		ObservedGeneration: frq.Generation,
	})
```

And add, next to `buildResourceList`:

```go
// sumQuotaUsage adds up status.used across every projected ResourceQuota.
// The apiserver computes each one; Frame only aggregates, so a key that no
// namespace reports is absent rather than zero — "not measured" and "measured
// as nothing" are different answers and the UI shows them differently.
func sumQuotaUsage(quotas []corev1.ResourceQuota) corev1.ResourceList {
	total := corev1.ResourceList{}
	for _, q := range quotas {
		for name, qty := range q.Status.Used {
			if existing, ok := total[name]; ok {
				existing.Add(qty)
				total[name] = existing
				continue
			}
			total[name] = qty.DeepCopy()
		}
	}
	if len(total) == 0 {
		return nil
	}
	return total
}
```

- [ ] **Step 3: Widen the RBAC marker**

The controller already has `resourcequotas` CRUD. Confirm it can read status:

```bash
grep -n 'kubebuilder:rbac.*resourcequotas' internal/controller/frame/frameresourcequota_controller.go
```

If the marker does not list `resourcequotas/status`, add above `Reconcile`:

```go
// +kubebuilder:rbac:groups="",resources=resourcequotas/status,verbs=get
```

Then `make manifests` regenerates `config/rbac/role.yaml`.

- [ ] **Step 4: Test it**

Append to `internal/controller/frame/frameresourcequota_controller_test.go`:

```go
	It("aggregates the projected ResourceQuotas' usage into status", func() {
		ctx := context.Background()

		// Two namespaces of the same class, so the sum is provably a sum.
		for _, name := range []string{"quota-usage-a", "quota-usage-b"} {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"frame.plume-labs.io/service-class": "MEDIUM"},
			}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		}

		frq := &framev1alpha1.FrameResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: "quota-usage", Namespace: "default"},
			Spec:       framev1alpha1.FrameResourceQuotaSpec{ServiceClass: "MEDIUM", MaxJobs: 10},
		}
		Expect(k8sClient.Create(ctx, frq)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, frq) })

		reconciler := &FrameResourceQuotaReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		key := types.NamespacedName{Name: "quota-usage", Namespace: "default"}
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		fetched := &framev1alpha1.FrameResourceQuota{}
		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		Expect(fetched.Status.Namespaces).To(BeNumerically(">=", 2),
			"both labelled namespaces must be counted")
	})

	It("sums usage across quotas without inventing keys nobody reported", func() {
		a := corev1.ResourceQuota{Status: corev1.ResourceQuotaStatus{
			Used: corev1.ResourceList{
				corev1.ResourceLimitsCPU: resource.MustParse("2"),
			},
		}}
		b := corev1.ResourceQuota{Status: corev1.ResourceQuotaStatus{
			Used: corev1.ResourceList{
				corev1.ResourceLimitsCPU:    resource.MustParse("3"),
				corev1.ResourceLimitsMemory: resource.MustParse("4Gi"),
			},
		}}

		total := sumQuotaUsage([]corev1.ResourceQuota{a, b})
		cpu := total[corev1.ResourceLimitsCPU]
		Expect(cpu.String()).To(Equal("5"))
		mem := total[corev1.ResourceLimitsMemory]
		Expect(mem.String()).To(Equal("4Gi"))
		Expect(total).NotTo(HaveKey(corev1.ResourceName("requests.nvidia.com/gpu")),
			"a key no namespace reported must be absent, not zero")

		Expect(sumQuotaUsage(nil)).To(BeNil(),
			"no projected quotas means no usage measured, not usage of zero")
	})
```

Add `"k8s.io/apimachinery/pkg/api/resource"` to the test file's imports.

> Note: envtest runs no quota controller, so the projected `ResourceQuota` objects report an empty `status.used`. That is why the aggregation itself is unit-tested against constructed objects (second `It`) while the envtest spec asserts only the wiring and the namespace count. Task 21's e2e spec is where a real apiserver populates `status.used`.

- [ ] **Step 5: Document it**

In `docs/crd-reference.md`, in `## FrameResourceQuota`, extend the **Status** line to:

```
**Status:** `observedGeneration`, `conditions[]`, `namespaces` (how many
namespaces this quota projects into) and `used` — the sum of `status.used`
across every projected `corev1.ResourceQuota`, keyed exactly as
`buildResourceList` writes them (`limits.cpu`, `limits.memory`,
`requests.nvidia.com/gpu`, `count/framejobs.frame.plume-labs.io`). A key no
namespace reported is absent rather than zero: "not measured" and "measured
as nothing" are different answers.
```

- [ ] **Step 6: Test cycle**

```bash
make manifests generate
go test ./internal/controller/frame/... -v -ginkgo.focus='aggregates the projected|sums usage across'
make test
make lint 2>&1 | grep frameresourcequota || echo "no new lint findings"
```

- [ ] **Step 7: Commit**

```bash
git add api/frame/v1alpha1/frameresourcequota_types.go \
        internal/controller/frame/frameresourcequota_controller.go \
        internal/controller/frame/frameresourcequota_controller_test.go \
        config/crd/bases/ config/rbac/role.yaml charts/frame/files/crds/ \
        charts/frame/templates/rbac-manager.yaml docs/crd-reference.md
git commit -m "feat(api): report real quota usage in FrameResourceQuota status

The ResourceQuota the controller projects into each matching namespace has
carried a real status.used all along and nothing read it back, so the UI
hardcoded usedCPU '0' / usedMemory '0Gi' / usedGPUs 0 — three numbers that
were structurally always zero.

status.used is now the sum across every projected quota, with
status.namespaces beside it so a zero can be told apart from 'nothing
selected this quota'. A key no namespace reported stays absent rather than
becoming zero.

Additive, and landed before the freeze for the same reason as
observedGeneration: it leaves v1beta1 with no field v1alpha1 lacks."
```

> If `charts/frame/templates/rbac-manager.yaml` did not change (the marker already granted the verb), drop it from the `git add` line.

---

### Task 8: Delete the two `InferenceRoute` stanzas (R4)

`deploy/jobs/pipeline-parallelism.yaml:159-175` and `deploy/jobs/speculative-decoding.yaml:160-174` both declare `apiVersion: frame.plume-labs.io/v1alpha1`, `kind: InferenceRoute`, with a full spec (`routes`, `backend`, `serviceClasses`, `weight`, `fallbackBackend`). **No such kind exists** — no Go type, no CRD, no controller, no `PROJECT` entry. Applying either file fails with `no matches for kind`.

This is the mirror of the writer-sweep lesson: the last pass missed writers of fields that had been *removed*; these are writers of a kind that was never *added*, both in `deploy/`, which is where that pass was not looking.

Do **not** add the kind. A routing CRD is S2 (SDN) territory, and adding a kind to a group being frozen in order to make a stale manifest parse is exactly backwards. Adding it later is additive.

**Live objects:** none — the kind does not exist, so nothing on any cluster is an `InferenceRoute`.

**Files:**
- Modify: `deploy/jobs/pipeline-parallelism.yaml`
- Modify: `deploy/jobs/speculative-decoding.yaml`

**Interfaces:**
- Consumes: nothing. Produces: nothing. This task is deliberately isolated so it can be reviewed and merged on its own.

- [ ] **Step 1: Confirm the kind really does not exist**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn 'InferenceRoute' --include='*.go' --include='*.yaml' --include='*.ts' --include='*.tsx' --include='*.md' \
  api internal cmd test config charts deploy src docs PROJECT | grep -v node_modules
```

Expected: exactly the two `deploy/jobs/` files, and nothing in `api/`, `PROJECT`, `config/crd/bases/` or `internal/`.

- [ ] **Step 2: Replace each stanza with a note**

In `deploy/jobs/pipeline-parallelism.yaml`, replace the whole document from the `# ── Frame InferenceRoute` comment at line 159 through the end of that YAML document (the `fallbackBackend: vllm-baseline` line and its preceding `---`) with:

```yaml
# ── Routing ───────────────────────────────────────────────────────────────
# There is deliberately no Frame routing resource here.
#
# This file used to declare an `InferenceRoute` in
# frame.plume-labs.io/v1alpha1. That kind has never existed: no Go type, no
# CRD, no controller, no PROJECT entry — applying this file failed with
# "no matches for kind". It was not added to satisfy the manifest, because
# adding a kind to a group being frozen in order to make a stale manifest
# parse is backwards. A routing CRD is S2 (SDN) territory and adding a kind
# later is additive.
#
# Until then, route to the Service above by whatever ingress the cluster
# already runs.
```

Apply the identical treatment to `deploy/jobs/speculative-decoding.yaml`, removing lines 160-174 and the `# Requires the Frame routing controller (frame.plume-labs.io/v1alpha1 CRD).` line above them.

- [ ] **Step 3: Prove both files now parse as Kubernetes objects**

```bash
for f in deploy/jobs/pipeline-parallelism.yaml deploy/jobs/speculative-decoding.yaml; do
  echo "== $f"
  python3 -c "import sys,yaml; list(yaml.safe_load_all(open('$f')))" && echo "  YAML OK"
  grep -c 'InferenceRoute' "$f" || echo "  no InferenceRoute"
done
```

Expected: both parse; `grep -c` reports 0 for `InferenceRoute` (the word appears only inside the explanatory comment, so accept a count of 1 there and read the line to confirm it is the comment).

- [ ] **Step 4: Dry-run them against the test cluster**

```bash
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml
kubectl apply --dry-run=server -f deploy/jobs/pipeline-parallelism.yaml
kubectl apply --dry-run=server -f deploy/jobs/speculative-decoding.yaml
```

Expected: every document reports `(server dry run)` and **no** `no matches for kind "InferenceRoute"`. Other failures (a missing namespace, an absent Volcano CRD) are pre-existing and out of scope — record them in the commit body if any appear, do not fix them here.

- [ ] **Step 5: Commit**

```bash
git add deploy/jobs/pipeline-parallelism.yaml deploy/jobs/speculative-decoding.yaml
git commit -m "fix(deploy): drop the InferenceRoute stanzas for a kind that does not exist

Both files declared frame.plume-labs.io/v1alpha1 InferenceRoute with a full
spec. There is no Go type, no CRD, no controller and no PROJECT entry —
applying either file failed with 'no matches for kind'.

The kind is not being added to satisfy them: a routing CRD is S2 (SDN)
territory, and adding a kind to a group being frozen so a stale manifest
parses is backwards. Adding one later is additive. A comment in each file
records what was there and why it is not coming back this way."
```

---

### Task 9: Stop the SDK reading fields the API does not have (R5, client half)

Three places where the UI reads something the API never provided:

- `frame-sdk.ts:683` declares `FrameNodeCR.spec.gpuCount` and `crToNode` reads it at `:760` — **`FrameNodeSpec` has no `gpuCount` field**, so the Nodes screen's GPU count is structurally always 0.
- `crToPolicy` (`:773-774`) hardcodes `maxGPUs: 0, maxCPUs: 0`; `SchedulingPolicySpec` has no such fields and never did.
- `crToQuota` (`:785-787`) hardcodes `usedCPU: '0'`, `usedMemory: '0Gi'`, `usedGPUs: 0`.

Task 7 made the third one real. This task wires it up and deletes the other two.

**Live objects:** none affected — this is client-side only.

**Files:**
- Modify: `src/lib/frame-sdk.ts`
- Modify: whichever views render the deleted fields (found in Step 1)
- Modify: `src/lib/frame-sdk.test.ts` if one exists, else create the mapper tests there

**Interfaces:**
- Consumes: `FrameResourceQuotaStatus.Used` / `.Namespaces` from Task 7, on the wire as `status.used` (a map keyed `limits.cpu`, `limits.memory`, `requests.nvidia.com/gpu`, `count/framejobs.frame.plume-labs.io`) and `status.namespaces`.
- Produces: `interface FrameNodeCR` without `spec.gpuCount`; `interface SchedulingPolicy` without `maxGPUs`/`maxCPUs`; `interface ResourceQuota` with `usedCPU`/`usedMemory`/`usedGPUs` populated from `status.used`. Task 22 edits the same file and must not reintroduce them.

- [ ] **Step 1: Find every consumer of the three phantoms**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn 'gpuCount' src/ --include='*.ts' --include='*.tsx' | grep -v node_modules
grep -rn 'maxGPUs\|maxCPUs' src/ --include='*.ts' --include='*.tsx' | grep -v node_modules
grep -rn 'usedCPU\|usedMemory\|usedGPUs' src/ --include='*.ts' --include='*.tsx' | grep -v node_modules
```

Every hit outside `frame-sdk.ts` is a view that renders the value. Note them; Step 4 fixes them.

- [ ] **Step 2: Fix the FrameNode mapper**

In `src/lib/frame-sdk.ts`, remove `gpuCount?: number;` from `interface FrameNodeCR`'s `spec` (line 683) and, in `crToNode` (line ~760), replace:

```ts
    gpuCount:     cr.spec.gpuCount ?? 0,
    gpuModel:     cr.spec.rdmaInterface ? 'RDMA' : 'Unknown',
```

with:

```ts
    // FrameNodeSpec has no gpuCount and never did: the number shown here was
    // structurally always 0. status.allocatable is what the controller
    // actually syncs from the corev1.Node, so read the GPU count from there.
    gpuCount:     quantityToNum(alloc['nvidia.com/gpu']),
    gpuModel:     cr.spec.rdmaInterface ? 'RDMA' : 'Unknown',
```

`alloc` is already in scope (`const alloc = cr.status?.allocatable ?? {}`).

- [ ] **Step 3: Fix the policy and quota mappers**

Replace `crToPolicy` with:

```ts
function crToPolicy(cr: SchedulingPolicyCR): SchedulingPolicy {
  return {
    name:        cr.metadata.name,
    scheduler:   (cr.spec.scheduler ?? 'default') as SchedulerType,
    queue:       cr.spec.queueName ?? '',
    queueWeight: cr.spec.queueWeight ?? 0,
    priority:    cr.spec.priorityValue ?? 0,
    preemption:  cr.spec.preemption ?? false,
    // maxGPUs/maxCPUs were hardcoded to 0 here. SchedulingPolicySpec has no
    // such fields and never did; resource ceilings are FrameResourceQuota's.
  }
}
```

and remove `maxGPUs: number` and `maxCPUs: number` from the exported `SchedulingPolicy` interface (around line 219-240 — find it with `grep -n 'interface SchedulingPolicy' src/lib/frame-sdk.ts`).

Replace `crToQuota` with:

```ts
function crToQuota(cr: FrameResourceQuotaCR): ResourceQuota {
  // status.used is the sum the controller aggregates across every projected
  // corev1.ResourceQuota, keyed exactly as buildResourceList writes them. A
  // key no namespace reported is absent, not zero — hence the ?? fallbacks
  // rather than a required shape.
  const used = cr.status?.used ?? {}
  return {
    namespace:    cr.metadata.namespace ?? frameNs(),
    serviceClass: (cr.spec.serviceClass ?? 'MEDIUM') as ServiceClass,
    maxCPU:       cr.spec.maxCPU ?? '0',
    maxMemory:    cr.spec.maxMemory ?? '0Gi',
    maxGPUs:      cr.spec.maxGPUs ?? 0,
    usedCPU:      used['limits.cpu'] ?? '0',
    usedMemory:   used['limits.memory'] ?? '0Gi',
    usedGPUs:     quantityToNum(used['requests.nvidia.com/gpu']),
    namespaces:   cr.status?.namespaces ?? 0,
  }
}
```

Extend `interface FrameResourceQuotaCR`:

```ts
interface FrameResourceQuotaCR {
  metadata: { name: string; namespace?: string }
  spec: { serviceClass: string; maxGPUs?: number; maxCPU?: string; maxMemory?: string }
  status?: { used?: Record<string, string>; namespaces?: number }
}
```

and add `namespaces: number` to the exported `ResourceQuota` interface.

- [ ] **Step 4: Fix the views**

For each view found in Step 1 that renders `policy.maxGPUs` or `policy.maxCPUs`, delete the cell/row — the number was always 0 and there is nothing to replace it with. For views rendering quota usage, no change is needed: the field names are unchanged and now carry real values. Add the namespace count where a quota table already shows a "scope" column; if none does, skip it.

- [ ] **Step 5: Test the mappers**

Add to `src/lib/frame-sdk.test.ts` (create it if absent, matching the project's Vitest setup in `vite.config.ts`):

```ts
import { describe, expect, it } from 'vitest'
import { __testing } from './frame-sdk'

// crToQuota, crToNode and crToPolicy are module-private. Export them through
// a __testing barrel at the bottom of frame-sdk.ts:
//   export const __testing = { crToQuota, crToNode, crToPolicy }
describe('crToQuota', () => {
  it('reads the real usage the controller aggregates', () => {
    const q = __testing.crToQuota({
      metadata: { name: 'quota-high', namespace: 'default' },
      spec: { serviceClass: 'HIGH', maxGPUs: 4, maxCPU: '8', maxMemory: '16Gi' },
      status: {
        used: { 'limits.cpu': '3', 'limits.memory': '6Gi', 'requests.nvidia.com/gpu': '2' },
        namespaces: 2,
      },
    })
    expect(q.usedCPU).toBe('3')
    expect(q.usedMemory).toBe('6Gi')
    expect(q.usedGPUs).toBe(2)
    expect(q.namespaces).toBe(2)
  })

  it('falls back when no namespace has reported usage', () => {
    const q = __testing.crToQuota({
      metadata: { name: 'quota-low', namespace: 'default' },
      spec: { serviceClass: 'LOW' },
    })
    expect(q.usedCPU).toBe('0')
    expect(q.usedGPUs).toBe(0)
    expect(q.namespaces).toBe(0)
  })
})

describe('crToNode', () => {
  it('takes the GPU count from status.allocatable, not a spec field that does not exist', () => {
    const n = __testing.crToNode({
      metadata: { name: 'w2' },
      spec: { ip: '10.0.0.2', serviceClass: 'HIGH' },
      status: { allocatable: { cpu: '8', memory: '32Gi', 'nvidia.com/gpu': '1' } },
    })
    expect(n.gpuCount).toBe(1)
  })
})

describe('crToPolicy', () => {
  it('no longer reports resource ceilings SchedulingPolicySpec never had', () => {
    const p = __testing.crToPolicy({
      metadata: { name: 'neura-default' },
      spec: { scheduler: 'volcano', queueName: 'neura-high', queueWeight: 100 },
    }) as Record<string, unknown>
    expect(p).not.toHaveProperty('maxGPUs')
    expect(p).not.toHaveProperty('maxCPUs')
  })
})
```

Add the barrel at the bottom of `src/lib/frame-sdk.ts`:

```ts
/** Module-private mappers, exposed for unit tests only. Not part of the SDK. */
export const __testing = { crToJob, crToNode, crToPolicy, crToQuota }
```

- [ ] **Step 6: Test cycle**

```bash
npx tsc --noEmit
npx vitest run src/lib/frame-sdk.test.ts
npx eslint src/lib/frame-sdk.ts
npm run build
```

Expected: no type errors (a missed view still reading `policy.maxGPUs` fails here, which is the point), tests pass, build succeeds.

- [ ] **Step 7: Commit**

```bash
git add src/lib/frame-sdk.ts src/lib/frame-sdk.test.ts src/components/
git commit -m "fix(ui): stop reading three fields the API never provided

FrameNodeCR declared spec.gpuCount and crToNode read it — FrameNodeSpec has
no such field, so the Nodes screen's GPU count was structurally always 0. It
now reads status.allocatable['nvidia.com/gpu'], which the controller really
does sync from the corev1.Node.

crToPolicy hardcoded maxGPUs/maxCPUs to 0 for fields SchedulingPolicySpec
never had; both are gone from the SDK type and from the views that rendered
them. Resource ceilings are FrameResourceQuota's.

crToQuota hardcoded usedCPU/usedMemory/usedGPUs; it now reads the
status.used the controller aggregates, with status.namespaces beside it."
```

---
# Part 1 — Conversion plumbing

Both tasks are green-to-green: they change no schema and no behaviour. They exist so that the moment a second version appears, the guards that would have missed it are already in place.

---

### Task 10: Teach the Helm parity guard to see CRD conversion wiring (F13, the trap)

**This is the single highest-risk item in the whole phase, and it must be closed before anything creates a two-version CRD.**

The conversion stanza is a kustomize patch (`config/crd/patches/webhook_in_*.yaml`, applied by `config/crd/kustomization.yaml`). The Helm chart does not run kustomize: `charts/frame/templates/crds.yaml:14` reads `files/crds/*.yaml`, which `make helm-sync-crds` (`Makefile:272-277`) copies **verbatim from `config/crd/bases/`** — the un-patched bases. And `hack/helm-parity.sh:179-181` **skips `CustomResourceDefinition` entirely** from its content diff, by explicit design (`:152-157`).

So a chart install would ship eight CRDs with `conversion.strategy: None` while the operator serves two versions. Every read at the non-storage version would silently return the stored object uninterpreted, and `make helm-crds-check`, `hack/helm-parity.sh` and CI would all be green.

The inventory says the fix has to be one of: put the conversion stanza in the `config/crd/bases/` output itself, teach `crds.yaml` to inject it, or make `helm-parity.sh` stop skipping CRDs. **The first is not available**: controller-gen v0.20.1 has no marker that emits `spec.conversion` — verified, the conversion stanza is kustomize-only in kubebuilder v4. So this plan does the other two: Task 19 makes the chart inject the stanza, and **this task makes the guard able to catch it if that ever regresses**.

The guard does not start diffing whole CRD bodies — the inventory is right that their OpenAPI schemas are large and slow to diff, and `make helm-crds-check` already keeps `files/crds/` byte-identical to `config/crd/bases/`. What it starts diffing is exactly the part that `helm-crds-check` **cannot** see, because it is added on only one side: the version topology and the conversion wiring.

**Live objects:** none. This task changes a CI script.

**Files:**
- Modify: `hack/helm-parity.sh`
- Modify: `charts/frame/README.md` (the documented-exceptions list)

**Interfaces:**
- Consumes: nothing.
- Produces: `hack/helm-parity.sh` gains a `crd_shape()` shell function and a "CRD shape diff" section. Task 19 relies on this section failing when the chart lacks the conversion stanza, and passing once it has it.

- [ ] **Step 1: Confirm the blind spot exists, today, with a deliberate break**

```bash
cd /home/rmocq/Neura/.externals/frame
make helm-parity && echo "BASELINE GREEN"
# Inject a bogus conversion stanza into the kustomize side only.
python3 - <<'PY'
import io, re, sys
p = "config/crd/bases/frame.plume-labs.io_framejobs.yaml"
s = open(p).read()
s = s.replace("spec:\n  group: frame.plume-labs.io\n",
              "spec:\n  conversion:\n    strategy: Webhook\n  group: frame.plume-labs.io\n", 1)
open(p, "w").write(s)
PY
make helm-parity && echo "STILL GREEN — this is the F13 blind spot, exactly as the inventory describes"
git checkout -- config/crd/bases/frame.plume-labs.io_framejobs.yaml
```

Expected: the second `make helm-parity` **also** prints green. Record that in the commit body — it is the evidence the guard was blind.

> The break above is on `config/crd/bases/`, which `helm-sync-crds` copies, so it would in fact reach both sides. Use it only to observe that the CRD content diff is skipped; the real divergence Task 19 creates is on the **kustomize-rendered** output, which the chart never sees at all. Step 4's verification uses that shape.

- [ ] **Step 2: Add the shape extractor**

In `hack/helm-parity.sh`, immediately after the existing `triples()` helper definition (before the `# --- helm side` section around line 125), add:

```bash
# crd_shape extracts, for every CustomResourceDefinition in a JSONL stream,
# the parts of the CRD that the two install paths can legitimately disagree
# on *without* helm-crds-check noticing:
#
#   - the version topology (name / served / storage per version), and
#   - the conversion wiring (strategy, review versions, service coordinate,
#     and whether a CA-injection annotation is present).
#
# helm-crds-check keeps charts/frame/files/crds/ byte-identical to
# config/crd/bases/, so anything present in the bases is verified already.
# What it cannot see is anything *added on one side only* — and the
# conversion stanza is exactly that: kustomize adds it via
# config/crd/patches/, and the chart has to add it in templates/crds.yaml.
# Before this check existed, a chart install would have shipped eight CRDs
# with conversion.strategy: None against a two-version operator, silently,
# with CI green (F13).
#
# The caBundle itself is deliberately NOT compared: cert-manager writes it at
# runtime on both paths and kustomize leaves a placeholder. Only the presence
# of the inject-ca-from annotation is compared, which is what causes it to be
# written at all.
crd_shape() {
  jq -S -r '
    .[]
    | select(.kind == "CustomResourceDefinition")
    | {
        name: .metadata.name,
        injectCA: ((.metadata.annotations // {})["cert-manager.io/inject-ca-from"] != null),
        versions: [.spec.versions[] | {name, served, storage, deprecated: (.deprecated // false)}],
        conversion: (
          if .spec.conversion == null then {strategy: "None"}
          else {
            strategy: .spec.conversion.strategy,
            reviewVersions: (.spec.conversion.webhook.conversionReviewVersions // []),
            service: {
              namespace: (.spec.conversion.webhook.clientConfig.service.namespace // ""),
              name: (.spec.conversion.webhook.clientConfig.service.name // ""),
              path: (.spec.conversion.webhook.clientConfig.service.path // "")
            }
          }
          end
        )
      }
    | "\(.name)\t\(. | @json)"
  ' -s "$1" | sort
}
```

- [ ] **Step 3: Run the comparison and narrow the skip**

Replace the CRD skip at lines 179-181:

```bash
  if [ "$kind" = "CustomResourceDefinition" ]; then
    continue
  fi
```

with:

```bash
  if [ "$kind" = "CustomResourceDefinition" ]; then
    # Body diff still skipped: the OpenAPI schemas are large, and
    # helm-crds-check already keeps files/crds/ byte-identical to
    # config/crd/bases/. The parts that check cannot see — version topology
    # and conversion wiring, both added on one side only — are compared
    # separately below.
    continue
  fi
```

Then, immediately after the `if [ "$body_fail" -ne 0 ]; then exit 1; fi` block and its `echo "OK: every shared resource's body matches...` line (around line 207), insert:

```bash
# --- CRD shape diff: version topology and conversion wiring ------------------
# See crd_shape() for why this is a separate, narrow comparison rather than a
# full body diff, and for what it is guarding against (F13).
echo "== CRD shape: version topology and conversion wiring, helm vs kustomize =="
crd_shape "$tmpdir/kustomize.jsonl"    > "$tmpdir/kustomize.crdshape"
crd_shape "$tmpdir/helm-default.jsonl" > "$tmpdir/helm-default.crdshape"

if ! diff -u "$tmpdir/kustomize.crdshape" "$tmpdir/helm-default.crdshape"; then
  cat >&2 <<'MSG'
FAIL: the chart and kustomize disagree on CRD version topology or conversion wiring.

This is the failure make helm-crds-check cannot see. The conversion stanza is
a kustomize patch (config/crd/patches/webhook_in_*.yaml); the chart copies
config/crd/bases/ verbatim and must add the same stanza itself in
charts/frame/templates/crds.yaml. If they diverge, a chart install ships CRDs
with conversion.strategy: None against a multi-version operator, and every
read at the non-storage version silently returns the stored object
uninterpreted.

Fix charts/frame/templates/crds.yaml (or the kustomize patches) so both sides
agree. Do not allow-list this.
MSG
  exit 1
fi
echo "OK: identical CRD version topology and conversion wiring."
echo
```

- [ ] **Step 4: Prove the guard catches the real divergence**

Simulate exactly what Task 19 would do wrong — add the conversion stanza on the kustomize side only:

```bash
cd /home/rmocq/Neura/.externals/frame
mkdir -p config/crd/patches
cat > config/crd/patches/webhook_in_framejobs.yaml <<'EOF'
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: framejobs.frame.plume-labs.io
spec:
  conversion:
    strategy: Webhook
    webhook:
      conversionReviewVersions: ["v1"]
      clientConfig:
        service:
          namespace: system
          name: webhook-service
          path: /convert
EOF
python3 - <<'PY'
p = "config/crd/kustomization.yaml"
s = open(p).read()
s = s.replace("# +kubebuilder:scaffold:crdkustomizewebhookpatch",
              "- path: patches/webhook_in_framejobs.yaml\n# +kubebuilder:scaffold:crdkustomizewebhookpatch", 1)
s = s.replace("#configurations:\n#- kustomizeconfig.yaml",
              "configurations:\n- kustomizeconfig.yaml", 1)
open(p, "w").write(s)
PY

make helm-parity; echo "exit=$?"
```

Expected: **exit 1**, with the `FAIL: the chart and kustomize disagree on CRD version topology or conversion wiring` message and a diff showing `"strategy":"Webhook"` on the kustomize side and `"strategy":"None"` on the helm side. **This is the proof the guard works.** Paste the diff into the commit body.

Then revert the simulation:

```bash
rm -f config/crd/patches/webhook_in_framejobs.yaml
rmdir config/crd/patches 2>/dev/null || true
git checkout -- config/crd/kustomization.yaml
make helm-parity && echo "GREEN again"
```

- [ ] **Step 5: Update the chart README's exception list**

In `charts/frame/README.md`, find the decisions section that documents the parity script's exceptions and amend the CRD entry to:

```markdown
- **CustomResourceDefinition bodies** are skipped from the content diff:
  their OpenAPI schemas are large, and `make helm-crds-check` already keeps
  `files/crds/` byte-identical to `config/crd/bases/`. Their **version
  topology and conversion wiring are not skipped** — `hack/helm-parity.sh`
  compares those separately, because they are added on one side only
  (kustomize patches the conversion stanza in; the chart's
  `templates/crds.yaml` has to inject the equivalent itself) and are
  therefore invisible to `helm-crds-check`. A chart shipping
  `conversion.strategy: None` against a multi-version operator is a silent
  data-interpretation failure with green CI; this check is what stops it.
```

- [ ] **Step 6: Test cycle**

```bash
make helm-lint
make helm-crds-check
make helm-parity
shellcheck hack/helm-parity.sh || echo "shellcheck not installed or pre-existing findings only"
```

Expected: all green.

- [ ] **Step 7: Commit**

```bash
git add hack/helm-parity.sh charts/frame/README.md
git commit -m "ci(helm): diff CRD version topology and conversion wiring

hack/helm-parity.sh skipped CustomResourceDefinition entirely from its
content diff, by design — helm-crds-check already keeps files/crds/
byte-identical to config/crd/bases/, so diffing the OpenAPI schemas would
be slow and redundant.

But the conversion stanza is not in config/crd/bases/. It is a kustomize
patch, and the chart copies the un-patched bases. So a chart install would
ship eight CRDs with conversion.strategy: None against a two-version
operator — every read at the non-storage version silently returning the
stored object uninterpreted — with make helm-crds-check, helm-parity and CI
all green. Verified: adding the stanza to the kustomize side alone left the
script green before this change and fails it after.

The body diff stays skipped. What is now compared is exactly what
helm-crds-check cannot see: per-version name/served/storage/deprecated, the
conversion strategy, its review versions and service coordinate, and
whether a cert-manager inject-ca-from annotation is present. The caBundle
itself is not compared — cert-manager writes it at runtime on both paths."
```

---

### Task 11: Render CRDs through kustomize for envtest

F14 point 3: the conversion functions being correct is orthogonal to the apiserver being able to *call* them — the CA, the service coordinate, the port, `conversionReviewVersions`, and the manager actually serving `/convert`. None of that is exercised by a Go test today, because both envtest suites load `config/crd/bases`, which by construction has no conversion stanza.

controller-runtime's envtest **can** drive a conversion webhook: `envtest.Environment.WebhookInstallOptions` rewrites each CRD's `spec.conversion.webhook.clientConfig` to point at the locally-served webhook with its generated CA — **but only for CRDs that already declare `spec.conversion.strategy: Webhook`**. So the suites must load kustomize-rendered CRDs, not the bases.

This task does the rendering and the repointing while everything is still single-version, so it is provably green-to-green.

**Live objects:** none. This changes the test harness.

**Files:**
- Modify: `Makefile`
- Modify: `internal/controller/frame/suite_test.go`
- Modify: `internal/controller/services/suite_test.go`
- Modify: `internal/webhook/frame/v1alpha1/webhook_suite_test.go`
- Modify: `internal/webhook/services/v1alpha1/webhook_suite_test.go`
- Modify: `docs/development.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `make crd-render` writes every rendered CRD into `bin/crd-render/` (already gitignored via `/bin/`). `make test` depends on it.
- Produces: `func renderedCRDPath() string` in each of the four `*suite_test.go` files, returning the absolute path to `bin/crd-render`. Tasks 18–21 rely on the suites loading conversion-capable CRDs.

- [ ] **Step 1: Add the render target**

In `Makefile`, in the `##@ Development` section next to `manifests`, add:

```makefile
CRD_RENDER_DIR := $(shell pwd)/bin/crd-render

.PHONY: crd-render
crd-render: manifests kustomize ## Render the CRDs as kustomize builds them (with conversion patches) into bin/crd-render for envtest.
	@rm -rf "$(CRD_RENDER_DIR)"
	@mkdir -p "$(CRD_RENDER_DIR)"
	@"$(KUSTOMIZE)" build config/crd > "$(CRD_RENDER_DIR)/crds.yaml"
	@# envtest reads every YAML document in the directory, so one multi-doc
	@# file is enough and keeps the target trivially idempotent.
	@test -s "$(CRD_RENDER_DIR)/crds.yaml" || { echo "kustomize build config/crd produced nothing"; exit 1; }
	@echo "Rendered $$(grep -c '^kind: CustomResourceDefinition' "$(CRD_RENDER_DIR)/crds.yaml") CRDs into $(CRD_RENDER_DIR)"
```

and change the `test` target's prerequisites (line 68) from:

```makefile
test: manifests generate fmt vet setup-envtest ## Run tests.
```

to:

```makefile
test: manifests generate fmt vet crd-render setup-envtest ## Run tests.
```

> `config/crd/kustomization.yaml` is a standalone kustomization (its own comment says it is meant to be run by `config/default`, but `kustomize build config/crd` works on its own — it has `resources:` and, once Task 19 lands, `patches:` and `configurations:`). The service name/namespace inside the conversion stanza will read `webhook-service`/`system` rather than the `frame-`-prefixed deployed names, which is correct: envtest overwrites the whole `clientConfig` anyway.

- [ ] **Step 2: Repoint the four envtest suites**

In each of `internal/controller/frame/suite_test.go`, `internal/controller/services/suite_test.go`, `internal/webhook/frame/v1alpha1/webhook_suite_test.go` and `internal/webhook/services/v1alpha1/webhook_suite_test.go`, add above `BeforeSuite`:

```go
// renderedCRDPath is bin/crd-render, where `make crd-render` writes the CRDs
// as kustomize builds them — conversion stanza included.
//
// The suites used to read config/crd/bases directly. Those are the
// controller-gen output, and controller-gen has no marker that emits
// spec.conversion: the stanza is a kustomize patch. envtest can drive a
// conversion webhook (WebhookInstallOptions rewrites clientConfig to the
// local server and injects its CA) but only for a CRD that already declares
// strategy: Webhook, so reading the bases would have made every conversion
// test silently exercise nothing (F14 point 3).
func renderedCRDPath() string {
	// The number of ".." segments differs per suite; see the value below.
	return filepath.Join(crdRenderRelativeRoot, "bin", "crd-render")
}
```

and set `crdRenderRelativeRoot` per file, as a package-level `const`:

| File | `crdRenderRelativeRoot` |
|---|---|
| `internal/controller/frame/suite_test.go` | `filepath.Join("..", "..", "..")` |
| `internal/controller/services/suite_test.go` | `filepath.Join("..", "..", "..")` |
| `internal/webhook/frame/v1alpha1/webhook_suite_test.go` | `filepath.Join("..", "..", "..", "..")` |
| `internal/webhook/services/v1alpha1/webhook_suite_test.go` | `filepath.Join("..", "..", "..", "..")` |

Since `filepath.Join` is not constant-expressible, declare it as a `var` instead:

```go
var crdRenderRelativeRoot = filepath.Join("..", "..", "..")
```

Then change each suite's `CRDDirectoryPaths` from `[]string{filepath.Join("..", "..", "..", "config", "crd", "bases")}` to:

```go
		CRDDirectoryPaths:     []string{renderedCRDPath()},
```

- [ ] **Step 3: Fail loudly when the render is missing**

Immediately before `testEnv.Start()` in each suite, add:

```go
	if _, err := os.Stat(renderedCRDPath()); err != nil {
		Fail(fmt.Sprintf("%s is missing — run `make crd-render` (or `make test`, which does): %v",
			renderedCRDPath(), err))
	}
```

Add `"fmt"` and `"os"` to the imports where absent. Without this the suite fails with `ErrorIfCRDPathMissing` deep inside envtest and the fix is not obvious from the message.

- [ ] **Step 4: Document it**

In `docs/development.md`, in the testing section, add:

```markdown
### Why envtest reads `bin/crd-render`, not `config/crd/bases`

`config/crd/bases/` is controller-gen's output. It has no `spec.conversion`
stanza, because controller-gen has no marker that emits one — the conversion
webhook is wired by a kustomize patch under `config/crd/patches/`.

envtest can drive a conversion webhook: `WebhookInstallOptions` rewrites each
CRD's `clientConfig` to the locally-served webhook and injects the CA it
generated. But it only does that for a CRD that already declares
`strategy: Webhook`. Reading the bases would therefore have made every
conversion test pass while exercising no conversion at all.

`make crd-render` runs `kustomize build config/crd` into `bin/crd-render/`
(gitignored) and `make test` depends on it. If a suite fails with
"bin/crd-render is missing", run `make crd-render`.
```

- [ ] **Step 5: Test cycle**

```bash
cd /home/rmocq/Neura/.externals/frame
make crd-render
grep -c '^kind: CustomResourceDefinition' bin/crd-render/crds.yaml
make test
```

Expected: `8` CRDs rendered; `make test` PASS with `internal/controller` coverage unchanged from before this task. A coverage drop or a suite failure means a `..` count is wrong.

```bash
git status --short
```

Expected: only the `Makefile`, the four suites and `docs/development.md`. `bin/` must not appear — it is gitignored (`/bin/`).

- [ ] **Step 6: Commit**

```bash
git add Makefile internal/controller/frame/suite_test.go \
        internal/controller/services/suite_test.go \
        internal/webhook/frame/v1alpha1/webhook_suite_test.go \
        internal/webhook/services/v1alpha1/webhook_suite_test.go \
        docs/development.md
git commit -m "test: run envtest against kustomize-rendered CRDs

The four envtest suites loaded config/crd/bases, which is controller-gen's
output and has no spec.conversion stanza — controller-gen has no marker
that emits one; the conversion webhook is a kustomize patch.

envtest can drive a conversion webhook, rewriting clientConfig to the local
server and injecting its own CA, but only for a CRD that already declares
strategy: Webhook. Reading the bases would have made every conversion test
added later pass while exercising nothing.

make crd-render builds config/crd into bin/crd-render (gitignored) and make
test depends on it. Green-to-green: still one version, still eight CRDs,
coverage unchanged."
```

---
# Part 2 — The conversion-compatible change

> **Tasks 12 through 22 land together, on one branch, as one merge.** Everything that alters the shape or meaning of a field has to be in the change that introduces `v1beta1`, because `v1beta1` is the only artefact that can carry it. The Tier 2 validation tightenings (T1–T8) are in here too: they are not conversion-relevant in themselves, but they can never be tightened after the freeze, so they must be in the *first* `v1beta1`.
>
> Each task still ends green and is still independently reviewable — a reviewer can reject Task 15's `TalosSecretReference` shape while approving Task 13's FrameNode. They just cannot ship apart.

---

### Task 12: `api/frame/v1beta1`, the shared `ServiceClass`, and `FrameJob` (F1, F4, F5, F2, T3, T5, T7)

The first hub type. It carries the package, the shared enum type every other kind reuses, and the four FrameJob decisions.

**What changes on `FrameJob`:**

| Decision | Change |
|---|---|
| **F2** (owner) | `status.phase` **removed**. The `Phase` printer column is repointed at the `Ready` condition. |
| **F5** | `spec.namespace` **removed**. The Workflow is created in the FrameJob's own namespace. This closes the security review's confused-deputy finding I4: a principal who could create a FrameJob in one namespace could make the operator create an Argo Workflow in *any* namespace, referencing any WorkflowTemplate there, executed under that namespace's ServiceAccount. |
| **F4** | `spec.serviceClass` becomes the shared `ServiceClass` type; its default moves from the mutating webhook (`framejob_webhook.go:55-57`) into the CRD schema, staying `LOW`. |
| **T3** | `spec.parameters` gains `MaxProperties=64` and a value bound of `MaxLength=1024`. |
| **T5** | `spec.gpuCount` gains `Maximum=1024`. |
| **T7** | `spec.pipeline` gains `MaxLength=253` and the DNS-1123-subdomain pattern. |
| **F9** | `spec.pipeline` stays an **open** string. Frame does not enumerate other people's Argo WorkflowTemplates. |
| **R1** | `status.observedGeneration` carried over from Task 6. |

**Live objects:** two FrameJobs, both `namespace: default` while living in `default` — so `spec.namespace` is a no-op on every object that exists — both `gpuCount: 0`, one `serviceClass: LOW`. All four writers of `spec.namespace` were swept by the inventory: `config/samples/frame_v1alpha1_framejob.yaml:13`, `test/e2e/e2e_test.go:503` (set to `crNamespace`, the FrameJob's *own* namespace, so the spec proves nothing about the cross-namespace path it enables), and `deploy/samples/test-cluster/workloads.yaml` (which does not set it). Step 1 re-runs the sweep rather than trusting it.

**Files:**
- Create: `api/frame/v1beta1/groupversion_info.go` (scaffolded)
- Create: `api/frame/v1beta1/serviceclass.go`
- Create: `api/frame/v1beta1/framejob_types.go`
- Modify: `PROJECT` (via the CLI only)
- Modify: `cmd/main.go` (scaffold marker)

**Interfaces:**
- Consumes: nothing from earlier tasks except the `ObservedGeneration` shape Task 6 established.
- Produces, in package `github.com/rmocq/frame/api/frame/v1beta1` (import alias `framev1beta1` everywhere):
  - `type ServiceClass string` with `ServiceClassHigh`, `ServiceClassMedium`, `ServiceClassLow`
  - `type ParameterValue string`
  - `type FrameJob struct { metav1.TypeMeta; metav1.ObjectMeta; Spec FrameJobSpec; Status FrameJobStatus }`
  - `type FrameJobSpec struct { Pipeline string; ServiceClass ServiceClass; Priority string; GPUCount int32; Parameters map[string]ParameterValue; Suspended bool }` — **no `Namespace`**
  - `type FrameJobStatus struct { ObservedGeneration int64; Conditions []metav1.Condition; ArgoWorkflowName string; StartTime *metav1.Time; CompletionTime *metav1.Time; Message string }` — **no `Phase`**
  - `type FrameJobList`
  - `func (*FrameJob) Hub()` is added in Task 18, not here.
  Tasks 13–18 and 20 all import these names.

- [ ] **Step 1: Sweep for `spec.namespace` writers and readers**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn 'spec.namespace\|Spec\.Namespace\|spec:\s*$' --include='*.go' --include='*.ts' --include='*.tsx' --include='*.yaml' --include='*.md' \
  api internal cmd test config charts deploy src docs | grep -iv 'metadata' | grep -v node_modules | grep -v zz_generated
grep -rn 'namespace:' config/samples/ deploy/samples/ | grep -v 'metadata'
```

Expected writers: `config/samples/frame_v1alpha1_framejob.yaml:13`, `test/e2e/e2e_test.go:503`. Expected readers: `internal/controller/frame/framejob_controller.go:87,170,218`, `src/lib/frame-sdk.ts:723,2315`. All are handled — the controller in Task 20, the SDK in Task 22, the sample and e2e in Task 19 and Task 21 respectively. **If the sweep finds a writer not in this list, stop and add it to the plan before proceeding.**

- [ ] **Step 2: Scaffold the version**

```bash
cd /home/rmocq/Neura/.externals/frame
/home/rmocq/bin/kubebuilder create api --group frame --version v1beta1 --kind FrameJob \
  --resource --controller=false
```

Expected: `api/frame/v1beta1/groupversion_info.go` and `api/frame/v1beta1/framejob_types.go` created; `PROJECT` gains a `FrameJob`/`v1beta1` entry; `cmd/main.go` gains a scheme registration at the scaffold marker. Answer "no" to any prompt about creating a controller.

- [ ] **Step 3: Write the shared `ServiceClass` type**

Create `api/frame/v1beta1/serviceclass.go` (with the standard Apache licence header this repo uses — copy it from `api/frame/v1beta1/groupversion_info.go`):

```go
package v1beta1

// ServiceClass is the tier a Frame object belongs to. One named type, used by
// FrameJob, FrameNode, FrameResourceQuota and (through an import)
// FrameService — before v1beta1 the same three-valued concept was declared
// four times, no two alike: different enums, different optionality,
// different defaults, and a fourth answer again in the SDK's fallbacks (F4).
//
// The empty string is deliberately not a member. Absence means
// "unclassified"; an explicitly-empty tier and an omitted one were
// indistinguishable and only FrameNode ever allowed it, on no stored object.
//
// The *default* is deliberately not unified. FrameJob defaults to LOW and
// FrameService to MEDIUM, and both are right for their kind: an unspecified
// batch job should be preemptible, an unspecified long-lived service
// instance should not be the first thing evicted. FrameNode has no default
// at all — a node is discovered before it is classified, and defaulting it
// would classify hardware nobody has looked at. FrameResourceQuota requires
// it: a quota that does not say what it caps is meaningless.
//
// +kubebuilder:validation:Enum=HIGH;MEDIUM;LOW
type ServiceClass string

const (
	ServiceClassHigh   ServiceClass = "HIGH"
	ServiceClassMedium ServiceClass = "MEDIUM"
	ServiceClassLow    ServiceClass = "LOW"
)

// ParameterValue bounds a value in one of the free-form parameter maps.
// Declared as a named type rather than as a marker on the map, because
// controller-gen emits a value bound only from the map's value *type* —
// verified with controller-gen v0.20.1, which emits
// additionalProperties.maxLength from a named value type and silently drops
// markers on a named key type.
//
// +kubebuilder:validation:MaxLength=1024
type ParameterValue string
```

- [ ] **Step 4: Write the FrameJob hub type**

Replace the scaffolded body of `api/frame/v1beta1/framejob_types.go` (below the licence header) with:

```go
package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FrameJobSpec defines the desired state of FrameJob.
//
// There is deliberately no rule coupling gpuCount to serviceClass. One was
// once enforced in the validating webhook for three pipeline names and
// nowhere else; it was removed in this freeze (F8) because it tied how much
// hardware a job wants to how preemptible it is, two orthogonal properties.
// Scheduling priority is spec.priority's, projected onto a frame-*
// PriorityClass by the controller.
//
// There is also deliberately no `namespace` field. v1alpha1 had one, and it
// named the namespace the backing ArgoWorkflow was created in — which need
// not have been the FrameJob's own. With the operator holding cluster-wide
// workflows.argoproj.io CRUD, that made a principal who could create a
// FrameJob in one namespace able to make the operator create a Workflow in
// any namespace, referencing any WorkflowTemplate there, executed under that
// namespace's ServiceAccount. A confused deputy (security review I4). The
// Workflow is now created beside its FrameJob. The rule this follows: no
// field in a Frame spec may name a namespace the CR does not live in, except
// through a mechanism that records what it wrote and refuses to touch
// anything it did not create — see internal/services/binding.go, whose
// spec.binding.projectTo does pass that test.
type FrameJobSpec struct {
	// Pipeline names the Argo WorkflowTemplate to run.
	//
	// Deliberately an open string with only form validation (F9). It names an
	// object that lives in the cluster and that Frame does not own; a closed
	// enum would make Frame's API the gatekeeper for a namespace of objects
	// someone else creates. The validating webhook warns — and only warns —
	// when the value is outside the list Frame knows about.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Pipeline string `json:"pipeline"`

	// ServiceClass is the resource tier this job's workloads run at.
	//
	// The default is in the schema, not in the mutating webhook where
	// v1alpha1 kept it. A CRD default applies before CEL and before
	// webhooks; a mutating-webhook default applies after CRD defaults, and
	// having one kind default at each stage is the ordering subtlety that
	// produced the has()-guard bug in the pre-freeze cleanup. It also means
	// kubectl and the UI now agree on what "unspecified" is: they did not
	// before — the webhook filled LOW, the SDK sent MEDIUM.
	// +optional
	// +kubebuilder:default=LOW
	ServiceClass ServiceClass `json:"serviceClass,omitempty"`

	// Priority is the scheduling urgency, separate from the resource tier: a
	// HIGH-tier nightly batch can legitimately be low-priority. It maps onto
	// a frame-* PriorityClass through internal/scheduling.
	// +optional
	// +kubebuilder:validation:Enum=critical;high;medium;low
	// +kubebuilder:default=medium
	Priority string `json:"priority,omitempty"`

	// GPUCount is how many GPUs the job asks for.
	//
	// The ceiling is three orders of magnitude above the one physical GPU
	// this cluster has, so it constrains nothing real — it turns an
	// accidental gpuCount: 100000 into a validation error rather than an
	// unschedulable pod (T5). A ceiling can only ever be raised after the
	// freeze, never introduced.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1024
	// +kubebuilder:default=0
	GPUCount int32 `json:"gpuCount,omitempty"`

	// Parameters are passed straight into the Argo Workflow's arguments.
	//
	// Bounded as an envelope (T3): 64 entries, 1024 characters per value. Key
	// form is not constrained — see the note on ParameterValue and the plan's
	// "Open disagreements" for why a key pattern is not expressible here
	// without an unbounded-cost CEL rule.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	Parameters map[string]ParameterValue `json:"parameters,omitempty"`

	// Suspended pauses the underlying Argo Workflow when true. Set to false
	// to resume.
	// +optional
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
}

// FrameJobStatus defines the observed state of FrameJob.
//
// There is no status.phase (F2). Conditions are the whole story: Ready
// carries the phase as its reason and is True only on Completed. A single
// enum forces the API to pick one dimension of health out of several and
// cannot express "provisioned but degraded", which is why SIG-Architecture
// has been steering away from phase since 2019. v1alpha1 still serves a
// phase field; it is computed out of these conditions on the way down and is
// never stored.
type FrameJobStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the FrameJob resource.
	// Ready's reason is one of Submitted, Running, Suspended, Completed,
	// Failed.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ArgoWorkflowName is the name of the created Argo Workflow. It always
	// lives in the FrameJob's own namespace.
	// +optional
	ArgoWorkflowName string `json:"argoWorkflowName,omitempty"`

	// StartTime is when the job started running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the job completed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message provides additional information about the current status.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fj
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=".spec.pipeline"
// +kubebuilder:printcolumn:name="ServiceClass",type=string,JSONPath=".spec.serviceClass"
// +kubebuilder:printcolumn:name="GPUs",type=integer,JSONPath=".spec.gpuCount"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameJob is the Schema for the framejobs API
type FrameJob struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of FrameJob
	// +required
	Spec FrameJobSpec `json:"spec"`

	// status defines the observed state of FrameJob
	// +optional
	Status FrameJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameJobList contains a list of FrameJob
type FrameJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameJob{}, &FrameJobList{})
}
```

> The `PHASE` column survives F2: `kubectl get fj` still shows a phase, read off the `Ready` condition's reason rather than a stored field. That is what makes the removal cost a projection rather than a lost column.

- [ ] **Step 5: Mark `v1alpha1` deprecated**

In `api/frame/v1alpha1/framejob_types.go`, add above the existing `// +kubebuilder:object:root=true` on `FrameJob`:

```go
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 FrameJob is deprecated; use frame.plume-labs.io/v1beta1. spec.namespace is ignored — the Argo Workflow is created in the FrameJob's own namespace. status.phase is computed from status.conditions and is not stored."
```

and **remove** `+kubebuilder:storageversion` if present (it is not; `v1alpha1` never carried one, and the marker on `v1beta1` is what moves storage).

That warning string is the migration policy's only enforcement mechanism, and it costs nothing.

- [ ] **Step 6: Generate and inspect**

```bash
make manifests generate
```

Then verify the shape by hand — this is the moment a wrong marker becomes a frozen wrong marker:

```bash
python3 - <<'PY'
import yaml
d = yaml.safe_load(open("config/crd/bases/frame.plume-labs.io_framejobs.yaml"))
for v in d["spec"]["versions"]:
    props = v["schema"]["openAPIV3Schema"]["properties"]
    spec = props["spec"]["properties"]
    status = props["status"]["properties"]
    print(v["name"], "served=", v["served"], "storage=", v["storage"],
          "deprecated=", v.get("deprecated", False))
    print("   spec keys:", sorted(spec))
    print("   status keys:", sorted(status))
    print("   serviceClass:", spec["serviceClass"])
    print("   gpuCount:", spec["gpuCount"])
    print("   parameters:", spec["parameters"])
    print("   x-kubernetes-validations on spec:",
          spec.get("x-kubernetes-validations"),
          props["spec"].get("x-kubernetes-validations"))
PY
```

Assert, by eye:
- `v1alpha1`: `served: true`, `storage: false`, `deprecated: true`, spec still has `namespace`, status still has `phase`.
- `v1beta1`: `served: true`, `storage: true`, spec has **no** `namespace`, status has **no** `phase`.
- `v1beta1` `serviceClass`: `enum: [HIGH, MEDIUM, LOW]`, `default: LOW`, and **no** `""` member.
- `v1beta1` `gpuCount`: `maximum: 1024`.
- `v1beta1` `parameters`: `maxProperties: 64`, `additionalProperties: {type: string, maxLength: 1024}`.
- **`x-kubernetes-validations` on the `spec` node itself: `None` for both versions.** FrameJob has no object-level CEL and must not gain one.

- [ ] **Step 7: Test cycle**

```bash
make test
make lint 2>&1 | grep v1beta1 || echo "no new lint findings"
```

Expected: PASS. Nothing imports `v1beta1` yet, so the suite is unchanged; what this proves is that the two-version CRD installs into envtest and that `zz_generated.deepcopy.go` for the new package compiles.

> `make helm-parity` will now report the CRD version topology differing? No — `helm-sync-crds` copies `config/crd/bases/`, which already carries both versions, so both sides agree. The conversion stanza is still absent on both sides. Run `make helm-parity` to confirm green; if it fails, Task 10's check is comparing something it should not and must be narrowed before continuing.

- [ ] **Step 8: Commit**

```bash
git add api/frame/v1beta1/ api/frame/v1alpha1/framejob_types.go PROJECT cmd/main.go \
        config/crd/bases/frame.plume-labs.io_framejobs.yaml charts/frame/files/crds/
git commit -m "feat(api)!: add frame.plume-labs.io/v1beta1 and the FrameJob hub type

v1beta1 is the storage version and the conversion hub; v1alpha1 stays served
and is marked deprecated with a warning that names both behaviour changes.

FrameJob loses two fields. spec.namespace is gone: it named the namespace
the ArgoWorkflow was created in, need not have matched the FrameJob's own,
and with the operator holding cluster-wide workflow CRUD that let a
principal who could create a FrameJob anywhere make the operator run a
Workflow everywhere (security review I4). Both stored FrameJobs set it to
their own namespace, so it is a no-op on every object that exists.

status.phase is gone (F2, owner decision): conditions only. The PHASE column
survives, read off the Ready condition's reason.

Introduces the one ServiceClass type all four kinds now share — the same
three-valued concept was declared four times, no two alike — and moves
FrameJob's default from the mutating webhook into the schema, so kubectl and
the UI stop disagreeing about what unspecified means.

Tier 2 tightenings that can never be applied after the freeze: pipeline
bounded and DNS-1123-shaped but still open (Frame does not enumerate other
people's WorkflowTemplates), gpuCount capped at 1024, parameters capped at
64 entries of 1024 characters. All field-level, so ratcheting protects
stored objects. No object-level CEL added."
```

---

### Task 13: `FrameNode` v1beta1 (F4, F2, T1, T2)

| Decision | Change |
|---|---|
| **F4** | `spec.serviceClass` becomes `ServiceClass` — which **drops the `""` enum member**. Stays optional and **undefaulted**: a node is discovered before it is classified. |
| **F2** (owner) | `status.phase` removed; the `Phase` column reads `Ready.reason`. The controller's state machine, which currently branches on `fn.Status.Phase` at `framenode_controller.go:89,104`, moves onto `readyReason` in Task 20. |
| **T1** | `spec.rack` and `spec.zone` gain `MaxLength=63` and the Kubernetes label-value pattern. **This is a live latent bug, not just a missing bound**: the inventory verified that a FrameNode with `rack: "rack/one with spaces and a very … long value"` is admitted today, and `framenode_controller.go`'s node patch is then rejected by the apiserver, so the FrameNode never reaches `Online` and the error names label validation rather than the field the user set. |
| **T2** | `spec.ip`'s `Format=ip` marker is non-functional — `ip` is not a recognised OpenAPI format (the apiserver knows `ipv4`/`ipv6`/`cidr`) and unrecognised formats are silently dropped. Replaced with `MaxLength=45` + `XValidation:rule="isIP(self)"`, matching the `network.dns` fix from Fix Round 1. `isIP()` is confirmed present in the pinned `k8s.io/apiserver@v0.35.0` CEL environment. |
| **R1** | `status.observedGeneration` carried over. |

**Live objects:** three FrameNodes, racks `rack-01` and `local`, all with a non-empty `serviceClass`. All three pass T1's pattern and length. **Dropping `""` is safe because it is field-level and therefore ratcheted, and because no stored FrameNode uses it** — the inventory checked. Task 4 (the empty-label skip) has already landed, which is the ordering constraint that matters here.

**Files:**
- Create: `api/frame/v1beta1/framenode_types.go`
- Modify: `api/frame/v1alpha1/framenode_types.go` (deprecation warning)
- Modify: `PROJECT`, `cmd/main.go` (via the CLI)

**Interfaces:**
- Consumes: `ServiceClass` from Task 12.
- Produces, in `api/frame/v1beta1`:
  - `type NetworkSpec struct { Address string; Gateway string; DNS []string; VLAN *int32; Bond *string }`
  - `type DiskInfo struct { Name, Size, Type string }`, `type NICInfo struct { Name, MAC, Speed string }`
  - `type FrameNodeSpec struct { IP string; Role string; Network NetworkSpec; Disk string; RDMAInterface string; Hostname string; Rack string; Zone string; ServiceClass ServiceClass }`
  - `type FrameNodeStatus struct { ObservedGeneration int64; DiscoveredHostname string; DiscoveredTalosVersion string; DiscoveredDisks []DiskInfo; DiscoveredNICs []NICInfo; Conditions []metav1.Condition; KubeletVersion string; Capacity corev1.ResourceList; Allocatable corev1.ResourceList; NodeName string }` — **no `Phase`**
  - `type FrameNode`, `type FrameNodeList`

- [ ] **Step 1: Scaffold**

```bash
/home/rmocq/bin/kubebuilder create api --group frame --version v1beta1 --kind FrameNode \
  --resource --controller=false
```

- [ ] **Step 2: Write the type**

Replace the scaffolded body of `api/frame/v1beta1/framenode_types.go` below the licence header:

```go
package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetworkSpec defines network configuration for a node.
type NetworkSpec struct {
	// +optional
	Address string `json:"address,omitempty"`
	// +optional
	Gateway string `json:"gateway,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="self.all(x, isIP(x))",message="each network.dns entry must be a valid IP"
	DNS []string `json:"dns,omitempty"`
	// +optional
	VLAN *int32 `json:"vlan,omitempty"`
	// +optional
	Bond *string `json:"bond,omitempty"`
}

// DiskInfo describes a disk discovered on a node in maintenance mode.
type DiskInfo struct {
	Name string `json:"name"`
	Size string `json:"size"`
	Type string `json:"type"`
}

// NICInfo describes a network interface discovered on a node in maintenance mode.
type NICInfo struct {
	Name  string `json:"name"`
	MAC   string `json:"mac"`
	Speed string `json:"speed"`
}

// FrameNodeSpec defines the desired state of FrameNode.
//
// The spec-level rule below is inherited from v1alpha1 unchanged. It is one
// of four object-level CEL rules the freeze makes permanent, and it
// re-evaluates on every update to any part of spec — including one that only
// changes serviceClass. The inventory verified against the live cluster that
// it strands none of the three stored FrameNodes.
//
// +kubebuilder:validation:XValidation:rule="!has(self.disk) || size(self.disk) == 0 || (has(self.network) && has(self.network.address) && size(self.network.address) > 0 && has(self.network.gateway) && size(self.network.gateway) > 0 && has(self.network.dns) && self.network.dns.size() > 0)",message="network.address, network.gateway, and at least one network.dns entry are required once disk is set"
type FrameNodeSpec struct {
	// IP address of the node in maintenance mode.
	//
	// Validated by CEL, not by Format. v1alpha1 carried
	// +kubebuilder:validation:Format=ip, which does nothing: `ip` is not a
	// recognised OpenAPI format — the apiserver knows ipv4, ipv6 and cidr —
	// and unrecognised formats are silently dropped, so the only thing
	// rejecting a malformed address was the Go webhook's net.ParseIP. The
	// MaxLength is what lets the CEL cost estimator bound isIP(), the same
	// way network.dns needed it (T2).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="isIP(self)",message="ip must be a valid IP address"
	IP string `json:"ip"`

	// Role of the node in the cluster. Empty during initial discovery.
	// +kubebuilder:validation:Enum=controlplane;worker;""
	// +optional
	Role string `json:"role,omitempty"`

	// Network configuration. Set after discovery to trigger provisioning.
	// +optional
	Network NetworkSpec `json:"network,omitempty"`

	// Disk device for Talos installation (e.g. /dev/nvme0n1). Set after discovery.
	// +optional
	Disk string `json:"disk,omitempty"`

	// RDMAInterface names the RDMA device (e.g. ib0, mlx5_0). Its presence is
	// projected onto the Kubernetes Node as frame.plume-labs.io/rdma=true.
	// +optional
	RDMAInterface string `json:"rdmaInterface,omitempty"`

	// Hostname for the node.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Hostname string `json:"hostname,omitempty"`

	// Rack identifier for topology. Projected onto the Kubernetes Node as the
	// label frame.plume-labs.io/rack, which is why it is bounded like a label
	// value: v1alpha1 accepted anything, and the controller then wrote it as
	// a label, so an over-long or malformed rack made the node patch fail and
	// left the FrameNode permanently short of Online with an error naming
	// label validation rather than this field (T1).
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$"
	Rack string `json:"rack,omitempty"`

	// Zone identifier for topology. Projected onto the Kubernetes Node as
	// topology.kubernetes.io/zone; bounded for the same reason as Rack.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$"
	Zone string `json:"zone,omitempty"`

	// ServiceClass is the tier of hardware this node offers, projected onto
	// the Kubernetes Node as frame.plume-labs.io/service-class and read by
	// the inference provider's NodeSelector.
	//
	// Optional and deliberately undefaulted: a node is discovered before it
	// is classified, and defaulting it would classify hardware nobody has
	// looked at. v1alpha1's enum also admitted the empty string; that member
	// is gone, because absence already means unclassified. Dropping it is
	// field-level, so ratcheting protects stored objects — and none of the
	// three that exist uses it.
	// +optional
	ServiceClass ServiceClass `json:"serviceClass,omitempty"`
}

// FrameNodeStatus defines the observed state of FrameNode.
//
// No status.phase (F2). The Ready condition's reason carries it: Discovered,
// Provisioning, Online, Degraded or Offline.
type FrameNodeStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	DiscoveredHostname string `json:"discoveredHostname,omitempty"`

	// +optional
	DiscoveredTalosVersion string `json:"discoveredTalosVersion,omitempty"`

	// +optional
	DiscoveredDisks []DiskInfo `json:"discoveredDisks,omitempty"`

	// +optional
	DiscoveredNICs []NICInfo `json:"discoveredNICs,omitempty"`

	// Conditions represent the current state of the FrameNode resource.
	// Ready's reason is the node's lifecycle state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	KubeletVersion string `json:"kubeletVersion,omitempty"`

	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// +optional
	Allocatable corev1.ResourceList `json:"allocatable,omitempty"`

	// +optional
	NodeName string `json:"nodeName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fn
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.role"
// +kubebuilder:printcolumn:name="ServiceClass",type=string,JSONPath=".spec.serviceClass"
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=".spec.zone"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameNode is the Schema for the framenodes API
type FrameNode struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec FrameNodeSpec `json:"spec"`

	// +optional
	Status FrameNodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameNodeList contains a list of FrameNode
type FrameNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameNode{}, &FrameNodeList{})
}
```

- [ ] **Step 3: Deprecate `v1alpha1`**

Above `// +kubebuilder:object:root=true` on `api/frame/v1alpha1/framenode_types.go`'s `FrameNode`:

```go
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 FrameNode is deprecated; use frame.plume-labs.io/v1beta1. serviceClass no longer admits the empty string, and status.phase is computed from status.conditions and is not stored."
```

- [ ] **Step 4: Verify T1 and T2 against the live objects before generating**

```bash
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml
kubectl get framenodes -A -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.rack}{"\t"}{.spec.zone}{"\t"}{.spec.serviceClass}{"\t"}{.spec.ip}{"\n"}{end}'
```

Expected: three rows; every `rack`/`zone` at most 63 characters and matching `^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$`; every `serviceClass` one of HIGH/MEDIUM/LOW and none empty; every `ip` a valid address. Check each by eye against the pattern. **If any row fails, stop** — this is the check that keeps a Tier 2 tightening from stranding a stored object.

- [ ] **Step 5: Generate and inspect**

```bash
make manifests generate
python3 - <<'PY'
import yaml
d = yaml.safe_load(open("config/crd/bases/frame.plume-labs.io_framenodes.yaml"))
for v in d["spec"]["versions"]:
    p = v["schema"]["openAPIV3Schema"]["properties"]
    spec, status = p["spec"], p["status"]["properties"]
    print(v["name"], "storage=", v["storage"], "deprecated=", v.get("deprecated", False))
    print("   status keys:", sorted(status))
    print("   serviceClass:", spec["properties"]["serviceClass"])
    print("   rack:", spec["properties"]["rack"])
    print("   ip:", spec["properties"]["ip"])
    print("   spec-level CEL:", [r["message"] for r in spec.get("x-kubernetes-validations", [])])
PY
```

Assert: `v1beta1` status has no `phase`; `serviceClass` enum is exactly `[HIGH, MEDIUM, LOW]` with no `default`; `rack` has `maxLength: 64`? — no, **63** — and the pattern; `ip` has `maxLength: 45` and one `x-kubernetes-validations` entry, and **no** `format: ip`; the spec-level CEL list contains **exactly one** message, the inherited network-once-disk rule, on both versions.

- [ ] **Step 6: Test cycle**

```bash
make test
make helm-parity
make lint 2>&1 | grep framenode || echo "no new lint findings"
```

- [ ] **Step 7: Commit**

```bash
git add api/frame/v1beta1/framenode_types.go api/frame/v1alpha1/framenode_types.go \
        PROJECT cmd/main.go config/crd/bases/frame.plume-labs.io_framenodes.yaml charts/frame/files/crds/
git commit -m "feat(api)!: add the FrameNode v1beta1 hub type

serviceClass becomes the shared ServiceClass type, which drops the empty
string from its enum — absence already means unclassified, the member was
FrameNode's alone, and no stored FrameNode uses it. It stays optional and
undefaulted: a node is discovered before it is classified.

status.phase is gone (F2); the PHASE column reads the Ready condition's
reason.

Two Tier 2 fixes that can never be applied after the freeze. rack and zone
are bounded and label-shaped: they were unbounded but written straight onto
the Kubernetes Node as label values, so a malformed rack was admitted and
then made the node patch fail, leaving the FrameNode permanently short of
Online with an error naming label validation rather than the field — a live
latent bug, verified against the deployed webhook. And ip's Format=ip marker
was doing nothing at all: ip is not a recognised OpenAPI format and
unrecognised formats are silently dropped, so it becomes an isIP() CEL rule
with the MaxLength the cost estimator needs.

All three stored FrameNodes were checked against every new bound before
generating. Field-level, so ratcheted. The one inherited object-level rule
is unchanged."
```

---

### Task 14: `FrameResourceQuota` and `SchedulingPolicy` v1beta1 (F4, T5, R1)

Two kinds in one task: neither loses a field, both carry an inherited object-level rule, and the only decisions are `ServiceClass` and two numeric ceilings. Splitting them would produce two near-identical reviews.

| Decision | Change |
|---|---|
| **F4** | `FrameResourceQuota.spec.serviceClass` becomes `ServiceClass`, staying **`Required`**. |
| **T5** | `FrameResourceQuota.spec.maxGPUs` gains `Maximum=1024`; `SchedulingPolicy.spec.queueWeight` gains `Maximum=1000000`. `SchedulingPolicy.spec.priorityValue` is already bounded (`-2147483648`..`1000000000`), so the absence elsewhere was inconsistency rather than policy. |
| **F2** | Neither kind has a `status.phase`; both are already conditions-only. Nothing to remove. |
| **R1**, **Task 7** | `status.observedGeneration` on both; `status.used` and `status.namespaces` on FrameResourceQuota. |

**Live objects:** three FrameResourceQuotas and one SchedulingPolicy. The quota objects sit at `generation: 3` with `Ready` at `observedGeneration: 2`. The SchedulingPolicy has `preemption: true` + `priorityClass: neura-high`, which satisfies its inherited object-level rule. Step 1 checks every stored value against the two new ceilings.

**Files:**
- Create: `api/frame/v1beta1/frameresourcequota_types.go`, `api/frame/v1beta1/schedulingpolicy_types.go`
- Modify: `api/frame/v1alpha1/frameresourcequota_types.go`, `schedulingpolicy_types.go` (deprecation warnings)
- Modify: `PROJECT`, `cmd/main.go` (via the CLI)

**Interfaces:**
- Consumes: `ServiceClass` from Task 12.
- Produces, in `api/frame/v1beta1`:
  - `type FrameResourceQuotaSpec struct { ServiceClass ServiceClass; MaxGPUs int32; MaxCPU *resource.Quantity; MaxMemory *resource.Quantity; MaxJobs int32 }`
  - `type FrameResourceQuotaStatus struct { ObservedGeneration int64; Conditions []metav1.Condition; Used corev1.ResourceList; Namespaces int32 }`
  - `type SchedulingPolicySpec struct { Scheduler string; QueueName string; PriorityClass string; Preemption bool; PriorityValue *int32; QueueWeight *int32 }`
  - `type SchedulingPolicyStatus struct { ObservedGeneration int64; Conditions []metav1.Condition }`
  - plus `FrameResourceQuota`, `FrameResourceQuotaList`, `SchedulingPolicy`, `SchedulingPolicyList`

- [ ] **Step 1: Check the stored values against the new ceilings**

```bash
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml
kubectl get frameresourcequotas -A -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.serviceClass}{"\t"}{.spec.maxGPUs}{"\n"}{end}'
kubectl get schedulingpolicies -A -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.queueWeight}{"\t"}{.spec.priorityValue}{"\n"}{end}'
```

Expected: every `maxGPUs` ≤ 1024, every `queueWeight` ≤ 1000000, every `serviceClass` one of HIGH/MEDIUM/LOW. **If any exceeds a ceiling, stop and raise the ceiling in this plan rather than stranding the object.**

- [ ] **Step 2: Scaffold both**

```bash
/home/rmocq/bin/kubebuilder create api --group frame --version v1beta1 --kind FrameResourceQuota --resource --controller=false
/home/rmocq/bin/kubebuilder create api --group frame --version v1beta1 --kind SchedulingPolicy --resource --controller=false
```

- [ ] **Step 3: Write `FrameResourceQuota`**

Replace the scaffolded body of `api/frame/v1beta1/frameresourcequota_types.go` below the licence header:

```go
package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FrameResourceQuotaSpec defines the desired state of FrameResourceQuota.
//
// The spec-level rule is inherited from v1alpha1 unchanged and is one of the
// four object-level CEL rules the freeze makes permanent. The inventory
// verified it with a server-side dry-run patch changing maxJobs on
// quota-high — which forces the rule to re-run — and the patch succeeded, so
// it strands none of the three stored objects.
//
// +kubebuilder:validation:XValidation:rule="(has(self.maxGPUs) && self.maxGPUs > 0) || has(self.maxCPU) || has(self.maxMemory) || (has(self.maxJobs) && self.maxJobs > 0)",message="at least one of maxGPUs, maxCPU, maxMemory, or maxJobs must be set"
type FrameResourceQuotaSpec struct {
	// ServiceClass this quota applies to. Required: a quota that does not say
	// what it caps is meaningless, which is why this one member of the shared
	// enum is not optional.
	// +kubebuilder:validation:Required
	ServiceClass ServiceClass `json:"serviceClass"`

	// MaxGPUs is the maximum number of GPUs allocatable across all jobs in
	// this service class. Capped for the same reason as FrameJob.gpuCount
	// (T5): three orders of magnitude above the one physical card, so it
	// constrains nothing real and turns a typo into a validation error.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1024
	MaxGPUs int32 `json:"maxGPUs,omitempty"`

	// MaxCPU is the maximum total CPU allocatable.
	// +optional
	MaxCPU *resource.Quantity `json:"maxCPU,omitempty"`

	// MaxMemory is the maximum total memory allocatable.
	// +optional
	MaxMemory *resource.Quantity `json:"maxMemory,omitempty"`

	// MaxJobs is the maximum number of FrameJob objects that may exist in a
	// namespace of this service class. Projected as the object-count quota
	// count/framejobs.frame.plume-labs.io, which the apiserver enforces on
	// creation. Completed FrameJobs keep counting until they are deleted.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxJobs int32 `json:"maxJobs,omitempty"`
}

// FrameResourceQuotaStatus defines the observed state of FrameResourceQuota.
type FrameResourceQuotaStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Used is the sum of status.used across every corev1.ResourceQuota this
	// object projects into, keyed as buildResourceList writes them. A key no
	// namespace reported is absent, not zero.
	// +optional
	Used corev1.ResourceList `json:"used,omitempty"`

	// Namespaces is how many namespaces this quota currently projects into.
	// +optional
	Namespaces int32 `json:"namespaces,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ServiceClass",type=string,JSONPath=".spec.serviceClass"
// +kubebuilder:printcolumn:name="Namespaces",type=integer,JSONPath=".status.namespaces"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameResourceQuota is the Schema for the frameresourcequotas API
type FrameResourceQuota struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec FrameResourceQuotaSpec `json:"spec"`

	// +optional
	Status FrameResourceQuotaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameResourceQuotaList contains a list of FrameResourceQuota
type FrameResourceQuotaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameResourceQuota `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameResourceQuota{}, &FrameResourceQuotaList{})
}
```

- [ ] **Step 4: Write `SchedulingPolicy`**

Replace the scaffolded body of `api/frame/v1beta1/schedulingpolicy_types.go` below the licence header:

```go
package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SchedulingPolicySpec defines the desired state of SchedulingPolicy.
//
// The spec-level rule is inherited from v1alpha1 unchanged, one of the four
// the freeze makes permanent. The inventory verified with a server-side
// dry-run patch changing queueWeight on neura-default — which forces it to
// re-run — that it does not strand the one stored object (preemption: true
// with priorityClass: neura-high).
//
// +kubebuilder:validation:XValidation:rule="!self.preemption || (has(self.priorityClass) && size(self.priorityClass) > 0)",message="priorityClass is required when preemption is true"
type SchedulingPolicySpec struct {
	// Scheduler selects the scheduler implementation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=volcano;yunikorn;default
	Scheduler string `json:"scheduler"`

	// QueueName is the Volcano/YuniKorn queue to submit jobs to. Not an enum:
	// it names an externally-created Queue object, but it is still a
	// Kubernetes object name, hence the pattern. The pattern accepts empty:
	// the controller branches on "" to skip queue reconciliation, and the
	// SDK's create form sends this field unconditionally, so a user clearing
	// it must still be able to save.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?$"
	QueueName string `json:"queueName,omitempty"`

	// PriorityClass is the default Kubernetes PriorityClass for jobs under
	// this policy. Accepts empty for the same reason as QueueName.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?$"
	PriorityClass string `json:"priorityClass,omitempty"`

	// Preemption allows higher-priority jobs to preempt lower-priority ones.
	// +optional
	// +kubebuilder:default=false
	Preemption bool `json:"preemption,omitempty"`

	// PriorityValue is the integer value for the Kubernetes PriorityClass.
	// Ignored when PriorityClass is empty. Higher values = higher priority;
	// system pods use 2000000000.
	// +optional
	// +kubebuilder:validation:Minimum=-2147483648
	// +kubebuilder:validation:Maximum=1000000000
	PriorityValue *int32 `json:"priorityValue,omitempty"`

	// QueueWeight is the relative weight of the Volcano/YuniKorn queue.
	//
	// The ceiling is new (T5). PriorityValue was already bounded, so its
	// absence here was inconsistency rather than policy, and a bound can
	// only ever be introduced before the freeze — adding one afterwards
	// rejects objects that were valid the day before.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000000
	QueueWeight *int32 `json:"queueWeight,omitempty"`
}

// SchedulingPolicyStatus defines the observed state of SchedulingPolicy.
type SchedulingPolicyStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Scheduler",type=string,JSONPath=".spec.scheduler"
// +kubebuilder:printcolumn:name="Queue",type=string,JSONPath=".spec.queueName"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// SchedulingPolicy is the Schema for the schedulingpolicies API
type SchedulingPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec SchedulingPolicySpec `json:"spec"`

	// +optional
	Status SchedulingPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SchedulingPolicyList contains a list of SchedulingPolicy
type SchedulingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SchedulingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SchedulingPolicy{}, &SchedulingPolicyList{})
}
```

- [ ] **Step 5: Deprecate both `v1alpha1` kinds**

Above `// +kubebuilder:object:root=true` on each of `api/frame/v1alpha1/frameresourcequota_types.go`'s `FrameResourceQuota` and `schedulingpolicy_types.go`'s `SchedulingPolicy`:

```go
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 FrameResourceQuota is deprecated; use frame.plume-labs.io/v1beta1."
```

```go
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 SchedulingPolicy is deprecated; use frame.plume-labs.io/v1beta1."
```

- [ ] **Step 6: Generate and inspect**

```bash
make manifests generate
python3 - <<'PY'
import yaml
for f, checks in [
    ("frameresourcequotas", ["serviceClass", "maxGPUs"]),
    ("schedulingpolicies", ["queueWeight", "priorityValue"]),
]:
    d = yaml.safe_load(open(f"config/crd/bases/frame.plume-labs.io_{f}.yaml"))
    for v in d["spec"]["versions"]:
        spec = v["schema"]["openAPIV3Schema"]["properties"]["spec"]
        print(f, v["name"], "storage=", v["storage"], "deprecated=", v.get("deprecated", False))
        for k in checks:
            print("   ", k, spec["properties"].get(k))
        print("    spec-level CEL:", [r["message"] for r in spec.get("x-kubernetes-validations", [])])
PY
```

Assert: `maxGPUs` has `maximum: 1024`; `queueWeight` has `maximum: 1000000`; `serviceClass` is the three-member enum and is in `required`; each kind's spec-level CEL list has **exactly one** entry, on both versions.

- [ ] **Step 7: Test cycle**

```bash
make test
make helm-parity
make lint 2>&1 | grep -E 'frameresourcequota|schedulingpolicy' || echo "no new lint findings"
```

- [ ] **Step 8: Commit**

```bash
git add api/frame/v1beta1/frameresourcequota_types.go api/frame/v1beta1/schedulingpolicy_types.go \
        api/frame/v1alpha1/frameresourcequota_types.go api/frame/v1alpha1/schedulingpolicy_types.go \
        PROJECT cmd/main.go config/crd/bases/ charts/frame/files/crds/
git commit -m "feat(api): add the FrameResourceQuota and SchedulingPolicy hub types

Neither kind loses a field: both were already conditions-only, so F2 costs
them nothing. serviceClass becomes the shared type, staying Required — a
quota that does not say what it caps is meaningless.

Two ceilings that can only ever be introduced before the freeze (T5):
maxGPUs at 1024 and queueWeight at 1000000. priorityValue was already
bounded, so their absence was inconsistency rather than policy. Every
stored value was checked first — three quotas, one policy, all well inside.

Both inherited object-level CEL rules carry over verbatim; the inventory
proved each against a stored object with a server-side dry-run patch that
forces it to re-run."
```

---
### Task 15: `TalosMachineConfig` and `TalosUpgrade` v1beta1 (F6, F7)

The two kinds that share `TalosSecretReference`, and the cheapest changes in the whole freeze: **zero objects of either kind exist on any cluster**, so there is literally nothing to convert, and the field is set nowhere in `config/samples/`, `deploy/` or `test/e2e/`. This will never again be this cheap.

| Decision | Change |
|---|---|
| **F6** | `TalosSecretReference.Namespace` **removed**. The Secret is resolved in the CR's own namespace. The blast radius this closes is worse than F5's: the manager holds cluster-wide `get secrets`, so a CR in a namespace the caller controls could make the operator read Talos client certificates — node root credentials — out of any namespace. |
| **F7** | `TalosSecretReference.Name` becomes **`Required`**. There is no code path where an unnamed Talos secret means anything; today the whole struct is `Required` but may be empty, and `buildTalosClient` then looks up a Secret named `""` and fails at reconcile time with a condition instead of at admission. |
| **F2** | Neither kind has a `status.phase`. Nothing to remove. |
| **R1** | `status.observedGeneration` on both. |

**A note on the type's shape.** F6 observes that with `Namespace` gone the type "can be `corev1.LocalObjectReference`". It cannot, jointly with F7: `corev1.LocalObjectReference.Name` is `+optional` and a kubebuilder marker cannot be attached to a subfield of an external `k8s.io/api` type — which is the exact reason `TalosSecretReference` was created in the first place. The local one-field type is kept, which satisfies F6's substance (the cross-namespace reach is gone) and F7's letter (`Name` is required) with **no CEL at all**. This deviation from F6's parenthetical is listed in "Open disagreements".

**Files:**
- Create: `api/frame/v1beta1/talosmachineconfig_types.go`, `api/frame/v1beta1/talosupgrade_types.go`
- Modify: `api/frame/v1alpha1/talosmachineconfig_types.go`, `talosupgrade_types.go` (deprecation warnings)
- Modify: `PROJECT`, `cmd/main.go` (via the CLI)

**Interfaces:**
- Consumes: nothing from Tasks 12–14 (these kinds have no `serviceClass`).
- Produces, in `api/frame/v1beta1`:
  - `type TalosSecretReference struct { Name string }` — **one field, required**
  - `type TalosMachineConfigSpec struct { NodeName string; TalosEndpoint string; TalosSecretRef TalosSecretReference; ConfigPatch string; ConfigPatchRef *corev1.ConfigMapKeySelector }`
  - `type TalosMachineConfigStatus struct { ObservedGeneration int64; Conditions []metav1.Condition }`
  - `type TalosUpgradeSpec struct { NodeName string; TalosEndpoint string; TalosSecretRef TalosSecretReference; Image string }`
  - `type TalosUpgradeStatus struct { ObservedGeneration int64; Conditions []metav1.Condition }`
  - plus the four root/list types.
  Task 20 rewrites `buildTalosClient` against `TalosSecretReference` without a `Namespace`.

- [ ] **Step 1: Confirm nothing sets the namespace, anywhere**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn 'talosSecretRef\|TalosSecretRef' --include='*.go' --include='*.ts' --include='*.tsx' --include='*.yaml' --include='*.md' \
  api internal cmd test config charts deploy src docs | grep -v node_modules | grep -v zz_generated
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml
kubectl get talosmachineconfigs,talosupgrades -A
```

Expected: readers only — `internal/controller/frame/talos_client.go` (`buildTalosClient`), the two controllers, `src/lib/frame-sdk.ts`'s `frame.talos` read-only panel — and **`No resources found`** from both `kubectl get`s. **If either kind has a stored object, stop**: F6 and F7 were costed on there being none, and a stored object with a cross-namespace ref or an empty name needs a decision this plan does not contain.

- [ ] **Step 2: Scaffold both**

```bash
/home/rmocq/bin/kubebuilder create api --group frame --version v1beta1 --kind TalosMachineConfig --resource --controller=false
/home/rmocq/bin/kubebuilder create api --group frame --version v1beta1 --kind TalosUpgrade --resource --controller=false
```

- [ ] **Step 3: Write `TalosMachineConfig`**

Replace the scaffolded body of `api/frame/v1beta1/talosmachineconfig_types.go` below the licence header:

```go
package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TalosSecretReference names the Secret holding Talos client certificates
// (keys: ca.crt, client.crt, client.key), in the referring CR's own
// namespace.
//
// v1alpha1 had a Namespace field here, and it reached anywhere: the manager
// holds cluster-wide `get secrets`, so a CR in a namespace the caller
// controls could make the operator read Talos client certificates — node
// root credentials — out of any namespace, and any failure surfacing the
// Secret's contents in a condition or an Event would exfiltrate them. It is
// gone (F6). The rule: no field in a Frame spec may name a namespace the CR
// does not live in, except through a mechanism that records what it wrote
// and refuses to touch anything it did not create.
//
// Name is Required (F7). v1alpha1 left it optional to mirror
// corev1.SecretReference, which is optional because it is a general-purpose
// type used where a name may legitimately be absent. This is not one of
// those contexts: there is no code path where an unnamed Talos secret means
// anything, and leaving it optional meant buildTalosClient looked up a
// Secret named "" and failed at reconcile time with a condition rather than
// at admission.
//
// The type stays local rather than becoming corev1.LocalObjectReference,
// which it now structurally matches: a kubebuilder marker cannot be attached
// to a subfield of an external k8s.io/api type, so Name could not be made
// Required there — the same limitation that created this type.
//
// +structType=atomic
type TalosSecretReference struct {
	// Name of the referenced Secret, in this CR's own namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Name string `json:"name"`
}

// TalosMachineConfigSpec defines the desired state of TalosMachineConfig.
//
// The spec-level rule is inherited from v1alpha1 unchanged, one of the four
// the freeze makes permanent. Zero TalosMachineConfigs exist on any cluster,
// so it strands nothing by construction.
//
// +kubebuilder:validation:XValidation:rule="(has(self.configPatch) && size(self.configPatch) > 0) != has(self.configPatchRef)",message="exactly one of configPatch or configPatchRef must be set"
type TalosMachineConfigSpec struct {
	// NodeName is the Kubernetes node name to apply the config patch to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	NodeName string `json:"nodeName"`

	// TalosEndpoint is the Talos API endpoint (host:port) for this node. The
	// bracketed form (e.g. [fd00::1]:50000) is accepted for IPv6, same as
	// net.SplitHostPort in the webhook check this mirrors.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^(\\[[0-9a-fA-F:]+\\]|[a-zA-Z0-9.-]+):[0-9]+$"
	TalosEndpoint string `json:"talosEndpoint"`

	// TalosSecretRef references the Secret containing Talos client
	// certificates, in this CR's own namespace.
	// +kubebuilder:validation:Required
	TalosSecretRef TalosSecretReference `json:"talosSecretRef"`

	// ConfigPatch is an inline Talos config patch document (YAML).
	// +optional
	ConfigPatch string `json:"configPatch,omitempty"`

	// ConfigPatchRef references a ConfigMap containing the patch under key
	// "patch.yaml".
	// +optional
	ConfigPatchRef *corev1.ConfigMapKeySelector `json:"configPatchRef,omitempty"`
}

// TalosMachineConfigStatus defines the observed state of TalosMachineConfig.
type TalosMachineConfigStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="NodeName",type=string,JSONPath=".spec.nodeName"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// TalosMachineConfig is the Schema for the talosmachineconfigs API
type TalosMachineConfig struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec TalosMachineConfigSpec `json:"spec"`

	// +optional
	Status TalosMachineConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TalosMachineConfigList contains a list of TalosMachineConfig
type TalosMachineConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosMachineConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosMachineConfig{}, &TalosMachineConfigList{})
}
```

- [ ] **Step 4: Write `TalosUpgrade`**

Replace the scaffolded body of `api/frame/v1beta1/talosupgrade_types.go` below the licence header:

```go
package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TalosUpgradeSpec defines the desired state of TalosUpgrade.
type TalosUpgradeSpec struct {
	// NodeName is the Kubernetes node name to upgrade.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	NodeName string `json:"nodeName"`

	// TalosEndpoint is the Talos API endpoint (host:port) for this node.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern="^(\\[[0-9a-fA-F:]+\\]|[a-zA-Z0-9.-]+):[0-9]+$"
	TalosEndpoint string `json:"talosEndpoint"`

	// TalosSecretRef references the Secret containing Talos client
	// certificates, in this CR's own namespace. See TalosSecretReference in
	// talosmachineconfig_types.go for why it no longer names one.
	// +kubebuilder:validation:Required
	TalosSecretRef TalosSecretReference `json:"talosSecretRef"`

	// Image is the Talos installer image to upgrade to
	// (e.g. ghcr.io/siderolabs/installer:v1.8.0).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="self.split('/')[self.split('/').size()-1].contains(':')",message="image must include a tag (e.g. installer:v1.8.0)"
	Image string `json:"image"`
}

// TalosUpgradeStatus defines the observed state of TalosUpgrade.
type TalosUpgradeStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="NodeName",type=string,JSONPath=".spec.nodeName"
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=".spec.image"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// TalosUpgrade is the Schema for the talosupgrades API
type TalosUpgrade struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec TalosUpgradeSpec `json:"spec"`

	// +optional
	Status TalosUpgradeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TalosUpgradeList contains a list of TalosUpgrade
type TalosUpgradeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosUpgrade `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosUpgrade{}, &TalosUpgradeList{})
}
```

- [ ] **Step 5: Deprecate both `v1alpha1` kinds**

```go
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 TalosMachineConfig is deprecated; use frame.plume-labs.io/v1beta1. talosSecretRef.namespace is ignored — the Secret is read from this CR's own namespace — and talosSecretRef.name is now required."
```

```go
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 TalosUpgrade is deprecated; use frame.plume-labs.io/v1beta1. talosSecretRef.namespace is ignored — the Secret is read from this CR's own namespace — and talosSecretRef.name is now required."
```

- [ ] **Step 6: Generate and inspect**

```bash
make manifests generate
python3 - <<'PY'
import yaml
for f in ("talosmachineconfigs", "talosupgrades"):
    d = yaml.safe_load(open(f"config/crd/bases/frame.plume-labs.io_{f}.yaml"))
    for v in d["spec"]["versions"]:
        spec = v["schema"]["openAPIV3Schema"]["properties"]["spec"]
        ref = spec["properties"]["talosSecretRef"]
        print(f, v["name"], "storage=", v["storage"], "deprecated=", v.get("deprecated", False))
        print("    talosSecretRef props:", sorted(ref.get("properties", {})),
              "required:", ref.get("required"))
        print("    spec-level CEL:", [r["message"] for r in spec.get("x-kubernetes-validations", [])])
PY
```

Assert: `v1beta1`'s `talosSecretRef` has **only** `name` in its properties and `required: [name]`; `v1alpha1`'s still has `name` and `namespace` with no `required`. `TalosMachineConfig`'s spec-level CEL has exactly one entry on both versions; `TalosUpgrade`'s has **none** on either (its `Image` rule is field-level).

- [ ] **Step 7: Test cycle**

```bash
make test
make helm-parity
make lint 2>&1 | grep -E 'talosmachineconfig|talosupgrade' || echo "no new lint findings"
```

- [ ] **Step 8: Commit**

```bash
git add api/frame/v1beta1/talosmachineconfig_types.go api/frame/v1beta1/talosupgrade_types.go \
        api/frame/v1alpha1/talosmachineconfig_types.go api/frame/v1alpha1/talosupgrade_types.go \
        PROJECT cmd/main.go config/crd/bases/ charts/frame/files/crds/
git commit -m "feat(api)!: drop TalosSecretReference.namespace and require its name

The Secret is now always read from the referring CR's own namespace.
v1alpha1's namespace field reached anywhere: the manager holds cluster-wide
get secrets, so a CR in a namespace the caller controls could make the
operator read Talos client certificates — node root credentials — out of
any namespace (F6).

name becomes Required (F7). It was optional to mirror
corev1.SecretReference, which is optional because it is a general-purpose
type; there is no Frame code path where an unnamed Talos secret means
anything, and leaving it optional meant buildTalosClient looked up a Secret
named \"\" and failed at reconcile time with a condition instead of at
admission.

The type stays local rather than collapsing to
corev1.LocalObjectReference, which it now structurally matches: a marker
cannot be attached to a subfield of an external k8s.io/api type, so name
could not be made required there — the same limitation that created this
type.

Zero objects of either kind exist on any cluster and the field is set
nowhere in config/samples, deploy/ or test/e2e, so there is nothing to
convert and nothing to strand."
```

---

### Task 16: `FrameUser` v1beta1 (F11, T6)

| Decision | Change |
|---|---|
| **F11** | `spec.passwordHash` moves to **`status.passwordHash`**, matching `status.credentials`. Raised as M1 in the security review: the argon2id PHC string lives in `spec`, readable by anything with `get frameusers`, while the WebAuthn public key material is deliberately in `status` so "an admin editing an account cannot corrupt a key by hand". The asymmetry is the finding — the *public* key material is protected from hand-editing and the *password hash* is not. |
| **T6** | `spec.email` gains `MaxLength=254` (the RFC 5321 limit). The pattern stays deliberately loose, which is right for email — but this value **becomes the Kubernetes username** in the token authd issues, and an unbounded username in an audit log and an RBAC subject is a poor idea. |
| **R1** | `status.observedGeneration`. |

**Where this stops short, and why.** Option 2 — moving the hash into a `Secret` — is the correct destination and is recorded as such, not done here: authd's store gains a second object to keep consistent, and the last-admin webhook guard at `frameuser_webhook.go:70-83` would have to survive a partially-written pair. Option 1 gets the RBAC separation immediately, is a pure field move within the same object, and is trivially expressible in the conversion webhook.

**Live objects:** **zero FrameUsers exist**, so there is nothing to migrate. The only consumers are `internal/authd/server_session.go:73-76` (reads it) and `internal/authd/server_test.go` (writes it in fixtures).

**Files:**
- Create: `api/frame/v1beta1/frameuser_types.go`
- Modify: `api/frame/v1alpha1/frameuser_types.go` (deprecation warning)
- Modify: `config/samples/frame_v1alpha1_frameuser.yaml` (comment only — it never set the field)
- Modify: `PROJECT`, `cmd/main.go` (via the CLI)

**Interfaces:**
- Consumes: nothing.
- Produces, in `api/frame/v1beta1`:
  - `const RoleAdmin = "admin"`, `RoleOperator`, `RoleViewer`, `PasswordEnabled = "enabled"`, `PasswordDisabled = "disabled"` (moved from `v1alpha1`, same values)
  - `type WebAuthnCredential struct { ID string; PublicKey string; SignCount uint32; AddedAt metav1.Time; Label string }`
  - `type FrameUserSpec struct { Email string; Role string; PasswordAuth string }` — **no `PasswordHash`**
  - `type FrameUserStatus struct { ObservedGeneration int64; PasswordHash string; Credentials []WebAuthnCredential }`
  - `type FrameUser`, `type FrameUserList`
  Task 20 rewrites `internal/authd` against `Status.PasswordHash`; Task 23's RBAC tiers depend on the hash being on the `status` subresource.

- [ ] **Step 1: Sweep for every reader and writer of `passwordHash`**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn 'PasswordHash\|passwordHash' --include='*.go' --include='*.ts' --include='*.tsx' --include='*.yaml' --include='*.md' \
  api internal cmd test config charts deploy src docs | grep -v node_modules | grep -v zz_generated | grep -v '/bases/' | grep -v 'files/crds'
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml
kubectl get frameusers -A
```

Expected: `internal/authd/server_session.go:73,76` (the only production reader), `internal/authd/server_test.go` (fixtures at `:257,270,332,370,416`), `api/frame/v1alpha1/frameuser_types.go`, and a comment in `config/samples/frame_v1alpha1_frameuser.yaml`. Note that `dummyPasswordHash`, `parsePasswordHash` and `hashIsUsable` in `internal/authd/password.go` are about the *hash format*, not the field, and do not change. `kubectl get frameusers` must report `No resources found`.

- [ ] **Step 2: Scaffold**

```bash
/home/rmocq/bin/kubebuilder create api --group frame --version v1beta1 --kind FrameUser --resource --controller=false
```

- [ ] **Step 3: Write the type**

Replace the scaffolded body of `api/frame/v1beta1/frameuser_types.go` below the licence header:

```go
package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"

	PasswordEnabled  = "enabled"
	PasswordDisabled = "disabled"
)

// WebAuthnCredential is one enrolled authenticator (a YubiKey, a phone
// passkey). Public data only: the private key never leaves the device.
type WebAuthnCredential struct {
	// ID is the base64url credential ID reported by the authenticator.
	ID string `json:"id"`
	// PublicKey is the base64-encoded COSE public key.
	PublicKey string `json:"publicKey"`
	// SignCount is the authenticator's counter as of the last successful
	// assertion. A value that fails to advance signals a cloned credential.
	SignCount uint32 `json:"signCount"`
	// AddedAt records enrolment time, so a user can tell two keys apart.
	AddedAt metav1.Time `json:"addedAt"`
	// Label is a human name for the key, e.g. "YubiKey 5C".
	// +optional
	Label string `json:"label,omitempty"`
}

type FrameUserSpec struct {
	// Email identifies the account and becomes the Kubernetes username in the
	// token authd issues.
	//
	// The pattern is deliberately loose, which is right for email — the
	// grammar RFC 5322 actually specifies is not something a CRD should try
	// to enforce. The length is not: this value ends up as an RBAC subject
	// and in every audit-log entry, and an unbounded username there is a
	// poor idea. 254 is the RFC 5321 limit (T6).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=254
	// +kubebuilder:validation:Pattern=`^[^@[:space:]]+@[^@[:space:]]+$`
	Email string `json:"email"`

	// Role decides which group the issued token carries.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=admin;operator;viewer
	Role string `json:"role"`

	// PasswordAuth controls whether this account may sign in with a password
	// at all. Defaults to disabled: an account is passkey-only unless someone
	// deliberately opens the other door.
	// +optional
	// +kubebuilder:validation:Enum=enabled;disabled
	// +kubebuilder:default=disabled
	PasswordAuth string `json:"passwordAuth,omitempty"`
}

// FrameUserStatus holds everything authd owns.
//
// PasswordHash lives here, not in spec (F11). It is credential material, and
// in spec it was readable by anything holding `get frameusers` and writable
// by anything holding `patch frameusers` — while the WebAuthn *public* key
// material sat safely in status, deliberately, so that an admin editing an
// account could not corrupt a key by hand. That asymmetry was the finding
// (security review M1): the public material was protected from hand-editing
// and the password hash was not. On status, only authd's
// frameusers/status grant can write it, and the viewer and editor tiers can
// be denied the subresource entirely.
//
// The destination is a Secret, not a status field — at-rest encryption and
// audit treatment a CR field does not have. That is a real design change
// (authd's store gains a second object to keep consistent, and the
// last-admin webhook guard would have to survive a partially-written pair),
// so it is recorded here rather than done in the freeze.
type FrameUserStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// PasswordHash is an argon2id PHC string, written only by authd. It is
	// meaningless while spec.passwordAuth is disabled.
	// +optional
	PasswordHash string `json:"passwordHash,omitempty"`

	// Credentials are the enrolled authenticators.
	// +optional
	Credentials []WebAuthnCredential `json:"credentials,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=`.spec.email`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Password",type=string,JSONPath=`.spec.passwordAuth`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// Enrolled key count is deliberately absent: printer columns evaluate a
// JSONPath, and JSONPath has no way to count a list. There is deliberately no
// Ready column either — FrameUser has no controller, so nothing would ever
// write one.

// FrameUser is a person who can sign in to the Cluster Control UI.
type FrameUser struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec FrameUserSpec `json:"spec"`

	// +optional
	Status FrameUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameUser{}, &FrameUserList{})
}
```

> `Spec` becomes `+required` here where `v1alpha1` had `json:"spec,omitempty"`. That is a correctness fix, not a new constraint: a FrameUser with no spec has no email and no role, and both are already required inside it.

- [ ] **Step 4: Deprecate `v1alpha1`**

```go
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 FrameUser is deprecated; use frame.plume-labs.io/v1beta1. spec.passwordHash has moved to status.passwordHash — writing it through v1alpha1 requires the frameusers/status subresource."
```

- [ ] **Step 5: Correct the sample's comment**

In `config/samples/frame_v1alpha1_frameuser.yaml`, replace the comment block above `passwordAuth: disabled` with:

```yaml
  # Passkey-only unless deliberately opened. authd writes the password hash
  # into status.passwordHash, not spec — it is credential material, and in
  # spec it was readable by anything holding `get frameusers`. Never set it
  # here. The validating webhook refuses to delete or demote the last account
  # holding the admin role.
```

- [ ] **Step 6: Generate and inspect**

```bash
make manifests generate
python3 - <<'PY'
import yaml
d = yaml.safe_load(open("config/crd/bases/frame.plume-labs.io_frameusers.yaml"))
for v in d["spec"]["versions"]:
    p = v["schema"]["openAPIV3Schema"]["properties"]
    print(v["name"], "storage=", v["storage"], "deprecated=", v.get("deprecated", False))
    print("    spec keys:  ", sorted(p["spec"]["properties"]))
    print("    status keys:", sorted(p["status"]["properties"]))
    print("    email:", p["spec"]["properties"]["email"])
PY
```

Assert: `v1beta1` spec keys are exactly `['email', 'passwordAuth', 'role']` — **no `passwordHash`** — and status keys include `passwordHash`; `email` has `maxLength: 254`. `v1alpha1` is unchanged apart from `deprecated: true`.

- [ ] **Step 7: Test cycle**

```bash
make test
make helm-parity
make lint 2>&1 | grep frameuser || echo "no new lint findings"
```

Nothing imports the new type yet, so `internal/authd` still compiles against `v1alpha1`. Task 20 moves it.

- [ ] **Step 8: Commit**

```bash
git add api/frame/v1beta1/frameuser_types.go api/frame/v1alpha1/frameuser_types.go \
        config/samples/frame_v1alpha1_frameuser.yaml \
        PROJECT cmd/main.go config/crd/bases/frame.plume-labs.io_frameusers.yaml charts/frame/files/crds/
git commit -m "feat(api)!: move FrameUser's password hash out of spec

The argon2id hash lived in spec, readable by anything holding get
frameusers and writable by anything holding patch frameusers — while the
WebAuthn public key material sat in status, deliberately, so an admin
editing an account could not corrupt a key by hand. That asymmetry was the
finding (security review M1): the public material was protected from
hand-editing and the credential was not.

On status, only authd's frameusers/status grant writes it and the viewer
and editor tiers can be denied the subresource outright — which is what
lets Task 23 give FrameUser the tier roles it has never had.

A Secret is the real destination, and is recorded as such rather than done
here: authd's store would gain a second object to keep consistent and the
last-admin webhook guard would have to survive a partially-written pair.

email gains the RFC 5321 length limit (T6): the pattern stays loose, which
is right for email, but this value becomes the Kubernetes username in every
issued token, so it ends up as an RBAC subject and in every audit-log line.

Zero FrameUsers exist on any cluster."
```

---

### Task 17: `api/services/v1beta1` and the `FrameService` hub type (F4, F2, F10, T4, T8, R7, R8)

| Decision | Change |
|---|---|
| **F4** | `spec.serviceClass` becomes `framev1beta1.ServiceClass`, keeping its `MEDIUM` default — **the default is deliberately not unified with FrameJob's `LOW`.** |
| **F10** (owner) | No new field. The doc comment records the second meaning `serviceClass` now carries; the mechanism landed in Task 5. |
| **F2** (owner) | `status.phase` removed. |
| **T4** | `spec.parameters` bounded as an envelope. The compatibility carve-out in its doc comment is about parameter *meaning*, not about the map being unbounded — an unbounded map is a resource concern regardless of who owns the keys. |
| **T8** | `spec.type` gains `MaxLength=63` and a lowercase-alphanumeric pattern, so a malformed type fails on **form** before it reaches the registry lookup that already hard-rejects unknown values. |
| **R7** | A `Ready` printer column, which this kind lacked. |
| **R8** | `omitzero` on `metadata`/`status`/`items` becomes `omitempty`, matching all seven `frame.plume-labs.io` types. |

**On R8, and why it goes the other way from the inventory's suggestion.** The inventory says "pick one; `omitzero` is the more correct of the two". This plan picks `omitempty`, because seven of the eight kinds already use it and uniformity across a frozen API is the whole point of the item — changing seven types to match one is a larger diff with more chance of an accident, for a wire difference that only shows on a zero-valued `metadata`, which no real object has. Listed in "Open disagreements".

**Live objects:** **zero FrameServices exist**, so every change here is free.

**Files:**
- Create: `api/services/v1beta1/groupversion_info.go`, `api/services/v1beta1/frameservice_types.go` (scaffolded, then written)
- Modify: `api/services/v1alpha1/frameservice_types.go` (deprecation warning)
- Modify: `PROJECT`, `cmd/main.go` (via the CLI)

**Interfaces:**
- Consumes: `framev1beta1.ServiceClass` from Task 12.
- Produces, in `github.com/rmocq/frame/api/services/v1beta1` (import alias `servicesv1beta1`):
  - `type FrameServiceSpec struct { Type string; Parameters map[string]framev1beta1.ParameterValue; ServiceClass framev1beta1.ServiceClass; Binding BindingSpec; DeletionPolicy string }`
  - `type BindingSpec struct { SecretName string; ProjectTo []string }`
  - `type Sizing struct { GPU, GPUMemory, CPU, Memory string }`
  - `type BindingStatus struct { SecretRef *corev1.LocalObjectReference; Endpoint string; Projected []ProjectedSecretRef }`
  - `type ProjectedSecretRef struct { Namespace, Name string }`
  - `type ProvisionedRef struct { APIVersion, Kind, Name, Namespace string }`
  - `type FrameServiceStatus struct { ObservedGeneration int64; Conditions []metav1.Condition; Binding BindingStatus; Sizing Sizing; Provisioned []ProvisionedRef }` — **no `Phase`**
  - `type FrameService`, `type FrameServiceList`

- [ ] **Step 1: Scaffold**

```bash
/home/rmocq/bin/kubebuilder create api --group services --version v1beta1 --kind FrameService --resource --controller=false
```

- [ ] **Step 2: Write the type**

Replace the scaffolded body of `api/services/v1beta1/frameservice_types.go` below the licence header:

```go
package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// FrameServiceSpec is the envelope. Everything type-specific lives in
// Parameters, which the provider owns and the webhook validates.
type FrameServiceSpec struct {
	// Type selects the provider. The valid set is closed and enforced by the
	// webhook against the provider registry, so a typo is refused at
	// admission rather than leaving an instance Pending forever.
	//
	// The schema bounds only the *form* (T8). The closed set genuinely is
	// compiled-in here, unlike FrameJob.pipeline, so the registry is the
	// right authority — but a malformed value should fail on shape before it
	// reaches a registry lookup at all.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Type string `json:"type"`

	// Parameters are provider-owned and validated at admission against the
	// JSON Schema that provider registers — not by this CRD's own OpenAPI.
	// They are deliberately outside the API compatibility guarantee: a
	// breaking parameter change ships as a new Type value rather than
	// redefining this one.
	//
	// The envelope is bounded anyway (T4). That carve-out is about parameter
	// *meaning*; an unbounded map is a resource concern regardless of who
	// owns the keys.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	Parameters map[string]framev1beta1.ParameterValue `json:"parameters,omitempty"`

	// ServiceClass is the tier the instance's workloads run at, so the
	// existing FrameResourceQuota and SchedulingPolicy apply to it like any
	// other workload. It never names a node: Frame decides placement.
	//
	// It carries a second meaning, deliberately (F10): it is also the
	// instance's scheduling priority, mapped onto a frame-* PriorityClass by
	// internal/scheduling.PriorityClassForServiceClass. There is no
	// spec.priority and no spec.priorityClassName. A long-lived instance's
	// tier is its urgency — unlike a job's, where a HIGH-tier nightly batch
	// can legitimately be low-priority — so a second field would duplicate
	// this one with no case where they differ, and then they could disagree.
	// Naming a PriorityClass directly would break the invariant above by
	// letting a user reach a class Frame did not create, including a system
	// one. If a HIGH-tier instance ever needs to be evicted before a MEDIUM
	// one, that is a v1beta2 problem.
	//
	// MEDIUM, not FrameJob's LOW. The type is shared; the default is not.
	// An unspecified batch job should be preemptible; an unspecified
	// long-lived service instance should not be the first thing evicted.
	// +optional
	// +kubebuilder:default=MEDIUM
	ServiceClass framev1beta1.ServiceClass `json:"serviceClass,omitempty"`

	// +optional
	Binding BindingSpec `json:"binding,omitempty"`

	// DeletionPolicy decides what happens to the instance's data when this
	// object is deleted. Retain is the default because the failure modes are
	// not symmetric: a retained volume costs disk and is visible, a deleted
	// one costs the data at the moment someone meant to redeploy.
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

	// ProjectTo copies the credentials Secret into these namespaces. Opt-in
	// and explicit: a catalog that writes Secrets into namespaces nobody
	// listed is a cross-tenant leak dressed as convenience.
	//
	// This is the one place a Frame spec may name a namespace the CR does not
	// live in, and it earns it: the controller only ever writes at a
	// coordinate it has itself recorded in status.binding.projected, and
	// refuses to claim a coordinate where an object already exists. Authority
	// comes from a record only the controller can write, never from data the
	// requester supplied. See internal/controller/services/binding.go.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	ProjectTo []string `json:"projectTo,omitempty"`
}

// Sizing is what the provider derived from the parameters. It is reported
// rather than requested — nothing in the spec states it, and an operator has
// to be able to see what an instance costs.
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

	// Projected records every Secret coordinate this controller has actually
	// written: the primary Secret beside the FrameService and every copy
	// spec.binding.projectTo asked for. It is the sole record the controller
	// consults to decide what it may write to and what it must delete when a
	// namespace leaves projectTo or secretName changes — never a label on the
	// Secret itself, which is data anyone with patch rights on Secrets can
	// set.
	// +optional
	Projected []ProjectedSecretRef `json:"projected,omitempty"`
}

// ProjectedSecretRef names one Secret coordinate this controller has written.
type ProjectedSecretRef struct {
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
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

// FrameServiceStatus defines the observed state of FrameService.
//
// No status.phase (F2). Ready carries it: True when the instance is serving,
// False otherwise with the reason naming why — UnknownType, NotProvisionable,
// SizeRefused, a provider degrade such as ModelCacheMissing, or a binding
// degrade. Those reasons are diagnostic and are deliberately *not* collapsed
// into a five-valued enum; v1alpha1's phase is projected back out of
// Ready.Status and the deletion timestamp, not out of the reason.
type FrameServiceStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

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
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.binding.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FrameService is the Schema for the frameservices API
type FrameService struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of FrameService
	// +required
	Spec FrameServiceSpec `json:"spec"`

	// status defines the observed state of FrameService
	// +optional
	Status FrameServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameServiceList contains a list of FrameService
type FrameServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameService{}, &FrameServiceList{})
}
```

> **On the cross-group import.** `api/services/v1beta1` now imports `api/frame/v1beta1` for `ServiceClass` and `ParameterValue`. The two groups were split so the service catalog could move without blocking the `frame.plume-labs.io` freeze — that is about the CRDs' release cadence, not their Go packages, and F4 explicitly asks for *one* shared type across all four kinds. `ServiceClass` is a string alias with no deepcopy of its own, so the import adds no runtime coupling: the `services` scheme still registers only `services` kinds. Note it in review; do not duplicate the enum.

- [ ] **Step 3: Deprecate `v1alpha1`**

```go
// +kubebuilder:deprecatedversion:warning="services.plume-labs.io/v1alpha1 FrameService is deprecated; use services.plume-labs.io/v1beta1. status.phase is computed from status.conditions and is not stored."
```

- [ ] **Step 4: Generate and inspect**

```bash
make manifests generate
python3 - <<'PY'
import yaml
d = yaml.safe_load(open("config/crd/bases/services.plume-labs.io_frameservices.yaml"))
for v in d["spec"]["versions"]:
    p = v["schema"]["openAPIV3Schema"]["properties"]
    spec = p["spec"]["properties"]
    print(v["name"], "storage=", v["storage"], "deprecated=", v.get("deprecated", False))
    print("    status keys:", sorted(p["status"]["properties"]))
    print("    type:", spec["type"])
    print("    serviceClass:", spec["serviceClass"])
    print("    parameters:", spec["parameters"])
    print("    spec-level CEL:", p["spec"].get("x-kubernetes-validations"))
print("printer columns:", [c["name"] for c in d["spec"]["versions"][-1].get("additionalPrinterColumns", [])])
PY
```

Assert: `v1beta1` status has **no `phase`** and **has `observedGeneration`**; `type` has `maxLength: 63` and the pattern; `serviceClass` is the three-member enum with `default: MEDIUM`; `parameters` has `maxProperties: 64` and `additionalProperties.maxLength: 1024`; **no** spec-level CEL on either version; the printer columns include `Ready`.

- [ ] **Step 5: Test cycle**

```bash
make test
make helm-parity
make lint 2>&1 | grep frameservice || echo "no new lint findings"
```

- [ ] **Step 6: Commit**

```bash
git add api/services/v1beta1/ api/services/v1alpha1/frameservice_types.go \
        PROJECT cmd/main.go config/crd/bases/services.plume-labs.io_frameservices.yaml charts/frame/files/crds/
git commit -m "feat(api)!: add the FrameService v1beta1 hub type

status.phase is gone (F2); Ready carries it, and its reason keeps naming
the real failure — UnknownType, SizeRefused, ModelCacheMissing — rather
than being flattened into a five-valued enum. A Ready printer column
appears, which this kind lacked (R7).

serviceClass becomes the type all four kinds share, keeping its MEDIUM
default: the type is unified, the default policy deliberately is not. An
unspecified batch job should be preemptible; an unspecified long-lived
instance should not be the first thing evicted. Its doc comment now records
the second meaning it carries — scheduling priority (F10) — so a later
reader finds the reasoning rather than rediscovering the overloading.

Tier 2, unrepeatable after the freeze: type bounded and lowercase-shaped so
a malformed value fails on form before the registry lookup (T8); parameters
capped at 64 entries of 1024 characters (T4) — the compatibility carve-out
on that map is about parameter meaning, not about it being unbounded;
projectTo capped and its entries validated as namespace names.

omitzero becomes omitempty, matching the seven frame.plume-labs.io types
(R8). Zero FrameServices exist anywhere, so all of this is free."
```

---
### Task 18: Conversion functions, the legacy phase projection, and the fuzzed round-trip (F1, F2, F14 layer 1)

The hub/spoke wiring, the eight pairs of conversion functions, the one-way `status.phase` projection the owner's F2 decision requires, and the first of F14's three test layers.

**The lossy-direction question, settled by construction.** F14 point 2 warns that where `v1beta1` has a field `v1alpha1` does not, a `v1beta1 → v1alpha1 → v1beta1` trip loses it unless it is stashed in an annotation. **After Tasks 6, 7 and 12–17 there is no such field**: every `v1beta1` addition (`status.observedGeneration` on seven kinds, `status.used`/`status.namespaces` on FrameResourceQuota) was landed in `v1alpha1` first, precisely so this direction would be empty. So **no annotation escape hatch is introduced**, and the fuzzed test asserts that direction is *exactly* lossless.

The other direction has two deliberate normalisations, and they are announced rather than hidden:

| `v1alpha1`-only field | `ConvertTo` (α→β) | `ConvertFrom` (β→α) | Why not stash it |
|---|---|---|---|
| `FrameJob.spec.namespace` | dropped | set to the object's **own** namespace | Stashing it would let a `v1alpha1` client read back `namespace: other-ns` and believe it still works. The field is a no-op on every stored object and the deprecation warning says so. |
| `TalosSecretReference.namespace` | dropped | left empty | Empty already meant "this CR's own namespace", so the normalised value is *the truth*, not a placeholder. |
| `status.phase` (FrameJob, FrameNode, FrameService) | ignored | **computed** from conditions | Owner decision: a one-way projection out of conditions, never stored. |

**Live objects:** the five that exist — two FrameJobs, three FrameNodes — plus three FrameResourceQuotas and one SchedulingPolicy, are the only objects a conversion will ever be tested against in practice, and none exercises a GPU request, a cross-namespace secret ref, or an empty `serviceClass`. That is exactly why the fuzzer matters: a hand-written fixture is written from the same mental model as the conversion function and systematically misses the field you added to `v1beta1` and forgot to carry through `ConvertFrom`.

**Files:**
- Create: `api/frame/v1alpha1/phase.go`, `api/frame/v1alpha1/conversion.go`, `api/frame/v1alpha1/conversion_test.go`
- Create: `api/frame/v1beta1/conversion.go` (the `Hub()` markers)
- Create: `api/services/v1alpha1/phase.go`, `conversion.go`, `conversion_test.go`
- Create: `api/services/v1beta1/conversion.go`
- Modify: `go.mod` / `go.sum` (`k8s.io/apimachinery/pkg/api/apitesting/roundtrip` and `sigs.k8s.io/randfill` come in as test deps)

**Interfaces:**
- Consumes: every type from Tasks 12–17, and `jobPhaseSubmitted`-style tokens as *string literals* (the conversion package must not import `internal/controller`).
- Produces:
  - `func (*framev1beta1.FrameJob) Hub()` and the same for the other seven hub types.
  - `func (src *v1alpha1.FrameJob) ConvertTo(dstRaw conversion.Hub) error` / `ConvertFrom(srcRaw conversion.Hub) error`, and the same for the other seven spokes.
  - In `api/frame/v1alpha1/phase.go`: `func FrameJobPhaseFromConditions(conditions []metav1.Condition) string`, `func FrameNodePhaseFromConditions(conditions []metav1.Condition) string`.
  - In `api/services/v1alpha1/phase.go`: `func FrameServicePhaseFromStatus(deleting bool, conditions []metav1.Condition) string`.
  Task 20 does **not** call these — controllers work on the hub only. Task 21's e2e spec asserts their output.

- [ ] **Step 1: Mark the hubs**

Create `api/frame/v1beta1/conversion.go` (licence header, then):

```go
package v1beta1

// v1beta1 is the conversion hub: every other version converts to and from
// these types, and they are the storage version. A hub implements no
// conversion of its own — Hub() is a marker method, and its presence is what
// controller-runtime's conversion webhook dispatches on.
//
// Adding a v1beta2 later means moving Hub() there and writing
// v1beta1 <-> v1beta2 alongside the existing v1alpha1 pair. It does not mean
// touching v1alpha1's functions.

func (*FrameJob) Hub()            {}
func (*FrameNode) Hub()           {}
func (*FrameResourceQuota) Hub()  {}
func (*SchedulingPolicy) Hub()    {}
func (*TalosMachineConfig) Hub()  {}
func (*TalosUpgrade) Hub()        {}
func (*FrameUser) Hub()           {}
```

Create `api/services/v1beta1/conversion.go`:

```go
package v1beta1

// See api/frame/v1beta1/conversion.go for what Hub() means.
func (*FrameService) Hub() {}
```

- [ ] **Step 2: Write the phase projections**

Create `api/frame/v1alpha1/phase.go`:

```go
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The legacy status.phase values. They exist only in v1alpha1: v1beta1 is
// conditions-only, and these are computed on the way down and never stored.
//
// A one-way projection is the whole trick that made removing phase possible.
// Conversion cannot reconstruct a stored phase from conditions in general —
// which is why the FrameJob controller first had to make its Ready condition
// track the lifecycle instead of being written once (F3). With that done,
// Ready.Reason *is* the phase for FrameJob and FrameNode, and the projection
// is a rename rather than an inference.
const (
	legacyPhasePending      = "Pending"
	legacyPhaseSubmitted    = "Submitted"
	legacyPhaseRunning      = "Running"
	legacyPhaseSuspended    = "Suspended"
	legacyPhaseCompleted    = "Completed"
	legacyPhaseFailed       = "Failed"
	legacyPhaseDiscovered   = "Discovered"
	legacyPhaseProvisioning = "Provisioning"
	legacyPhaseOnline       = "Online"
	legacyPhaseDegraded     = "Degraded"
	legacyPhaseOffline      = "Offline"
)

// FrameJobPhaseFromConditions projects v1beta1's conditions back onto
// v1alpha1's status.phase.
//
// The FrameJob controller writes exactly one condition, Ready, whose reason
// is one of the five phase tokens. No Ready condition means the controller
// has not run yet, which is Pending — and that is the only way Pending is
// reachable at all. v1alpha1's enum always allowed it and the controller
// never wrote it (R6); the projection now does, correctly.
func FrameJobPhaseFromConditions(conditions []metav1.Condition) string {
	ready := meta.FindStatusCondition(conditions, "Ready")
	if ready == nil {
		return legacyPhasePending
	}
	switch ready.Reason {
	case legacyPhaseSubmitted, legacyPhaseRunning, legacyPhaseSuspended,
		legacyPhaseCompleted, legacyPhaseFailed:
		return ready.Reason
	default:
		// A reason outside the phase vocabulary is a controller bug, not a
		// client's problem. Report the least-committal legal value rather
		// than emitting a phase the v1alpha1 enum would reject on write.
		return legacyPhaseSubmitted
	}
}

// FrameNodePhaseFromConditions projects v1beta1's conditions back onto
// v1alpha1's status.phase.
//
// The FrameNode controller's Ready reason has always been the phase string —
// framenode_controller.go set Reason: phase alongside Status.Phase = phase —
// so this is a straight read. An absent condition means nothing has
// reconciled yet, which v1alpha1 represented as an empty phase.
//
// Discovering and Failed were in v1alpha1's enum and no controller ever wrote
// either (R6); they stay unreachable here, which is faithful.
func FrameNodePhaseFromConditions(conditions []metav1.Condition) string {
	ready := meta.FindStatusCondition(conditions, "Ready")
	if ready == nil {
		return ""
	}
	switch ready.Reason {
	case legacyPhaseDiscovered, legacyPhaseProvisioning, legacyPhaseOnline,
		legacyPhaseDegraded, legacyPhaseOffline:
		return ready.Reason
	default:
		return ""
	}
}
```

Create `api/services/v1alpha1/phase.go`:

```go
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FrameServicePhaseFromStatus projects v1beta1's conditions back onto
// v1alpha1's status.phase.
//
// FrameService's Ready reason is diagnostic — UnknownType, NotProvisionable,
// SizeRefused, ModelCacheMissing, and whatever a provider returns — and
// collapsing that vocabulary into a five-valued enum would throw away the
// only useful part. So unlike FrameJob and FrameNode, this projection reads
// Ready.Status and the deletion timestamp, not the reason.
//
// The mapping is exactly what the controller used to store:
// setStatus was only ever called with "Ready" (on ConditionTrue) or
// "Degraded" (on ConditionFalse), so those two are faithful. Pending is what
// an object looks like before its first reconcile. Deleting is derived from
// the deletion timestamp, which the controller never encoded in the field at
// all. Provisioning was in the enum and was never written by anything.
func FrameServicePhaseFromStatus(deleting bool, conditions []metav1.Condition) string {
	if deleting {
		return "Deleting"
	}
	ready := meta.FindStatusCondition(conditions, "Ready")
	if ready == nil {
		return "Pending"
	}
	if ready.Status == metav1.ConditionTrue {
		return "Ready"
	}
	return "Degraded"
}
```

- [ ] **Step 3: Write the `frame` group's conversions**

Create `api/frame/v1alpha1/conversion.go`. The FrameJob and FrameNode pairs are given in full; the other five follow the identical shape and are field-for-field copies with the noted differences.

```go
package v1alpha1

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/conversion"

	v1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Conversion between v1alpha1 (spoke) and v1beta1 (hub).
//
// Two rules govern every function here.
//
//  1. ConvertFrom must reproduce a v1alpha1 object faithfully enough that a
//     v1beta1 -> v1alpha1 -> v1beta1 round trip is *exactly* lossless. That is
//     achievable without any annotation escape hatch because v1beta1 has no
//     field v1alpha1 lacks: status.observedGeneration and
//     FrameResourceQuota's status.used/status.namespaces were all added to
//     v1alpha1 before the freeze, deliberately, so this direction would be
//     empty.
//
//  2. ConvertTo may normalise, and does so in exactly two places —
//     FrameJob.spec.namespace and TalosSecretReference.namespace. Both name a
//     namespace the CR does not live in, both are removed in v1beta1, and
//     both are announced in the version's deprecation warning. They are not
//     stashed in an annotation: a v1alpha1 client reading back
//     `namespace: other-ns` and believing it still works would be worse than
//     seeing the value the operator actually acts on.
//
// status.phase is never carried in either direction. It is computed on the
// way down, out of conditions, and never stored (F2).

// --- FrameJob ---------------------------------------------------------------

func (src *FrameJob) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.FrameJob)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.FrameJob, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Pipeline = src.Spec.Pipeline
	dst.Spec.ServiceClass = v1beta1.ServiceClass(src.Spec.ServiceClass)
	dst.Spec.Priority = src.Spec.Priority
	dst.Spec.GPUCount = src.Spec.GPUCount
	dst.Spec.Suspended = src.Spec.Suspended
	dst.Spec.Parameters = toParameterValues(src.Spec.Parameters)
	// src.Spec.Namespace is dropped: the Workflow is created beside its
	// FrameJob now (F5). Every stored FrameJob set it to its own namespace,
	// so this is a no-op on everything that exists.

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.ArgoWorkflowName = src.Status.ArgoWorkflowName
	dst.Status.StartTime = src.Status.StartTime
	dst.Status.CompletionTime = src.Status.CompletionTime
	dst.Status.Message = src.Status.Message
	// src.Status.Phase is dropped: conditions are the storage (F2).

	return nil
}

func (dst *FrameJob) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.FrameJob)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.FrameJob, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.Pipeline = src.Spec.Pipeline
	dst.Spec.ServiceClass = string(src.Spec.ServiceClass)
	dst.Spec.Priority = src.Spec.Priority
	dst.Spec.GPUCount = src.Spec.GPUCount
	dst.Spec.Suspended = src.Spec.Suspended
	dst.Spec.Parameters = fromParameterValues(src.Spec.Parameters)
	// The one honest answer for a field that no longer exists: the namespace
	// the operator actually acts in, which is the object's own.
	dst.Spec.Namespace = src.Namespace

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.ArgoWorkflowName = src.Status.ArgoWorkflowName
	dst.Status.StartTime = src.Status.StartTime
	dst.Status.CompletionTime = src.Status.CompletionTime
	dst.Status.Message = src.Status.Message
	dst.Status.Phase = FrameJobPhaseFromConditions(src.Status.Conditions)

	return nil
}

// --- FrameNode --------------------------------------------------------------

func (src *FrameNode) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v1beta1.FrameNode)
	if !ok {
		return fmt.Errorf("ConvertTo: expected *v1beta1.FrameNode, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.IP = src.Spec.IP
	dst.Spec.Role = src.Spec.Role
	dst.Spec.Disk = src.Spec.Disk
	dst.Spec.RDMAInterface = src.Spec.RDMAInterface
	dst.Spec.Hostname = src.Spec.Hostname
	dst.Spec.Rack = src.Spec.Rack
	dst.Spec.Zone = src.Spec.Zone
	dst.Spec.ServiceClass = v1beta1.ServiceClass(src.Spec.ServiceClass)
	dst.Spec.Network = v1beta1.NetworkSpec{
		Address: src.Spec.Network.Address,
		Gateway: src.Spec.Network.Gateway,
		DNS:     src.Spec.Network.DNS,
		VLAN:    src.Spec.Network.VLAN,
		Bond:    src.Spec.Network.Bond,
	}

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.DiscoveredHostname = src.Status.DiscoveredHostname
	dst.Status.DiscoveredTalosVersion = src.Status.DiscoveredTalosVersion
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.KubeletVersion = src.Status.KubeletVersion
	dst.Status.Capacity = src.Status.Capacity
	dst.Status.Allocatable = src.Status.Allocatable
	dst.Status.NodeName = src.Status.NodeName
	dst.Status.DiscoveredDisks = make([]v1beta1.DiskInfo, len(src.Status.DiscoveredDisks))
	for i, d := range src.Status.DiscoveredDisks {
		dst.Status.DiscoveredDisks[i] = v1beta1.DiskInfo{Name: d.Name, Size: d.Size, Type: d.Type}
	}
	dst.Status.DiscoveredNICs = make([]v1beta1.NICInfo, len(src.Status.DiscoveredNICs))
	for i, n := range src.Status.DiscoveredNICs {
		dst.Status.DiscoveredNICs[i] = v1beta1.NICInfo{Name: n.Name, MAC: n.MAC, Speed: n.Speed}
	}
	if len(src.Status.DiscoveredDisks) == 0 {
		dst.Status.DiscoveredDisks = nil
	}
	if len(src.Status.DiscoveredNICs) == 0 {
		dst.Status.DiscoveredNICs = nil
	}

	return nil
}

func (dst *FrameNode) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v1beta1.FrameNode)
	if !ok {
		return fmt.Errorf("ConvertFrom: expected *v1beta1.FrameNode, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta

	dst.Spec.IP = src.Spec.IP
	dst.Spec.Role = src.Spec.Role
	dst.Spec.Disk = src.Spec.Disk
	dst.Spec.RDMAInterface = src.Spec.RDMAInterface
	dst.Spec.Hostname = src.Spec.Hostname
	dst.Spec.Rack = src.Spec.Rack
	dst.Spec.Zone = src.Spec.Zone
	dst.Spec.ServiceClass = string(src.Spec.ServiceClass)
	dst.Spec.Network = NetworkSpec{
		Address: src.Spec.Network.Address,
		Gateway: src.Spec.Network.Gateway,
		DNS:     src.Spec.Network.DNS,
		VLAN:    src.Spec.Network.VLAN,
		Bond:    src.Spec.Network.Bond,
	}

	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.DiscoveredHostname = src.Status.DiscoveredHostname
	dst.Status.DiscoveredTalosVersion = src.Status.DiscoveredTalosVersion
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.KubeletVersion = src.Status.KubeletVersion
	dst.Status.Capacity = src.Status.Capacity
	dst.Status.Allocatable = src.Status.Allocatable
	dst.Status.NodeName = src.Status.NodeName
	if len(src.Status.DiscoveredDisks) > 0 {
		dst.Status.DiscoveredDisks = make([]DiskInfo, len(src.Status.DiscoveredDisks))
		for i, d := range src.Status.DiscoveredDisks {
			dst.Status.DiscoveredDisks[i] = DiskInfo{Name: d.Name, Size: d.Size, Type: d.Type}
		}
	}
	if len(src.Status.DiscoveredNICs) > 0 {
		dst.Status.DiscoveredNICs = make([]NICInfo, len(src.Status.DiscoveredNICs))
		for i, n := range src.Status.DiscoveredNICs {
			dst.Status.DiscoveredNICs[i] = NICInfo{Name: n.Name, MAC: n.MAC, Speed: n.Speed}
		}
	}
	dst.Status.Phase = FrameNodePhaseFromConditions(src.Status.Conditions)

	return nil
}

// --- parameter maps ---------------------------------------------------------

// v1beta1 bounds parameter values through a named type, which is the only
// way controller-gen emits additionalProperties.maxLength. On the wire both
// versions are map[string]string; these two functions are the Go-side cost.
func toParameterValues(in map[string]string) map[string]v1beta1.ParameterValue {
	if in == nil {
		return nil
	}
	out := make(map[string]v1beta1.ParameterValue, len(in))
	for k, v := range in {
		out[k] = v1beta1.ParameterValue(v)
	}
	return out
}

func fromParameterValues(in map[string]v1beta1.ParameterValue) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = string(v)
	}
	return out
}
```

Append the remaining five pairs to the same file, following the shape above exactly:

- **`FrameResourceQuota`** — every spec field copies straight across, `ServiceClass` through the `v1beta1.ServiceClass` cast; status copies `ObservedGeneration`, `Conditions`, `Used`, `Namespaces`. No phase either side.
- **`SchedulingPolicy`** — all six spec fields and both status fields copy straight across. No phase.
- **`TalosMachineConfig`** — `NodeName`, `TalosEndpoint`, `ConfigPatch`, `ConfigPatchRef` copy; `TalosSecretRef` becomes `v1beta1.TalosSecretReference{Name: src.Spec.TalosSecretRef.Name}` on the way up (**dropping `Namespace`**) and `TalosSecretReference{Name: src.Spec.TalosSecretRef.Name}` on the way down (**leaving `Namespace` empty** — which already meant "this CR's own namespace", so the normalised value is the truth).
- **`TalosUpgrade`** — same, plus `Image`.
- **`FrameUser`** — `Email`, `Role`, `PasswordAuth` copy; **`src.Spec.PasswordHash` goes to `dst.Status.PasswordHash` on the way up and back to `dst.Spec.PasswordHash` on the way down**; `Credentials` maps element-wise between the two `WebAuthnCredential` types.

- [ ] **Step 4: Write the `services` group's conversion**

Create `api/services/v1alpha1/conversion.go` with the same structure: `FrameService.ConvertTo` copies `Type`, `Parameters` (through the shared `ParameterValue` cast — import `framev1beta1` for it), `ServiceClass`, `Binding`, `DeletionPolicy`, and status's `ObservedGeneration`, `Conditions`, `Binding` (including `Projected`), `Sizing`, `Provisioned`; `ConvertFrom` does the reverse and sets `dst.Status.Phase = FrameServicePhaseFromStatus(!src.DeletionTimestamp.IsZero(), src.Status.Conditions)`.

- [ ] **Step 5: Add the fuzz dependencies**

```bash
cd /home/rmocq/Neura/.externals/frame
go get k8s.io/apimachinery@v0.35.0
go get sigs.k8s.io/randfill@latest
go mod tidy
```

Both must land in the `require` block with a version pin. If `randfill` resolves above a major boundary, pin it explicitly in `go.mod` — this repo requires an upper bound on every dependency.

- [ ] **Step 6: Write the fuzzed round-trip**

Create `api/frame/v1alpha1/conversion_test.go`:

```go
package v1alpha1

import (
	"math/rand"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/conversion"
	"sigs.k8s.io/randfill"

	v1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// A hand-written fixture is written from the same mental model as the
// conversion function it checks, so it systematically misses the field you
// added to v1beta1 and forgot to carry through ConvertFrom. Random objects
// do not share that mental model.
//
// The seed is fixed so a failure is reproducible; bump it deliberately if you
// want a different corpus, never automatically.
const fuzzSeed = 20260809

func newFiller() *randfill.Filler {
	return randfill.New().
		RandSource(rand.NewSource(fuzzSeed)).
		NilChance(0.2).
		NumElements(0, 4).
		Funcs(
			// metav1.Time round-trips through RFC3339 with second
			// granularity; a fuzzed nanosecond would fail equality for a
			// reason that has nothing to do with conversion.
			func(t *metav1.Time, c randfill.Continue) {
				*t = metav1.Unix(c.Int63n(1<<31), 0)
			},
			// Conditions must be structurally valid: the projections read
			// Reason, and a fuzzed reason would make the phase assertion
			// meaningless.
			func(c *[]metav1.Condition, cont randfill.Continue) {
				reasons := []string{"Submitted", "Running", "Suspended", "Completed", "Failed"}
				statuses := []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown}
				n := cont.Intn(3)
				out := make([]metav1.Condition, 0, n)
				for i := 0; i < n; i++ {
					out = append(out, metav1.Condition{
						Type:               []string{"Ready", "Progressing", "Degraded"}[i],
						Status:             statuses[cont.Intn(len(statuses))],
						Reason:             reasons[cont.Intn(len(reasons))],
						Message:            cont.String(0),
						ObservedGeneration: cont.Int63n(100),
						LastTransitionTime: metav1.Unix(cont.Int63n(1<<31), 0),
					})
				}
				if n == 0 {
					out = nil
				}
				*c = out
			},
		)
}

// hubRoundTrip is the direction that must be exactly lossless: v1beta1 is the
// storage version, so anything it can hold has to survive a trip through the
// served v1alpha1 endpoint unchanged. It is lossless by construction —
// v1beta1 has no field v1alpha1 lacks, because observedGeneration and
// FrameResourceQuota's used/namespaces were added to v1alpha1 before the
// freeze specifically so this would hold — and this test is what keeps that
// true as fields are added.
func hubRoundTrip[H conversion.Hub, S conversion.Convertible](t *testing.T, name string, newHub func() H, newSpoke func() S) {
	t.Helper()
	f := newFiller()
	for i := 0; i < 200; i++ {
		original := newHub()
		f.Fill(original)

		spoke := newSpoke()
		if err := spoke.ConvertFrom(original); err != nil {
			t.Fatalf("%s: ConvertFrom: %v", name, err)
		}
		roundTripped := newHub()
		if err := spoke.ConvertTo(roundTripped); err != nil {
			t.Fatalf("%s: ConvertTo: %v", name, err)
		}

		if diff := cmp.Diff(original, roundTripped); diff != "" {
			t.Fatalf("%s: v1beta1 -> v1alpha1 -> v1beta1 lost data (-want +got):\n%s", name, diff)
		}
	}
}

func TestHubRoundTripIsLossless(t *testing.T) {
	hubRoundTrip(t, "FrameJob",
		func() *v1beta1.FrameJob { return &v1beta1.FrameJob{} },
		func() *FrameJob { return &FrameJob{} })
	hubRoundTrip(t, "FrameNode",
		func() *v1beta1.FrameNode { return &v1beta1.FrameNode{} },
		func() *FrameNode { return &FrameNode{} })
	hubRoundTrip(t, "FrameResourceQuota",
		func() *v1beta1.FrameResourceQuota { return &v1beta1.FrameResourceQuota{} },
		func() *FrameResourceQuota { return &FrameResourceQuota{} })
	hubRoundTrip(t, "SchedulingPolicy",
		func() *v1beta1.SchedulingPolicy { return &v1beta1.SchedulingPolicy{} },
		func() *SchedulingPolicy { return &SchedulingPolicy{} })
	hubRoundTrip(t, "TalosMachineConfig",
		func() *v1beta1.TalosMachineConfig { return &v1beta1.TalosMachineConfig{} },
		func() *TalosMachineConfig { return &TalosMachineConfig{} })
	hubRoundTrip(t, "TalosUpgrade",
		func() *v1beta1.TalosUpgrade { return &v1beta1.TalosUpgrade{} },
		func() *TalosUpgrade { return &TalosUpgrade{} })
	hubRoundTrip(t, "FrameUser",
		func() *v1beta1.FrameUser { return &v1beta1.FrameUser{} },
		func() *FrameUser { return &FrameUser{} })
}

// The other direction is deliberately *not* lossless, in exactly two places.
// This test pins both, so a future change that starts silently preserving —
// or silently dropping something else — fails here rather than in production.
func TestSpokeRoundTripNormalisesTheTwoRemovedNamespaceFields(t *testing.T) {
	t.Run("FrameJob.spec.namespace becomes the object's own", func(t *testing.T) {
		src := &FrameJob{}
		src.Namespace = "team-a"
		src.Spec.Pipeline = "neura-training-dag"
		src.Spec.Namespace = "somewhere-else"

		hub := &v1beta1.FrameJob{}
		if err := src.ConvertTo(hub); err != nil {
			t.Fatalf("ConvertTo: %v", err)
		}
		back := &FrameJob{}
		if err := back.ConvertFrom(hub); err != nil {
			t.Fatalf("ConvertFrom: %v", err)
		}
		if back.Spec.Namespace != "team-a" {
			t.Fatalf("spec.namespace = %q, want the object's own namespace %q — "+
				"a v1alpha1 client must see the namespace the operator acts in, "+
				"not the one it asked for",
				back.Spec.Namespace, "team-a")
		}
	})

	t.Run("TalosSecretRef.namespace is dropped, not stashed", func(t *testing.T) {
		src := &TalosMachineConfig{}
		src.Namespace = "team-a"
		src.Spec.NodeName = "w2"
		src.Spec.TalosEndpoint = "10.0.0.2:50000"
		src.Spec.ConfigPatch = "machine: {}"
		src.Spec.TalosSecretRef = TalosSecretReference{Name: "talos-certs", Namespace: "kube-system"}

		hub := &v1beta1.TalosMachineConfig{}
		if err := src.ConvertTo(hub); err != nil {
			t.Fatalf("ConvertTo: %v", err)
		}
		if hub.Spec.TalosSecretRef.Name != "talos-certs" {
			t.Fatalf("name = %q, want talos-certs", hub.Spec.TalosSecretRef.Name)
		}
		back := &TalosMachineConfig{}
		if err := back.ConvertFrom(hub); err != nil {
			t.Fatalf("ConvertFrom: %v", err)
		}
		if back.Spec.TalosSecretRef.Namespace != "" {
			t.Fatalf("namespace = %q, want empty — empty already meant "+
				"'this CR's own namespace', so the normalised value is the truth",
				back.Spec.TalosSecretRef.Namespace)
		}
		if len(back.Annotations) != 0 {
			t.Fatalf("annotations = %v, want none — nothing is stashed", back.Annotations)
		}
	})

	t.Run("FrameUser's password hash moves between spec and status", func(t *testing.T) {
		src := &FrameUser{}
		src.Spec.Email = "a@b.test"
		src.Spec.Role = "admin"
		src.Spec.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"

		hub := &v1beta1.FrameUser{}
		if err := src.ConvertTo(hub); err != nil {
			t.Fatalf("ConvertTo: %v", err)
		}
		if hub.Status.PasswordHash != src.Spec.PasswordHash {
			t.Fatalf("status.passwordHash = %q, want the spec value", hub.Status.PasswordHash)
		}
		back := &FrameUser{}
		if err := back.ConvertFrom(hub); err != nil {
			t.Fatalf("ConvertFrom: %v", err)
		}
		if back.Spec.PasswordHash != src.Spec.PasswordHash {
			t.Fatalf("spec.passwordHash = %q, want it back", back.Spec.PasswordHash)
		}
	})
}

func TestLegacyPhaseIsProjectedNotStored(t *testing.T) {
	cases := []struct {
		name      string
		condition *metav1.Condition
		want      string
	}{
		{"no conditions at all", nil, "Pending"},
		{"submitted", &metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Submitted"}, "Submitted"},
		{"running", &metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Running"}, "Running"},
		{"completed", &metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Completed"}, "Completed"},
		{"failed", &metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Failed"}, "Failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &v1beta1.FrameJob{}
			hub.Spec.Pipeline = "neura-training-dag"
			if tc.condition != nil {
				hub.Status.Conditions = []metav1.Condition{*tc.condition}
			}
			spoke := &FrameJob{}
			if err := spoke.ConvertFrom(hub); err != nil {
				t.Fatalf("ConvertFrom: %v", err)
			}
			if spoke.Status.Phase != tc.want {
				t.Fatalf("projected phase = %q, want %q", spoke.Status.Phase, tc.want)
			}

			// And the projection is one-way: converting back must not put it
			// anywhere.
			roundTripped := &v1beta1.FrameJob{}
			if err := spoke.ConvertTo(roundTripped); err != nil {
				t.Fatalf("ConvertTo: %v", err)
			}
			if len(roundTripped.Status.Conditions) != len(hub.Status.Conditions) {
				t.Fatalf("conditions changed on the way back up")
			}
		})
	}
}
```

Create the equivalent `api/services/v1alpha1/conversion_test.go` with `hubRoundTrip` for `FrameService` and a `TestFrameServicePhaseProjection` covering the four cases in `FrameServicePhaseFromStatus`: deleting → `Deleting`, no condition → `Pending`, `Ready=True` → `Ready`, `Ready=False` → `Degraded`.

> `github.com/google/go-cmp` may not yet be a direct dependency — `go mod tidy` in Step 5 adds it. If the repo prefers `reflect.DeepEqual`, use it and print both objects on failure; `cmp.Diff`'s output is worth the dependency here because a fuzzed diff is otherwise unreadable.

- [ ] **Step 7: Test cycle**

```bash
go test ./api/... -v -run 'RoundTrip|Normalises|LegacyPhase|PhaseProjection'
make test
make lint 2>&1 | grep -E 'api/frame|api/services' || echo "no new lint findings"
```

Expected: all pass. **A failure in `TestHubRoundTripIsLossless` names the field that was added to `v1beta1` and not carried through `ConvertFrom` — fix the conversion, never the test.**

- [ ] **Step 8: Commit**

```bash
git add api/frame/v1alpha1/conversion.go api/frame/v1alpha1/phase.go api/frame/v1alpha1/conversion_test.go \
        api/frame/v1beta1/conversion.go \
        api/services/v1alpha1/conversion.go api/services/v1alpha1/phase.go api/services/v1alpha1/conversion_test.go \
        api/services/v1beta1/conversion.go go.mod go.sum
git commit -m "feat(api): convert v1alpha1 <-> v1beta1, with a fuzzed round-trip

Eight Hub() markers and eight ConvertTo/ConvertFrom pairs.

status.phase is a one-way projection out of conditions, computed on the way
down and never stored (F2, owner decision). FrameJob and FrameNode read it
straight off Ready.Reason — which is only possible because F3 first made
FrameJob's Ready condition track the lifecycle instead of being written
once. FrameService reads Ready.Status and the deletion timestamp instead,
because its reasons are diagnostic (UnknownType, SizeRefused,
ModelCacheMissing) and collapsing them into a five-valued enum would throw
away the only useful part. Pending becomes reachable for the first time,
correctly: it is what an object with no Ready condition looks like.

No annotation escape hatch, because none is needed: v1beta1 has no field
v1alpha1 lacks. observedGeneration and FrameResourceQuota's used/namespaces
went into v1alpha1 before the freeze precisely so the
v1beta1 -> v1alpha1 -> v1beta1 direction would be exactly lossless, and the
fuzzed test over 200 random objects per kind is what keeps it that way. A
hand-written fixture is written from the same mental model as the function
it checks and systematically misses the field someone forgot.

The other direction normalises in exactly two places, both announced in the
deprecation warnings and both pinned by a test: FrameJob.spec.namespace
comes back as the object's own namespace, and TalosSecretRef.namespace comes
back empty — which is what empty always meant."
```

---
### Task 19: Turn the conversion webhook on, in both install paths (F13)

The manifest plumbing. Six steps from the inventory's F13 checklist, plus the Helm half the parity guard from Task 10 now insists on.

**Decide before writing anything: how does the chart get the stanza?** The inventory offers three options and this plan has already eliminated one — controller-gen v0.20.1 has no marker that emits `spec.conversion`, verified, so it cannot go in `config/crd/bases/`. Of the remaining two, **both** are done: `templates/crds.yaml` injects the stanza (so a chart install is correct), and `hack/helm-parity.sh` compares it (so the two paths can never diverge again silently). That combination is the F13 answer.

**Live objects:** all eight CRDs are installed on the test cluster with `spec.conversion.strategy: None` and `status.storedVersions: [v1alpha1]`. Applying this task's output flips `strategy` to `Webhook` and adds `v1beta1` as storage. Objects are *not* rewritten by that alone — Task 21 owns the migration.

**Files:**
- Create: `config/crd/patches/webhook_in_framejobs.yaml` and seven siblings
- Modify: `config/crd/kustomization.yaml`
- Modify: `config/default/kustomization.yaml`
- Modify: `PROJECT` (via the CLI)
- Modify: `charts/frame/templates/crds.yaml`
- Modify: `cmd/main.go` (conversion webhook registration)

**Interfaces:**
- Consumes: the `Hub()`/`Convertible` implementations from Task 18.
- Produces: `kustomize build config/crd` emits eight CRDs with `spec.conversion.strategy: Webhook`, and `helm template` emits the same. Task 11's `make crd-render` therefore starts producing conversion-capable CRDs, which is what makes Task 20's envtest specs meaningful.
- Produces: no new Go symbols. controller-runtime serves `/convert` automatically for every type in the manager's scheme that implements `Hub`/`Convertible`; there is **no** `SetupWebhookWithManager` call to write for conversion.

- [ ] **Step 1: Record the conversion webhook in `PROJECT`**

```bash
cd /home/rmocq/Neura/.externals/frame
for kind in FrameNode FrameJob SchedulingPolicy FrameResourceQuota TalosMachineConfig TalosUpgrade FrameUser; do
  /home/rmocq/bin/kubebuilder create webhook --group frame --version v1beta1 --kind "$kind" --conversion --spoke v1alpha1
done
/home/rmocq/bin/kubebuilder create webhook --group services --version v1beta1 --kind FrameService --conversion --spoke v1alpha1
```

Expected: `PROJECT` gains `conversion: true` (and a `spoke` entry) under each resource's `webhooks:` block; `config/crd/patches/webhook_in_<plural>.yaml` is scaffolded for each; `config/crd/kustomization.yaml`'s `patches:` block gains eight entries above the `+kubebuilder:scaffold:crdkustomizewebhookpatch` marker; `config/default/kustomization.yaml`'s two commented conversion replacement blocks are uncommented and populated at the two `crdkustomizecainjection*` markers.

The CLI may also scaffold `*_conversion.go` stubs or a `SetupWebhookWithManager` for conversion. **Delete any stub that would overwrite Task 18's hand-written functions**, and check `git diff api/` shows nothing before continuing:

```bash
git status --short -- api/ && git diff --stat -- api/
```

- [ ] **Step 2: Verify what the CLI actually wrote, and fill the gaps by hand**

The scaffolding is not always complete. Check each of the six checklist items:

```bash
ls config/crd/patches/
grep -n 'patches/' config/crd/kustomization.yaml
grep -n 'configurations:' config/crd/kustomization.yaml
grep -n 'conversion' PROJECT
sed -n '215,300p' config/default/kustomization.yaml
```

Required end state:

1. **Eight patch files.** Each must read (shown for `framejobs`; the other seven differ only in `metadata.name`):

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: framejobs.frame.plume-labs.io
spec:
  conversion:
    strategy: Webhook
    webhook:
      conversionReviewVersions:
        - v1
      clientConfig:
        service:
          namespace: system
          name: webhook-service
          path: /convert
```

The eight names: `framejobs.frame.plume-labs.io`, `framenodes.frame.plume-labs.io`, `frameresourcequotas.frame.plume-labs.io`, `schedulingpolicies.frame.plume-labs.io`, `talosmachineconfigs.frame.plume-labs.io`, `talosupgrades.frame.plume-labs.io`, `frameusers.frame.plume-labs.io`, `frameservices.services.plume-labs.io`.

2. **All eight registered** in `config/crd/kustomization.yaml`'s `patches:` block, each as `- path: patches/webhook_in_<plural>.yaml`, above the scaffold marker (never delete the marker).

3. **`configurations:` uncommented** at the bottom of `config/crd/kustomization.yaml`:

```yaml
configurations:
- kustomizeconfig.yaml
```

Without it, kustomize does not know to rewrite the service name/namespace inside the conversion stanza — `config/crd/kustomizeconfig.yaml` already contains exactly that `nameReference` rule for `spec/conversion/webhook/clientConfig/service/name`.

4. **The two CA-injection replacement blocks live** in `config/default/kustomization.yaml`, with all eight CRDs listed as targets under each `crdkustomizecainjection*` marker. Each target reads:

```yaml
     - select:
         kind: CustomResourceDefinition
         name: framejobs.frame.plume-labs.io
       fieldPaths:
         - .metadata.annotations.[cert-manager.io/inject-ca-from]
       options:
         delimiter: '/'
         index: 0
         create: true
```

(`index: 1` in the second block, which supplies the certificate *name* rather than its namespace.) The scaffold markers must remain in place, at the end of each `targets:` list.

- [ ] **Step 3: Teach the chart to inject the same stanza**

In `charts/frame/templates/crds.yaml`, replace the single `set` line that adds the resource policy (currently line 37) with:

```gotemplate
{{- $_ := set $crd.metadata "annotations" (merge (default (dict) $crd.metadata.annotations) (dict "helm.sh/resource-policy" "keep")) }}
{{/*
The conversion stanza, injected here rather than synced.

files/crds/*.yaml are verbatim copies of config/crd/bases/*.yaml, which is
controller-gen's output — and controller-gen has no marker that emits
spec.conversion. On the kustomize path the stanza arrives from
config/crd/patches/. Nothing carries it onto this path, so the chart has to
add the equivalent itself. Without this block a chart install shipped CRDs
with conversion.strategy: None against a multi-version operator: every read
at the non-storage version silently returning the stored object
uninterpreted, with make helm-crds-check and CI both green (F13).

hack/helm-parity.sh compares this against the kustomize rendering, which is
what stops the two drifting again. If you change the service name, the path
or the review versions here, change config/crd/patches/ to match — the
parity check will tell you if you forget.

Only multi-version CRDs get it: a single-version CRD needs no conversion,
and declaring a webhook for one would make the apiserver call /convert for
nothing.
*/}}
{{- if gt (len $crd.spec.versions) 1 }}
{{- $_ := set $crd.metadata.annotations "cert-manager.io/inject-ca-from" (printf "%s/%s-serving-cert" $.Release.Namespace (include "frame.name" $)) }}
{{- $_ := set $crd.spec "conversion" (dict
      "strategy" "Webhook"
      "webhook" (dict
        "conversionReviewVersions" (list "v1")
        "clientConfig" (dict
          "service" (dict
            "namespace" $.Release.Namespace
            "name" (printf "%s-webhook-service" (include "frame.name" $))
            "path" "/convert")))) }}
{{- end }}
```

> `include "frame.name"` returns the fixed `frame` prefix — see the comment at the top of `_helpers.tpl` explaining why chart resource names are not derived from `.Release.Name`. The certificate is `frame-serving-cert` and the service `frame-webhook-service`, matching `templates/certmanager.yaml:16` and `templates/webhookconfigurations.yaml:5`.

- [ ] **Step 4: Confirm the manager serves `/convert`**

controller-runtime's webhook server registers `/convert` automatically when the manager is built, dispatching on the scheme. Confirm `cmd/main.go` adds **both** versions of **both** groups to the scheme:

```bash
grep -n 'AddToScheme' cmd/main.go
```

Required: `framev1alpha1`, `framev1beta1`, `servicesv1alpha1` and `servicesv1beta1` all present. The `kubebuilder create api` runs in Tasks 12–17 added the `v1beta1` lines at the scaffold marker; verify rather than assume, because a missing spoke registration makes conversion fail at runtime with `no kind registered`, not at compile time.

- [ ] **Step 5: Render and inspect both paths**

```bash
make manifests generate crd-render
python3 - <<'PY'
import yaml
docs = [d for d in yaml.safe_load_all(open("bin/crd-render/crds.yaml")) if d]
print("kustomize:", len(docs), "CRDs")
for d in docs:
    c = d["spec"].get("conversion", {})
    print(" ", d["metadata"]["name"],
          "strategy=", c.get("strategy"),
          "svc=", (c.get("webhook", {}).get("clientConfig", {}).get("service", {})),
          "injectCA=", "cert-manager.io/inject-ca-from" in (d["metadata"].get("annotations") or {}),
          "versions=", [(v["name"], v["served"], v["storage"]) for v in d["spec"]["versions"]])
PY
```

Assert for all eight: `strategy: Webhook`; the service is `frame-webhook-service` in `frame-system` at `/convert` (kustomize's `namePrefix` and `namespace` have been applied — if it still reads `webhook-service`/`system`, step 2 item 3 is missing); the CA annotation is present; versions are `[(v1alpha1, True, False), (v1beta1, True, True)]`.

Then the chart:

```bash
helm template frame charts/frame --namespace frame-system --set image.repository=ci-placeholder.invalid/frame \
  | python3 -c "
import sys, yaml
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get('kind') == 'CustomResourceDefinition':
        c = d['spec'].get('conversion', {})
        print(d['metadata']['name'], c.get('strategy'),
              c.get('webhook', {}).get('clientConfig', {}).get('service', {}),
              'cert-manager.io/inject-ca-from' in (d['metadata'].get('annotations') or {}))
"
```

Both listings must agree.

- [ ] **Step 6: Run the guard that exists for this**

```bash
make helm-crds-check
make helm-lint
make helm-parity
```

`make helm-parity` must print `OK: identical CRD version topology and conversion wiring.` **If it fails, the chart and kustomize disagree — that is the exact failure Task 10 was built to surface, and it must be fixed here, not allow-listed.**

- [ ] **Step 7: Prove it again by breaking it**

```bash
# Temporarily disable the chart's injection.
python3 - <<'PY'
p = "charts/frame/templates/crds.yaml"
s = open(p).read()
s = s.replace("{{- if gt (len $crd.spec.versions) 1 }}", "{{- if false }}", 1)
open(p, "w").write(s)
PY
make helm-parity; echo "exit=$? (must be 1)"
git checkout -- charts/frame/templates/crds.yaml
make helm-parity && echo "GREEN again"
```

Expected: exit 1 in the middle, green either side. Paste the failure into the commit body — it is the standing evidence that F13 cannot recur silently.

- [ ] **Step 8: Test cycle**

```bash
make test
```

Expected: PASS. The envtest suites now load conversion-capable CRDs (Task 11), and envtest's `WebhookInstallOptions` rewrites `clientConfig` to the local server — but the suites do not yet start a webhook server for conversion, so they exercise storage at `v1beta1` while the controllers still use `v1alpha1` types. **A failure here is expected and is Task 20's job**; if the suite goes red, do not fix it here — commit this task's manifests and move straight to Task 20, which repoints the controllers. Record the failing spec names in the commit body.

> If a red suite between two tasks is unacceptable to the reviewer, merge Tasks 19 and 20 into one commit. They are separated because their reviews are different — manifests versus Go — not because the tree is green between them.

- [ ] **Step 9: Commit**

```bash
git add PROJECT config/crd/patches/ config/crd/kustomization.yaml \
        config/default/kustomization.yaml charts/frame/templates/crds.yaml cmd/main.go
git commit -m "feat(config): turn the conversion webhook on, in both install paths

Eight kustomize patches add spec.conversion to the CRDs, registered under
the scaffold marker, with config/crd/kustomizeconfig.yaml enabled so
kustomize rewrites the service coordinate inside the stanza. The two
commented CA-injection replacement blocks in config/default are live, with
all eight CRDs as targets, so cert-manager fills the caBundle.

And the chart injects the same stanza itself. It has to: files/crds/ is a
verbatim copy of config/crd/bases/, which is controller-gen's output, and
controller-gen has no marker that emits spec.conversion — verified. Without
this a chart install would have shipped eight CRDs with strategy: None
against a two-version operator, every read at the non-storage version
silently returning the stored object uninterpreted, with helm-crds-check
and CI green (F13). hack/helm-parity.sh compares the two renderings; both
were confirmed identical, and disabling the chart's injection was confirmed
to fail the check."
```

---

### Task 20: Repoint the controllers, webhooks and authd at the hub (F2, F3, F5, F6, F11, F14 layer 2)

Everything under `internal/` moves from `v1alpha1` to `v1beta1`. Three consequences are real work rather than mechanical renaming, and each is called out below.

**Admission webhooks register on `v1beta1` only.** The apiserver's default `matchPolicy: Equivalent` converts an admission request arriving at `v1alpha1` into the storage version before dispatching, so one registration covers both. Registering on both versions instead would mean a typed `CustomValidator` receiving objects of two different Go types on one path, which controller-runtime cannot do. This is why the webhook packages *move* rather than gaining a sibling.

**Live objects:** all nine — two FrameJobs, three FrameNodes, three FrameResourceQuotas, one SchedulingPolicy — are read by these controllers through the conversion webhook the moment this deploys. That is the first real exercise of Task 18's `ConvertTo`.

**Files:**
- Move: `internal/webhook/frame/v1alpha1/` → `internal/webhook/frame/v1beta1/`; `internal/webhook/services/v1alpha1/` → `internal/webhook/services/v1beta1/`
- Modify: every file under `internal/controller/frame/`, `internal/controller/services/`, `internal/services/provider/`, `internal/authd/`
- Modify: `cmd/main.go`
- Create: `internal/controller/frame/conversion_envtest_test.go`

**Interfaces:**
- Consumes: every hub type from Tasks 12–17, the conversions from Task 18, `readyReason`/`conditionTypeReady` from Task 2, `setJobReady` and the five phase constants from Task 3, `internal/scheduling` from Task 5.
- Produces:
  - `internal/webhook/frame/v1beta1` with `SetupFrameJobWebhookWithManager` and its six siblings, all taking `framev1beta1` types; webhook marker `versions=v1beta1` and names `vframejob-v1beta1.kb.io` / `mframejob-v1beta1.kb.io`.
  - `internal/webhook/services/v1beta1` likewise.
  - `func nodePhaseFromStatus(fn *framev1beta1.FrameNode) string` in `internal/controller/frame/framenode_controller.go` — the state-machine read that replaced `fn.Status.Phase`.
  - `func buildTalosClient(...)` unchanged in signature but resolving the Secret in the CR's own namespace.

- [ ] **Step 1: Move the webhook packages**

```bash
cd /home/rmocq/Neura/.externals/frame
mkdir -p internal/webhook/frame/v1beta1 internal/webhook/services/v1beta1
git mv internal/webhook/frame/v1alpha1/*.go internal/webhook/frame/v1beta1/
git mv internal/webhook/services/v1alpha1/*.go internal/webhook/services/v1beta1/
rmdir internal/webhook/frame/v1alpha1 internal/webhook/services/v1alpha1
```

- [ ] **Step 2: Retype every package**

```bash
grep -rl 'api/frame/v1alpha1\|api/services/v1alpha1' --include='*.go' internal/ cmd/ \
  | xargs sed -i \
    -e 's|github.com/rmocq/frame/api/frame/v1alpha1|github.com/rmocq/frame/api/frame/v1beta1|g' \
    -e 's|github.com/rmocq/frame/api/services/v1alpha1|github.com/rmocq/frame/api/services/v1beta1|g' \
    -e 's|framev1alpha1|framev1beta1|g' \
    -e 's|servicesv1alpha1|servicesv1beta1|g'
# The moved webhook packages are themselves named v1alpha1.
sed -i 's|^package v1alpha1$|package v1beta1|' internal/webhook/frame/v1beta1/*.go internal/webhook/services/v1beta1/*.go
# And their kubebuilder markers name the version four times each.
sed -i -e 's|-v1alpha1-|-v1beta1-|g' -e 's|versions=v1alpha1|versions=v1beta1|g' -e 's|-v1alpha1\.kb\.io|-v1beta1.kb.io|g' \
  internal/webhook/frame/v1beta1/*.go internal/webhook/services/v1beta1/*.go
make fmt
go build ./... 2>&1 | head -40
```

`go build` will fail. The remaining errors are the three real changes, below.

- [ ] **Step 3: `FrameJob` — drop `spec.namespace` and `status.phase`**

In `internal/controller/frame/framejob_controller.go`:

- Replace `ns := job.Spec.Namespace` (line 87) with:

```go
	// The Workflow lives beside its FrameJob. spec.namespace is gone (F5):
	// with the operator holding cluster-wide workflow CRUD it let a caller
	// direct creation into any namespace.
	ns := job.Namespace
```

- In `reconcileDelete`, replace `ns := job.Spec.Namespace` with `ns := job.Namespace`.
- In `buildWorkflow`, replace `"namespace": job.Spec.Namespace` with `"namespace": job.Namespace`.
- Delete every `job.Status.Phase = …` assignment (three of them: the `Submitted` write, the transition write, and any in `reconcileDelete`).
- Replace the transition guard from Task 3 with the condition-only form:

```go
	phase := workflowPhase(existing, job.Spec.Suspended)
	if readyReason(job.Status.Conditions) != phase {
```

- Delete `job.Status.Message = workflowMessage(existing)`? **No** — `Message` survives on `v1beta1`. Keep it.

- [ ] **Step 4: `FrameNode` — move the state machine onto the condition**

In `internal/controller/frame/framenode_controller.go`, add:

```go
// nodePhaseFromStatus is the FrameNode reconciler's state-machine input.
//
// It used to read fn.Status.Phase. That field is gone (F2), and the Ready
// condition's reason has always carried the same value — the controller set
// Reason: phase alongside it — so this is a read of the same information
// from the place it now lives. It is deliberately not the conversion
// package's FrameNodePhaseFromConditions: controllers work on the hub and
// must not depend on a spoke's projection.
func nodePhaseFromStatus(fn *framev1beta1.FrameNode) string {
	return readyReason(fn.Status.Conditions)
}
```

Then:
- Replace `if fn.Status.Phase == "" || fn.Status.Phase == nodePhaseDiscovered {` (line 89) with `if p := nodePhaseFromStatus(fn); p == "" || p == nodePhaseDiscovered {`.
- Replace `if fn.Status.Phase == nodePhaseDiscovered {` (line 104) with `if nodePhaseFromStatus(fn) == nodePhaseDiscovered {`.
- Delete every `fn.Status.Phase = …` assignment (three: the discovery branch, `reconcileOnline`, `setPhase`). `setPhase` keeps its name and signature — it still takes a phase string, it just writes it as the condition's reason instead of into a field:

```go
func (r *FrameNodeReconciler) setPhase(ctx context.Context, fn *framev1beta1.FrameNode, phase, msg string) error {
	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.ObservedGeneration = fn.Generation
	meta.SetStatusCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             conditionStatus(phase == nodePhaseOnline),
		Reason:             phase,
		Message:            msg,
		ObservedGeneration: fn.Generation,
	})
	return r.Status().Patch(ctx, fn, patch)
}
```

Note the `Status` is now derived from the phase rather than hardcoded to `false`, so `setPhase(ctx, fn, "Online", …)` is honest if it is ever called that way.

- [ ] **Step 5: Talos — resolve the Secret in the CR's own namespace**

In `internal/controller/frame/talos_client.go`, `buildTalosClient` currently falls back to the CR's namespace when `TalosSecretRef.Namespace` is empty. Delete the fallback and the field read; the namespace is now unconditionally the CR's:

```go
	// The Secret is always in the CR's own namespace. v1beta1 removed the
	// namespace field from TalosSecretReference (F6): the manager holds
	// cluster-wide `get secrets`, so a caller could otherwise make the
	// operator read Talos client certificates — node root credentials — out
	// of any namespace.
	secretNS := crNamespace
```

Find the exact call shape with `grep -n 'TalosSecretRef' internal/controller/frame/*.go` and adjust the two callers.

- [ ] **Step 6: authd — read the hash from `status`**

In `internal/authd/server_session.go`, replace the two reads at `:73,76`:

```go
	usable := err == nil && u.Spec.PasswordAuth == framev1beta1.PasswordEnabled && hashIsUsable(u.Status.PasswordHash)
	hash := dummyPasswordHash
	if usable {
		hash = u.Status.PasswordHash
	}
```

In `internal/authd/server_test.go`, change the five fixture writes from `u.Spec.PasswordHash = hash` to `u.Status.PasswordHash = hash`. Update the comment at `password.go:57-66` that says "spec.passwordHash" to say "status.passwordHash".

**Check the write path too:** if anything in `internal/authd/` ever writes the hash back, it must now use `Status().Update()`, not `Update()`. Find it with `grep -rn 'PasswordHash' internal/authd/ | grep -v _test`.

- [ ] **Step 7: Repoint the webhook suites and controller tests**

The two webhook envtest suites moved one directory across, not deeper, so their `..` counts are unchanged — but their `Paths` for the webhook manifests and their package name did change. Rebuild and fix:

```bash
go build ./... && go vet ./... 2>&1 | head -40
```

Then update every test fixture that sets a removed field:

```bash
grep -rn 'Spec.Namespace\|Status.Phase\|Spec.PasswordHash\|TalosSecretReference{' --include='*_test.go' internal/ | grep -v conversion_test
```

Every hit is a fixture to fix. `Status.Phase` assertions become `readyReason(...)` assertions; `Spec.Namespace` writes are deleted.

- [ ] **Step 8: Add the envtest conversion specs (F14 layer 2)**

Create `internal/controller/frame/conversion_envtest_test.go`:

```go
package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// What a unit test of ConvertTo/ConvertFrom cannot prove: that the apiserver
// can *call* them. The CA, the service coordinate, the path, the
// conversionReviewVersions and the manager actually serving /convert are all
// manifest plumbing, and none of it is exercised by a Go test. These specs
// go through a real apiserver, against the CRDs as kustomize renders them
// (see renderedCRDPath), with envtest's WebhookInstallOptions having
// rewritten each clientConfig to the locally-served webhook (F14 point 3).
//
// They also prove the thing point 4 is about, in miniature: an object
// *written* as v1alpha1 and *read back* as v1beta1 goes through ConvertTo on
// the way in, because v1beta1 is the storage version. Task 21's e2e spec does
// the same against objects that were stored before the webhook existed.
var _ = Describe("Conversion", func() {
	ctx := context.Background()

	It("stores a v1alpha1 FrameJob at v1beta1 and drops spec.namespace", func() {
		alpha := &framev1alpha1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "conv-job", Namespace: "default"},
			Spec: framev1alpha1.FrameJobSpec{
				Pipeline:     "neura-training-dag",
				ServiceClass: "HIGH",
				Priority:     "high",
				Namespace:    "somewhere-else",
				GPUCount:     2,
			},
		}
		Expect(k8sClient.Create(ctx, alpha)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, alpha) })

		key := types.NamespacedName{Name: "conv-job", Namespace: "default"}

		By("reading it at the storage version")
		beta := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, key, beta)).To(Succeed())
		Expect(beta.Spec.Pipeline).To(Equal("neura-training-dag"))
		Expect(beta.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassHigh))
		Expect(beta.Spec.GPUCount).To(BeNumerically("==", 2))

		By("reading it back at v1alpha1 and seeing the normalised namespace")
		readBack := &framev1alpha1.FrameJob{}
		Expect(k8sClient.Get(ctx, key, readBack)).To(Succeed())
		Expect(readBack.Spec.Namespace).To(Equal("default"),
			"a v1alpha1 client must see the namespace the operator acts in")
	})

	It("projects a phase out of conditions for a v1alpha1 reader", func() {
		beta := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "conv-phase", Namespace: "default"},
			Spec:       framev1beta1.FrameJobSpec{Pipeline: "neura-inference-dag"},
		}
		Expect(k8sClient.Create(ctx, beta)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, beta) })

		key := types.NamespacedName{Name: "conv-phase", Namespace: "default"}

		By("reading it at v1alpha1 before any controller has run")
		alpha := &framev1alpha1.FrameJob{}
		Expect(k8sClient.Get(ctx, key, alpha)).To(Succeed())
		Expect(alpha.Status.Phase).To(Equal("Pending"),
			"no Ready condition means the controller has not seen it yet")

		By("setting a Ready condition at the storage version")
		Expect(k8sClient.Get(ctx, key, beta)).To(Succeed())
		beta.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "Failed",
			Message:            "workflow failed",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: beta.Generation,
		}}
		Expect(k8sClient.Status().Update(ctx, beta)).To(Succeed())

		Expect(k8sClient.Get(ctx, key, alpha)).To(Succeed())
		Expect(alpha.Status.Phase).To(Equal("Failed"))
	})

	It("moves a v1alpha1 FrameUser's password hash onto status", func() {
		hash := "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"
		alpha := &framev1alpha1.FrameUser{
			ObjectMeta: metav1.ObjectMeta{Name: "conv-user", Namespace: "default"},
			Spec: framev1alpha1.FrameUserSpec{
				Email:        "conv@example.test",
				Role:         "viewer",
				PasswordAuth: "enabled",
				PasswordHash: hash,
			},
		}
		Expect(k8sClient.Create(ctx, alpha)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, alpha) })

		beta := &framev1beta1.FrameUser{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "conv-user", Namespace: "default"}, beta)).
			To(Succeed())
		Expect(beta.Status.PasswordHash).To(Equal(hash),
			"the credential must land on the status subresource, not in spec")
	})

	It("refuses a v1beta1 FrameNode with the empty serviceClass v1alpha1 allowed", func() {
		beta := &framev1beta1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: "conv-node", Namespace: "default"},
			Spec: framev1beta1.FrameNodeSpec{
				IP:           "10.0.0.9",
				ServiceClass: framev1beta1.ServiceClass(""),
			},
		}
		// An empty string is the zero value and is omitted by omitempty, so
		// this actually asserts that *absence* is fine — the enum has no ""
		// member and absence is how "unclassified" is spelled now.
		Expect(k8sClient.Create(ctx, beta)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, beta) })
	})
})
```

Add the matching spec for `FrameService` in `internal/controller/services/`, asserting that a `v1alpha1` read of a `v1beta1` object with `Ready=True` reports `phase: Ready`, and one with no conditions reports `phase: Pending`.

The suites must serve the conversion webhook. In each `suite_test.go`'s `BeforeSuite`, after `testEnv.Start()`, confirm `testEnv.WebhookInstallOptions` is populated (the webhook suites already do this; the two controller suites do not and must gain it):

```go
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{renderedCRDPath()},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join(crdRenderRelativeRoot, "config", "webhook")},
		},
	}
```

and start a manager serving the webhooks, following the pattern already in `internal/webhook/frame/v1beta1/webhook_suite_test.go`.

- [ ] **Step 9: Test cycle**

```bash
make manifests generate crd-render
make test
make helm-parity
make lint 2>&1 | grep -E 'internal/' | grep -v 'pre-existing' || echo "check output"
```

Expected: PASS, `internal/controller` coverage at or above its pre-Task-12 value. `make helm-parity` will now flag the webhook configurations, because `config/webhook/manifests.yaml` regenerated with `v1beta1` names while `charts/frame/templates/webhookconfigurations.yaml` is hand-written and still says `v1alpha1`. **Fix the chart template**: rename each `name: vframejob-v1alpha1.kb.io` to `-v1beta1.kb.io`, each `apiVersions: [v1alpha1]` to `[v1beta1]`, and each `path: /validate-frame-plume-labs-io-v1alpha1-framejob` to `-v1beta1-`. The parity check is what tells you when they all match.

- [ ] **Step 10: Commit**

```bash
git add internal/ cmd/main.go config/webhook/ config/rbac/role.yaml \
        charts/frame/templates/webhookconfigurations.yaml charts/frame/templates/rbac-manager.yaml
git commit -m "refactor!: move every controller, webhook and authd onto v1beta1

The admission webhooks register on v1beta1 only. The apiserver's default
matchPolicy: Equivalent converts a request arriving at v1alpha1 into the
storage version before dispatch, so one registration covers both — and
registering both would mean one typed CustomValidator receiving two
different Go types on one path, which controller-runtime cannot do. Hence
the packages move rather than gaining a sibling.

Three changes are more than a rename. The FrameJob controller creates the
Workflow in the FrameJob's own namespace, because spec.namespace is gone
(F5). The FrameNode reconciler's state machine reads the Ready condition's
reason where it read status.phase — the controller has always written the
same value to both, so this is the same information from where it now lives
(F2). buildTalosClient resolves the Secret in the CR's own namespace
unconditionally (F6), and authd reads and writes the password hash on the
status subresource (F11).

Adds the envtest conversion specs: objects written at one version and read
at the other, through a real apiserver, against the CRDs as kustomize
renders them, with envtest's WebhookInstallOptions wiring the CA. That is
what a unit test of the conversion functions cannot prove — that the
apiserver can call them at all."
```

---
### Task 21: The Kind e2e conversion spec and the storage migration (F14 layers 3–5)

The two things neither a unit test nor envtest can prove.

**Layer 3 — the real wiring.** envtest rewrites `clientConfig` itself and injects its own CA. Only a Kind cluster running the deployed manager, with cert-manager filling `caBundle` from the `frame-serving-cert` Certificate, proves the shipped manifests work. The existing e2e suite already asserts CA injection for the mutating and validating webhook configurations (`e2e_test.go:330-356`); this adds the CRD equivalent, which is a different object and a different replacement block.

**Layer 4 — objects written before the webhook existed.** The apiserver converts on read from etcd, so an object stored under `v1alpha1` goes through `ConvertTo` the first time anything lists it. The test must start from objects created at `v1alpha1`, not from objects it just created at `v1beta1`.

**Layer 5 — the migration completes.** Until every stored object has been rewritten, `.status.storedVersions` contains both versions and `v1alpha1` can never be removed. Without this assertion the deprecation policy written in Task 24 is unenforceable.

**Live objects:** the e2e suite runs against a throwaway Kind cluster and creates its own. The **test cluster's** nine real objects are migrated in Step 6, which is a separate, manual, once-only operation.

**Files:**
- Modify: `test/e2e/e2e_test.go`
- Modify: `config/samples/*.yaml` and `config/samples/kustomization.yaml` (bump `apiVersion`, drop removed fields)
- Modify: `deploy/samples/test-cluster/workloads.yaml`
- Create: `hack/migrate-storage-version.sh`
- Modify: `docs/runbook.md`

**Interfaces:**
- Consumes: everything from Tasks 12–20.
- Produces: `hack/migrate-storage-version.sh`, invoked by hand and by Task 24's documented upgrade path.
- Produces: three new `It` blocks in `test/e2e/e2e_test.go`'s `CRD reconciliation` context.

- [ ] **Step 1: Bump the samples and the deploy manifests**

```bash
cd /home/rmocq/Neura/.externals/frame
sed -i 's|^apiVersion: frame.plume-labs.io/v1alpha1$|apiVersion: frame.plume-labs.io/v1beta1|' config/samples/frame_v1alpha1_*.yaml
sed -i 's|^apiVersion: services.plume-labs.io/v1alpha1$|apiVersion: services.plume-labs.io/v1beta1|' config/samples/services_v1alpha1_*.yaml
sed -i 's|apiVersion: frame.plume-labs.io/v1alpha1|apiVersion: frame.plume-labs.io/v1beta1|' deploy/samples/test-cluster/workloads.yaml
```

Then remove the two fields that no longer exist. In `config/samples/frame_v1alpha1_framejob.yaml`, delete lines 11-13 (the `# The namespace the Argo Workflow is created in…` comment and `namespace: default`) and add in their place:

```yaml
  # The Argo Workflow is created in this FrameJob's own namespace. v1alpha1
  # had a spec.namespace naming another one; it is gone (F5).
```

**Do not rename the sample files.** `config/samples/kustomization.yaml` lists them by name, and the documented smoke test is `kubectl apply -k config/samples/`. The `v1alpha1` in the filename is now a lie, but renaming eight files and a kustomization for cosmetics inside a change this size is how a merge conflict becomes a broken smoke test. Task 24 notes it as follow-up.

Sweep for anything left behind:

```bash
grep -rn 'frame.plume-labs.io/v1alpha1\|services.plume-labs.io/v1alpha1' \
  config/ deploy/ test/ docs/ charts/ src/ --include='*.yaml' --include='*.go' --include='*.ts' --include='*.md' \
  | grep -v node_modules | grep -v '/bases/' | grep -v 'files/crds' | grep -v superpowers
```

Expected remaining hits: `test/e2e/e2e_test.go` (deliberately — Step 3's conversion spec writes at `v1alpha1`), `docs/upgrading.md` and `docs/crd-reference.md` (Task 24 rewrites them), and the CRD manifests themselves.

- [ ] **Step 2: Assert CRD CA injection in e2e**

In `test/e2e/e2e_test.go`, after the `should have CA injection for validating webhooks` spec (which ends around line 356) and **before** the `// +kubebuilder:scaffold:e2e-webhooks-checks` marker, add:

```go
		It("should have CA injection for the CRD conversion webhooks", func() {
			// A different object and a different replacement block from the
			// two above: the conversion CA lands in the CRD's own
			// spec.conversion.webhook.clientConfig.caBundle, wired by the
			// blocks under config/default's crdkustomizecainjection markers.
			// Nothing in Go exercises this — it is pure manifest plumbing,
			// and it is the part of the conversion webhook that fails
			// silently when it is wrong (F14 point 3).
			for _, crd := range frameCRDs {
				By("checking " + crd)
				verify := func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "crd", crd,
						"-o", "jsonpath={.spec.conversion.strategy}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(Equal("Webhook"), crd+" is not served by the conversion webhook")

					out, err = utils.Run(exec.Command("kubectl", "get", "crd", crd,
						"-o", "jsonpath={.spec.conversion.webhook.clientConfig.caBundle}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(len(out)).To(BeNumerically(">", 10),
						crd+" has no caBundle — cert-manager did not inject it")
				}
				Eventually(verify).Should(Succeed())
			}
		})
```

and add beside `frameKinds` at line 56:

```go
// frameCRDs is every Frame CRD by its full resource name, for the checks that
// operate on the CustomResourceDefinition object rather than on CRs.
var frameCRDs = []string{
	"framejobs.frame.plume-labs.io",
	"framenodes.frame.plume-labs.io",
	"frameresourcequotas.frame.plume-labs.io",
	"schedulingpolicies.frame.plume-labs.io",
	"talosmachineconfigs.frame.plume-labs.io",
	"talosupgrades.frame.plume-labs.io",
	"frameusers.frame.plume-labs.io",
	"frameservices.services.plume-labs.io",
}
```

- [ ] **Step 3: Write at `v1alpha1`, read at `v1beta1`, through a real apiserver**

Add to the `CRD reconciliation` context, after the existing FrameJob spec:

```go
		It("converts a FrameJob written as v1alpha1 and read as v1beta1", func() {
			// Deliberately written at the *old* version, the way an existing
			// client or a stored object arrives. The apiserver runs
			// ConvertTo on the way into etcd, because v1beta1 is storage —
			// so this exercises the deployed webhook, its cert-manager CA,
			// its service coordinate and its conversionReviewVersions, none
			// of which any Go test can reach (F14 point 3).
			applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1alpha1
kind: FrameJob
metadata:
  name: conv-e2e-job
  namespace: %s
spec:
  pipeline: training
  namespace: some-other-namespace
  serviceClass: HIGH
  priority: high
  gpuCount: 1
`, crNamespace))

			By("reading it at v1beta1 and finding no spec.namespace")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get",
					"framejobs.v1beta1.frame.plume-labs.io", "conv-e2e-job",
					"-n", crNamespace, "-o", "jsonpath={.spec}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring(`"pipeline":"training"`))
				g.Expect(out).NotTo(ContainSubstring("some-other-namespace"),
					"spec.namespace must not survive into the storage version")
			}).Should(Succeed())

			By("reading it back at v1alpha1 and finding the normalised namespace")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get",
					"framejobs.v1alpha1.frame.plume-labs.io", "conv-e2e-job",
					"-n", crNamespace, "-o", "jsonpath={.spec.namespace}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal(crNamespace),
					"a v1alpha1 client sees the namespace the operator acts in")
			}).Should(Succeed())

			By("finding the Workflow beside the FrameJob, not in the namespace it asked for")
			Eventually(func(g Gomega) {
				_, err := kubectlGet(g, "workflow.argoproj.io", "conv-e2e-job", crNamespace,
					"{.metadata.name}")
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			By("seeing a phase projected out of conditions at v1alpha1")
			Eventually(func(g Gomega) {
				phase, err := kubectlGet(g, "framejobs.v1alpha1.frame.plume-labs.io", "conv-e2e-job",
					crNamespace, "{.status.phase}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(phase).To(BeElementOf("Submitted", "Running", "Completed", "Failed", "Suspended"),
					"status.phase is computed from conditions, never stored")

				stored, err := kubectlGet(g, "framejobs.v1beta1.frame.plume-labs.io", "conv-e2e-job",
					crNamespace, "{.status.phase}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(stored).To(BeEmpty(), "v1beta1 has no status.phase at all")
			}).Should(Succeed())

			By("emitting the deprecation warning that is the migration policy's only enforcement")
			out, err := utils.Run(exec.Command("kubectl", "get",
				"framejobs.v1alpha1.frame.plume-labs.io", "conv-e2e-job", "-n", crNamespace))
			Expect(err).NotTo(HaveOccurred())
			_ = out // the warning goes to stderr; utils.Run merges it, hence the check below
			Expect(out+err.Error()).To(Or(
				ContainSubstring("deprecated"),
				ContainSubstring("v1beta1"),
			), "reading at v1alpha1 must warn")
		})
```

> If `utils.Run` does not merge stderr, drop the last `By` block rather than guessing — the warning is asserted more reliably in Step 4's migration check via `kubectl get crd -o jsonpath={.spec.versions[?(@.name=='v1alpha1')].deprecated}`. Check with `grep -n 'func Run' test/utils/utils.go` first.

- [ ] **Step 4: Assert the storage migration completes**

Add as the **last** spec in the `CRD reconciliation` context:

```go
		It("completes the storage migration so v1alpha1 could be removed", func() {
			// storedVersions only grows. A version cannot be dropped from a
			// CRD while it appears there, and it stays until every stored
			// object has been rewritten at the new storage version. Without
			// this assertion the deprecation policy is unenforceable: there
			// would be no point at which anyone could say v1alpha1 is
			// removable (F14 point 5).
			By("rewriting every stored object at the storage version")
			for _, kind := range frameKinds {
				out, err := utils.Run(exec.Command("kubectl", "get", kind,
					"-n", crNamespace, "-o", "name", "--ignore-not-found"))
				Expect(err).NotTo(HaveOccurred())
				for _, ref := range utils.GetNonEmptyLines(out) {
					if !strings.Contains(ref, "/") {
						continue
					}
					// A no-op patch is enough: the apiserver rewrites the
					// object at the current storage version on any write.
					_, err := utils.Run(exec.Command("kubectl", "patch", ref, "-n", crNamespace,
						"--type=merge", "-p", `{"metadata":{"annotations":{"frame.plume-labs.io/storage-migrated":"true"}}}`))
					Expect(err).NotTo(HaveOccurred())
				}
			}

			By("waiting for the apiserver to drop v1alpha1 from storedVersions")
			for _, crd := range frameCRDs {
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "crd", crd,
						"-o", "jsonpath={.status.storedVersions}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(Equal(`["v1beta1"]`),
						crd+" still lists v1alpha1 as a stored version, so it cannot be removed")
				}).Should(Succeed())
			}
		})
```

> The apiserver prunes `storedVersions` only when the CRD's `status` is updated, which happens on the next CRD reconcile after the last object is rewritten. `Eventually`'s default timeout may be short for this; if it flakes, give it `WithTimeout(2*time.Minute).WithPolling(5*time.Second)`. A CRD with **zero** objects has `storedVersions: ["v1alpha1"]` forever until someone patches its status — the migration script in Step 5 handles that case explicitly, and the e2e suite creates at least one object of every kind, so it does not hit it.

- [ ] **Step 5: Write the migration script**

Create `hack/migrate-storage-version.sh`:

```bash
#!/usr/bin/env bash
# Rewrite every stored Frame CR at the current storage version, then confirm
# the apiserver has dropped the old version from .status.storedVersions.
#
# Why this is a script and not a one-liner: storedVersions only grows. A
# version cannot be removed from a CRD while it appears there, and it stays
# until every stored object has been rewritten. Objects are rewritten on any
# write, so a no-op annotation patch is enough — but a kind with *zero*
# objects is never written at all, and its storedVersions keeps listing the
# old version forever. Four of Frame's eight kinds are in exactly that state
# (FrameUser, TalosMachineConfig, TalosUpgrade, FrameService), so those need
# their CRD status patched directly.
#
# Usage: KUBECONFIG=... ./hack/migrate-storage-version.sh [--apply]
# Without --apply it reports what it would do and changes nothing.
set -euo pipefail

STORAGE_VERSION="v1beta1"
APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

CRDS=(
  framejobs.frame.plume-labs.io
  framenodes.frame.plume-labs.io
  frameresourcequotas.frame.plume-labs.io
  schedulingpolicies.frame.plume-labs.io
  talosmachineconfigs.frame.plume-labs.io
  talosupgrades.frame.plume-labs.io
  frameusers.frame.plume-labs.io
  frameservices.services.plume-labs.io
)

for crd in "${CRDS[@]}"; do
  resource="${crd%%.*}"
  stored="$(kubectl get crd "$crd" -o jsonpath='{.status.storedVersions}')"
  echo "== $crd  storedVersions=$stored"

  if [ "$stored" = "[\"$STORAGE_VERSION\"]" ]; then
    echo "   already migrated"
    continue
  fi

  mapfile -t refs < <(kubectl get "$resource" -A \
    -o jsonpath='{range .items[*]}{.metadata.namespace}{"\t"}{.metadata.name}{"\n"}{end}')

  if [ "${#refs[@]}" -eq 0 ] || [ -z "${refs[0]}" ]; then
    echo "   no stored objects — the CRD status must be patched directly"
    if [ "$APPLY" -eq 1 ]; then
      kubectl patch crd "$crd" --subresource=status --type=merge \
        -p "{\"status\":{\"storedVersions\":[\"$STORAGE_VERSION\"]}}"
      echo "   patched"
    fi
    continue
  fi

  for ref in "${refs[@]}"; do
    ns="${ref%%$'\t'*}"; name="${ref##*$'\t'}"
    echo "   rewrite $ns/$name"
    if [ "$APPLY" -eq 1 ]; then
      kubectl patch "$resource" "$name" -n "$ns" --type=merge \
        -p '{"metadata":{"annotations":{"frame.plume-labs.io/storage-migrated":"true"}}}' >/dev/null
    fi
  done

  if [ "$APPLY" -eq 1 ]; then
    # The apiserver prunes storedVersions on its next CRD status update, and
    # will not do it while any object is still stored at the old version.
    for _ in $(seq 1 24); do
      stored="$(kubectl get crd "$crd" -o jsonpath='{.status.storedVersions}')"
      [ "$stored" = "[\"$STORAGE_VERSION\"]" ] && break
      sleep 5
    done
    echo "   storedVersions=$stored"
    if [ "$stored" != "[\"$STORAGE_VERSION\"]" ]; then
      echo "   FAILED to converge — do not remove $crd's old version" >&2
      exit 1
    fi
  fi
done

echo
if [ "$APPLY" -eq 1 ]; then
  echo "Migration complete. Every CRD now stores only $STORAGE_VERSION."
else
  echo "Dry run. Re-run with --apply to perform the migration."
fi
```

```bash
chmod +x hack/migrate-storage-version.sh
```

- [ ] **Step 6: Migrate the test cluster**

Only after the operator carrying Tasks 12–20 is deployed there.

```bash
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml
./hack/migrate-storage-version.sh          # dry run first, read the output
./hack/migrate-storage-version.sh --apply
kubectl get framejobs -A -o yaml | head -40   # confirm the two jobs still read correctly
kubectl get framenodes -A -o wide             # confirm the three nodes still show a PHASE
```

Expected: all eight CRDs end at `storedVersions: ["v1beta1"]`; the two FrameJobs and three FrameNodes still list, with `PHASE` populated from their `Ready` condition's reason. **If a FrameNode's PHASE is blank**, its `Ready` condition has a reason outside the projection's vocabulary — check Task 18's `FrameNodePhaseFromConditions` against what the controller actually wrote.

- [ ] **Step 7: Document the runbook entry**

Add to `docs/runbook.md`:

```markdown
## Migrating the storage version

`.status.storedVersions` on a CRD only grows. A version cannot be removed
while it appears there, and it stays until every stored object has been
rewritten at the new storage version. Objects are rewritten on any write, so
a no-op annotation patch is enough — but a kind with **zero** objects is
never written and keeps listing the old version forever, so its CRD status
has to be patched directly.

```bash
./hack/migrate-storage-version.sh            # dry run
./hack/migrate-storage-version.sh --apply
```

The script fails loudly rather than half-finishing: if a CRD does not
converge to a single stored version within two minutes, it exits non-zero and
that CRD's old version must not be removed.
```

- [ ] **Step 8: Test cycle**

```bash
make test
make test-e2e
```

Expected: the full Kind e2e suite green, including the three new specs. This is the run that proves F14 layers 3, 4 and 5. **Do not skip it** — it is the only place the deployed manifests are exercised.

- [ ] **Step 9: Commit**

```bash
git add test/e2e/e2e_test.go config/samples/ deploy/samples/test-cluster/workloads.yaml \
        hack/migrate-storage-version.sh docs/runbook.md
git commit -m "test(e2e): prove conversion end to end, and migrate the storage version

Three things neither a unit test nor envtest can reach.

The CRDs' own caBundle: envtest rewrites clientConfig and injects its own
CA, so only a Kind cluster running the deployed manager with cert-manager
filling spec.conversion.webhook.clientConfig.caBundle proves the shipped
manifests work. That is a different object and a different replacement
block from the mutating and validating webhook configurations the suite
already checked.

An object written as v1alpha1 and read as v1beta1, through a real
apiserver: spec.namespace does not survive into storage, a v1alpha1 reader
sees its own namespace back, the Workflow is created beside the FrameJob
rather than where the spec asked, and status.phase appears at v1alpha1
while being absent at v1beta1 — computed, never stored.

And the migration itself. storedVersions only grows, so without rewriting
every stored object there is no point at which anyone could say v1alpha1 is
removable, and the deprecation policy would be unenforceable.
hack/migrate-storage-version.sh does it, including the case the loop
misses: four of the eight kinds have zero objects anywhere, are therefore
never written, and need their CRD status patched directly."
```

---

### Task 22: Point the SDK at `v1beta1` and rebuild the phase mappers (R9, F2 client fallout)

`src/lib/frame-sdk.ts:578` hardcodes `const VERSION = 'v1alpha1'`, and every Frame API path in the UI is built from it by `apiBase` (`:615-617`); `src/lib/k8s-watch.ts` inherits it. Until it is flipped, **every UI read and write goes through the conversion webhook**, which makes the UI the primary consumer of a code path nothing else exercises. Flip it in the same release as the conversion webhook so the number of versions in flight never exceeds what the tests cover.

The UI is a *safe* old client — its only update path is `patchSpec` (`:2281-2287`), a `merge-patch+json` on `spec` alone, which leaves keys it does not name untouched — but it is a client of two fields that no longer exist at `v1beta1`: `status.phase` on FrameJob and FrameNode, read by `mapJobPhase` (`:706-713`) and `mapNodePhase` (`:731-740`).

**Live objects:** none affected directly; this is client-side. But the Nodes screen's provisioning wizard polls `frame.nodes.getStatus()` for `phase === 'Discovered'` and `'Online'` (`NodeProvisionWizard.tsx:112,134,170-174`), so a wrong mapping here breaks node provisioning in the UI.

**Files:**
- Modify: `src/lib/frame-sdk.ts`
- Modify: `src/lib/k8s-watch.ts` (comment only, if it names the version in prose)
- Modify: `src/components/NodeProvisionWizard.tsx`
- Modify: `src/lib/frame-sdk.test.ts`

**Interfaces:**
- Consumes: the `v1beta1` wire shapes from Tasks 12–17, and the `__testing` barrel from Task 9.
- Produces:
  - `const VERSION = 'v1beta1'`
  - `function readyCondition(conditions?: Condition[]): Condition | undefined`
  - `function mapJobPhase(cr: FrameJobCR): JobStatus` — **signature changed**: it now takes the whole CR, not a phase string.
  - `function mapNodePhase(cr: FrameNodeCR): NodeStatus` — same.
  - `FrameNodeStatus.phase` stays in the SDK's *domain* type, computed from the condition, so `NodeProvisionWizard` keeps working unchanged.

- [ ] **Step 1: Flip the version and add the condition reader**

In `src/lib/frame-sdk.ts`, change line 578 and add the shared helper beneath the CR interfaces:

```ts
const GROUP   = 'frame.plume-labs.io'
const VERSION = 'v1beta1'
```

```ts
interface Condition {
  type: string
  status: 'True' | 'False' | 'Unknown'
  reason?: string
  message?: string
  observedGeneration?: number
  lastTransitionTime?: string
}

/**
 * The Ready condition, which is where every Frame kind reports health.
 *
 * v1beta1 removed status.phase from every kind: a single enum forces the API
 * to pick one dimension of health out of several and cannot express
 * "provisioned but degraded". For FrameJob and FrameNode the Ready
 * condition's `reason` carries exactly the string the old phase field held,
 * so the mappers below read it from there.
 */
function readyCondition(conditions?: Condition[]): Condition | undefined {
  return conditions?.find((c) => c.type === 'Ready')
}
```

- [ ] **Step 2: Rewrite the two mappers**

Replace `mapJobPhase`/`crToJob`'s call and `mapNodePhase`/`crToNode`'s call:

```ts
function mapJobPhase(cr: FrameJobCR): JobStatus {
  // No Ready condition means the controller has not seen the job yet.
  switch (readyCondition(cr.status?.conditions)?.reason) {
    case 'Running':   return 'running'
    case 'Completed': return 'completed'
    case 'Failed':    return 'failed'
    default:          return 'queued'
  }
}

function mapNodePhase(cr: FrameNodeCR): NodeStatus {
  switch (readyCondition(cr.status?.conditions)?.reason) {
    case 'Online':       return 'online'
    case 'Degraded':     return 'degraded'
    case 'Provisioning':
    case 'Discovered':   return 'provisioning'
    default:             return 'offline'
  }
}
```

`'Discovering'` is dropped from the FrameNode switch: no controller has ever written it (R6), and keeping a case for a value that cannot occur invites someone to "fix" the controller to produce it.

Update both call sites:

```ts
    status:       mapJobPhase(cr),
```
```ts
    status:       mapNodePhase(cr),
```

- [ ] **Step 3: Update the CR interfaces**

```ts
interface FrameJobCR {
  metadata: { name: string; namespace?: string; creationTimestamp?: string }
  spec: {
    pipeline: string; serviceClass?: string; priority?: string
    gpuCount?: number; suspended?: boolean
  }
  status?: {
    conditions?: Condition[]
    observedGeneration?: number
    startTime?: string; completionTime?: string
    argoWorkflowName?: string; message?: string
  }
}
```

`spec.namespace` is gone. In `crToJob`, replace `namespace: cr.spec.namespace ?? cr.metadata.namespace ?? frameNs(),` with:

```ts
    // The FrameJob's own namespace is where its Workflow runs. spec.namespace
    // is gone at v1beta1 (F5) — it let a caller direct workflow creation into
    // any namespace the operator could reach.
    namespace:    cr.metadata.namespace ?? frameNs(),
```

And in `JobClient.submit`, delete the `namespace:` line from the POST body and change the serviceClass fallback:

```ts
        spec: {
          pipeline:     spec.pipeline,
          // No fallback: the CRD defaults serviceClass to LOW and priority to
          // medium now, so sending a value here is what made kubectl and the
          // UI disagree about what "unspecified" means (F4). Send only what
          // the user chose.
          ...(spec.serviceClass ? { serviceClass: spec.serviceClass } : {}),
          ...(spec.priority ? { priority: spec.priority } : {}),
          gpuCount:     spec.gpuCount ?? 0,
        },
```

Likewise in `FrameNodeCR`, replace `status.phase` with `conditions`:

```ts
interface FrameNodeCR {
  metadata: { name: string; namespace?: string }
  spec: { ip: string; serviceClass?: string; zone?: string; rack?: string; rdmaInterface?: string; hostname?: string }
  status?: {
    conditions?: Condition[]
    observedGeneration?: number
    capacity?: Record<string, string>; allocatable?: Record<string, string>
    discoveredHostname?: string; discoveredTalosVersion?: string
    discoveredDisks?: Array<{ name: string; size: string; type: string }>
    discoveredNICs?: Array<{ name: string; mac: string; speed: string }>
  }
}
```

- [ ] **Step 4: Keep `getStatus` reporting a phase**

`NodeProvisionWizard` polls for `'Discovered'`, `'Online'`, `'Failed'` and `'Offline'`. Keep its contract by computing the value in `NodeClient.getStatus` (`:2270-2279`):

```ts
  async getStatus(name: string): Promise<FrameNodeStatus> {
    const cr = await k8sFetch<FrameNodeCR>(`${apiBase('framenodes', this.ns)}/${name}`)
    return {
      // v1beta1 has no status.phase. The Ready condition's reason is the same
      // string the field used to hold — Discovered, Provisioning, Online,
      // Degraded, Offline — so the wizard's polling contract is unchanged.
      phase:                  readyCondition(cr.status?.conditions)?.reason ?? '',
      discoveredHostname:     cr.status?.discoveredHostname,
      discoveredTalosVersion: cr.status?.discoveredTalosVersion,
      discoveredDisks:        cr.status?.discoveredDisks,
      discoveredNICs:         cr.status?.discoveredNICs,
    }
  }
```

In `NodeProvisionWizard.tsx`, delete the `status.phase === 'Failed'` branch at `:134` and `:171`: `Failed` was in `v1alpha1`'s enum and no controller ever wrote it (R6), so the branch is unreachable. Leave the `'Offline'` branch — that one is real.

- [ ] **Step 5: Sweep the views for anything reading a Frame CR's `status.phase`**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn "status?.phase\|status\.phase" src/ --include='*.ts' --include='*.tsx' | grep -v node_modules
```

Every hit must be either a **core Kubernetes** object (`Pod.status.phase`, `Namespace.status.phase` — those are unaffected and stay) or already routed through `readyCondition`. Frame CRs no longer have the field; a missed one silently reads `undefined` and renders as offline/queued forever, which TypeScript will not catch because the property was optional.

- [ ] **Step 6: Extend the mapper tests**

Add to `src/lib/frame-sdk.test.ts`:

```ts
describe('phase mapping after F2', () => {
  it('reads a job status off the Ready condition, not a stored phase', () => {
    const running = __testing.crToJob({
      metadata: { name: 'j1', namespace: 'default' },
      spec: { pipeline: 'neura-training-dag' },
      status: { conditions: [{ type: 'Ready', status: 'False', reason: 'Running' }] },
    })
    expect(running.status).toBe('running')

    const done = __testing.crToJob({
      metadata: { name: 'j2', namespace: 'default' },
      spec: { pipeline: 'neura-training-dag' },
      status: { conditions: [{ type: 'Ready', status: 'True', reason: 'Completed' }] },
    })
    expect(done.status).toBe('completed')

    const unseen = __testing.crToJob({
      metadata: { name: 'j3', namespace: 'default' },
      spec: { pipeline: 'neura-training-dag' },
    })
    expect(unseen.status).toBe('queued')
  })

  it('takes a job namespace from metadata, since spec.namespace is gone', () => {
    const j = __testing.crToJob({
      metadata: { name: 'j4', namespace: 'team-a' },
      spec: { pipeline: 'neura-training-dag' },
    })
    expect(j.namespace).toBe('team-a')
  })

  it('reads a node status off the Ready condition', () => {
    const online = __testing.crToNode({
      metadata: { name: 'w2' },
      spec: { ip: '10.0.0.2' },
      status: { conditions: [{ type: 'Ready', status: 'True', reason: 'Online' }] },
    })
    expect(online.status).toBe('online')

    const provisioning = __testing.crToNode({
      metadata: { name: 'w3' },
      spec: { ip: '10.0.0.3' },
      status: { conditions: [{ type: 'Ready', status: 'False', reason: 'Discovered' }] },
    })
    expect(provisioning.status).toBe('provisioning')
  })
})
```

- [ ] **Step 7: Test cycle**

```bash
npx tsc --noEmit
npx vitest run
npx eslint src/
npm run build
```

Then, against the test cluster with the new operator deployed, load the UI and check three screens by hand: **Jobs** (phases render), **Nodes** (statuses render, and the provisioning wizard's first poll returns a phase), **Service classes** (quota usage shows the real numbers Task 7 added).

- [ ] **Step 8: Commit**

```bash
git add src/lib/frame-sdk.ts src/lib/frame-sdk.test.ts src/lib/k8s-watch.ts src/components/NodeProvisionWizard.tsx
git commit -m "feat(ui): move the SDK to v1beta1 and read phases off conditions

VERSION was hardcoded to v1alpha1 and every Frame API path in the UI is
built from it, so until this flip every UI read and write went through the
conversion webhook — making the UI the primary consumer of a path nothing
else exercises. It flips in the same release as the webhook, so the number
of versions in flight never exceeds what the tests cover.

v1beta1 has no status.phase, so mapJobPhase and mapNodePhase now take the
whole CR and read the Ready condition's reason — the same strings the field
used to hold. NodeClient.getStatus still returns a phase string, so the
provisioning wizard's polling contract is unchanged.

spec.namespace is gone from the FrameJob shape and from submit(); the job's
namespace comes from metadata. submit() also stops sending a serviceClass
and priority the user did not choose: the CRD defaults them now, and the
SDK sending MEDIUM while the webhook filled LOW was the reason kubectl and
the UI disagreed about what unspecified meant.

Drops the unreachable Discovering and Failed branches: both were in
v1alpha1's enums and no controller ever wrote either."
```

---
# Part 3 — After the freeze

Both tasks depend on Part 2 having landed. Neither gates it.

---

### Task 23: Give `FrameUser` RBAC tiers and replace the wildcard verbs (R2, R3)

Two findings, one file set.

**R2 — `FrameUser` has no tier roles at all.** `config/rbac/` holds admin/editor/viewer triples for seven kinds; there is no `frameuser_admin_role.yaml`, `frameuser_editor_role.yaml` or `frameuser_viewer_role.yaml`, and `charts/frame/templates/_helpers.tpl:59-81`'s `frame.tierRoleCRDs` omits `frameuser` too. So the one kind holding credential material is the one kind with no tier.

**R3 — the tiers ship `*` verbs and are bound to nobody.** `config/rbac/framejob_admin_role.yaml:20-21` and `charts/frame/templates/rbac-tier-roles.yaml:21-22` both grant `verbs: ['*']`. `*` on a resource also covers verbs that do not exist yet; an admin tier that automatically acquires any future subresource is not a frozen tier.

The roadmap's "extend them to the new API groups" is already **done** for the one kind `services.plume-labs.io` has: `config/rbac/services_frameservice_{admin,editor,viewer}_role.yaml` exist, are registered at `config/rbac/kustomization.yaml:25-27`, and the chart helper includes `services-frameservice`.

**The one schema dependency, now settled.** F11 moved `passwordHash` onto `status`, so `frameusers/status` must **not** be readable by the viewer or editor tiers — reading it hands them the credential. Only the admin tier gets it, and only for `get`. This is the reason this task waits for Part 2.

**Live objects:** zero FrameUsers, and no `RoleBinding` or `ClusterRoleBinding` anywhere in the repo references any tier role — confirmed by grep across `config/`, `charts/` and `deploy/`. So nothing gains or loses access today; the tiers are manifests an administrator binds. `docs/roadmap.md:61` already says this plainly and must keep saying it.

**Files:**
- Create: `config/rbac/frameuser_admin_role.yaml`, `frameuser_editor_role.yaml`, `frameuser_viewer_role.yaml`
- Modify: `config/rbac/kustomization.yaml`
- Modify: all 24 existing `config/rbac/*_{admin,editor,viewer}_role.yaml`
- Modify: `charts/frame/templates/_helpers.tpl`, `charts/frame/templates/rbac-tier-roles.yaml`
- Modify: `docs/deployment.md`

**Interfaces:**
- Consumes: `FrameUserStatus.PasswordHash` from Task 16 — specifically the fact that it is on the status subresource.
- Produces: 27 tier `ClusterRole`s with explicit verb lists, and a `frame.tierRoleCRDs` list of eight entries. Nothing in Go depends on these.

- [ ] **Step 1: Confirm nothing is bound**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn 'admin-role\|editor-role\|viewer-role' config/ charts/ deploy/ | grep -i 'binding' || echo "no bindings — nothing gains or loses access"
```

- [ ] **Step 2: Replace `*` with an explicit verb list**

For each of the 24 existing tier roles, the required rule shape. **Admin** (`<kind>_admin_role.yaml`):

```yaml
rules:
- apiGroups:
  - frame.plume-labs.io
  resources:
  - framejobs
  verbs:
  - create
  - delete
  - deletecollection
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - frame.plume-labs.io
  resources:
  - framejobs/status
  verbs:
  - get
  - patch
  - update
```

**Editor** — the same resource verbs minus `deletecollection`, and `get` only on `/status`:

```yaml
rules:
- apiGroups:
  - frame.plume-labs.io
  resources:
  - framejobs
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - frame.plume-labs.io
  resources:
  - framejobs/status
  verbs:
  - get
```

**Viewer** — read-only:

```yaml
rules:
- apiGroups:
  - frame.plume-labs.io
  resources:
  - framejobs
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - frame.plume-labs.io
  resources:
  - framejobs/status
  verbs:
  - get
```

Replace the `verbs: - '*'` block in every admin role with the eight-verb list above, and check each editor and viewer role matches its shape. Also replace the header comment on each admin role's `'*'` justification with:

```yaml
# Grants the full set of verbs over the resource, enumerated explicitly.
#
# It used to be '*', which also covers verbs and subresources that do not
# exist yet — an admin tier that silently acquires whatever a future version
# adds is not a frozen tier, which is the whole point of pinning these to
# the frozen schema.
```

Apply the eight-verb list mechanically, then verify:

```bash
grep -rn "'\*'" config/rbac/ && echo "STILL WILDCARDS" || echo "no wildcard verbs left"
```

- [ ] **Step 3: Create the three `FrameUser` tiers**

`config/rbac/frameuser_admin_role.yaml`:

```yaml
# This rule is not used by the project frame itself.
# It is provided to allow the cluster admin to help manage permissions for users.
#
# Grants the full set of verbs over frameusers, enumerated explicitly.
#
# frameusers/status is admin-only, and deliberately so: status.passwordHash
# is an argon2id credential. It lives on the status subresource precisely so
# that the viewer and editor tiers can be denied it (F11) — before the
# freeze it was in spec, where anything holding `get frameusers` could read
# it and anything holding `patch frameusers` could overwrite it.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/name: frame
    app.kubernetes.io/managed-by: kustomize
  name: frameuser-admin-role
rules:
- apiGroups:
  - frame.plume-labs.io
  resources:
  - frameusers
  verbs:
  - create
  - delete
  - deletecollection
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - frame.plume-labs.io
  resources:
  - frameusers/status
  verbs:
  - get
  - patch
  - update
```

`config/rbac/frameuser_editor_role.yaml` — same header, name `frameuser-editor-role`, verbs `create,delete,get,list,patch,update,watch` on `frameusers`, and **no `frameusers/status` rule at all**.

`config/rbac/frameuser_viewer_role.yaml` — name `frameuser-viewer-role`, verbs `get,list,watch` on `frameusers`, and **no `frameusers/status` rule at all**.

Register all three in `config/rbac/kustomization.yaml`, in the tier-role block:

```yaml
- frameuser_admin_role.yaml
- frameuser_editor_role.yaml
- frameuser_viewer_role.yaml
```

- [ ] **Step 4: Update the chart**

In `charts/frame/templates/_helpers.tpl`, add to `frame.tierRoleCRDs` (keeping the list's existing order convention):

```yaml
- roleBase: frameuser
  apiGroup: frame.plume-labs.io
  resource: frameusers
```

and amend the block's comment from "The seven CRD-tier RBAC sets" to "The eight CRD-tier RBAC sets".

In `charts/frame/templates/rbac-tier-roles.yaml`, replace the `verbs: ['*']` on the admin tier with the same eight-verb list, and add the `frameuser` carve-out so its `/status` rule is admin-only:

```gotemplate
{{- range $crd := (include "frame.tierRoleCRDs" . | fromYamlArray) }}
{{- /*
frameusers/status carries an argon2id credential (F11), so only the admin
tier may read it. Every other kind's status is safe to expose to the viewer
and editor tiers, which is what makes this a per-kind exception rather than
a blanket rule.
*/}}
{{- $statusTiers := list "admin" "editor" "viewer" }}
{{- if eq $crd.roleBase "frameuser" }}
{{- $statusTiers = list "admin" }}
{{- end }}
...
```

Adapt to whatever loop shape the file already uses — read it first with `sed -n '1,60p' charts/frame/templates/rbac-tier-roles.yaml`. The requirement is only that the rendered output matches `config/rbac/`, which `make helm-parity` verifies.

- [ ] **Step 5: Document the tiers**

In `docs/deployment.md`, in the RBAC section:

```markdown
### The viewer / editor / admin tiers

Twenty-four `ClusterRole`s, three per kind, across both API groups. They are
**not bound to anything** — the UI authenticates with a single ServiceAccount
token, so the tiers are not currently enforced against any human. V1 delivers
correct, frozen, tested tiers; enforcing them per user needs authd Stages 2
and 3, which are post-V1.

Their verbs are enumerated explicitly rather than `'*'`. A wildcard also
covers verbs and subresources that do not exist yet, so an admin tier granted
`'*'` silently acquires whatever a future API version adds — which is not a
frozen tier.

**`frameusers/status` is admin-only.** It carries an argon2id password hash.
The hash lives on the status subresource precisely so that the editor and
viewer tiers can be denied it; before `v1beta1` it was in `spec`, where
anything holding `get frameusers` could read it. A Secret is the right
long-term home and is recorded as such in the CRD reference.
```

- [ ] **Step 6: Test cycle**

```bash
make manifests
make helm-lint
make helm-parity
kubectl --dry-run=client -o yaml apply -f config/rbac/frameuser_admin_role.yaml >/dev/null && echo "manifests parse"
```

`make helm-parity` must be green: the chart's rendered tier roles have to match `config/rbac/` exactly, which is the only mechanical check on 27 hand-edited files.

Then verify the denial actually holds, against the test cluster:

```bash
export KUBECONFIG=/home/rmocq/Neura/.test-cluster/kubeconfig-neura-test.yaml
kubectl apply -f config/rbac/frameuser_viewer_role.yaml
kubectl create serviceaccount rbac-probe -n default
kubectl create clusterrolebinding rbac-probe --clusterrole=frameuser-viewer-role --serviceaccount=default:rbac-probe
kubectl auth can-i get frameusers --as=system:serviceaccount:default:rbac-probe            # expect: yes
kubectl auth can-i get frameusers/status --as=system:serviceaccount:default:rbac-probe     # expect: no
kubectl delete clusterrolebinding rbac-probe; kubectl delete serviceaccount rbac-probe -n default
```

**The second command must print `no`.** If it prints `yes`, the viewer role still has a `/status` rule and the credential is readable by the lowest tier.

- [ ] **Step 7: Commit**

```bash
git add config/rbac/ charts/frame/templates/_helpers.tpl charts/frame/templates/rbac-tier-roles.yaml docs/deployment.md
git commit -m "feat(rbac): give FrameUser tier roles and drop the wildcard verbs

FrameUser had no admin/editor/viewer triple at all — the one kind holding
credential material was the one kind with no tier, in config/rbac and in
the chart's frame.tierRoleCRDs alike. It has one now, with frameusers/status
granted to admin only: that subresource carries the argon2id hash, and
putting the hash there (F11) was what made denying it to the lower tiers
possible in the first place. Verified with kubectl auth can-i that a viewer
can get frameusers and cannot get frameusers/status.

Every admin tier's verbs: ['*'] becomes the eight-verb list plus
deletecollection. A wildcard also covers verbs and subresources that do not
exist yet, so a '*' admin tier silently acquires whatever a future version
adds — not a frozen tier.

Nothing binds any of these roles, in config/, charts/ or deploy/, so no
principal gains or loses access. They are manifests an administrator binds,
which docs/roadmap.md already says plainly and continues to."
```

---

### Task 24: Write the freeze down (F2's rule, the deprecation policy, R6)

The documentation the phase's exit criterion names: "`v1beta1` is the storage version, conversion is tested round-trip against objects written as `v1alpha1`, and **a documented upgrade path exists from the alpha**."

`docs/upgrading.md:153-169` currently states plainly that no compatibility guarantee exists. That statement was true and is now false, and it is the single most misleading paragraph in the repository the moment Part 2 merges.

**Files:**
- Modify: `docs/upgrading.md`
- Modify: `docs/crd-reference.md`
- Modify: `docs/roadmap.md`
- Modify: `docs/api.md`
- Modify: `README.md` (the version claim, if it makes one)

**Interfaces:**
- Consumes: everything. Produces: no code.

- [ ] **Step 1: Replace the "not covered yet" section in `docs/upgrading.md`**

Delete the whole `## 3. What is not covered yet` section's first two bullets (the ones beginning "**There is no compatibility guarantee across chart versions.**" and the paragraph above them stating the API is "explicitly unfrozen") and replace with:

```markdown
## 3. API versions and the migration path

Frame's API is frozen at **`v1beta1`**, in both `frame.plume-labs.io` and
`services.plume-labs.io`. `v1beta1` is the storage version and the conversion
hub; `v1alpha1` is still served, is marked deprecated, and emits a warning on
every read and write naming what changed.

**`v1` is deliberately not part of V1.** Frame is in beta and needs
capability before it needs a stability promise it cannot yet keep; promotion
waits until `v1beta1` has survived real use. Shipping V1 on `v1beta1` says
that honestly, where a `v1` issued on schedule would not.

### What the guarantee is

Within `v1beta1`:

- No field is renamed, removed, or given a new meaning.
- No validation is tightened. It may be loosened.
- New optional fields, new status fields, new printer columns and new enum
  *values* may appear in any `v1beta1.z`. They require no conversion and old
  clients ignore them.
- Condition `type` strings are part of the contract. Every kind writes
  `Ready`; its `reason` vocabulary is documented per kind in
  [crd-reference.md](crd-reference.md).

### Upgrading from `v1alpha1`

A `helm upgrade` installs the two-version CRDs and the conversion webhook.
Existing objects keep working immediately — the apiserver converts them on
read — but they are still *stored* at `v1alpha1` until something rewrites
them, and a version cannot be removed from a CRD while it appears in
`.status.storedVersions`.

```bash
./hack/migrate-storage-version.sh            # dry run
./hack/migrate-storage-version.sh --apply
```

See [runbook.md](runbook.md) for what the script does about the kinds that
have no objects at all, which are never rewritten and need their CRD status
patched directly.

### What changed between `v1alpha1` and `v1beta1`

Nine changes, all announced by the deprecation warning on the version you are
leaving:

| Change | Effect on a `v1alpha1` client |
|---|---|
| `status.phase` removed from FrameJob, FrameNode and FrameService | Still readable at `v1alpha1`, computed from `status.conditions` on the way down. Never stored. Writing it has no effect. |
| `FrameJob.spec.namespace` removed | Ignored. The Argo Workflow is created in the FrameJob's own namespace. A read at `v1alpha1` returns the object's own namespace, not the value you set. |
| `TalosSecretReference.namespace` removed | Ignored. The Secret is read from the CR's own namespace, which is what an empty value always meant. A read returns empty. |
| `TalosSecretReference.name` now required | A `v1beta1` write without it is rejected. |
| `FrameUser.spec.passwordHash` moved to `status.passwordHash` | Writing it through `v1alpha1` requires the `frameusers/status` subresource. |
| `FrameNode.spec.serviceClass` no longer accepts `""` | Omit the field instead; absence means unclassified. |
| `FrameJob.spec.serviceClass` defaults in the schema, not the webhook | Unchanged value (`LOW`), but it is now applied before CEL rather than after. |
| The GPU / `serviceClass: LOW` constraint deleted | A GPU job at `LOW` is admitted. It was only ever enforced for three pipeline names. |
| `topology.kubernetes.io/rack` on Nodes → `frame.plume-labs.io/rack` | Any selector you wrote on the old key must be updated. The controller removes the old key on reconcile. |

### The deprecation policy

`v1alpha1` is served for at least **two minor chart releases** after the
release that introduced `v1beta1`, and is removed no earlier than the first
release in which every install this project knows about reports
`storedVersions: ["v1beta1"]`. The `deprecationWarning` on each `v1alpha1`
kind is the policy's only enforcement mechanism; it costs nothing and it is
what a client sees before the removal, not after.
```

- [ ] **Step 2: Write F2's rule down, in `docs/crd-reference.md`**

Add after the intro block, next to the `status.observedGeneration` paragraph Task 6 added:

```markdown
### No `status.phase`

No Frame kind has one. Health is reported through `status.conditions`, and
every kind writes a `Ready` condition.

This is a rule, not a drift. A single enum forces the API to pick one
dimension of health out of several and cannot express "provisioned but
degraded", which is why the Kubernetes API conventions have called `phase`
strongly discouraged for new APIs since 2019. **Do not add a `phase` field to
a Frame kind.** If a lifecycle needs more than `Ready`, add a second
condition type and document its reason vocabulary here.

Three kinds — FrameJob, FrameNode, FrameService — had one at `v1alpha1`, and
that version still serves it: it is computed out of the conditions on the way
down and is never stored. The `PHASE` column in `kubectl get` survives on all
three, reading the `Ready` condition's `reason`.

| Kind | `Ready.reason` vocabulary |
|---|---|
| FrameJob | `Submitted`, `Running`, `Suspended`, `Completed`, `Failed`. `True` only on `Completed`. |
| FrameNode | `Discovered`, `Provisioning`, `Online`, `Degraded`, `Offline`. `True` only on `Online`. |
| FrameService | diagnostic, not a lifecycle: `Reconciled`, `UnknownType`, `NotProvisionable`, `SizeRefused`, and whatever the provider returns. Read `status`, not `reason`. |
| FrameResourceQuota, SchedulingPolicy, TalosMachineConfig, TalosUpgrade | `Reconciled` on success, a failure reason otherwise. |
| FrameUser | none — it has no controller. |
```

- [ ] **Step 3: Close R6 explicitly**

In the same file, under the FrameJob and FrameNode sections:

```markdown
> Two reason values are reachable only through the `v1alpha1` projection and
> are never written by a controller: FrameJob's `Pending` (what an object with
> no `Ready` condition projects to) and, on FrameNode, nothing at all — the
> `Discovering` and `Failed` values `v1alpha1`'s enum allowed were never
> written by anything and are gone with the field. That is missing controller
> behaviour, not dead schema: adding those states later is a controller change
> with no API impact, because a condition `reason` is a free string.
```

- [ ] **Step 4: Update the roadmap**

In `docs/roadmap.md`, tick Phase B's six boxes and replace its **Exit** line with:

```markdown
**Exit: met.** `v1beta1` is the storage version in both groups, `v1alpha1` is
served and deprecated with a warning, conversion is tested at three layers —
a fuzzed round-trip per kind, envtest specs writing at one version and reading
at the other against kustomize-rendered CRDs, and a Kind e2e spec through a
real apiserver with cert-manager-injected CA — and the storage migration is
scripted and asserted. The upgrade path is in
[upgrading.md](upgrading.md). Full decision record:
`docs/superpowers/specs/2026-08-09-frame-api-freeze-inventory.md`; the
implementation: `docs/superpowers/plans/2026-08-09-frame-api-freeze.md`.

**Two things this phase deliberately did not do.** `FrameUser`'s password
hash moved from `spec` to `status`, not into a `Secret` — the right
destination, and a real design change (authd's store gains a second object to
keep consistent, and the last-admin webhook guard would have to survive a
partially-written pair). And `FrameJob.spec.namespace` was removed rather
than gated on a `SubjectAccessReview`; the SAR is the correct multi-tenant
answer and needs `AdmissionRequest.UserInfo` plumbed into a raw
`admission.Handler`, a `create subjectaccessreviews` grant, and a fail-closed
story. Removal got there first and is strictly safer.
```

Also correct the Phase D exit note, which says a tag is blocked because "there is no frozen version to tag until Phase B ships `v1beta1`" — that half is now unblocked; the GitHub credential half is not.

- [ ] **Step 5: Sweep the docs for stale version claims**

```bash
cd /home/rmocq/Neura/.externals/frame
grep -rn 'v1alpha1' docs/*.md README.md PRD.md AGENTS.md charts/frame/README.md | grep -v superpowers
```

Every remaining hit must be either historical (describing what `v1alpha1` was) or a correct statement about the still-served version. In particular `docs/crd-reference.md`'s opening line — "Eight CRDs across two API groups, both at `v1alpha1`" — is now wrong and must read:

```markdown
Eight CRDs across two API groups, all namespaced, all at **`v1beta1`** with
`v1alpha1` still served and deprecated: seven in `frame.plume-labs.io` and
`FrameService` in `services.plume-labs.io`.
```

and its blockquote — "`v1alpha1` means the schema may change without conversion guarantees" — must be replaced with a pointer to `upgrading.md`'s guarantee section.

Note as follow-up, do not do here: `config/samples/frame_v1alpha1_*.yaml` still carry `v1alpha1` in their **filenames** while declaring `apiVersion: …/v1beta1`. Renaming them means editing `config/samples/kustomization.yaml` and every doc that names a sample path; it is cosmetic and belongs in its own commit.

- [ ] **Step 6: Test cycle**

```bash
grep -rn 'no compatibility guarantee\|explicitly unfrozen' docs/ && echo "STALE CLAIM REMAINS" || echo "clean"
npx markdownlint docs/*.md 2>/dev/null || echo "markdownlint not configured — skip"
```

Then read `docs/upgrading.md` §3 end to end against the nine-row table and confirm each row matches what the code actually does. Every row is a claim someone will rely on during an upgrade.

- [ ] **Step 7: Commit**

```bash
git add docs/upgrading.md docs/crd-reference.md docs/roadmap.md docs/api.md README.md
git commit -m "docs: write the API freeze down

docs/upgrading.md said plainly that no compatibility guarantee exists and
that v1beta1 with a conversion webhook was an unstarted Phase B
deliverable. Both were true and both are now false, which made that section
the most misleading paragraph in the repository. It is replaced with what
the guarantee actually is, the nine changes between the versions, the
storage-migration command, and a deprecation policy with a stated horizon.

docs/crd-reference.md gains the rule F2 established — no Frame kind has a
status.phase, health is conditions, and if a lifecycle needs more than
Ready then add a second condition type — together with each kind's
Ready.reason vocabulary, which is now part of the frozen contract because
clients branch on it.

Closes R6 in writing rather than in code: the enum values no controller
ever wrote were missing controller behaviour, not dead schema, and adding
those states later is a controller change with no API impact now that a
reason is a free string."
```

---

## Self-review

Performed against this plan before committing it.

### Every inventory decision maps to a task

| Decision | Task |
|---|---|
| F1 version topology / storage version / `deprecated` | 12 (topology + FrameJob), 13–17 (per kind), 19 (manifests) |
| F2 `status.phase` — **owner: conditions only** | 12, 13, 17 (field removal), 18 (the one-way projection), 20 (controller state machines), 22 (SDK), 24 (the rule written down) |
| F3 `Submitted` → `Ready` | 3 (with 2 as its prerequisite) |
| F4 shared `ServiceClass`, `""` dropped, defaults in schema | 12 (type + FrameJob default), 13 (`""` dropped), 14 (Required), 17 (FrameService default kept at MEDIUM) |
| F5 `FrameJob.spec.namespace` removed | 12 (schema), 20 (controller), 21 (samples, e2e), 22 (SDK) |
| F6 `TalosSecretReference.Namespace` removed | 15 (schema), 18 (conversion), 20 (`buildTalosClient`) |
| F7 `TalosSecretReference.Name` Required | 15 |
| F8 GPU/`LOW` — **owner: delete** | 1 |
| F9 `pipeline` stays open | 12 (open string + T7 form bounds), 1 (warn-only webhook retained) |
| F10 `FrameService` priority — **owner: from `serviceClass`** | 5 (mechanism), 17 (documented on the field) |
| F11 `passwordHash` → `status` | 16 (schema), 18 (conversion), 20 (authd), 23 (RBAC that depends on it) |
| F12 node labels: rename `rack`, skip empties, document | 4 |
| F13 conversion wiring + the Helm trap | 10 (parity guard), 11 (envtest rendering), 19 (both install paths) |
| F14 three-layer conversion test | 18 (layer 1, fuzzed), 20 (layer 2, envtest), 21 (layers 3–5, Kind + migration + `storedVersions`) |
| T1 `rack`/`zone` bounds | 13 |
| T2 `ip` `isIP()` | 13 |
| T3 `FrameJob.parameters` envelope | 12 |
| T4 `FrameService.parameters` envelope | 17 |
| T5 numeric ceilings | 12 (`gpuCount`), 14 (`maxGPUs`, `queueWeight`) |
| T6 `email` MaxLength | 16 |
| T7 `pipeline` form bounds | 12 |
| T8 `FrameService.type` form bounds | 17 |
| R1 `observedGeneration` | 6, carried by 12–17 |
| R2 `FrameUser` tier roles | 23 |
| R3 explicit verbs | 23 |
| R4 `InferenceRoute` manifests | 8 |
| R5 SDK phantom reads | 7 (controller half), 9 (client half) |
| R6 unreachable enum members | 18 (`Pending` becomes reachable via the projection), 22 (dead SDK branches), 24 (closed in writing) |
| R7 printer-column gaps | 17 (FrameService `Ready`), 12/13 (`Ready` alongside the repointed `Phase`) |
| R8 `omitzero` → `omitempty` | 17 |
| R9 SDK `VERSION` | 22 |

Plus the five smaller findings the brief named beyond the inventory's own numbering: the reserved `topology.kubernetes.io/rack` prefix (Task 4), `frame.plume-labs.io/service-class` meaning two things (Task 4, documented not changed), `FrameNode.spec.rack` unbounded as a live latent bug (Task 13), `FrameUser` with no RBAC tier (Task 23), `InferenceRoute` in two `deploy/jobs/` manifests (Task 8). And one this plan found on its own: `setCondition` dropping reason-only updates (Task 2), which would have silently frozen the phase projection.

### Placeholder scan

No task says "add appropriate validation", "similar to Task N", or "write tests for the above". Every code step shows its code. Three places delegate deliberately and say why, each with a command to resolve the ambiguity before writing:

- Task 5 Step 5 reuses `reconcile_test.go`'s existing fixture helpers rather than naming them blind, with a `grep` to find them.
- Task 18 Step 3 gives FrameJob and FrameNode in full and describes the other five as field-for-field copies, listing every difference — the alternative is 600 lines of near-identical assignment.
- Task 23 Step 4 adapts to `rbac-tier-roles.yaml`'s existing loop shape, with `make helm-parity` as the mechanical check that the result matches.

### Cross-task name consistency

Checked in both directions — every name a later task consumes is produced by an earlier one, spelled identically:

`conditionTypeReady`, `conditionStatus`, `readyReason` (Task 2 → 3, 20). `setJobReady`, `jobPhaseSubmitted`/`Running`/`Suspended`/`Completed`/`Failed` (Task 3 → 20; the same five strings appear as `legacyPhase*` constants in Task 18, which is a separate package and must not import `internal/`). `nodeLabelRack`/`Zone`/`ServiceClass`/`Role`/`RDMA`, `frameNodeLabels`, `applyNodeLabel` (Task 4 → 20). `internal/scheduling.PriorityClassForJobPriority`/`PriorityClassForServiceClass` (Task 5 → 12, 17, 20). `Status.ObservedGeneration` (Task 6 → 12–18). `FrameResourceQuotaStatus.Used`/`.Namespaces`, `sumQuotaUsage` (Task 7 → 9, 14, 18). `__testing` barrel (Task 9 → 22). `renderedCRDPath`, `crdRenderRelativeRoot`, `make crd-render` (Task 11 → 19, 20). `ServiceClass`/`ServiceClassHigh`/`Medium`/`Low`, `ParameterValue` (Task 12 → 13, 14, 17, 18). `TalosSecretReference{Name}` (Task 15 → 18, 20). `FrameUserStatus.PasswordHash` (Task 16 → 18, 20, 23). `Hub()`, `ConvertTo`, `ConvertFrom`, `FrameJobPhaseFromConditions`, `FrameNodePhaseFromConditions`, `FrameServicePhaseFromStatus` (Task 18 → 19, 21). `frameCRDs`, `hack/migrate-storage-version.sh` (Task 21 → 24). `readyCondition` (Task 22, internal).

Two corrections made during this pass rather than left to be discovered:

- Task 3 originally had the FrameJob controller write only the condition, which would have broken its own task-local test while `status.phase` still existed on the type. It now writes both and Task 20 removes the field assignment.
- Task 13's `nodePhaseFromStatus` was originally going to call `v1alpha1.FrameNodePhaseFromConditions`. That would make a controller depend on a spoke's projection. It is now a local `readyReason` wrapper in Task 20, and the plan says why.

### The ordering, restated so it cannot be got wrong

Task 4 **before** Task 13 (F12 before F4). Task 5 **before** Task 17 (F10 before F4 freezes the semantics). Tasks 6 and 7 **before** Task 18 (they are what make the hub round trip lossless without an annotation). Task 2 **before** Task 3 **before** Task 18 (a frozen condition reason is a frozen projected phase). Tasks 10 and 11 **before** Task 19 (the guard must exist before the divergence can). Task 16 **before** Task 23 (the RBAC tier depends on where the hash lives).

---

## Open disagreements

Four. Each implements the inventory's recommendation as written except where doing so is not possible, and says so.

**1. T3 / T4 — the DNS-1123 key pattern on `parameters` is not implemented.** The recommendation is `MaxProperties=64`, key `MaxLength=63` with a DNS-1123-label pattern, value `MaxLength=1024`. Three of those four are implemented. The key pattern is not, and I do not believe it is expressible here. controller-gen v0.20.1 emits `additionalProperties.maxLength` from a named value type but **silently drops every marker on a named key type** — verified by running it against a throwaway `map[ParamName]ParamValue` with `MaxLength` and `Pattern` on `ParamName`: the output carried `maxProperties` and `additionalProperties.maxLength`, and no `propertyNames` at all. The only remaining route is a CEL rule on the map (`self.all(k, k.matches(...))`), and the cost estimator has no `maxLength` on the key to bound the regex against — the exact failure mode that caused `TalosSecretReference` to be invented as a local type in the first place, and one that surfaces as the apiserver rejecting the CRD outright. Implementing the recommendation literally would ship a CRD that will not install. **What I want adjudicated:** whether the envelope bound alone is enough, or whether the keys should be moved into a named struct list (`parameters: [{name, value}]`) in `v1beta1`, which *can* carry per-field markers. The second is a shape change and I did not think a freeze was the place to invent one.

**2. R8 — `omitzero` versus `omitempty` resolved the other way.** The inventory says "pick one; `omitzero` is the more correct of the two". This plan picks `omitempty`, changing `FrameService`'s three tags to match the seven `frame.plume-labs.io` types rather than changing seven types to match one. My reasoning is that the item's value is uniformity across a frozen API, and the smaller diff achieves it with less chance of an accident, for a wire difference that only manifests on a zero-valued `metadata`/`status`/`items` — which no real object has. **I think the inventory is technically right and practically wrong here**, and if the owner wants `omitzero` everywhere it is a one-line change in each of eight files, best done as its own commit rather than folded into Task 17.

**3. F6 — the local `TalosSecretReference` type survives.** F6 notes that with `Namespace` gone "the type can be `corev1.LocalObjectReference` and the CEL-cost workaround disappears with it". It cannot, jointly with F7: `corev1.LocalObjectReference.Name` is `+optional`, and a kubebuilder marker cannot be attached to a subfield of an external `k8s.io/api` type — the same limitation that created the local type. The alternatives are a CEL rule on the `talosSecretRef` node (which works, is field-level, and is cheap, but adds an `XValidation` for something a marker expresses better) or accepting an optional name (which contradicts F7). I kept the local type with one required field. **Not really a disagreement with F6's substance** — the cross-namespace reach is gone, which is what F6 was about — but it is a deviation from its letter and I would rather it be adjudicated than found in review.

**4. F2's consequence for the FrameNode controller is larger than the inventory implies.** F2's ⚠ note says removing `phase` is "doable for FrameNode (`Ready` reason carries the phase)". That is true of the *projection*, and it understates the controller change: `framenode_controller.go:89,104` reads `fn.Status.Phase` as a **state-machine input**, deciding whether to contact the maintenance API, apply a machine config, or sync from the Kubernetes Node. Removing the field moves that decision onto a condition reason — and the condition writer was silently dropping reason-only updates (`helpers.go:31`), so a FrameNode moving `Provisioning → Degraded → Offline` would have kept its first reason forever and the state machine would have stuck. That is Task 2, which the inventory does not contain because it is a bug the inventory had no reason to look for. **Recorded here because it changes F2's cost estimate**: the inventory prices removing `phase` at "three printer columns and two SDK mappers, a day's work". It is that plus a controller state machine and a condition-writer fix. Still the right decision — but if the estimate was load-bearing in making it, the owner should know the estimate was low.
