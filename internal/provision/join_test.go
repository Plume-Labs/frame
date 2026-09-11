package provision

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// errNotFound is what a Session.ReadFile returns for a path that is not
// there -- the same shape fakeSession.ReadFile uses for its own "no such
// file" case, named here because recordingSession's files map needs the same
// miss behavior and job.go's callers must be able to tell "not found" apart
// from a real transport error in the tests below.
var errNotFound = errors.New("no such file")

// recordingSession embeds fakeSession so it inherits HostKey/Close, but
// overrides Run and ReadFile to record what Join actually sends rather than
// the fixed answers fakeSession gives every caller.
type recordingSession struct {
	fakeSession
	cmds  []string
	files map[string]string
}

func (r *recordingSession) Run(_ context.Context, cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	return "", nil
}

func (r *recordingSession) ReadFile(_ context.Context, path string) ([]byte, error) {
	if b, ok := r.files[path]; ok {
		return []byte(b), nil
	}
	return nil, errNotFound
}

const k3sKubeconfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: QQ==
    server: https://127.0.0.1:6443
  name: default
contexts:
- context: {cluster: default, user: default}
  name: default
current-context: default
kind: Config
users:
- name: default
  user: {client-certificate-data: QQ==, client-key-data: QQ==}
`

// Creating a cluster needs no token. That is the fact that makes the G9
// installable today, and it is the opposite of what one expects.
func TestJoinInitNeedsNoToken(t *testing.T) {
	s := &recordingSession{files: map[string]string{"/etc/rancher/k3s/k3s.yaml": k3sKubeconfig}}
	kc, err := Join(context.Background(), s, ClusterTarget{Mode: ClusterInit, K3sVersion: "v1.33.4+k3s1"}, "192.168.2.210")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(s.cmds, "\n")
	if !strings.Contains(joined, "--cluster-init") {
		t.Errorf("no --cluster-init in:\n%s", joined)
	}
	if !strings.Contains(joined, `INSTALL_K3S_VERSION='v1.33.4+k3s1'`) {
		t.Error("the k3s version is not pinned")
	}
	if !strings.Contains(string(kc), "https://192.168.2.210:6443") {
		t.Errorf("kubeconfig still points at localhost:\n%s", kc)
	}
}

// A cluster that exists and nobody can talk to is not a delivered cluster.
func TestJoinInitFailsLoudlyIfTheKubeconfigNeverAppears(t *testing.T) {
	s := &recordingSession{files: map[string]string{}}
	if _, err := Join(context.Background(), s, ClusterTarget{Mode: ClusterInit, K3sVersion: "v1.33.4+k3s1"}, "192.168.2.210"); err == nil {
		t.Fatal("want an error when k3s.yaml is not there, got nil")
	}
}

func TestJoinRequiresATokenAndAServerURL(t *testing.T) {
	for name, target := range map[string]ClusterTarget{
		"no token":  {Mode: ClusterJoin, ServerURL: "https://192.168.2.201:6443", K3sVersion: "v1.33.4+k3s1"},
		"no server": {Mode: ClusterJoin, JoinToken: "K10x", K3sVersion: "v1.33.4+k3s1"},
	} {
		s := &recordingSession{files: map[string]string{}}
		if _, err := Join(context.Background(), s, target, "192.168.2.210"); err == nil {
			t.Errorf("%s: want an error, got nil", name)
		}
	}
}

// The kubeconfig fixture is supplied so nothing downstream of
// validateK3sVersion could produce the error instead: with an empty files
// map, Join would still fail -- at the ReadFile step -- even with the
// version guard disabled entirely, and the test would prove nothing about
// that guard specifically.
func TestJoinRefusesAnUnpinnedK3sVersion(t *testing.T) {
	s := &recordingSession{files: map[string]string{"/etc/rancher/k3s/k3s.yaml": k3sKubeconfig}}
	if _, err := Join(context.Background(), s, ClusterTarget{Mode: ClusterInit}, "192.168.2.210"); err == nil {
		t.Fatal("want an error for an empty k3s version, got nil")
	}
}

func TestRewriteKubeconfigServerReplacesOnlyTheServer(t *testing.T) {
	out, err := RewriteKubeconfigServer([]byte(k3sKubeconfig), "192.168.2.210")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "127.0.0.1") {
		t.Error("localhost survived the rewrite")
	}
	if !strings.Contains(string(out), "certificate-authority-data") {
		t.Error("the rewrite dropped the CA data")
	}
}

// An IPv6 node address must come out bracketed in the rewritten server URL
// ("[::1]:6443", not "::1:6443", which is not even a valid URL authority).
func TestRewriteKubeconfigServerBracketsAnIPv6Address(t *testing.T) {
	out, err := RewriteKubeconfigServer([]byte(k3sKubeconfig), "::1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "https://[::1]:6443") {
		t.Errorf("IPv6 address was not bracketed:\n%s", out)
	}
}

// A kubeconfig whose clusters list has entries, but none shaped like
// clusters[].cluster.server, must fail loudly rather than return the
// original document unchanged and unrewritten: silently succeeding here is
// the same "cluster nobody can talk to" failure decision 6 exists to catch,
// just triggered by a malformed document instead of a missing one.
func TestRewriteKubeconfigServerFailsIfNoClusterEntryHasTheExpectedShape(t *testing.T) {
	const malformed = `clusters:
- name: default
`
	if _, err := RewriteKubeconfigServer([]byte(malformed), "192.168.2.210"); err == nil {
		t.Fatal("a kubeconfig with no cluster.server field to rewrite was accepted")
	}
}

// A token carrying shell metacharacters never reaches the command line at
// all: validateJoinToken's character allowlist refuses it before Join builds
// or sends anything. shellQuote is a second line of defence for values that
// pass validation, not the only one -- it is proven independently, on its
// own, in TestShellQuoteEscapesEmbeddedSingleQuotes below, because an
// untested second line is not a line of defence.
func TestJoinRefusesAJoinTokenThatWouldNeedShellEscaping(t *testing.T) {
	s := &recordingSession{files: map[string]string{}}
	target := ClusterTarget{
		Mode:       ClusterJoin,
		ServerURL:  "https://192.168.2.201:6443",
		JoinToken:  "K10x'; rm -rf / #",
		K3sVersion: "v1.33.4+k3s1",
	}
	if _, err := Join(context.Background(), s, target, "192.168.2.210"); err == nil {
		t.Fatal("a join token containing shell metacharacters was accepted")
	}
	if len(s.cmds) != 0 {
		t.Errorf("Join ran a command before validating the token: %v", s.cmds)
	}
}

// shellQuote is tested directly, independent of the validators above, because
// it is the second line of defence and an untested second line is not one.
func TestShellQuoteEscapesEmbeddedSingleQuotes(t *testing.T) {
	got := shellQuote(`it's a token`)
	want := `'it'\''s a token'`
	if got != want {
		t.Errorf("shellQuote(%q) = %q, want %q", `it's a token`, got, want)
	}
}

// --- validateK3sVersion ---
//
// "1.33.4" (no v, no +k3sN) is shell-safe on its own -- it would pass any
// guard that only looks for metacharacters -- so refusing it is what proves
// this is a shape check, not a character blocklist wearing a version-shaped
// name.
func TestValidateK3sVersionRefusesAMissingVPrefix(t *testing.T) {
	if err := validateK3sVersion("1.33.4+k3s1"); err == nil {
		t.Fatal("a version missing its v prefix was accepted")
	}
}

func TestValidateK3sVersionRefusesAMissingK3sSuffix(t *testing.T) {
	if err := validateK3sVersion("v1.33.4"); err == nil {
		t.Fatal("a version missing its +k3sN suffix was accepted")
	}
}

func TestValidateK3sVersionAcceptsAWellFormedVersion(t *testing.T) {
	if err := validateK3sVersion("v1.33.4+k3s1"); err != nil {
		t.Errorf("a well-formed version was refused: %v", err)
	}
}

// --- validateJoinToken ---
//
// "K10xTOKEN/extra" contains a "/", which is not a shell metacharacter --
// nothing about it needs shellQuote's escaping -- so refusing it is what
// proves the check is a character-set allowlist tied to what a k3s token
// actually looks like, not injection defence wearing a token-shaped name.
func TestValidateJoinTokenRefusesACharacterOutsideTheSet(t *testing.T) {
	if err := validateJoinToken("K10xTOKEN/extra"); err == nil {
		t.Fatal("a token containing a character outside [A-Za-z0-9:._-] was accepted")
	}
}

func TestValidateJoinTokenRefusesAnEmptyToken(t *testing.T) {
	if err := validateJoinToken(""); err == nil {
		t.Fatal("an empty token was accepted")
	}
}

func TestValidateJoinTokenRefusesATokenLongerThanTheBound(t *testing.T) {
	if err := validateJoinToken(strings.Repeat("a", maxJoinTokenLen+1)); err == nil {
		t.Fatal("a token past the length bound was accepted")
	}
}

func TestValidateJoinTokenAcceptsAWellFormedToken(t *testing.T) {
	if err := validateJoinToken("K10abcdef123::server:mypassword"); err != nil {
		t.Errorf("a well-formed token was refused: %v", err)
	}
}

