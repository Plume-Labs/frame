package provision

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Join installs k3s and either starts a cluster or joins one.
//
// Starting a cluster is the simpler of the two, which is the opposite of what
// one expects: --cluster-init needs no token, while joining needs the one
// that lives at /var/lib/rancher/k3s/server/node-token on a server node,
// which a pod does not read. That asymmetry is why a machine with no cluster
// at all is installable today and a machine joining one needs an
// administrator first.
//
// The k3s installer is fetched from https://get.k3s.io at a pinned
// INSTALL_K3S_VERSION -- an internet dependency, stated in the design (§7)
// rather than hidden.
//
// K3sVersion, ServerURL, JoinToken and nodeAddress are not passed through
// checkPreseedValue. That function exists for a different hazard: a value
// that lands, unescaped, in a preseed directive (where a newline starts a new
// directive that runs as root) and in a naively-interpolated shell word
// inside early_command/late_command, so it refuses quotes and backslashes
// outright because nothing downstream of it re-escapes them. Neither
// condition holds here -- this command never touches a preseed file -- and a
// generic refusal would also be looser than what these four values actually
// are: a k3s version, an https URL, a k3s token and an IP address each have a
// known shape, and a shape check (validateK3sVersion, validateServerURL,
// validateJoinToken, validateNodeAddress below) refuses a bad value by
// construction rather than by character blocklist. ClusterTarget reaches Join
// from a CRD an operator fills in, not from a literal already validated
// elsewhere in this package -- nothing else in internal/provision touches
// K3sVersion, ServerURL or JoinToken (checked: only join.go and types.go
// reference them) -- so "already trusted by the time it gets here" is never
// true and each of the four is validated in this function.
//
// Validation is the first line of defence and shellQuote (below) is the
// second: every value is quoted into the command line regardless of having
// already passed its shape check, so a future change that loosens a pattern
// does not by itself reopen shell injection.
func Join(ctx context.Context, sess Session, t ClusterTarget, nodeAddress string) ([]byte, error) {
	if err := validateK3sVersion(t.K3sVersion); err != nil {
		return nil, fmt.Errorf("cluster: %w; an unpinned or malformed install makes two machines built a week apart different machines", err)
	}
	if err := validateNodeAddress(nodeAddress); err != nil {
		return nil, fmt.Errorf("cluster: %w", err)
	}

	switch t.Mode {
	case ClusterInit:
		cmd := fmt.Sprintf(
			"curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=%s sh -s - server --cluster-init --node-ip %s",
			shellQuote(t.K3sVersion), shellQuote(nodeAddress))
		if _, err := sess.Run(ctx, cmd); err != nil {
			return nil, fmt.Errorf("k3s server --cluster-init: %w", err)
		}
		raw, err := sess.ReadFile(ctx, "/etc/rancher/k3s/k3s.yaml")
		if err != nil {
			return nil, fmt.Errorf("the cluster started but its kubeconfig is not readable, so nobody can talk to it: %w", err)
		}
		return RewriteKubeconfigServer(raw, nodeAddress)

	case ClusterJoin:
		if err := validateJoinToken(t.JoinToken); err != nil {
			return nil, fmt.Errorf("cluster join: %w. It lives at /var/lib/rancher/k3s/server/node-token on a server node and must be placed in a Secret first", err)
		}
		if err := validateServerURL(t.ServerURL); err != nil {
			return nil, fmt.Errorf("cluster join: %w", err)
		}
		cmd := fmt.Sprintf(
			"curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=%s K3S_URL=%s K3S_TOKEN=%s sh -s - agent --node-ip %s",
			shellQuote(t.K3sVersion), shellQuote(t.ServerURL), shellQuote(t.JoinToken), shellQuote(nodeAddress))
		if _, err := sess.Run(ctx, cmd); err != nil {
			return nil, fmt.Errorf("k3s agent: %w", err)
		}
		return nil, nil

	default:
		return nil, fmt.Errorf("cluster: unknown mode %q", t.Mode)
	}
}

