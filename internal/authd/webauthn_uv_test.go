package authd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// The tests in this file drive real WebAuthn ceremonies through go-webauthn
// with a software authenticator (an ES256 key held in memory), so that what
// is under test is the user verification decision itself, against the
// library's own parsing and signature checks — not a stub of them.

const (
	testRPID     = "example.com"
	testRPOrigin = "https://example.com"
)

type softAuthenticator struct {
	key   *ecdsa.PrivateKey
	rawID []byte
}

func newSoftAuthenticator(t *testing.T, id string) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	return &softAuthenticator{key: key, rawID: []byte(id)}
}

func (s *softAuthenticator) cosePublicKey(t *testing.T) []byte {
	t.Helper()
	raw, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{
			KeyType:   int64(webauthncose.EllipticKey),
			Algorithm: int64(webauthncose.AlgES256),
		},
		Curve:  int64(webauthncose.P256),
		XCoord: s.key.X.FillBytes(make([]byte, 32)),
		YCoord: s.key.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatalf("encoding COSE key: %v", err)
	}
	return raw
}

// enrolled is the credential as status.credentials would hold it.
func (s *softAuthenticator) enrolled(t *testing.T) framev1beta1.WebAuthnCredential {
	return framev1beta1.WebAuthnCredential{
		ID:        base64.RawURLEncoding.EncodeToString(s.rawID),
		PublicKey: base64.RawStdEncoding.EncodeToString(s.cosePublicKey(t)),
	}
}

func authFlags(uv bool, extra protocol.AuthenticatorFlags) byte {
	f := protocol.FlagUserPresent | extra
	if uv {
		f |= protocol.FlagUserVerified
	}
	return byte(f)
}

func rpIDHash() []byte {
	h := sha256.Sum256([]byte(testRPID))
	return h[:]
}

