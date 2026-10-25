package authd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// challengeTTL bounds how long a ceremony may take. Long enough to find the
// key in a drawer, short enough that a captured challenge is worthless.
const challengeTTL = 5 * time.Minute

// ErrCounterRegression signals an authenticator counter that failed to
// advance — the library's clone/replay signal.
//
// It is deliberately a distinct error so the caller can log it for manual
// investigation while refusing the login. go-webauthn verifies the assertion's
// signature before it ever compares counters (see the comment on the
// CloneWarning check in FinishLogin), so by the time this error is produced
// the caller has already proven possession of the private key: this is a
// genuine clone/replay signal — or an authenticator restored from a backup —
// not something an unauthenticated caller can trigger by guessing a
// credentialId. It must still never trigger deleting or disabling the
// credential: a legitimate owner whose authenticator glitched or was
// restored from a backup must not automatically lose their only way in. The
// login is refused and left for a human to investigate; the key stays
// enrolled.
var ErrCounterRegression = errors.New("authenticator sign counter did not advance")

// ErrUserNotVerified refuses a ceremony whose authenticator did not set the
// UV (user verified) flag while the Authenticator runs in
// UserVerificationRequired mode: the key was touched, but nobody proved to it
// who they were (no PIN, no biometric), so possession of the key alone would
// be the whole sign-in.
var ErrUserNotVerified = errors.New("authenticator did not verify the user")

// UserVerificationMode decides what authd does with WebAuthn user
// verification (UV: a PIN or biometric checked by the authenticator itself).
//
// It is rolled out in two steps. UserVerificationLog, the default, keeps
// the behaviour authd always had — the options leave UV unset, which WebAuthn
// reads as "preferred", and a bare security key without a PIN is accepted —
// and logs, for every successful passkey sign-in, whether the UV
// flag was present, so an operator can see which keys would be refused before
// refusing them. UserVerificationRequired asks for UV when each ceremony
// opens and refuses any registration or assertion that comes back without it.
type UserVerificationMode string

const (
	UserVerificationLog      UserVerificationMode = "log"
	UserVerificationRequired UserVerificationMode = "required"
)

// ParseUserVerificationMode reads the WEBAUTHN_USER_VERIFICATION setting. An
// empty value is the default, UserVerificationLog; anything other than the two
// known words is an error, so a typo cannot silently fall back to the weaker
// mode.
func ParseUserVerificationMode(s string) (UserVerificationMode, error) {
	switch UserVerificationMode(s) {
	case "", UserVerificationLog:
		return UserVerificationLog, nil
	case UserVerificationRequired:
		return UserVerificationRequired, nil
	}
	return "", fmt.Errorf("unknown user verification mode %q (want %q or %q)",
		s, UserVerificationLog, UserVerificationRequired)
}

// Authenticator runs the two WebAuthn ceremonies against the FrameUser store.
type Authenticator struct {
	web    *webauthn.WebAuthn
	store  *Store
	codec  *ChallengeCodec
	uvMode UserVerificationMode
}

// AuthenticatorOption adjusts an Authenticator built by NewAuthenticator.
type AuthenticatorOption func(*Authenticator)

// WithUserVerification sets the user verification mode. Without it the
// Authenticator runs in UserVerificationLog.
func WithUserVerification(mode UserVerificationMode) AuthenticatorOption {
	return func(a *Authenticator) { a.uvMode = mode }
}

func NewAuthenticator(rpID, rpOrigin string, store *Store, codec *ChallengeCodec, opts ...AuthenticatorOption) (*Authenticator, error) {
	web, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "Frame Cluster Control",
		RPID:          rpID,
		RPOrigins:     []string{rpOrigin},
	})
	if err != nil {
		return nil, fmt.Errorf("configuring webauthn: %w", err)
	}
	a := &Authenticator{web: web, store: store, codec: codec, uvMode: UserVerificationLog}
	for _, opt := range opts {
		opt(a)
	}
	if _, err := ParseUserVerificationMode(string(a.uvMode)); err != nil {
		return nil, err
	}
	return a, nil
}

// UserVerification reports the mode this Authenticator runs in.
func (a *Authenticator) UserVerification() UserVerificationMode { return a.uvMode }

func (a *Authenticator) requiresUV() bool { return a.uvMode == UserVerificationRequired }

