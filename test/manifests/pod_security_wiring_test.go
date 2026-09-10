package manifests

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

// kustomizationRoots is every top-level kustomize target under
// deploy/kubernetes that actually ships somewhere: `base` itself (nothing
// applies it directly, but both overlays embed it), and the two overlays.
// The Argo CD `frame` Application (deploy/gitops/argocd/applications/frame.yaml)
// syncs deploy/kubernetes/overlays/production — that source is not read here
// because this package never shells out to kustomize or reads Application
// objects as a build graph; it walks the same `resources`/`bases` fields
// kustomize itself would.
var kustomizationRoots = []string{
	filepath.Join("deploy", "kubernetes", "base"),
	filepath.Join("deploy", "kubernetes", "overlays", "production"),
	filepath.Join("deploy", "kubernetes", "overlays", "development"),
}

type kustomizationFile struct {
	Resources []string `json:"resources"`
	Bases     []string `json:"bases"`
}

// resolveKustomizationFiles returns every plain YAML manifest transitively
// reachable from the `resources:`/`bases:` entries of the kustomization.yaml
// in dir, following directory entries into their own kustomization.yaml
// recursively. It is deliberately not a full kustomize implementation — no
// patches, no generators, no transformers — because the one thing this test
// needs to know is which manifest *files* a given root renders, not what
// kustomize does to their contents. It does not shell out to kustomize or
// kubectl.
func resolveKustomizationFiles(t *testing.T, dir string) []string {
	t.Helper()
	visitedDirs := map[string]bool{}
	var out []string
	var walk func(d string)
	walk = func(d string) {
		if visitedDirs[d] {
			return
		}
		visitedDirs[d] = true
		kpath := filepath.Join(d, "kustomization.yaml")
		raw, err := os.ReadFile(kpath)
		if err != nil {
			t.Fatalf("read %s: %v", kpath, err)
		}
		var k kustomizationFile
		if err := yaml.Unmarshal(raw, &k); err != nil {
			t.Fatalf("parse %s: %v", kpath, err)
		}
		entries := append(append([]string{}, k.Resources...), k.Bases...)
		for _, entry := range entries {
			p := filepath.Join(d, entry)
			info, err := os.Stat(p)
			if err != nil {
				t.Fatalf("resolve resource %q from %s: %v", entry, kpath, err)
			}
			if info.IsDir() {
				walk(p)
				continue
			}
			out = append(out, p)
		}
	}
	walk(dir)
	return out
}

// TestGrantIsPairedWithItsControl is the regression test for whole-branch
// review Critical 1: deploy/kubernetes/base/kustomization.yaml shipped
// rbac-workload-operator.yaml — the RoleBindings that grant `patch` on
// Deployments/StatefulSets in seven namespaces — without ever rendering
// deploy/kubernetes/pod-security/namespaces.yaml, the baseline Pod Security
// enforcement that is the only thing refusing the privileged pod that grant
// would otherwise allow. Neither overlay, nor the Argo CD `frame` Application,
// nor any doc referenced the pod-security target at all, so the default
// deploy path (Argo CD syncing deploy/kubernetes/overlays/production) handed
// back the exact escalation the 2026-08-10 removal closed.
//
// This checks the pairing directly against the rendered file set of every
// kustomization root that actually ships (see kustomizationRoots), rather
// than asserting on one specific file path — a root added later that repeats
// the omission is caught the same way this one would have been.
func TestGrantIsPairedWithItsControl(t *testing.T) {
	root := Root(t)
	for _, rel := range kustomizationRoots {
		dir := filepath.Join(root, rel)
		t.Run(rel, func(t *testing.T) {
			files := resolveKustomizationFiles(t, dir)
			if len(files) == 0 {
				t.Fatalf("resolved no files from %s — the fixture is not reading what it claims", dir)
			}

			var grantFile string
			for _, f := range files {
				if filepath.Base(f) == "rbac-workload-operator.yaml" {
					grantFile = f
					break
				}
			}
			if grantFile == "" {
				t.Fatalf("%s does not render rbac-workload-operator.yaml — if that's now intentional, "+
					"kustomizationRoots needs updating rather than this test deleting itself", rel)
			}

			// The namespaces the grant's RoleBindings actually bind into —
			// read from the rendered file, not hand-typed, for the same
			// reason enforcedNamespaces() in rbac_workload_operator_test.go
			// parses rather than duplicates.
			bindings := RoleBindings(t, grantFile)
			boundNamespaces := map[string]bool{}
			for _, rb := range bindings {
				if rb.RoleRef.Name == "cluster-control-workload-operator" ||
					rb.RoleRef.Name == "cluster-control-workload-admin" {
					boundNamespaces[rb.Namespace] = true
				}
			}
			if len(boundNamespaces) == 0 {
				t.Fatalf("%s: parsed no workload RoleBindings out of %s — the fixture is not reading "+
					"what it claims", rel, grantFile)
			}

			// Every Namespace this root renders that actually enforces
			// baseline — warn/audit alone does not count, see
			// enforcedNamespaces() below.
			enforced := map[string]bool{}
			for name, ns := range Namespaces(t, files...) {
				if ns.Labels[PodSecurityEnforceLabel] == "baseline" {
					enforced[name] = true
				}
			}

			for ns := range boundNamespaces {
				if !enforced[ns] {
					t.Errorf("%s renders a workload RoleBinding into namespace %q but no enforcing "+
						"Namespace for it — this kustomization hands back the 2026-08-10 escalation "+
						"(patch on a pod template with nothing refusing a privileged pod)", rel, ns)
				}
			}
		})
	}
}