// --- validateServerURL ---
//
// "https://192.168.2.201:6443/extra" is shell-safe -- a path segment is not
// a metacharacter -- so refusing it is what proves the check enforces "k3s
// server URL", not merely "parses as a URL".
func TestValidateServerURLRefusesAPath(t *testing.T) {
	if err := validateServerURL("https://192.168.2.201:6443/extra"); err == nil {
		t.Fatal("a server URL with a path was accepted")
	}
}

func TestValidateServerURLRefusesNonHTTPS(t *testing.T) {
	if err := validateServerURL("http://192.168.2.201:6443"); err == nil {
		t.Fatal("a non-https server URL was accepted")
	}
}

// "https://" (no host, and also no path/query/fragment) is what isolates
// this guard: "https:///path" also has an empty host, but the path check
// below would refuse it too, so disabling the host check alone would not
// turn that test red -- it would prove nothing about this branch specifically.
func TestValidateServerURLRefusesNoHost(t *testing.T) {
	if err := validateServerURL("https://"); err == nil {
		t.Fatal("a server URL with no host was accepted")
	}
}

// "https://user:pass@192.168.2.201:6443" satisfies every other check here --
// https scheme, non-empty host, no path/query/fragment -- so refusing it is
// what isolates this guard specifically: disabling it alone, and nothing
// else, is what would need to turn this test red.
func TestValidateServerURLRefusesEmbeddedCredentials(t *testing.T) {
	if err := validateServerURL("https://user:pass@192.168.2.201:6443"); err == nil {
		t.Fatal("a server URL with embedded credentials was accepted")
	}
}

func TestValidateServerURLAcceptsSchemeAndHostOnly(t *testing.T) {
	if err := validateServerURL("https://192.168.2.201:6443"); err != nil {
		t.Errorf("a scheme-and-host-only server URL was refused: %v", err)
	}
}

// --- validateNodeAddress ---
//
// "node1.example.internal" is shell-safe -- it is what a hostname reaching
// this point would actually look like -- so refusing it is what proves the
// check requires an IP literal, not merely "looks like an address".
func TestValidateNodeAddressRefusesAHostname(t *testing.T) {
	if err := validateNodeAddress("node1.example.internal"); err == nil {
		t.Fatal("a hostname was accepted as a node address")
	}
}

func TestValidateNodeAddressAcceptsAnIP(t *testing.T) {
	if err := validateNodeAddress("192.168.2.210"); err != nil {
		t.Errorf("a valid IP was refused: %v", err)
	}
}

// --- wiring: Join actually calls the validators above, not just the
// empty-string checks the earlier tests in this file cover ---

// Same masking risk as TestJoinRefusesAnUnpinnedK3sVersion above, and it
// bit this exact test in review: with an empty files map, disabling
// validateK3sVersion entirely still left this test green, because Join fails
// at the ReadFile step regardless. The kubeconfig fixture below closes that
// -- with it present, a defeated version guard makes Join succeed all the
// way through, so only the guard being intact turns this red for the right
// reason.
func TestJoinRefusesAMalformedK3sVersion(t *testing.T) {
	s := &recordingSession{files: map[string]string{"/etc/rancher/k3s/k3s.yaml": k3sKubeconfig}}
	if _, err := Join(context.Background(), s, ClusterTarget{Mode: ClusterInit, K3sVersion: "1.33.4"}, "192.168.2.210"); err == nil {
		t.Fatal("a malformed but non-empty k3s version was accepted")
	}
}

func TestJoinRefusesAServerURLWithAPath(t *testing.T) {
	s := &recordingSession{files: map[string]string{}}
	target := ClusterTarget{Mode: ClusterJoin, ServerURL: "https://192.168.2.201:6443/extra", JoinToken: "K10abcdef123::server:mypassword", K3sVersion: "v1.33.4+k3s1"}
	if _, err := Join(context.Background(), s, target, "192.168.2.210"); err == nil {
		t.Fatal("a server URL with a path was accepted")
	}
}

// The kubeconfig fixture is supplied here, unlike the other Join-level
// wiring tests: with no fixture, Join would fail at the ReadFile step
// regardless of whether the node address guard ran, and the test would prove
// nothing about that guard specifically. With the fixture present, a defeated
// guard makes Join succeed -- proceeding all the way to
// RewriteKubeconfigServer with an address that was never checked -- so only
// the guard being intact turns this red for the right reason.
func TestJoinRefusesANodeAddressThatIsNotAnIP(t *testing.T) {
	s := &recordingSession{files: map[string]string{"/etc/rancher/k3s/k3s.yaml": k3sKubeconfig}}
	if _, err := Join(context.Background(), s, ClusterTarget{Mode: ClusterInit, K3sVersion: "v1.33.4+k3s1"}, "not-an-ip"); err == nil {
		t.Fatal("a non-IP node address was accepted")
	}
}