// challengeOf pulls the challenge (and the requested userVerification) back
// out of the options authd sent the browser.
func challengeOf(t *testing.T, options []byte) (challenge, userVerification string) {
	t.Helper()
	var parsed struct {
		PublicKey struct {
			Challenge              string `json:"challenge"`
			UserVerification       string `json:"userVerification"`
			AuthenticatorSelection struct {
				UserVerification string `json:"userVerification"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &parsed); err != nil {
		t.Fatalf("decoding options: %v", err)
	}
	uv := parsed.PublicKey.UserVerification
	if uv == "" {
		uv = parsed.PublicKey.AuthenticatorSelection.UserVerification
	}
	return parsed.PublicKey.Challenge, uv
}

func clientData(t *testing.T, kind, challenge string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": testRPOrigin})
	if err != nil {
		t.Fatalf("encoding client data: %v", err)
	}
	return raw
}

// assert signs an assertion for the login options, the way a browser would
// return it from navigator.credentials.get().
func (s *softAuthenticator) assert(t *testing.T, options []byte, userHandle string, uv bool) []byte {
	t.Helper()
	challenge, _ := challengeOf(t, options)
	cd := clientData(t, "webauthn.get", challenge)

	authData := append(rpIDHash(), authFlags(uv, 0))
	authData = binary.BigEndian.AppendUint32(authData, 1)

	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, s.key, digest[:])
	if err != nil {
		t.Fatalf("signing: %v", err)
	}

	id := base64.RawURLEncoding.EncodeToString(s.rawID)
	body, err := json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key",
		"response": map[string]string{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cd),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
			"userHandle":        base64.RawURLEncoding.EncodeToString([]byte(userHandle)),
		},
	})
	if err != nil {
		t.Fatalf("encoding assertion: %v", err)
	}
	return body
}

// attest builds a "none" attestation for the registration options, the way a
// browser would return it from navigator.credentials.create().
func (s *softAuthenticator) attest(t *testing.T, options []byte, uv bool) []byte {
	t.Helper()
	challenge, _ := challengeOf(t, options)
	cd := clientData(t, "webauthn.create", challenge)

	authData := append(rpIDHash(), authFlags(uv, protocol.FlagAttestedCredentialData))
	authData = binary.BigEndian.AppendUint32(authData, 0)
	authData = append(authData, make([]byte, 16)...) // AAGUID
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(s.rawID)))
	authData = append(authData, s.rawID...)
	authData = append(authData, s.cosePublicKey(t)...)

	attObj, err := webauthncbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": authData,
	})
	if err != nil {
		t.Fatalf("encoding attestation object: %v", err)
	}
	id := base64.RawURLEncoding.EncodeToString(s.rawID)
	body, err := json.Marshal(map[string]any{
		"id": id, "rawId": id, "type": "public-key",
		"response": map[string]string{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(cd),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attObj),
		},
	})
	if err != nil {
		t.Fatalf("encoding attestation: %v", err)
	}
	return body
}

// isPreferred reports whether options leave user verification at
// "preferred": spelled out, or omitted, which WebAuthn defines as the same
// thing. Log mode leaves the options exactly as they were before the mode
// existed, and they omit it.
func isPreferred(uv string) bool {
	return uv == "" || uv == string(protocol.VerificationPreferred)
}

func uvAuthenticator(t *testing.T, store *Store, mode UserVerificationMode) *Authenticator {
	t.Helper()
	a, err := NewAuthenticator(testRPID, testRPOrigin, store, testCodec(), WithUserVerification(mode))
	if err != nil {
		t.Fatalf("NewAuthenticator(%q): %v", mode, err)
	}
	return a
}

// loginFixture is alice, holding the soft key as her only credential.
func loginFixture(t *testing.T) (*Store, *softAuthenticator) {
	t.Helper()
	key := newSoftAuthenticator(t, "alice-key")
	return storeWith(t, fixture("alice", "alice@example.com", framev1beta1.RoleAdmin, key.enrolled(t))), key
}

func signCountOf(t *testing.T, s *Store) uint32 {
	t.Helper()
	u, err := s.ByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("ByEmail: %v", err)
	}
	return u.Status.Credentials[0].SignCount
}

func TestParseUserVerificationMode(t *testing.T) {
	for in, want := range map[string]UserVerificationMode{
		"":         UserVerificationLog,
		"log":      UserVerificationLog,
		"required": UserVerificationRequired,
	} {
		got, err := ParseUserVerificationMode(in)
		if err != nil || got != want {
			t.Fatalf("ParseUserVerificationMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// A typo must not fall back to the weaker mode.
	for _, in := range []string{"Required", "preferred", "require", "off"} {
		if _, err := ParseUserVerificationMode(in); err == nil {
			t.Fatalf("ParseUserVerificationMode(%q) accepted an unknown mode", in)
		}
	}
	if _, err := NewAuthenticator(testRPID, testRPOrigin, storeWith(t), testCodec(),
		WithUserVerification("preferred")); err == nil {
		t.Fatal("NewAuthenticator accepted an unknown user verification mode")
	}
}

func TestDefaultModeIsLog(t *testing.T) {
	a := testAuthenticator(t)
	if a.UserVerification() != UserVerificationLog {
		t.Fatalf("default mode = %q, want %q", a.UserVerification(), UserVerificationLog)
	}
}

// Log mode is today's behaviour: UV preferred, and a key without a PIN still
// signs in. What it adds is the flag in the result, for the log line.
func TestLogModeAcceptsAnAssertionWithoutUserVerification(t *testing.T) {
	store, key := loginFixture(t)
	a := uvAuthenticator(t, store, UserVerificationLog)

	options, sealed, err := a.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if _, uv := challengeOf(t, options); !isPreferred(uv) {
		t.Fatalf("log mode asked for userVerification %q, want it unchanged (preferred)", uv)
	}

	u, result, err := a.FinishLogin(context.Background(), sealed, key.assert(t, options, "alice", false))
	if err != nil {
		t.Fatalf("log mode refused an assertion without UV: %v", err)
	}
	if u.Spec.Email != "alice@example.com" {
		t.Fatalf("signed in as %q, want alice", u.Spec.Email)
	}
	if result.UserVerified {
		t.Fatal("result reports UV for an assertion without the UV flag")
	}
	if len(result.Credential) != 12 || strings.Contains(result.Credential, key.enrolled(t).ID) {
		t.Fatalf("result.Credential = %q, want a 12-character fingerprint, not the ID", result.Credential)
	}
	if got := signCountOf(t, store); got != 1 {
		t.Fatalf("sign count = %d, want 1", got)
	}
}

func TestLogModeReportsUserVerification(t *testing.T) {
	store, key := loginFixture(t)
	a := uvAuthenticator(t, store, UserVerificationLog)
	options, sealed, err := a.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, result, err := a.FinishLogin(context.Background(), sealed, key.assert(t, options, "alice", true))
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if !result.UserVerified {
		t.Fatal("result does not report UV for an assertion carrying the UV flag")
	}
}

func TestRequiredModeAsksForUserVerificationOnLogin(t *testing.T) {
	store, _ := loginFixture(t)
	a := uvAuthenticator(t, store, UserVerificationRequired)
	options, _, err := a.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if _, uv := challengeOf(t, options); uv != string(protocol.VerificationRequired) {
		t.Fatalf("required mode asked for userVerification %q, want %q", uv, protocol.VerificationRequired)
	}
}

func TestRequiredModeRefusesAnAssertionWithoutUserVerification(t *testing.T) {
	store, key := loginFixture(t)
	a := uvAuthenticator(t, store, UserVerificationRequired)
	options, sealed, err := a.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	u, _, err := a.FinishLogin(context.Background(), sealed, key.assert(t, options, "alice", false))
	if !errors.Is(err, ErrUserNotVerified) {
		t.Fatalf("FinishLogin without UV in required mode = %v, want ErrUserNotVerified", err)
	}
	if u != nil {
		t.Fatal("a refused assertion still returned an account")
	}
	if got := signCountOf(t, store); got != 0 {
		t.Fatalf("a refused assertion wrote the sign count: %d, want 0", got)
	}
}

func TestRequiredModeAcceptsAnAssertionWithUserVerification(t *testing.T) {
	store, key := loginFixture(t)
	a := uvAuthenticator(t, store, UserVerificationRequired)
	options, sealed, err := a.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, result, err := a.FinishLogin(context.Background(), sealed, key.assert(t, options, "alice", true))
	if err != nil {
		t.Fatalf("required mode refused an assertion with UV: %v", err)
	}
	if !result.UserVerified {
		t.Fatal("result does not report UV")
	}
}

// A ceremony opened in log mode carries "preferred" in its sealed session, so
// go-webauthn itself would accept the missing flag. Switching authd to
// required must still refuse it: that is the explicit check after validation.
func TestRequiredModeRefusesACeremonyOpenedInLogMode(t *testing.T) {
	store, key := loginFixture(t)
	before := uvAuthenticator(t, store, UserVerificationLog)
	after := uvAuthenticator(t, store, UserVerificationRequired)

	options, sealed, err := before.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, _, err = after.FinishLogin(context.Background(), sealed, key.assert(t, options, "alice", false))
	if !errors.Is(err, ErrUserNotVerified) {
		t.Fatalf("FinishLogin = %v, want ErrUserNotVerified", err)
	}
	if got := signCountOf(t, store); got != 0 {
		t.Fatalf("a refused assertion wrote the sign count: %d, want 0", got)
	}
}

// ErrUserNotVerified must mean "valid in every respect but UV". An assertion
// that fails for another reason keeps its own error even when it also lacks
// the UV flag: otherwise a caller holding no key could have every failed
// sign-in reported, and logged, as a missing PIN.
func TestRequiredModeKeepsOtherFailuresOutOfErrUserNotVerified(t *testing.T) {
	store, key := loginFixture(t)
	a := uvAuthenticator(t, store, UserVerificationRequired)

	cases := map[string]func(options []byte) []byte{
		"unknown key": func(options []byte) []byte {
			return newSoftAuthenticator(t, "not-enrolled").assert(t, options, "alice", false)
		},
		"wrong signature": func(options []byte) []byte {
			// Same credential ID, different private key.
			impostor := newSoftAuthenticator(t, "alice-key")
			return impostor.assert(t, options, "alice", false)
		},
		"wrong challenge": func([]byte) []byte {
			other, _, err := a.BeginLogin(context.Background())
			if err != nil {
				t.Fatalf("BeginLogin: %v", err)
			}
			return key.assert(t, other, "alice", false)
		},
	}
	for name, build := range cases {
		options, sealed, err := a.BeginLogin(context.Background())
		if err != nil {
			t.Fatalf("BeginLogin: %v", err)
		}
		_, _, err = a.FinishLogin(context.Background(), sealed, build(options))
		if err == nil {
			t.Fatalf("%s: assertion accepted", name)
		}
		if errors.Is(err, ErrUserNotVerified) {
			t.Fatalf("%s: reported as ErrUserNotVerified: %v", name, err)
		}
	}
}

func registrationFixture(t *testing.T, mode UserVerificationMode) (*Authenticator, *Store, *framev1beta1.FrameUser) {
	t.Helper()
	store := storeWith(t, fixture("alice", "alice@example.com", framev1beta1.RoleAdmin))
	u, err := store.ByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("ByEmail: %v", err)
	}
	return uvAuthenticator(t, store, mode), store, u
}

func credentialCount(t *testing.T, s *Store) int {
	t.Helper()
	u, err := s.ByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("ByEmail: %v", err)
	}
	return len(u.Status.Credentials)
}

func TestLogModeEnrolsAKeyWithoutUserVerification(t *testing.T) {
	a, store, u := registrationFixture(t, UserVerificationLog)
	options, sealed, err := a.BeginRegistration(context.Background(), u)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if _, uv := challengeOf(t, options); !isPreferred(uv) {
		t.Fatalf("log mode asked for userVerification %q at registration, want it unchanged (preferred)", uv)
	}
	key := newSoftAuthenticator(t, "new-key")
	if err := a.FinishRegistration(context.Background(), u, sealed, key.attest(t, options, false), "k"); err != nil {
		t.Fatalf("log mode refused a registration without UV: %v", err)
	}
	if n := credentialCount(t, store); n != 1 {
		t.Fatalf("credentials = %d, want 1", n)
	}
}

func TestRequiredModeAsksForAndEnforcesUserVerificationOnRegistration(t *testing.T) {
	a, store, u := registrationFixture(t, UserVerificationRequired)
	options, sealed, err := a.BeginRegistration(context.Background(), u)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if _, uv := challengeOf(t, options); uv != string(protocol.VerificationRequired) {
		t.Fatalf("required mode asked for userVerification %q at registration, want %q", uv, protocol.VerificationRequired)
	}

	key := newSoftAuthenticator(t, "no-pin-key")
	if err := a.FinishRegistration(context.Background(), u, sealed, key.attest(t, options, false), "k"); !errors.Is(err, ErrUserNotVerified) {
		t.Fatalf("FinishRegistration without UV in required mode = %v, want ErrUserNotVerified", err)
	}
	if n := credentialCount(t, store); n != 0 {
		t.Fatalf("a refused registration stored a credential: %d", n)
	}

	options, sealed, err = a.BeginRegistration(context.Background(), u)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	key = newSoftAuthenticator(t, "pin-key")
	if err := a.FinishRegistration(context.Background(), u, sealed, key.attest(t, options, true), "k"); err != nil {
		t.Fatalf("required mode refused a registration with UV: %v", err)
	}
	if n := credentialCount(t, store); n != 1 {
		t.Fatalf("credentials = %d, want 1", n)
	}
}

// The registration counterpart of TestRequiredModeRefusesACeremonyOpenedInLogMode.
func TestRequiredModeRefusesARegistrationOpenedInLogMode(t *testing.T) {
	before, store, u := registrationFixture(t, UserVerificationLog)
	after := uvAuthenticator(t, store, UserVerificationRequired)
	options, sealed, err := before.BeginRegistration(context.Background(), u)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	key := newSoftAuthenticator(t, "no-pin-key")
	err = after.FinishRegistration(context.Background(), u, sealed, key.attest(t, options, false), "k")
	if !errors.Is(err, ErrUserNotVerified) {
		t.Fatalf("FinishRegistration = %v, want ErrUserNotVerified", err)
	}
	if n := credentialCount(t, store); n != 0 {
		t.Fatalf("a refused registration stored a credential: %d", n)
	}
}

// captureLogs swaps the default slog logger for one writing to a buffer, for
// the duration of one test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// The log line is the whole point of log mode: one per successful sign-in,
// carrying the UV flag, and neither the address nor the account name (which
// is derived from the address) nor a credential ID.
func TestSuccessfulPasskeySignInLogsUserVerification(t *testing.T) {
	for _, uv := range []bool{false, true} {
		u := fixture("alice-at-example.com-0123abcd", "alice@example.com", framev1beta1.RoleAdmin)
		srv := testServer(t, u)
		restore := finishLogin
		finishLogin = func(*Authenticator, context.Context, string, []byte) (*framev1beta1.FrameUser, LoginResult, error) {
			return u, LoginResult{UserVerified: uv, Credential: "0123456789ab"}, nil
		}
		logs := captureLogs(t)

		rec := postLoginFinish(t, srv)
		finishLogin = restore
		if rec.Code != http.StatusNoContent {
			t.Fatalf("passkey sign-in = %d, want 204", rec.Code)
		}
		line := logs.String()
		for _, want := range []string{
			"Signed in with passkey",
			"userVerified=" + map[bool]string{false: "false", true: "true"}[uv],
			"credential=0123456789ab",
			"userVerificationMode=log",
		} {
			if !strings.Contains(line, want) {
				t.Fatalf("sign-in log %q does not contain %q", line, want)
			}
		}
		for _, leak := range []string{"alice@example.com", u.Name} {
			if strings.Contains(line, leak) {
				t.Fatalf("sign-in log %q carries %q", line, leak)
			}
		}
	}
}

func TestRefusedSignInIsNotLoggedAsASignIn(t *testing.T) {
	srv := testServer(t, fixture("alice", "alice@example.com", framev1beta1.RoleAdmin))
	restore := finishLogin
	t.Cleanup(func() { finishLogin = restore })
	finishLogin = func(*Authenticator, context.Context, string, []byte) (*framev1beta1.FrameUser, LoginResult, error) {
		return nil, LoginResult{}, ErrUserNotVerified
	}
	logs := captureLogs(t)

	if rec := postLoginFinish(t, srv); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refused sign-in = %d, want 401", rec.Code)
	}
	if strings.Contains(logs.String(), "Signed in with passkey") {
		t.Fatalf("a refused sign-in was logged as a sign-in: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "without user verification") {
		t.Fatalf("a refusal for missing UV was not logged as such: %q", logs.String())
	}
}