// LoginResult is what a verified assertion tells the caller beyond the
// account: enough to log the sign-in without logging identifiers.
type LoginResult struct {
	// UserVerified is the assertion's UV flag.
	UserVerified bool
	// Credential is a short, one-way fingerprint of the credential ID (see
	// credentialFingerprint), never the ID itself.
	Credential string
}

// credentialFingerprint is what a log line carries in place of a credential
// ID: the first 12 hex characters of its SHA-256. Enough to tell an
// account's keys apart across log lines, and nothing that could be replayed
// into an allowCredentials list or matched against status.credentials
// without already holding the ID.
func credentialFingerprint(rawID []byte) string {
	sum := sha256.Sum256(rawID)
	return hex.EncodeToString(sum[:])[:12]
}

// webauthnUser adapts a FrameUser to the library's interface.
type webauthnUser struct{ u *framev1beta1.FrameUser }

func (w webauthnUser) WebAuthnID() []byte          { return []byte(w.u.Name) }
func (w webauthnUser) WebAuthnName() string        { return w.u.Spec.Email }
func (w webauthnUser) WebAuthnDisplayName() string { return w.u.Spec.Email }

func (w webauthnUser) WebAuthnCredentials() []webauthn.Credential {
	creds := make([]webauthn.Credential, 0, len(w.u.Status.Credentials))
	for _, c := range w.u.Status.Credentials {
		id, err := base64.RawURLEncoding.DecodeString(c.ID)
		if err != nil {
			continue
		}
		pk, err := base64.RawStdEncoding.DecodeString(c.PublicKey)
		if err != nil {
			continue
		}
		creds = append(creds, webauthn.Credential{
			ID:            id,
			PublicKey:     pk,
			Authenticator: webauthn.Authenticator{SignCount: c.SignCount},
		})
	}
	return creds
}

func (a *Authenticator) BeginRegistration(_ context.Context, u *framev1beta1.FrameUser) ([]byte, string, error) {
	opts := []webauthn.RegistrationOption{
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	}
	if a.requiresUV() {
		// Set on the options, and so on the sealed session: go-webauthn's
		// CreateCredential then refuses an attestation without the UV flag.
		opts = append(opts, func(o *protocol.PublicKeyCredentialCreationOptions) {
			o.AuthenticatorSelection.UserVerification = protocol.VerificationRequired
		})
	}
	options, session, err := a.web.BeginRegistration(webauthnUser{u}, opts...)
	if err != nil {
		return nil, "", fmt.Errorf("begin registration: %w", err)
	}
	return a.seal(options, session)
}

func (a *Authenticator) FinishRegistration(ctx context.Context, u *framev1beta1.FrameUser, sealed string, response []byte, label string) error {
	session, err := a.openSession(sealed)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(strings.NewReader(string(response)))
	if err != nil {
		return fmt.Errorf("parsing registration response: %w", err)
	}
	cred, err := a.web.CreateCredential(webauthnUser{u}, *session, parsed)
	if err != nil {
		return fmt.Errorf("verifying registration: %w", err)
	}
	// The library already refuses this when the session asked for UV; this
	// covers a ceremony opened before the mode was switched to required
	// (its sealed session says "preferred") and finished after.
	if a.requiresUV() && !cred.Flags.UserVerified {
		return fmt.Errorf("%w: registration of credential %s", ErrUserNotVerified, credentialFingerprint(cred.ID))
	}
	return a.store.AddCredential(ctx, u, framev1beta1.WebAuthnCredential{
		ID:        base64.RawURLEncoding.EncodeToString(cred.ID),
		PublicKey: base64.RawStdEncoding.EncodeToString(cred.PublicKey),
		SignCount: cred.Authenticator.SignCount,
		AddedAt:   metav1.Now(),
		Label:     label,
	})
}

// BeginLogin starts a usernameless ceremony: no allowCredentials, so the
// browser offers whichever resident key matches the RP ID. Listing credentials
// here would tell an unauthenticated caller which accounts exist.
//
// In UserVerificationRequired mode the options ask for UV, and the sealed
// session carries that requirement into FinishLogin.
func (a *Authenticator) BeginLogin(_ context.Context) ([]byte, string, error) {
	var opts []webauthn.LoginOption
	if a.requiresUV() {
		opts = append(opts, webauthn.WithUserVerification(protocol.VerificationRequired))
	}
	options, session, err := a.web.BeginDiscoverableLogin(opts...)
	if err != nil {
		return nil, "", fmt.Errorf("begin login: %w", err)
	}
	return a.seal(options, session)
}

