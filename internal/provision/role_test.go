package provision

import (
	"strings"
	"testing"
)

func TestRoleIsRecordedOnTheTargetBeforeTheRelock(t *testing.T) {
	s := goodSpec()
	s.Role = RoleWorkerCeph
	late := commands(t, parsed(t, s, ""), "late-commands")
	n := -1
	for i, c := range late {
		if strings.Contains(c, "frame-node-role") {
			n = i
		}
	}
	if n < 0 {
		t.Fatal("no late-command records the role")
	}
	if n == len(late)-1 {
		t.Error("the role is written after the relock, which must stay last")
	}
}

func TestNoRoleRecordsNothing(t *testing.T) {
	for _, c := range commands(t, parsed(t, goodSpec(), ""), "late-commands") {
		if strings.Contains(c, "frame-node-role") {
			t.Errorf("role file written without a role: %s", c)
		}
	}
}

func TestRoleOutsideTheListIsRefused(t *testing.T) {
	for _, r := range []string{"admin", "worker; rm -rf /", "Worker", "worker+gpu"} {
		s := goodSpec()
		s.Role = r
		if err := ValidateSpec(s); err == nil {
			t.Errorf("role %q accepted", r)
		}
	}
	for _, r := range append(Roles(), "") {
		s := goodSpec()
		s.Role = r
		if err := ValidateSpec(s); err != nil {
			t.Errorf("role %q refused: %v", r, err)
		}
	}
}
