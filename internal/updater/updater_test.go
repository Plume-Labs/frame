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

package updater

import (
	"strings"
	"testing"
)

const d1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const d2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

func img(name, d string) string { return "ghcr.io/plume-labs/hearthrack-" + name + "@" + d }

func TestVersionOrdering(t *testing.T) {
	// The ascending order from semver.org, section 11: each is below the next.
	asc := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := range asc {
		for j := range asc {
			got, err := CompareVersions(asc[i], asc[j])
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got != want {
				t.Errorf("Compare(%s,%s)=%d want %d", asc[i], asc[j], got, want)
			}
		}
	}
}

func TestParseVersionRefusesWhatTheManifestNeverCarries(t *testing.T) {
	for _, bad := range []string{"", "v1.0.0", "1.0", "1.0.0.0", "01.0.0", "1.0.0+build", "1.0.0-", "1.0.0-a..b", "latest", "1.0.x"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
}

func goodManifest() string {
	return `{"schema":1,"version":"1.2.0","channel":"stable","publishedAt":"2026-11-26T00:00:00Z","images":{"ui":"` + img("ui", d1) + `"},"minFrom":null,"rollbackSafeTo":"1.1.0"}`
}

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest()))
	if err != nil {
		t.Fatal(err)
	}
	r := ReleaseFromManifest(m)
	if r.Verified {
		t.Fatal("a freshly parsed release must not be verified")
	}
	if r.RollbackSafeTo != "1.1.0" || r.MinFrom != "" {
		t.Fatalf("policy fields: %+v", r)
	}
}

