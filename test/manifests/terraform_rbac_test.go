package manifests

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

// deploy/terraform is a supported install path, but its viewer ClusterRole is
// a hand copy of the one in deploy/kubernetes/base/rbac.yaml. #108 found the
// copy three rules short of what the console reads: every list it could not
// make came back 403 and the screens said "nothing declared".
//
// This reads the Terraform file as text (no terraform binary in CI) and
// compares what each role grants, rule by rule, in both directions.

var (
	tfRuleBlock = regexp.MustCompile(`(?s)\brule\s*\{(.*?)\}`)
	tfList      = func(attr string) *regexp.Regexp {
		return regexp.MustCompile(attr + `\s*=\s*\[([^\]]*)\]`)
	}
	tfQuoted = regexp.MustCompile(`"([^"]*)"`)
)

func tfStrings(t *testing.T, block, attr string) []string {
	t.Helper()
	m := tfList(attr).FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("terraform rule has no %s: %q", attr, block)
	}
	quoted := tfQuoted.FindAllStringSubmatch(m[1], -1)
	out := make([]string, 0, len(quoted))
	for _, q := range quoted {
		out = append(out, q[1])
	}
	return out
}

// ruleKeys flattens rules to one "group/resource:verb" key per grant, so the
// comparison does not depend on how rules are split or ordered.
func ruleKeys(rules []rbacv1.PolicyRule) []string {
	seen := map[string]bool{}
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, v := range r.Verbs {
					seen[g+"/"+res+":"+v] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func diff(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	return out
}

func TestTerraformViewerRoleMatchesTheManifest(t *testing.T) {
	root := Root(t)
	rbacPath := filepath.Join(root, "deploy", "kubernetes", "base", "rbac.yaml")
	manifest, ok := ClusterRoles(t, rbacPath)["cluster-control-viewer"]
	if !ok {
		t.Fatal("cluster-control-viewer is not in deploy/kubernetes/base/rbac.yaml")
	}

	for _, r := range manifest.Rules {
		if len(r.ResourceNames) > 0 || len(r.NonResourceURLs) > 0 {
			t.Fatalf("rbac.yaml's viewer role now uses resourceNames or nonResourceURLs (%v): "+
				"teach this test to compare them before copying the rule to Terraform", r)
		}
	}

	raw, err := os.ReadFile(filepath.Join(root, "deploy", "terraform", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, `resource "kubernetes_cluster_role" "viewer"`)
	end := strings.Index(src, `resource "kubernetes_cluster_role_binding" "viewer"`)
	if start < 0 || end < start {
		t.Fatal(`deploy/terraform/main.tf has no resource "kubernetes_cluster_role" "viewer" before its binding`)
	}
	section := src[start:end]

	blocks := tfRuleBlock.FindAllStringSubmatch(section, -1)
	tf := make([]rbacv1.PolicyRule, 0, len(blocks))
	for _, m := range blocks {
		tf = append(tf, rbacv1.PolicyRule{
			APIGroups: tfStrings(t, m[1], "api_groups"),
			Resources: tfStrings(t, m[1], "resources"),
			Verbs:     tfStrings(t, m[1], "verbs"),
		})
	}
	if len(tf) == 0 {
		t.Fatal("found no rule blocks in the Terraform viewer role")
	}

	want, have := ruleKeys(manifest.Rules), ruleKeys(tf)
	if missing := diff(want, have); len(missing) > 0 {
		t.Errorf("deploy/terraform/main.tf viewer role lacks what rbac.yaml grants (copy the rules over):\n  %s",
			strings.Join(missing, "\n  "))
	}
	if extra := diff(have, want); len(extra) > 0 {
		t.Errorf("deploy/terraform/main.tf viewer role grants more than rbac.yaml (rbac.yaml is the source of truth):\n  %s",
			strings.Join(extra, "\n  "))
	}

	// The tier label is what aggregates the role into frame-viewer; without it
	// a Terraform install binds a role the tiers do not know about.
	const tier = "rbac.frame.plume-labs.io/tier"
	label := regexp.MustCompile(`"` + regexp.QuoteMeta(tier) + `"\s*=\s*"` + regexp.QuoteMeta(manifest.Labels[tier]) + `"`)
	if !label.MatchString(section) {
		t.Errorf("deploy/terraform/main.tf viewer role lacks the label %s=%s", tier, manifest.Labels[tier])
	}
}
