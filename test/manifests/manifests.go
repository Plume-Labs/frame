// Package manifests loads the RBAC that is actually shipped — the YAML under
// deploy/kubernetes and config/rbac — so tests can assert on it.
//
// It exists because of what the whole-branch review of the per-user identity
// lot found: twelve per-task reviews passed, and the Go tests proved every
// mechanism, while the manifests those mechanisms run against granted nobody
// the rights to use them. `internal/uiproxy/impersonation_envtest_test.go`
// hand-creates the ClusterRole it needs, so it proves impersonation works and
// conceals that nothing in `deploy/` grants what it impersonates into.
//
// Nothing here talks to a cluster or shells out to kustomize: it reads the
// files, which is what an operator applies.
package manifests

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Root is the repository root, resolved from this file's own path so the
// tests do not depend on the working directory.
func Root(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve this file's path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

// AllYAMLFiles walks dir and returns every *.yaml/*.yml file under it,
// sorted. Used for a manifest-wide scan rather than a hand-picked file list —
// a file a scan misses because nobody added it to a list is exactly how the
// gap this package exists to catch stays invisible.
func AllYAMLFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch filepath.Ext(path) {
		case ".yaml", ".yml":
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(out)
	return out
}

// docs splits a multi-document YAML file into its non-empty documents.
func docs(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	r := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var out [][]byte
	for {
		chunk, err := r.Read()
		if err != nil {
			return out
		}
		if len(bytes.TrimSpace(chunk)) == 0 {
			continue
		}
		out = append(out, chunk)
	}
}

type typeMeta struct {
	Kind string `json:"kind"`
}

// ClusterRoles returns every ClusterRole found in the given files, keyed by name.
func ClusterRoles(t *testing.T, paths ...string) map[string]rbacv1.ClusterRole {
	t.Helper()
	out := map[string]rbacv1.ClusterRole{}
	for _, p := range paths {
		for _, d := range docs(t, p) {
			var tm typeMeta
			if err := yaml.Unmarshal(d, &tm); err != nil || tm.Kind != "ClusterRole" {
				continue
			}
			var cr rbacv1.ClusterRole
			if err := yaml.Unmarshal(d, &cr); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			out[cr.Name] = cr
		}
	}
	return out
}

// RoleBindings returns every RoleBinding found in the given files, keyed by
// "namespace/name".
func RoleBindings(t *testing.T, paths ...string) map[string]rbacv1.RoleBinding {
	t.Helper()
	out := map[string]rbacv1.RoleBinding{}
	for _, p := range paths {
		for _, d := range docs(t, p) {
			var tm typeMeta
			if err := yaml.Unmarshal(d, &tm); err != nil || tm.Kind != "RoleBinding" {
				continue
			}
			var rb rbacv1.RoleBinding
			if err := yaml.Unmarshal(d, &rb); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			out[rb.Namespace+"/"+rb.Name] = rb
		}
	}
	return out
}

// Roles returns every namespaced Role found in the given files, keyed by
// "namespace/name".
func Roles(t *testing.T, paths ...string) map[string]rbacv1.Role {
	t.Helper()
	out := map[string]rbacv1.Role{}
	for _, p := range paths {
		for _, d := range docs(t, p) {
			var tm typeMeta
			if err := yaml.Unmarshal(d, &tm); err != nil || tm.Kind != "Role" {
				continue
			}
			var r rbacv1.Role
			if err := yaml.Unmarshal(d, &r); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			out[r.Namespace+"/"+r.Name] = r
		}
	}
	return out
}

// PodSecurityEnforceLabel and PodSecurityEnforceVersionLabel are the Pod
// Security Admission labels that actually refuse a privileged pod. A
// Namespace can carry `warn`/`audit` alone — that is a deliberate, documented
// first phase of the rollout (see pod-security/namespaces.yaml's own header)
// — and must not be mistaken for "enforced": nothing refuses admission until
// `enforce` itself is set to `baseline`.
const (
	PodSecurityEnforceLabel        = "pod-security.kubernetes.io/enforce"
	PodSecurityEnforceVersionLabel = "pod-security.kubernetes.io/enforce-version"
)

// EnforceVersionPinRe matches a pinned PSA version label (`v1.29`, `v1.30`,
// …) but not `latest` or an empty string — either of which would let a
// cluster upgrade silently tighten what `baseline` admits.
var EnforceVersionPinRe = regexp.MustCompile(`^v\d+\.\d+$`)

// Namespaces returns every Namespace object found in the given files, keyed
// by name.
func Namespaces(t *testing.T, paths ...string) map[string]corev1.Namespace {
	t.Helper()
	out := map[string]corev1.Namespace{}
	for _, p := range paths {
		for _, d := range docs(t, p) {
			var tm typeMeta
			if err := yaml.Unmarshal(d, &tm); err != nil || tm.Kind != "Namespace" {
				continue
			}
			var ns corev1.Namespace
			if err := yaml.Unmarshal(d, &ns); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			out[ns.Name] = ns
		}
	}
	return out
}

// ClusterRoleBindings returns every ClusterRoleBinding found in the given
// files, keyed by name.
func ClusterRoleBindings(t *testing.T, paths ...string) map[string]rbacv1.ClusterRoleBinding {
	t.Helper()
	out := map[string]rbacv1.ClusterRoleBinding{}
	for _, p := range paths {
		for _, d := range docs(t, p) {
			var tm typeMeta
			if err := yaml.Unmarshal(d, &tm); err != nil || tm.Kind != "ClusterRoleBinding" {
				continue
			}
			var crb rbacv1.ClusterRoleBinding
			if err := yaml.Unmarshal(d, &crb); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			out[crb.Name] = crb
		}
	}
	return out
}

// TierLabel is the key the three aggregated ClusterRoles select on.
const TierLabel = "rbac.frame.plume-labs.io/tier"

// TiersFor returns the tier labels an aggregated role of the given tier
// selects, mirroring rbac-tier-bindings.yaml: each tier selects its own and
// every tier below it.
func TiersFor(tier string) []string {
	switch tier {
	case "viewer":
		return []string{"viewer"}
	case "editor":
		return []string{"viewer", "editor"}
	case "admin":
		return []string{"viewer", "editor", "admin"}
	}
	return nil
}

// Access is one (apiGroup, resource, verb) triple, the granularity RBAC
// actually decides at.
type Access struct {
	Group, Resource, Verb string
	// Why names the call site that needs it, so a failure says what breaks.
	Why string
}

func (a Access) String() string {
	g := a.Group
	if g == "" {
		g = "core"
	}
	return g + "/" + a.Resource + ":" + a.Verb
}

// Grants reports whether any of the rules allows the access.
func Grants(rules []rbacv1.PolicyRule, a Access) bool {
	for _, r := range rules {
		if matches(r.APIGroups, a.Group) && matches(r.Resources, a.Resource) && matches(r.Verbs, a.Verb) {
			return true
		}
	}
	return false
}

func matches(have []string, want string) bool {
	for _, h := range have {
		if h == "*" || h == want {
			return true
		}
	}
	return false
}

// AggregatedRules returns the union of the rules of every ClusterRole in
// `roles` whose tier label is one the given tier selects — that is, what the
// controller-manager will fill `frame-<tier>` with.
func AggregatedRules(roles map[string]rbacv1.ClusterRole, tier string) []rbacv1.PolicyRule {
	want := map[string]bool{}
	for _, t := range TiersFor(tier) {
		want[t] = true
	}
	var out []rbacv1.PolicyRule
	for _, cr := range roles {
		if want[cr.Labels[TierLabel]] {
			out = append(out, cr.Rules...)
		}
	}
	return out
}

// SubjectNames renders subjects as "Kind:name" for readable assertions.
func SubjectNames(subs []rbacv1.Subject) []string {
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.Kind+":"+s.Name)
	}
	return out
}

// Has reports whether the list contains v.
func Has(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Join is a small helper for failure messages.
func Join(list []string) string { return strings.Join(list, ", ") }
