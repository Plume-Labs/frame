package manifests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The leader-election alert selects the lease by its ID. If cmd/main.go's
// LeaderElectionID changes and the rule keeps the old one, the alert selects
// nothing and absent() fires permanently, or the real lease goes unwatched.
// Pin the two to each other.
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
