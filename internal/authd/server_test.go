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
