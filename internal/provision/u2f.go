package provision

import (
	"fmt"
	"regexp"
	"strings"
)

// Console login with a FIDO2 key (pam_u2f), instead of a password.
//
// The `frame` account has no password, so before this the local console was a
// login prompt nobody could pass: the machine could be reached by SSH key only.
// With U2FKeys set, the console accepts a key plus its PIN. Nothing else
// changes: SSH stays key-only, sudo stays as it was, and /etc/pam.d/login is the
// only PAM file touched (not sshd, not sudo, not a display manager).
//
// What the image carries is public: a credential is a key handle and a public
// key (pamu2fcfg's output), useless without the physical key and its PIN.
// Enrolling happens beforehand, on the operator's machine (hack/enroll-yubikey.sh):
// touching a key is not something an unattended install can ask for.

const (
	// u2fOrigin is the WebAuthn origin and appid, fixed. pamu2fcfg binds a
	// credential to an origin, and its default is "pam://<hostname of the
	// machine enrolling>": a key enrolled on a laptop would then be refused by
	// every node, whose hostname differs. One constant on both sides.
	u2fOrigin = "pam://hearthrack"
	// u2fAuthFile is where the installed system reads credentials from.
	u2fAuthFile = "/etc/Yubico/u2f_keys"
	// maxU2FKeys: a second key for when the first is lost, and room for two
	// operators. More is an access list, not a recovery path.
	maxU2FKeys = 4
	// pamU2FModule is where Ubuntu 24.04 (amd64) puts the module.
	pamU2FModule = "/usr/lib/x86_64-linux-gnu/security/pam_u2f.so"
)

// u2fKeyShape is one credential as pamu2fcfg prints it after the username:
// keyHandle,publicKey,type,options. Base64 (standard or URL alphabet) for the
// two blobs, a COSE type name, and options like "+presence+pin". Nothing else
// can reach the shell command that writes it: the file travels base64-encoded
// anyway, but a shape that cannot hold a newline, colon or quote is also what
// makes the `frame:k1:k2` file unambiguous.
var u2fKeyShape = regexp.MustCompile(`^[A-Za-z0-9+/_=-]{16,1000},[A-Za-z0-9+/_=-]{16,512},(es256|rs256|eddsa),[a-z+-]{0,40}$`)

func validateU2FKeys(keys []string) error {
	if len(keys) > maxU2FKeys {
		return fmt.Errorf("u2f: at most %d keys, got %d; this is a recovery path, not an access list", maxU2FKeys, len(keys))
	}
	seen := map[string]bool{}
	for i, k := range keys {
		if !u2fKeyShape.MatchString(k) {
			return fmt.Errorf("u2f key %d: not a pam_u2f credential (keyHandle,publicKey,es256|rs256|eddsa,options); pass the part pamu2fcfg prints after \"frame:\"", i+1)
		}
		if seen[k] {
			return fmt.Errorf("u2f key %d: listed twice", i+1)
		}
		seen[k] = true
	}
	return nil
}

// u2fAuthLine is the PAM line the console login gets, before common-auth.
// `sufficient`: a key plus its PIN is enough and no password is asked, and a
// failure falls through to the stock stack, which cannot succeed (no password).
// A success skips the rest of the stack, including what common-auth and the
// lines after it do (pam_cap, pam_group): both are no-ops in the stock file.
// pinverification=1 makes the PIN mandatory whatever the credential's options
// say: a key lying on a desk must not open a console. cue prints the prompt to
// touch the key.
func u2fAuthLine() string {
	return fmt.Sprintf("auth sufficient pam_u2f.so authfile=%s origin=%s appid=%s pinverification=1 cue",
		u2fAuthFile, u2fOrigin, u2fOrigin)
}

// u2fConsoleLogin is the late-command that enables the login. It refuses (exit
// 1, install aborted) when the module is not in /target or when PAM ends up
// without the line, rather than leaving a node that looks configured and is
// not. target is a parameter so the tests run it against a temporary tree.
func u2fConsoleLogin(keys []string, target string) string {
	file := "frame:" + strings.Join(keys, ":") + "\n"
	pam := target + "/etc/pam.d/login"
	refuse := func(why string) string {
		return fmt.Sprintf("echo 'FRAME: %s, refusing' > /dev/console; exit 1", why)
	}
	return strings.Join([]string{
		fmt.Sprintf("[ -f '%s%s' ] || { %s; }", target, pamU2FModule, refuse("libpam-u2f is not installed in the target")),
		fmt.Sprintf("grep -q '^@include common-auth' '%s' || { %s; }", pam, refuse("/etc/pam.d/login has no common-auth include to put the key login before")),
		fmt.Sprintf("mkdir -p '%s/etc/Yubico' && %s", target, writeFileCommand(target+u2fAuthFile, file, "644")),
		// Written once: a login file that already carries the line is left alone.
		fmt.Sprintf("grep -q '^auth sufficient pam_u2f.so' '%[1]s' || sed -i '/^@include common-auth/i %[2]s' '%[1]s'", pam, u2fAuthLine()),
		fmt.Sprintf("grep -q '^auth sufficient pam_u2f.so' '%s' || { %s; }", pam, refuse("the key login line did not reach /etc/pam.d/login")),
	}, "; ")
}
