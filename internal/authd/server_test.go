package authd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

func testServer(t *testing.T, users ...*framev1alpha1.FrameUser) *Server {
	t.Helper()
	store := storeWith(t, users...)
	auth, err := NewAuthenticator("example.com", "https://example.com", store, testCodec())
	if err != nil {
		t.Fatalf("authenticator: %v", err)
	}
	srv, err := NewServer(ServerConfig{
		Store:           store,
		Auth:            auth,
		Issuer:          testIssuer(t),
		Codec:           testCodec(),
		BootstrapSecret: "s3cret-bootstrap",
		Namespace:       "cluster-control",
		TokenTTL:        15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return srv
}

func do(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// bootstrapServer builds a Server whose Store and ServerConfig.Client share the
// same fake client, so a test can both drive /auth/bootstrap and read back
// what it did to the FrameUser and the bootstrap Secret. testServer (above)
// cannot be reused for this: it doesn't expose the client underneath its
// Store, and the bootstrap-completion tests need to inspect that same client
// directly (via NewStore, and via a raw Get on the Secret).
func bootstrapServer(t *testing.T, token, secretName string, seedSecret bool, users ...*framev1alpha1.FrameUser) (*Server, client.Client) {
	t.Helper()
	s := scheme.Scheme
	if err := framev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&framev1alpha1.FrameUser{})
	for _, u := range users {
		b = b.WithObjects(u)
	}
	if seedSecret {
		b = b.WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "cluster-control"},
			Data:       map[string][]byte{"token": []byte(token)},
		})
	}
	c := b.Build()
	store := NewStore(c, "cluster-control")
	auth, err := NewAuthenticator("example.com", "https://example.com", store, testCodec())
	if err != nil {
		t.Fatalf("authenticator: %v", err)
	}
	srv, err := NewServer(ServerConfig{
		Store:               store,
		Auth:                auth,
		Issuer:              testIssuer(t),
		Codec:               testCodec(),
		BootstrapSecret:     token,
		BootstrapSecretName: secretName,
		Client:              c,
		Namespace:           "cluster-control",
		TokenTTL:            15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return srv, c
}

func TestDiscoveryAndKeysArePublic(t *testing.T) {
	srv := testServer(t)
	for _, path := range []string{"/.well-known/openid-configuration", "/keys"} {
		rec := do(t, srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, rec.Code)
		}
	}
}

func TestBootstrapClosesOnceAUserExists(t *testing.T) {
	srv := testServer(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"eve@example.com"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bootstrap with an existing user = %d, want 404", rec.Code)
	}
}

func TestBootstrapRejectsWrongToken(t *testing.T) {
	srv := testServer(t)
	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"guess","email":"eve@example.com"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bootstrap with a wrong token = %d, want 401", rec.Code)
	}
}

