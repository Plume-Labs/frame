package authd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
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

// Authenticator runs the two WebAuthn ceremonies against the FrameUser store.
type Authenticator struct {
	web   *webauthn.WebAuthn
	store *Store
	codec *ChallengeCodec
}

func NewAuthenticator(rpID, rpOrigin string, store *Store, codec *ChallengeCodec) (*Authenticator, error) {
	web, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "Frame Cluster Control",
		RPID:          rpID,
		RPOrigins:     []string{rpOrigin},
	})
	if err != nil {
		return nil, fmt.Errorf("configuring webauthn: %w", err)
	}
	return &Authenticator{web: web, store: store, codec: codec}, nil
}

// webauthnUser adapts a FrameUser to the library's interface.
type webauthnUser struct{ u *framev1alpha1.FrameUser }

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

func (a *Authenticator) BeginRegistration(_ context.Context, u *framev1alpha1.FrameUser) ([]byte, string, error) {
	options, session, err := a.web.BeginRegistration(
		webauthnUser{u},
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	)
	if err != nil {
		return nil, "", fmt.Errorf("begin registration: %w", err)
	}
	return a.seal(options, session)
}

func (a *Authenticator) FinishRegistration(ctx context.Context, u *framev1alpha1.FrameUser, sealed string, response []byte, label string) error {
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
	return a.store.AddCredential(ctx, u, framev1alpha1.WebAuthnCredential{
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
func (a *Authenticator) BeginLogin(_ context.Context) ([]byte, string, error) {
	options, session, err := a.web.BeginDiscoverableLogin()
	if err != nil {
		return nil, "", fmt.Errorf("begin login: %w", err)
	}
	return a.seal(options, session)
}

func (a *Authenticator) FinishLogin(ctx context.Context, sealed string, response []byte) (*framev1alpha1.FrameUser, error) {
	session, err := a.openSession(sealed)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(strings.NewReader(string(response)))
	if err != nil {
		return nil, fmt.Errorf("parsing assertion: %w", err)
	}

	var matched *framev1alpha1.FrameUser
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
		return nil, fmt.Errorf("verifying assertion: %w", err)
	}

	if err := a.recordLoginCounter(ctx, matched, cred); err != nil {
		return nil, err
	}
	return matched, nil
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
func (a *Authenticator) recordLoginCounter(ctx context.Context, u *framev1alpha1.FrameUser, cred *webauthn.Credential) error {
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