func TestParseManifestAcceptsTheUpdaterImageButNeverPlansIt(t *testing.T) {
	doc := strings.Replace(goodManifest(), `"images":{`, `"images":{"updater":"`+img("updater", d1)+`",`, 1)
	m, err := ParseManifest([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if m.Images[UpdaterComponent] == "" {
		t.Fatal("the updater image was dropped")
	}
	// The updater is applied by the operator: it is not one of the components
	// the updater patches, so a plan never holds a step for it.
	for _, c := range Components {
		if c == UpdaterComponent {
			t.Fatal("the updater must not be an apply-order component")
		}
	}
	in := fixture()
	rel := in.Releases["1.2.0"]
	rel.Images = map[string]string{"updater": img("updater", d2)}
	in.Releases["1.2.0"] = rel
	if p := BuildPlan(in); len(p.Steps) != 0 {
		t.Fatalf("steps = %+v", p.Steps)
	}
}

func TestParseManifestRefuses(t *testing.T) {
	cases := map[string]string{
		"tag instead of digest": strings.Replace(goodManifest(), "@"+d1, ":1.2.0", 1),
		"unknown component":     strings.Replace(goodManifest(), `"ui"`, `"shell"`, 1),
		"unknown field":         strings.Replace(goodManifest(), `"schema":1,`, `"schema":1,"extra":true,`, 1),
		"wrong schema":          strings.Replace(goodManifest(), `"schema":1`, `"schema":2`, 1),
		"pre-release on stable": strings.Replace(goodManifest(), `"version":"1.2.0"`, `"version":"1.2.0-rc.1"`, 1),
		"release on beta":       strings.Replace(goodManifest(), `"channel":"stable"`, `"channel":"beta"`, 1),
		"bad policy version":    strings.Replace(goodManifest(), `"rollbackSafeTo":"1.1.0"`, `"rollbackSafeTo":"soon"`, 1),
		"no images":             `{"schema":1,"version":"1.2.0","channel":"stable","publishedAt":"x","images":{},"minFrom":null,"rollbackSafeTo":null}`,
		"not json":              `{`,
	}
	for name, doc := range cases {
		if _, err := ParseManifest([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fixture: running 1.1.0, release 1.2.0 verified, everything at rest.
func fixture() Input {
	return Input{
		Request: Request{TargetVersion: "1.2.0"},
		Current: "1.1.0",
		Running: []Running{{"controller", img("controller", d1)}, {"ui", img("ui", d1)}, {"uiproxy", img("uiproxy", d1)}},
		Releases: map[string]Release{
			"1.1.0": {Version: "1.1.0", Verified: true, RollbackSafeTo: "1.0.0", Images: map[string]string{"controller": img("controller", d1)}},
			"1.2.0": {Version: "1.2.0", Verified: true, Images: map[string]string{
				"ui": img("ui", d2), "controller": img("controller", d2), "uiproxy": img("uiproxy", d1), "agent": img("agent", d2),
			}},
		},
	}
}

func TestPlanOrdersStepsAndSkipsWhatDoesNotChange(t *testing.T) {
	p := BuildPlan(fixture())
	if p.Refusal != nil {
		t.Fatalf("refused: %+v", p.Refusal)
	}
	// controller before ui (the console goes last); uiproxy is unchanged and
	// agent is not run by this install, so neither appears.
	if len(p.Steps) != 2 || p.Steps[0].Component != "controller" || p.Steps[1].Component != "ui" {
		t.Fatalf("steps: %+v", p.Steps)
	}
	if p.Steps[0].From != img("controller", d1) || p.Steps[0].To != img("controller", d2) {
		t.Fatalf("step 0: %+v", p.Steps[0])
	}
}

func refusal(t *testing.T, in Input) Refusal {
	t.Helper()
	p := BuildPlan(in)
	if p.Refusal == nil {
		t.Fatalf("expected a refusal, got steps %+v", p.Steps)
	}
	return *p.Refusal
}

func TestHardRefusalsAreNeverLiftedByForce(t *testing.T) {
	mut := map[string]func(*Input){
		UnknownKey: func(in *Input) { in.Request.TargetVersion = "9.9.9" },
		RefusedUnverified: func(in *Input) {
			r := in.Releases["1.2.0"]
			r.Verified = false
			in.Releases["1.2.0"] = r
		},
		RefusedRevoked: func(in *Input) {
			r := in.Releases["1.2.0"]
			r.Revoked = true
			in.Releases["1.2.0"] = r
		},
		RefusedAlreadyCurrent:  func(in *Input) { in.Current = "1.2.0" },
		RefusedDowngrade:       func(in *Input) { in.Current = "1.3.0" },
		RefusedBelowMinFrom:    func(in *Input) { r := in.Releases["1.2.0"]; r.MinFrom = "1.1.5"; in.Releases["1.2.0"] = r },
		RefusedOSDReplacement:  func(in *Input) { in.Activity.OSDReplacementsActive = 1 },
		RefusedNothingToChange: func(in *Input) { in.Running = []Running{{"uiproxy", img("uiproxy", d1)}} },
		RefusedUnknownCurrent:  func(in *Input) { in.Current = "" },
	}
	for code, f := range mut {
		in := fixture()
		in.Request.Force = true
		f(&in)
		got := refusal(t, in)
		want := code
		if code == UnknownKey {
			want = RefusedUnknownRelease
		}
		if got.Code != want {
			t.Errorf("%s: got %s (%s)", code, got.Code, got.Message)
		}
		if got.Overridable {
			t.Errorf("%s must not be overridable", code)
		}
	}
}

const UnknownKey = "unknown-target"

func TestWorkInFlightIsLiftedByForceOnly(t *testing.T) {
	in := fixture()
	in.Activity = Activity{RunningJobs: 2, UnreadyComponents: 1}
	r := refusal(t, in)
	if r.Code != RefusedInFlight || !r.Overridable {
		t.Fatalf("got %+v", r)
	}
	in.Request.Force = true
	if p := BuildPlan(in); p.Refusal != nil {
		t.Fatalf("force should lift work in flight: %+v", p.Refusal)
	}
}

func TestRollback(t *testing.T) {
	in := fixture()
	in.Current = "1.2.0"
	in.Previous = "1.1.0"
	in.Request = Request{Rollback: true}
	in.Running = []Running{{"controller", img("controller", d2)}, {"ui", img("ui", d2)}}
	in.Releases["1.1.0"] = Release{Version: "1.1.0", Verified: true, Images: map[string]string{"controller": img("controller", d1), "ui": img("ui", d1)}}
	r := in.Releases["1.2.0"]
	r.RollbackSafeTo = "1.0.0"
	in.Releases["1.2.0"] = r

	p := BuildPlan(in)
	if p.Refusal != nil || p.Target != "1.1.0" || len(p.Steps) != 2 {
		t.Fatalf("rollback: %+v", p)
	}

	t.Run("no previous version", func(t *testing.T) {
		x := in
		x.Previous = ""
		if got := refusal(t, x); got.Code != RefusedNoPrevious {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("running release offers no rollback", func(t *testing.T) {
		x := in
		x.Releases = map[string]Release{"1.1.0": in.Releases["1.1.0"], "1.2.0": {Version: "1.2.0", Verified: true, Images: in.Releases["1.2.0"].Images}}
		if got := refusal(t, x); got.Code != RefusedRollbackUnsafe {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("previous is older than the safe floor", func(t *testing.T) {
		x := in
		x.Previous = "0.9.0"
		x.Releases = map[string]Release{"0.9.0": {Version: "0.9.0", Verified: true, Images: map[string]string{"ui": img("ui", d1)}}, "1.2.0": in.Releases["1.2.0"]}
		if got := refusal(t, x); got.Code != RefusedRollbackUnsafe {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("running version unknown", func(t *testing.T) {
		x := in
		x.Releases = map[string]Release{"1.1.0": in.Releases["1.1.0"]}
		if got := refusal(t, x); got.Code != RefusedUnknownCurrent {
			t.Fatalf("%+v", got)
		}
	})
}

func TestPlanIsDeterministic(t *testing.T) {
	a, b := BuildPlan(fixture()), BuildPlan(fixture())
	if len(a.Steps) != len(b.Steps) {
		t.Fatal("length differs")
	}
	for i := range a.Steps {
		if a.Steps[i] != b.Steps[i] {
			t.Fatalf("step %d differs", i)
		}
	}
}