// TestBootstrapRefusesWhenNoTokenIsConfigured covers the authentication
// bypass found in review: subtle.ConstantTimeCompare([]byte(""), []byte(""))
// returns 1, so a server whose BootstrapSecret is empty — the designed
// steady state once bootstrap has already consumed and deleted the Secret,
// per cmd/authd/main.go's loadBootstrapToken — must refuse every bootstrap
// attempt outright, not fall through to a compare that a caller can satisfy
// by simply omitting the token field.
func TestBootstrapRefusesWhenNoTokenIsConfigured(t *testing.T) {
	store := storeWith(t)
	auth, err := NewAuthenticator("example.com", "https://example.com", store, testCodec())
	if err != nil {
		t.Fatalf("authenticator: %v", err)
	}
	srv, err := NewServer(ServerConfig{
		Store:           store,
		Auth:            auth,
		Issuer:          testIssuer(t),
		Codec:           testCodec(),
		BootstrapSecret: "", // never configured, or already consumed
		Namespace:       "cluster-control",
		TokenTTL:        15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}

	for _, body := range []string{`{"token":""}`, `{}`} {
		rec := do(t, srv, http.MethodPost, "/auth/bootstrap", body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("bootstrap with body %q against an empty configured token = %d, want 404 "+
				"(an empty submitted token must never match an empty configured one)", body, rec.Code)
		}
	}
}

// TestBootstrapCreatesTheFirstAdminAndDeletesTheSecret is the test the brief's
// stub could never fail: it reads the FrameUser back and asserts on its role
// and password mode, and confirms the bootstrap Secret is actually gone
// afterwards, not just that the HTTP call returned 204.
func TestBootstrapCreatesTheFirstAdminAndDeletesTheSecret(t *testing.T) {
	srv, c := bootstrapServer(t, "s3cret-bootstrap", "frame-auth-bootstrap", true)

	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"Eve@Example.com"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bootstrap = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	store := NewStore(c, "cluster-control")
	u, err := store.ByEmail(context.Background(), "Eve@Example.com")
	if err != nil {
		t.Fatalf("ByEmail after bootstrap: %v", err)
	}
	if u.Spec.Role != framev1alpha1.RoleAdmin {
		t.Fatalf("role = %q, want %q", u.Spec.Role, framev1alpha1.RoleAdmin)
	}
	if u.Spec.PasswordAuth == framev1alpha1.PasswordEnabled {
		t.Fatal("bootstrap admin was created with password auth enabled; it must enrol a passkey first")
	}

	var secret corev1.Secret
	err = c.Get(context.Background(), client.ObjectKey{Name: "frame-auth-bootstrap", Namespace: "cluster-control"}, &secret)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("bootstrap secret still readable after use (err=%v) — the token could be replayed", err)
	}
}

// TestSecondBootstrapCallIs404AfterFirstSucceeds exercises the full
// close-the-door path end to end, through the real AdminCount guard rather
// than a pre-seeded admin fixture.
func TestSecondBootstrapCallIs404AfterFirstSucceeds(t *testing.T) {
	srv, _ := bootstrapServer(t, "s3cret-bootstrap", "frame-auth-bootstrap", true)

	first := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"eve@example.com"}`)
	if first.Code != http.StatusNoContent {
		t.Fatalf("first bootstrap = %d, want 204: %s", first.Code, first.Body.String())
	}

	second := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"mallory@example.com"}`)
	if second.Code != http.StatusNotFound {
		t.Fatalf("second bootstrap = %d, want 404", second.Code)
	}
}

func TestBootstrapRejectsMalformedEmail(t *testing.T) {
	srv, _ := bootstrapServer(t, "s3cret-bootstrap", "frame-auth-bootstrap", true)
	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"not-an-email"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap with a malformed email = %d, want 400", rec.Code)
	}
}

