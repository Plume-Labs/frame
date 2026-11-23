package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	u2fKeyA = "Z3JlZW5Ub21hdG9LZXlIYW5kbGU,cHVibGljS2V5QmxvYkFBQUFBQQ==,es256,+presence+pin"
	u2fKeyB = "YmFja3VwS2V5SGFuZGxlQkJC,YmFja3VwUHVibGljS2V5QkJCQg==,es256,+presence+pin"
)

func u2fSpec() Spec {
	s := goodSpec()
	s.U2FKeys = []string{u2fKeyA, u2fKeyB}
	return s
}

// With no key the document is what it was: no extra package, no PAM edit.
func TestRenderAutoinstallWithoutU2FChangesNothing(t *testing.T) {
	ai := parsed(t, goodSpec(), "")
	if _, ok := ai["packages"]; ok {
		t.Error("packages rendered without a u2f key")
	}
	for _, c := range commands(t, ai, "late-commands") {
		if strings.Contains(c, "pam_u2f") || strings.Contains(c, "pam.d") {
			t.Errorf("PAM touched without a u2f key: %s", c)
		}
	}
}

// With keys: the module is installed from the archive, the login is set up
// before the relock (last), and no credential travels in clear text in the
// command (the file is base64) nor does anything but public material.
func TestRenderAutoinstallWithU2FInstallsTheModuleAndConfiguresTheLogin(t *testing.T) {
	ai := parsed(t, u2fSpec(), "")
	pk, _ := ai["packages"].([]any)
	if len(pk) != 1 || pk[0] != "libpam-u2f" {
		t.Errorf("packages = %v, want [libpam-u2f]", ai["packages"])
	}
	late := commands(t, ai, "late-commands")
	n := -1
	for i, c := range late {
		if strings.Contains(c, "pam_u2f.so") {
			n = i
		}
	}
	if n < 0 {
		t.Fatalf("no late-command configures the login: %v", late)
	}
	if !strings.Contains(late[len(late)-1], "99-zz-frame-lock") {
		t.Errorf("the relock command must stay last: %v", late[len(late)-1])
	}
	if strings.Contains(late[n], "pinverification=1") == false {
		t.Errorf("PIN verification must be mandatory: %s", late[n])
	}
}

// The command, run against a temporary tree shaped like the target.
func TestU2FConsoleLoginWritesTheKeysAndTheLineBeforeCommonAuthWhenRun(t *testing.T) {
	target := t.TempDir()
	mk := func(path, body string) {
		p := filepath.Join(target, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(pamU2FModule, "")
	mk("/etc/pam.d/login", "# header\nauth optional pam_faildelay.so delay=3000000\n@include common-auth\nauth optional pam_group.so\n")

	cmd := u2fConsoleLogin([]string{u2fKeyA, u2fKeyB}, target)
	if code, out := runSh(t, cmd, t.TempDir()); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, cmd, out)
	}
	keys, err := os.ReadFile(filepath.Join(target, u2fAuthFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := "frame:" + u2fKeyA + ":" + u2fKeyB + "\n"; string(keys) != want {
		t.Errorf("u2f_keys = %q, want %q", keys, want)
	}
	login, _ := os.ReadFile(filepath.Join(target, "/etc/pam.d/login"))
	lines := strings.Split(string(login), "\n")
	var u2f, common = -1, -1
	for i, l := range lines {
		if strings.HasPrefix(l, "auth sufficient pam_u2f.so") {
			u2f = i
		}
		if strings.HasPrefix(l, "@include common-auth") {
			common = i
		}
	}
	if u2f < 0 || common < 0 || u2f >= common {
		t.Errorf("the key line must come right before common-auth:\n%s", login)
	}
	for _, want := range []string{"authfile=/etc/Yubico/u2f_keys", "origin=pam://hearthrack", "appid=pam://hearthrack", "pinverification=1"} {
		if !strings.Contains(string(login), want) {
			t.Errorf("login line lacks %s:\n%s", want, login)
		}
	}
	// A second run does not stack a second line.
	if code, out := runSh(t, cmd, t.TempDir()); code != 0 {
		t.Fatalf("second run exit %d\n%s", code, out)
	}
	again, _ := os.ReadFile(filepath.Join(target, "/etc/pam.d/login"))
	if strings.Count(string(again), "pam_u2f.so") != 1 {
		t.Errorf("line written twice:\n%s", again)
	}
}

// The install is aborted, not finished half-configured.
func TestU2FConsoleLoginRefusesWhenTheTargetCannotTakeItWhenRun(t *testing.T) {
	for name, setup := range map[string]func(string){
		"module not installed": func(target string) {
			_ = os.MkdirAll(filepath.Join(target, "etc/pam.d"), 0o755)
			_ = os.WriteFile(filepath.Join(target, "etc/pam.d/login"), []byte("@include common-auth\n"), 0o644)
		},
		"no common-auth include to anchor on": func(target string) {
			_ = os.MkdirAll(filepath.Join(target, filepath.Dir(pamU2FModule)), 0o755)
			_ = os.WriteFile(filepath.Join(target, pamU2FModule), nil, 0o644)
			_ = os.MkdirAll(filepath.Join(target, "etc/pam.d"), 0o755)
			_ = os.WriteFile(filepath.Join(target, "etc/pam.d/login"), []byte("auth required pam_unix.so\n"), 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			target := t.TempDir()
			setup(target)
			if code, _ := runSh(t, u2fConsoleLogin([]string{u2fKeyA}, target), t.TempDir()); code == 0 {
				t.Error("exit 0, want a refusal")
			}
			if _, err := os.Stat(filepath.Join(target, u2fAuthFile)); err == nil {
				t.Error("credentials written although the login was refused")
			}
		})
	}
}

func TestValidateSpecRefusesBadU2FKeys(t *testing.T) {
	for name, keys := range map[string][]string{
		"empty string":           {""},
		"a whole mapping":        {"frame:" + u2fKeyA},
		"a newline":              {u2fKeyA + "\nroot:" + u2fKeyB},
		"a colon":                {u2fKeyA + ":" + u2fKeyB},
		"a quote":                {"Z3JlZW5Ub21hdG9LZXlIYW5kbGU,cHVibGljS2V5QmxvYkFB'QQ==,es256,+presence"},
		"unknown type":           {"Z3JlZW5Ub21hdG9LZXlIYW5kbGU,cHVibGljS2V5QmxvYkFBQUFBQQ==,md5,+presence"},
		"duplicate":              {u2fKeyA, u2fKeyA},
		"more than four":         {u2fKeyA, u2fKeyB, u2fKeyA + "x", u2fKeyB + "x", u2fKeyA + "y"},
		"a private-looking blob": {"-----BEGIN PRIVATE KEY-----,cHVibGljS2V5QmxvYkFBQUFBQQ==,es256,"},
	} {
		t.Run(name, func(t *testing.T) {
			s := goodSpec()
			s.U2FKeys = keys
			if err := ValidateSpec(s); err == nil {
				t.Error("accepted")
			}
		})
	}
	if err := ValidateSpec(u2fSpec()); err != nil {
		t.Errorf("two good keys refused: %v", err)
	}
}