// FinishLogin verifies the assertion and returns the account it names, with
// what the caller needs to log the sign-in.
//
// In UserVerificationRequired mode an assertion without the UV flag is
// refused with ErrUserNotVerified, before the sign counter is written. For a
// ceremony opened in that mode go-webauthn refuses it itself (the sealed
// session says "required"); it is matched here too so the refusal reads as
// ErrUserNotVerified rather than as any other bad assertion, and the explicit
// check after validation covers a ceremony opened before the switch.
func (a *Authenticator) FinishLogin(ctx context.Context, sealed string, response []byte) (*framev1beta1.FrameUser, LoginResult, error) {
	session, err := a.openSession(sealed)
	if err != nil {
		return nil, LoginResult{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(strings.NewReader(string(response)))
	if err != nil {
		return nil, LoginResult{}, fmt.Errorf("parsing assertion: %w", err)
	}

	var matched *framev1beta1.FrameUser
	lookup := func(rawID, _ []byte) (webauthn.User, error) {
		u, err := a.store.ByCredentialID(ctx, base64.RawURLEncoding.EncodeToString(rawID))
		if err != nil {
			return nil, err
		}
		matched = u
		return webauthnUser{u}, nil
	}

	cred, err := a.web.ValidateDiscoverableLogin(lookup, *session, parsed)
	if err != nil {
		if a.requiresUV() && !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
			return nil, LoginResult{}, fmt.Errorf("%w: %w", ErrUserNotVerified, err)
		}
		return nil, LoginResult{}, fmt.Errorf("verifying assertion: %w", err)
	}
	result := LoginResult{UserVerified: cred.Flags.UserVerified, Credential: credentialFingerprint(cred.ID)}
	if a.requiresUV() && !result.UserVerified {
		return nil, LoginResult{}, fmt.Errorf("%w: credential %s", ErrUserNotVerified, result.Credential)
	}

	if err := a.recordLoginCounter(ctx, matched, cred); err != nil {
		return nil, LoginResult{}, err
	}
	return matched, result, nil
}

// recordLoginCounter applies the post-validation counter check for a login
// that has already passed signature verification.
//
// The library never returns an error for a counter that failed to advance:
// it verifies the signature (steps 4-16 of §7.2) first, and only afterwards
// (step 17) compares the reported counter against the stored one, recording
// the outcome as Authenticator.CloneWarning rather than failing the call. So
// a successful ValidateDiscoverableLogin still needs this check before the
// login can be trusted. Using the typed CloneWarning field here — instead of
// matching on error text — is also more robust: err.Error() is not a stable
// API and could change across point releases without a major-version bump.
//
// On a regression the login is refused (ErrCounterRegression) and the store
// is deliberately left untouched: no UpdateSignCount, no credential removal.
// Only a clean counter reaches the write that rotates it forward.
func (a *Authenticator) recordLoginCounter(ctx context.Context, u *framev1beta1.FrameUser, cred *webauthn.Credential) error {
	if cred.Authenticator.CloneWarning {
		return fmt.Errorf("%w: authenticator reported counter %d", ErrCounterRegression, cred.Authenticator.SignCount)
	}

	credID := base64.RawURLEncoding.EncodeToString(cred.ID)
	if err := a.store.UpdateSignCount(ctx, u, credID, cred.Authenticator.SignCount); err != nil {
		return fmt.Errorf("recording sign count: %w", err)
	}
	return nil
}

func (a *Authenticator) seal(options any, session *webauthn.SessionData) ([]byte, string, error) {
	raw, err := json.Marshal(options)
	if err != nil {
		return nil, "", fmt.Errorf("encoding options: %w", err)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		return nil, "", fmt.Errorf("encoding session: %w", err)
	}
	sealed, err := a.codec.Seal(PurposeChallenge, encoded, challengeTTL)
	if err != nil {
		return nil, "", fmt.Errorf("sealing challenge: %w", err)
	}
	return raw, sealed, nil
}

func (a *Authenticator) openSession(sealed string) (*webauthn.SessionData, error) {
	payload, err := a.codec.Open(PurposeChallenge, sealed)
	if err != nil {
		return nil, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(payload, &session); err != nil {
		return nil, fmt.Errorf("decoding challenge: %w", err)
	}
	return &session, nil
}