func TestPasswordLoginRefusedWhenDisabled(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	u.Spec.PasswordAuth = framev1alpha1.PasswordDisabled
	hash, _ := HashPassword("hunter2")
	u.Spec.PasswordHash = hash
	srv := testServer(t, u)

	rec := do(t, srv, http.MethodPost, "/auth/login/password", `{"email":"alice@example.com","password":"hunter2"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("password login on a passkey-only account = %d, want 401", rec.Code)
	}
}

func TestPasswordLoginSucceedsWhenEnabled(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	u.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
	hash, _ := HashPassword("hunter2")
	u.Spec.PasswordHash = hash
	srv := testServer(t, u)

	rec := do(t, srv, http.MethodPost, "/auth/login/password", `{"email":"alice@example.com","password":"hunter2"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("password login = %d, want 204", rec.Code)
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "Secure") {
		t.Fatalf("session cookie is not HttpOnly+Secure: %q", cookie)
	}
	if !strings.Contains(cookie, "SameSite=Strict") {
		t.Fatalf("session cookie is not SameSite=Strict: %q", cookie)
	}
}

func TestTokenRequiresASession(t *testing.T) {
	srv := testServer(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
	rec := do(t, srv, http.MethodPost, "/auth/token", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token without session = %d, want 401", rec.Code)
	}
}

// TestDummyPasswordHashIsValidAndVerifiable covers the timing-oracle fix
// found in review: handlePasswordLogin must call VerifyPassword exactly once
// on every failure path, including the ones where there is no real account
// or no real hash (unknown email, password disabled), using
// dummyPasswordHash so the argon2id cost — and so the wall-clock time — is
// identical to a genuine attempt. That guarantee only holds if
// dummyPasswordHash is itself a real, correctly-parsing argon2id hash rather
// than an empty or malformed placeholder that VerifyPassword would reject in
// microseconds via its early parse-failure return (see VerifyPassword's own
// "malformed input" fast path in password.go) — which would silently
// reintroduce the timing gap this fix closes.
func TestDummyPasswordHashIsValidAndVerifiable(t *testing.T) {
	if dummyPasswordHash == "" {
		t.Fatal("dummyPasswordHash was not initialized")
	}
	if !VerifyPassword(dummyPasswordHash, "authd-dummy-password-for-timing-safety") {
		t.Fatal("dummyPasswordHash does not parse as a genuine argon2id hash — VerifyPassword would " +
			"take its malformed-input fast path instead of paying the real argon2 cost, reopening the timing oracle")
	}
}

// TestPasswordLoginAlwaysVerifiesExactlyOnce is the structural,
// timing-independent counterpart to the timing-oracle fix: it proves
// handlePasswordLogin calls verifyPassword exactly once per request on every
// failure path (unknown email, password disabled, wrong password), not just
// that the dummy hash constant happens to be valid. A regression to the
// original `err != nil || u.Spec.PasswordAuth != Enabled || !VerifyPassword(...)`
// short-circuit would make this test fail with 0 calls for the first two
// cases, even though TestUnknownEmailAndWrongPasswordAreIndistinguishable and
// TestDummyPasswordHashIsValidAndVerifiable would both still pass — neither
// of those actually observes whether verifyPassword ran.
func TestPasswordLoginAlwaysVerifiesExactlyOnce(t *testing.T) {
	enabled := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	enabled.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	enabled.Spec.PasswordHash = hash

	disabled := fixture("bob", "bob@example.com", framev1alpha1.RoleViewer)
	disabled.Spec.PasswordAuth = framev1alpha1.PasswordDisabled

	cases := []struct {
		name string
		srv  *Server
		body string
	}{
		{"unknown email", testServer(t, enabled), `{"email":"ghost@example.com","password":"nope"}`},
		{"password disabled", testServer(t, disabled), `{"email":"bob@example.com","password":"nope"}`},
		{"wrong password", testServer(t, enabled), `{"email":"alice@example.com","password":"nope"}`},
	}

	original := verifyPassword
	defer func() { verifyPassword = original }()

	for _, tc := range cases {
		calls := 0
		verifyPassword = func(encoded, plain string) bool {
			calls++
			return original(encoded, plain)
		}
		rec := do(t, tc.srv, http.MethodPost, "/auth/login/password", tc.body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.name, rec.Code)
		}
		if calls != 1 {
			t.Fatalf("%s: verifyPassword called %d times, want exactly 1", tc.name, calls)
		}
	}
}

func TestUnknownEmailAndWrongPasswordAreIndistinguishable(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	u.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
	hash, _ := HashPassword("hunter2")
	u.Spec.PasswordHash = hash
	srv := testServer(t, u)

	wrong := do(t, srv, http.MethodPost, "/auth/login/password", `{"email":"alice@example.com","password":"nope"}`)
	unknown := do(t, srv, http.MethodPost, "/auth/login/password", `{"email":"ghost@example.com","password":"nope"}`)
	if wrong.Code != unknown.Code || wrong.Body.String() != unknown.Body.String() {
		t.Fatalf("responses differ: wrong=%d/%q unknown=%d/%q — this leaks which accounts exist",
			wrong.Code, wrong.Body.String(), unknown.Code, unknown.Body.String())
	}
}