// k3sVersionPattern is what INSTALL_K3S_VERSION actually looks like:
// "v1.33.4+k3s1". The anchors matter as much as the body -- without them,
// "v1.33.4+k3s1; rm -rf /" would match as a prefix of something the pattern
// only partially describes.
var k3sVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+$`)

// validateK3sVersion refuses anything that is not exactly a k3s version
// string. This is a shape check, not a character blocklist: "1.33.4" is
// shell-safe on its own -- it would pass any guard that only looks for
// metacharacters -- but it is not a k3s version, and
// INSTALL_K3S_VERSION=1.33.4 fetches a release that does not exist.
func validateK3sVersion(v string) error {
	if !k3sVersionPattern.MatchString(v) {
		return fmt.Errorf("k3s version %q does not look like a k3s version (want vX.Y.Z+k3sN)", v)
	}
	return nil
}

// joinTokenPattern is a k3s token's actual character set. A k3s token is
// shaped like K10<sha256 hex>::server:<password> -- letters, digits, colons,
// dots, underscores and hyphens. Nothing else belongs in one.
var joinTokenPattern = regexp.MustCompile(`^[A-Za-z0-9:._-]+$`)

// maxJoinTokenLen bounds the token independently of the character check: a
// well-formed-looking value of unbounded length is still not a token k3s
// issued.
const maxJoinTokenLen = 512

// validateJoinToken refuses anything outside a k3s token's character set or
// length, regardless of whether shellQuote would also escape it correctly.
func validateJoinToken(v string) error {
	if v == "" || len(v) > maxJoinTokenLen {
		return fmt.Errorf("join token: must be between 1 and %d characters, got %d", maxJoinTokenLen, len(v))
	}
	if !joinTokenPattern.MatchString(v) {
		return fmt.Errorf("join token: contains a character outside [A-Za-z0-9:._-]")
	}
	return nil
}

// validateServerURL refuses anything that is not exactly a k3s server
// address: https, a host, and nothing else. A k3s K3S_URL is
// "https://host:6443" -- no path, query or fragment belongs in one, and
// allowing them would let a value carry content past the point a reader
// expects the URL to end.
func validateServerURL(v string) error {
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("server URL %q does not parse: %w", v, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("server URL %q: scheme must be https, got %q", v, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("server URL %q: no host", v)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("server URL %q: must be scheme and host only, no path, query or fragment", v)
	}
	return nil
}

// validateNodeAddress refuses anything that is not an IP address literal.
// nodeAddress becomes --node-ip on the command line and the rewritten
// kubeconfig server address; only an IP belongs in either, never a hostname
// that could resolve to something else by the time it is used.
func validateNodeAddress(v string) error {
	if net.ParseIP(v) == nil {
		return fmt.Errorf("node address %q is not an IP address", v)
	}
	return nil
}

// RewriteKubeconfigServer replaces the server address in a k3s kubeconfig.
//
// It unmarshals into map[string]any and walks clusters[].cluster.server
// rather than round-tripping through a typed struct. A typed struct only
// knows the fields someone bothered to declare on it; anything else --
// certificate-authority-data among it -- is silently dropped on the way back
// out, and a kubeconfig with no CA data is a kubeconfig nobody can use to
// talk to the cluster it names.
func RewriteKubeconfigServer(in []byte, address string) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(in, &doc); err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}

	clustersRaw, ok := doc["clusters"]
	if !ok {
		return nil, fmt.Errorf("kubeconfig has no clusters field")
	}
	clusters, ok := clustersRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("kubeconfig clusters field is not a list")
	}
	if len(clusters) == 0 {
		return nil, fmt.Errorf("kubeconfig has no clusters")
	}

	newServer := fmt.Sprintf("https://%s:6443", address)
	rewritten := 0
	for _, c := range clusters {
		entry, ok := c.(map[string]any)
		if !ok {
			continue
		}
		clusterRaw, ok := entry["cluster"]
		if !ok {
			continue
		}
		cluster, ok := clusterRaw.(map[string]any)
		if !ok {
			continue
		}
		cluster["server"] = newServer
		rewritten++
	}
	if rewritten == 0 {
		return nil, fmt.Errorf("kubeconfig has no cluster.server field to rewrite")
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshaling kubeconfig: %w", err)
	}
	return out, nil
}

// shellQuote wraps a value in single quotes and escapes embedded ones, so it
// arrives at the remote shell as exactly one word regardless of its content.
// Single quotes are the strongest quoting POSIX shells offer: everything
// between them is literal, including backslashes, newlines and control
// characters, with the one exception being a single quote itself, which
// cannot appear inside a single-quoted string at all. That exception is
// handled by ending the quoted string, emitting a backslash-escaped literal
// quote outside it, and reopening the quoted string right after.
//
// This runs on every value Join places on a command line regardless of
// validation above: validation is what a well-behaved caller satisfies,
// quoting is what still holds if a future change loosens a pattern.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
