package manifests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The leader-election alerts select the lease by its ID. If cmd/main.go's
// LeaderElectionID changes and the rule keeps the old one, the "no leader"
// alert goes quiet exactly when it should fire (absent() would still catch it,
// but only after a longer delay). Pin the two to each other.
func TestOperatorAlertsUseTheRealLeaderElectionID(t *testing.T) {
	main, err := os.ReadFile(filepath.Join(Root(t), "cmd/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`LeaderElectionID:\s*"([^"]+)"`).FindSubmatch(main)
	if m == nil {
		t.Fatal("LeaderElectionID not found in cmd/main.go")
	}
	rules, err := os.ReadFile(filepath.Join(Root(t), "charts/frame/templates/prometheusrule.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rules), `name="`+string(m[1])+`"`) {
		t.Errorf("prometheusrule.yaml does not select the leader election ID %q", m[1])
	}
}

// Off by default keeps `helm template` equal to `kustomize build config/default`
// (hack/helm-parity.sh) and avoids requiring the Prometheus Operator CRDs.
func TestPrometheusRuleIsOptIn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(Root(t), "charts/frame/templates/prometheusrule.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "{{- if and .Values.metrics.enabled .Values.metrics.prometheusRule.enabled }}") {
		t.Error("prometheusrule.yaml must be gated on metrics.enabled and metrics.prometheusRule.enabled")
	}
	values, err := os.ReadFile(filepath.Join(Root(t), "charts/frame/values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?s)prometheusRule:.*?enabled: false`).Match(values) {
		t.Error("metrics.prometheusRule.enabled must default to false")
	}
}
