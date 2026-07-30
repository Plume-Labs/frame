# Frame authd — Stage 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the `FrameUser` CRD, its anti-lockout admission webhook, and the `authd` service — deployed alongside the existing UI, changing nothing about how the UI authenticates today.

**Architecture:** `authd` is a new Go binary in the existing kubebuilder project. It authenticates a person with WebAuthn or a password, then mints a short-lived OIDC ID token and publishes the JWKS the Kubernetes apiserver will later use to verify it. Accounts live in `FrameUser` custom resources; WebAuthn challenges live in a signed cookie, so the two replicas share no state and need no database.

**Tech Stack:** Go 1.26, controller-runtime 0.23, `github.com/go-webauthn/webauthn` v0.17.4, `github.com/go-jose/go-jose/v4` v4.1.4, `golang.org/x/crypto/argon2`, envtest, cert-manager.

## Global Constraints

- Group/version for all new API types: `frame.plume-labs.io/v1alpha1`, in `api/v1alpha1/`, matching the six existing kinds.
- Webhooks live in `internal/webhook/v1alpha1/` and are registered from `internal/webhook/v1alpha1/webhook_suite_test.go` for envtest.
- Run `make manifests generate` after any change to `api/v1alpha1/` — CRD YAML and deepcopy functions are generated, never hand-edited.
- Files stay under 500 lines.
- Nothing in this stage may change the UI, its nginx config, its RBAC, or the apiserver. Stage 1 is additive by definition; the existing ServiceAccount path must keep working untouched.
- `authd` never receives the permissions it grants. Its own RBAC is: get/list on `frameusers`, patch on `frameusers/status`, create on `frameusers` (bootstrap only), delete on the single `frame-auth-bootstrap` Secret.
- Secrets are never logged. Password hashes, private keys and session cookies must not appear in any log line, including at debug level.

---

### Task 1: FrameUser CRD types

**Files:**
- Create: `api/v1alpha1/frameuser_types.go`
- Modify: `PROJECT` (add the resource entry)
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/frame.plume-labs.io_frameusers.yaml`

**Interfaces:**
- Consumes: nothing.
- Produces: `FrameUser`, `FrameUserSpec`, `FrameUserStatus`, `WebAuthnCredential` in package `github.com/rmocq/frame/api/v1alpha1`. Role constants `RoleAdmin = "admin"`, `RoleOperator = "operator"`, `RoleViewer = "viewer"`. Password mode constants `PasswordEnabled = "enabled"`, `PasswordDisabled = "disabled"`.

- [ ] **Step 1: Write the type definitions**

Create `api/v1alpha1/frameuser_types.go`:

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"

	PasswordEnabled  = "enabled"
	PasswordDisabled = "disabled"
)

// WebAuthnCredential is one enrolled authenticator (a YubiKey, a phone
// passkey). Public data only: the private key never leaves the device.
type WebAuthnCredential struct {
	// ID is the base64url credential ID reported by the authenticator.
	ID string `json:"id"`
	// PublicKey is the base64-encoded COSE public key.
	PublicKey string `json:"publicKey"`
	// SignCount is the authenticator's counter as of the last successful
	// assertion. A value that fails to advance signals a cloned credential.
	SignCount uint32 `json:"signCount"`
	// AddedAt records enrolment time, so a user can tell two keys apart.
	AddedAt metav1.Time `json:"addedAt"`
	// Label is a human name for the key, e.g. "YubiKey 5C".
	// +optional
	Label string `json:"label,omitempty"`
}

type FrameUserSpec struct {
	// Email identifies the account and becomes the Kubernetes username.
	// +kubebuilder:validation:Pattern=`^[^@[:space:]]+@[^@[:space:]]+$`
	Email string `json:"email"`

	// Role decides which group the issued token carries.
	// +kubebuilder:validation:Enum=admin;operator;viewer
	Role string `json:"role"`

	// PasswordAuth controls whether this account may sign in with a password
	// at all. Defaults to disabled: an account is passkey-only unless someone
	// deliberately opens the other door.
	// +kubebuilder:validation:Enum=enabled;disabled
	// +kubebuilder:default=disabled
	PasswordAuth string `json:"passwordAuth,omitempty"`

	// PasswordHash is an argon2id PHC string, written only by authd. It is
	// meaningless while PasswordAuth is disabled.
	// +optional
	PasswordHash string `json:"passwordHash,omitempty"`
}

type FrameUserStatus struct {
	// Credentials are owned by authd, which is why they live in status: an
	// admin editing an account cannot corrupt a key by hand.
	// +optional
	Credentials []WebAuthnCredential `json:"credentials,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=`.spec.email`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Password",type=string,JSONPath=`.spec.passwordAuth`
// +kubebuilder:printcolumn:name="Keys",type=integer,JSONPath=`.status.credentials[*]`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FrameUser is a person who can sign in to the Cluster Control UI.
type FrameUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FrameUserSpec   `json:"spec,omitempty"`
	Status FrameUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameUser{}, &FrameUserList{})
}
```

- [ ] **Step 2: Register the resource in PROJECT**

Append to the `resources:` list in `PROJECT`, keeping the existing entries untouched:

```yaml
- api:
    crdVersion: v1
    namespaced: true
  controller: false
  domain: plume-labs.io
  group: frame
  kind: FrameUser
  path: github.com/rmocq/frame/api/v1alpha1
  version: v1alpha1
  webhooks:
    validation: true
    webhookVersion: v1
```

`controller: false` is deliberate — nothing reconciles a FrameUser. `authd` reads and writes it directly.

- [ ] **Step 3: Generate deepcopy and CRD manifests**

Run: `make manifests generate`
Expected: `api/v1alpha1/zz_generated.deepcopy.go` gains `FrameUser`, `FrameUserList`, `FrameUserSpec`, `FrameUserStatus` and `WebAuthnCredential` methods, and `config/crd/bases/frame.plume-labs.io_frameusers.yaml` is created.

- [ ] **Step 4: Verify it compiles and the schema is valid**

Run: `make build && kubectl apply --dry-run=server -f config/crd/bases/frame.plume-labs.io_frameusers.yaml`
Expected: build succeeds; the CRD is accepted by the apiserver.

- [ ] **Step 5: Verify the schema actually rejects bad input**

Run:

```bash
kubectl apply -f config/crd/bases/frame.plume-labs.io_frameusers.yaml
cat <<'EOF' | kubectl apply --dry-run=server -f -
apiVersion: frame.plume-labs.io/v1alpha1
kind: FrameUser
metadata:
  name: bad-role
  namespace: cluster-control
spec:
  email: alice@example.com
  role: superuser
EOF
```

Expected: rejected with `spec.role: Unsupported value: "superuser"`.

- [ ] **Step 6: Commit**

```bash
git add api/v1alpha1/frameuser_types.go api/v1alpha1/zz_generated.deepcopy.go \
        config/crd/bases/frame.plume-labs.io_frameusers.yaml PROJECT
git commit -m "feat(api): FrameUser CRD for per-user authentication"
```

---

### Task 2: Anti-lockout admission webhook

**Files:**
- Create: `internal/webhook/v1alpha1/frameuser_webhook.go`
- Create: `internal/webhook/v1alpha1/frameuser_webhook_test.go`
- Modify: `internal/webhook/v1alpha1/webhook_suite_test.go` (register the new webhook next to `SetupSchedulingPolicyWebhookWithManager`, around line 118)
- Modify: `cmd/main.go` (register the webhook with the manager, alongside the existing `Setup*WebhookWithManager` calls)

**Interfaces:**
- Consumes: `framev1alpha1.FrameUser`, `RoleAdmin` from Task 1.
- Produces: `SetupFrameUserWebhookWithManager(mgr ctrl.Manager) error` and `FrameUserCustomValidator{Client client.Client}` in package `internal/webhook/v1alpha1`.

This is the guard that makes the design safe. `authd` checking the same rule would be bypassed by a plain `kubectl delete`; admission is the only place that holds regardless of who writes.

- [ ] **Step 1: Write the failing tests**

Create `internal/webhook/v1alpha1/frameuser_webhook_test.go`:

```go
package v1alpha1

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

func user(name, role string) *framev1alpha1.FrameUser {
	return &framev1alpha1.FrameUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cluster-control"},
		Spec: framev1alpha1.FrameUserSpec{
			Email: name + "@example.com",
			Role:  role,
		},
	}
}

var _ = Describe("FrameUser webhook", func() {
	newValidator := func(objs ...*framev1alpha1.FrameUser) *FrameUserCustomValidator {
		b := fake.NewClientBuilder().WithScheme(scheme.Scheme)
		for _, o := range objs {
			b = b.WithObjects(o)
		}
		return &FrameUserCustomValidator{Client: b.Build()}
	}

	It("refuses deleting the only admin", func() {
		only := user("alice", framev1alpha1.RoleAdmin)
		v := newValidator(only, user("bob", framev1alpha1.RoleViewer))
		_, err := v.ValidateDelete(context.Background(), only)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	It("allows deleting an admin when another remains", func() {
		alice := user("alice", framev1alpha1.RoleAdmin)
		v := newValidator(alice, user("carol", framev1alpha1.RoleAdmin))
		_, err := v.ValidateDelete(context.Background(), alice)
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses demoting the only admin", func() {
		alice := user("alice", framev1alpha1.RoleAdmin)
		v := newValidator(alice)
		demoted := alice.DeepCopy()
		demoted.Spec.Role = framev1alpha1.RoleViewer
		_, err := v.ValidateUpdate(context.Background(), alice, demoted)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("last admin"))
	})

	It("allows an admin to keep being an admin", func() {
		alice := user("alice", framev1alpha1.RoleAdmin)
		v := newValidator(alice)
		same := alice.DeepCopy()
		same.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
		_, err := v.ValidateUpdate(context.Background(), alice, same)
		Expect(err).NotTo(HaveOccurred())
	})

	It("allows deleting a non-admin even if no admin exists", func() {
		bob := user("bob", framev1alpha1.RoleViewer)
		v := newValidator(bob)
		_, err := v.ValidateDelete(context.Background(), bob)
		Expect(err).NotTo(HaveOccurred())
	})
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/webhook/v1alpha1/ -run TestAPIs -v 2>&1 | head -30`
Expected: compile failure — `undefined: FrameUserCustomValidator`.

- [ ] **Step 3: Write the webhook**

Create `internal/webhook/v1alpha1/frameuser_webhook.go`:

```go
package v1alpha1

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

// SetupFrameUserWebhookWithManager registers the webhook for FrameUser.
func SetupFrameUserWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1alpha1.FrameUser{}).
		WithValidator(&FrameUserCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1alpha1-frameuser,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=frameusers,verbs=create;update;delete,versions=v1alpha1,name=vframeuser-v1alpha1.kb.io,admissionReviewVersions=v1

// FrameUserCustomValidator keeps at least one admin in existence.
//
// This lives at admission rather than inside authd because authd is not the
// only writer: admins create, delete and re-role accounts straight through the
// apiserver under their own identity, and kubectl bypasses authd entirely.
// Admission is the only chokepoint every write passes through.
type FrameUserCustomValidator struct {
	Client client.Client
}

func (v *FrameUserCustomValidator) ValidateCreate(_ context.Context, _ *framev1alpha1.FrameUser) (admission.Warnings, error) {
	return nil, nil
}

func (v *FrameUserCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *framev1alpha1.FrameUser) (admission.Warnings, error) {
	// Only a demotion can remove an admin; anything else leaves the count alone.
	if oldObj.Spec.Role != framev1alpha1.RoleAdmin || newObj.Spec.Role == framev1alpha1.RoleAdmin {
		return nil, nil
	}
	return nil, v.requireAnotherAdmin(ctx, oldObj.Name)
}

func (v *FrameUserCustomValidator) ValidateDelete(ctx context.Context, obj *framev1alpha1.FrameUser) (admission.Warnings, error) {
	if obj.Spec.Role != framev1alpha1.RoleAdmin {
		return nil, nil
	}
	return nil, v.requireAnotherAdmin(ctx, obj.Name)
}

// requireAnotherAdmin fails unless some admin other than `excluding` exists.
func (v *FrameUserCustomValidator) requireAnotherAdmin(ctx context.Context, excluding string) error {
	var users framev1alpha1.FrameUserList
	if err := v.Client.List(ctx, &users); err != nil {
		// Fail closed: an unreadable list is not evidence that another admin
		// exists, and guessing wrong here locks everyone out of the UI.
		return fmt.Errorf("cannot verify remaining admins: %w", err)
	}
	for _, u := range users.Items {
		if u.Name != excluding && u.Spec.Role == framev1alpha1.RoleAdmin {
			return nil
		}
	}
	return fmt.Errorf("refusing to remove the last admin (%s): no other account holds the admin role", excluding)
}
```

- [ ] **Step 4: Register the webhook in the envtest suite and the manager**

In `internal/webhook/v1alpha1/webhook_suite_test.go`, immediately after the existing `err = SetupSchedulingPolicyWebhookWithManager(mgr)` block (around line 118), add:

```go
	err = SetupFrameUserWebhookWithManager(mgr)
	Expect(err).NotTo(HaveOccurred())
```

In `cmd/main.go`, next to the other `Setup*WebhookWithManager` calls, add:

```go
	if err := webhookv1alpha1.SetupFrameUserWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "FrameUser")
		os.Exit(1)
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `make test`
Expected: PASS, including the five new FrameUser webhook specs.

- [ ] **Step 6: Commit**

```bash
git add internal/webhook/v1alpha1/frameuser_webhook.go \
        internal/webhook/v1alpha1/frameuser_webhook_test.go \
        internal/webhook/v1alpha1/webhook_suite_test.go \
        cmd/main.go config/webhook
git commit -m "feat(webhook): refuse removing the last FrameUser admin"
```

---

### Task 3: Password hashing

**Files:**
- Create: `internal/authd/password.go`
- Create: `internal/authd/password_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `HashPassword(plain string) (string, error)` returning an argon2id PHC string, and `VerifyPassword(encoded, plain string) bool`, in package `github.com/rmocq/frame/internal/authd`.

- [ ] **Step 1: Write the failing tests**

Create `internal/authd/password_test.go`:

```go
package authd

import "testing"

func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword(h, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(h, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}

func TestHashIsSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("identical hashes for the same password: salt is not random")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, encoded := range []string{"", "not-a-phc-string", "$argon2id$v=19$m=1", "$bcrypt$x$y$z$w"} {
		if VerifyPassword(encoded, "anything") {
			t.Fatalf("malformed hash %q was accepted", encoded)
		}
	}
}

func TestVerifyEmptyPasswordAgainstEmptyHash(t *testing.T) {
	// An account with no hash stored must never authenticate, whatever is sent.
	if VerifyPassword("", "") {
		t.Fatal("empty hash accepted an empty password")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/authd/ -run TestHash -v`
Expected: compile failure — `undefined: HashPassword`.

- [ ] **Step 3: Implement**

Create `internal/authd/password.go`:

```go
package authd

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. Deliberately above the RFC 9106 second-recommended
// option: this is an admin console with a handful of accounts, so login
// latency is irrelevant next to making an offline crack expensive.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns a PHC-format argon2id string, salt included.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether plain matches the encoded hash.
//
// Returns false rather than an error on malformed input: every caller would
// treat a parse failure as a failed login anyway, and collapsing the two
// removes any chance of a caller acting on the error while ignoring the
// boolean.
func VerifyPassword(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version, memory, time int
	var threads int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(plain), salt, uint32(time), uint32(memory), uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/authd/ -v`
Expected: all four tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/authd/password.go internal/authd/password_test.go go.mod go.sum
git commit -m "feat(authd): argon2id password hashing"
```

---

### Task 4: Signed-cookie challenge store

**Files:**
- Create: `internal/authd/challenge.go`
- Create: `internal/authd/challenge_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `NewChallengeCodec(key []byte) *ChallengeCodec`, with methods `Seal(data []byte, ttl time.Duration) (string, error)` and `Open(token string) ([]byte, error)`.

This is what lets `authd` run two replicas with no shared store: the challenge travels with the browser instead of living in a Redis both replicas must reach.

- [ ] **Step 1: Write the failing tests**

Create `internal/authd/challenge_test.go`:

```go
package authd

import (
	"strings"
	"testing"
	"time"
)

func testCodec() *ChallengeCodec {
	return NewChallengeCodec([]byte("0123456789abcdef0123456789abcdef"))
}

func TestSealOpenRoundTrip(t *testing.T) {
	c := testCodec()
	sealed, err := c.Seal([]byte(`{"challenge":"abc"}`), time.Minute)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := c.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(got) != `{"challenge":"abc"}` {
		t.Fatalf("payload mangled: %s", got)
	}
}

func TestOpenRejectsExpired(t *testing.T) {
	c := testCodec()
	sealed, _ := c.Seal([]byte("x"), -time.Second)
	if _, err := c.Open(sealed); err == nil {
		t.Fatal("expired challenge accepted")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	c := testCodec()
	sealed, _ := c.Seal([]byte("x"), time.Minute)
	parts := strings.Split(sealed, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected format %q", sealed)
	}
	// Flip the payload, keep the signature.
	if _, err := c.Open("Zm9ybmV5." + parts[1]); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestOpenRejectsOtherKey(t *testing.T) {
	sealed, _ := testCodec().Seal([]byte("x"), time.Minute)
	other := NewChallengeCodec([]byte("ffffffffffffffffffffffffffffffff"))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("signature from another key accepted")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/authd/ -run TestSeal -v`
Expected: compile failure — `undefined: ChallengeCodec`.

- [ ] **Step 3: Implement**

Create `internal/authd/challenge.go`:

```go
package authd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

// ChallengeCodec seals short-lived data into a self-contained, signed token.
//
// WebAuthn requires the challenge issued at the start of a ceremony to be
// checked at the end. Keeping it server-side would force the replicas to share
// a store; signing it and handing it to the browser removes that requirement
// without weakening anything, because the signature is what makes the value
// trustworthy, not where it was parked.
type ChallengeCodec struct {
	key []byte
}

func NewChallengeCodec(key []byte) *ChallengeCodec {
	return &ChallengeCodec{key: key}
}

var errBadChallenge = errors.New("challenge is missing, expired or has been tampered with")

// Seal returns "<base64url payload>.<base64url signature>". The expiry is part
// of the signed payload, so it cannot be extended by the holder.
func (c *ChallengeCodec) Seal(data []byte, ttl time.Duration) (string, error) {
	payload := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().Add(ttl).Unix()))
	copy(payload[8:], data)

	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(c.sign(encoded)), nil
}

// Open verifies the signature, then the expiry, and returns the payload.
func (c *ChallengeCodec) Open(token string) ([]byte, error) {
	encoded, sig, found := strings.Cut(token, ".")
	if !found {
		return nil, errBadChallenge
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(gotSig, c.sign(encoded)) {
		return nil, errBadChallenge
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) < 8 {
		return nil, errBadChallenge
	}
	if time.Now().After(time.Unix(int64(binary.BigEndian.Uint64(payload[:8])), 0)) {
		return nil, errBadChallenge
	}
	return payload[8:], nil
}

func (c *ChallengeCodec) sign(encoded string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(encoded))
	return m.Sum(nil)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/authd/ -v`
Expected: all challenge tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/authd/challenge.go internal/authd/challenge_test.go
git commit -m "feat(authd): stateless signed-cookie challenge codec"
```

---

### Task 5: OIDC issuer — ID tokens and JWKS

**Files:**
- Create: `internal/authd/issuer.go`
- Create: `internal/authd/issuer_test.go`

**Interfaces:**
- Consumes: `RoleAdmin`/`RoleOperator`/`RoleViewer` from Task 1.
- Produces: `NewIssuer(url, clientID string, key *ecdsa.PrivateKey) (*Issuer, error)` with methods `Mint(email, role string, ttl time.Duration) (string, error)`, `JWKS() ([]byte, error)` and `Discovery() ([]byte, error)`; plus the package-level function `GroupForRole(role string) string`.

The apiserver only ever consumes these three things: the discovery document, the JWKS, and a signed ID token. Getting the claim names right here is what makes Stage 2 a configuration change rather than a rewrite.

- [ ] **Step 1: Write the failing tests**

Create `internal/authd/issuer_test.go`:

```go
package authd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

func testIssuer(t *testing.T) *Issuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	iss, err := NewIssuer("https://auth.example.svc", "frame-ui", key)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	return iss
}

func TestMintCarriesEmailAndGroup(t *testing.T) {
	iss := testIssuer(t)
	raw, err := iss.Mint("alice@example.com", framev1alpha1.RoleOperator, 15*time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var claims struct {
		Issuer   string   `json:"iss"`
		Audience []string `json:"aud"`
		Email    string   `json:"email"`
		Groups   []string `json:"groups"`
	}
	if err := tok.UnsafeClaimsWithoutVerification(&claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims.Email != "alice@example.com" {
		t.Fatalf("email claim = %q", claims.Email)
	}
	if len(claims.Groups) != 1 || claims.Groups[0] != "operators" {
		t.Fatalf("groups claim = %v, want [operators]", claims.Groups)
	}
	if claims.Issuer != "https://auth.example.svc" {
		t.Fatalf("iss = %q", claims.Issuer)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != "frame-ui" {
		t.Fatalf("aud = %v", claims.Audience)
	}
}

func TestMintRejectsUnknownRole(t *testing.T) {
	if _, err := testIssuer(t).Mint("a@b.c", "wizard", time.Minute); err == nil {
		t.Fatal("unknown role was minted a token")
	}
}

func TestJWKSPublishesPublicKeyOnly(t *testing.T) {
	raw, err := testIssuer(t).JWKS()
	if err != nil {
		t.Fatalf("jwks: %v", err)
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(set.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(set.Keys))
	}
	if !set.Keys[0].IsPublic() {
		t.Fatal("JWKS exposed a private key")
	}
}

func TestDiscoveryPointsAtOurEndpoints(t *testing.T) {
	raw, err := testIssuer(t).Discovery()
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	var doc struct {
		Issuer  string   `json:"issuer"`
		JWKSURI string   `json:"jwks_uri"`
		Algs    []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Issuer != "https://auth.example.svc" {
		t.Fatalf("issuer = %q", doc.Issuer)
	}
	if doc.JWKSURI != "https://auth.example.svc/keys" {
		t.Fatalf("jwks_uri = %q", doc.JWKSURI)
	}
	if len(doc.Algs) != 1 || doc.Algs[0] != "ES256" {
		t.Fatalf("algs = %v", doc.Algs)
	}
}
```

- [ ] **Step 2: Add the dependency and run to verify failure**

Run:

```bash
go get github.com/go-jose/go-jose/v4@v4.1.4
go test ./internal/authd/ -run TestMint -v
```

Expected: compile failure — `undefined: NewIssuer`.

- [ ] **Step 3: Implement**

Create `internal/authd/issuer.go`:

```go
package authd

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

// keyID is stable because the apiserver caches the JWKS: a changing kid would
// make it refetch on every token.
const keyID = "frame-authd"

// Issuer mints the ID tokens the Kubernetes apiserver validates, and publishes
// the two documents it needs to do so.
type Issuer struct {
	url      string
	clientID string
	signer   jose.Signer
	public   jose.JSONWebKey
}

func NewIssuer(url, clientID string, key *ecdsa.PrivateKey) (*Issuer, error) {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: keyID}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return nil, fmt.Errorf("building signer: %w", err)
	}
	return &Issuer{
		url:      url,
		clientID: clientID,
		signer:   signer,
		public:   jose.JSONWebKey{Key: key.Public(), KeyID: keyID, Algorithm: string(jose.ES256), Use: "sig"},
	}, nil
}

// GroupForRole maps a FrameUser role to the group claim. The apiserver adds
// its own `frame:` prefix, so these stay unprefixed here.
func GroupForRole(role string) string {
	switch role {
	case framev1alpha1.RoleAdmin:
		return "admins"
	case framev1alpha1.RoleOperator:
		return "operators"
	case framev1alpha1.RoleViewer:
		return "viewers"
	default:
		return ""
	}
}

// Mint issues a short-lived ID token for one person.
func (i *Issuer) Mint(email, role string, ttl time.Duration) (string, error) {
	group := GroupForRole(role)
	if group == "" {
		// Refusing beats defaulting: a typo in a role must not silently
		// produce a token, and least of all a viewer-shaped one that looks
		// like it worked.
		return "", fmt.Errorf("unknown role %q", role)
	}
	now := time.Now()
	claims := struct {
		jwt.Claims
		Email  string   `json:"email"`
		Groups []string `json:"groups"`
	}{
		Claims: jwt.Claims{
			Issuer:    i.url,
			Subject:   email,
			Audience:  jose.Audience{i.clientID},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Expiry:    jwt.NewNumericDate(now.Add(ttl)),
		},
		Email:  email,
		Groups: []string{group},
	}
	return jwt.Signed(i.signer).Claims(claims).Serialize()
}

func (i *Issuer) JWKS() ([]byte, error) {
	return json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{i.public}})
}

func (i *Issuer) Discovery() ([]byte, error) {
	return json.Marshal(map[string]any{
		"issuer":                                i.url,
		"jwks_uri":                              i.url + "/keys",
		"id_token_signing_alg_values_supported": []string{"ES256"},
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"claims_supported":                      []string{"iss", "sub", "aud", "exp", "iat", "email", "groups"},
	})
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/authd/ -v`
Expected: all issuer tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/authd/issuer.go internal/authd/issuer_test.go go.mod go.sum
git commit -m "feat(authd): OIDC issuer with ES256 ID tokens and JWKS"
```

---

### Task 6: FrameUser store

**Files:**
- Create: `internal/authd/store.go`
- Create: `internal/authd/store_test.go`

**Interfaces:**
- Consumes: `framev1alpha1.FrameUser`, `FrameUserList` from Task 1.
- Produces: `NewStore(c client.Client, namespace string) *Store` with methods `ByEmail(ctx, email) (*framev1alpha1.FrameUser, error)`, `ByCredentialID(ctx, credID string) (*framev1alpha1.FrameUser, error)`, `AdminCount(ctx) (int, error)`, `AddCredential(ctx, u *framev1alpha1.FrameUser, cred framev1alpha1.WebAuthnCredential) error`, `UpdateSignCount(ctx, u *framev1alpha1.FrameUser, credID string, count uint32) error`, and `RemoveCredential(ctx, u *framev1alpha1.FrameUser, credID string) error`. `ErrUserNotFound` is returned when no account matches.

- [ ] **Step 1: Write the failing tests**

Create `internal/authd/store_test.go`:

```go
package authd

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

func storeWith(t *testing.T, users ...*framev1alpha1.FrameUser) *Store {
	t.Helper()
	s := scheme.Scheme
	if err := framev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&framev1alpha1.FrameUser{})
	for _, u := range users {
		b = b.WithObjects(u)
	}
	return NewStore(b.Build(), "cluster-control")
}

func fixture(name, email, role string, creds ...framev1alpha1.WebAuthnCredential) *framev1alpha1.FrameUser {
	return &framev1alpha1.FrameUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cluster-control"},
		Spec:       framev1alpha1.FrameUserSpec{Email: email, Role: role},
		Status:     framev1alpha1.FrameUserStatus{Credentials: creds},
	}
}

func TestByEmailFindsAndMisses(t *testing.T) {
	s := storeWith(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
	got, err := s.ByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("ByEmail: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("got %q", got.Name)
	}
	if _, err := s.ByEmail(context.Background(), "nobody@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("want ErrUserNotFound, got %v", err)
	}
}

func TestByCredentialID(t *testing.T) {
	cred := framev1alpha1.WebAuthnCredential{ID: "cred-1", PublicKey: "pk", SignCount: 7}
	s := storeWith(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, cred))
	got, err := s.ByCredentialID(context.Background(), "cred-1")
	if err != nil {
		t.Fatalf("ByCredentialID: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("got %q", got.Name)
	}
	if _, err := s.ByCredentialID(context.Background(), "unknown"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("want ErrUserNotFound, got %v", err)
	}
}

func TestUpdateSignCount(t *testing.T) {
	cred := framev1alpha1.WebAuthnCredential{ID: "cred-1", PublicKey: "pk", SignCount: 7}
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, cred)
	s := storeWith(t, u)
	if err := s.UpdateSignCount(context.Background(), u, "cred-1", 9); err != nil {
		t.Fatalf("UpdateSignCount: %v", err)
	}
	got, _ := s.ByEmail(context.Background(), "alice@example.com")
	if got.Status.Credentials[0].SignCount != 9 {
		t.Fatalf("counter = %d, want 9", got.Status.Credentials[0].SignCount)
	}
}

func TestRemoveCredentialKeepsLastKeyWhenPasswordDisabled(t *testing.T) {
	cred := framev1alpha1.WebAuthnCredential{ID: "only", PublicKey: "pk"}
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, cred)
	u.Spec.PasswordAuth = framev1alpha1.PasswordDisabled
	s := storeWith(t, u)
	if err := s.RemoveCredential(context.Background(), u, "only"); err == nil {
		t.Fatal("removing the last key of a passkey-only account was allowed")
	}
}

func TestRemoveCredentialAllowedWhenPasswordEnabled(t *testing.T) {
	cred := framev1alpha1.WebAuthnCredential{ID: "only", PublicKey: "pk"}
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin, cred)
	u.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
	s := storeWith(t, u)
	if err := s.RemoveCredential(context.Background(), u, "only"); err != nil {
		t.Fatalf("RemoveCredential: %v", err)
	}
	got, _ := s.ByEmail(context.Background(), "alice@example.com")
	if len(got.Status.Credentials) != 0 {
		t.Fatalf("credential not removed: %v", got.Status.Credentials)
	}
}

func TestAdminCount(t *testing.T) {
	s := storeWith(t,
		fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin),
		fixture("bob", "bob@example.com", framev1alpha1.RoleViewer),
	)
	n, err := s.AdminCount(context.Background())
	if err != nil {
		t.Fatalf("AdminCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("AdminCount = %d, want 1", n)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/authd/ -run TestByEmail -v`
Expected: compile failure — `undefined: NewStore`.

- [ ] **Step 3: Implement**

Create `internal/authd/store.go`:

```go
package authd

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

// ErrUserNotFound is returned for both an unknown email and an unknown
// credential, so a caller cannot use the error to tell which addresses exist.
var ErrUserNotFound = errors.New("no such user")

// Store is authd's view of the FrameUser resources.
type Store struct {
	client    client.Client
	namespace string
}

func NewStore(c client.Client, namespace string) *Store {
	return &Store{client: c, namespace: namespace}
}

func (s *Store) list(ctx context.Context) ([]framev1alpha1.FrameUser, error) {
	var users framev1alpha1.FrameUserList
	if err := s.client.List(ctx, &users, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	return users.Items, nil
}

func (s *Store) ByEmail(ctx context.Context, email string) (*framev1alpha1.FrameUser, error) {
	items, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Spec.Email == email {
			return &items[i], nil
		}
	}
	return nil, ErrUserNotFound
}

// ByCredentialID resolves the account owning a credential, which is how the
// usernameless sign-in flow identifies who is at the keyboard.
func (s *Store) ByCredentialID(ctx context.Context, credID string) (*framev1alpha1.FrameUser, error) {
	items, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		for _, c := range items[i].Status.Credentials {
			if c.ID == credID {
				return &items[i], nil
			}
		}
	}
	return nil, ErrUserNotFound
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	items, err := s.list(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range items {
		if u.Spec.Role == framev1alpha1.RoleAdmin {
			n++
		}
	}
	return n, nil
}

func (s *Store) AddCredential(ctx context.Context, u *framev1alpha1.FrameUser, cred framev1alpha1.WebAuthnCredential) error {
	for _, existing := range u.Status.Credentials {
		if existing.ID == cred.ID {
			return fmt.Errorf("credential %s is already enrolled", cred.ID)
		}
	}
	u.Status.Credentials = append(u.Status.Credentials, cred)
	return s.client.Status().Update(ctx, u)
}

func (s *Store) UpdateSignCount(ctx context.Context, u *framev1alpha1.FrameUser, credID string, count uint32) error {
	for i := range u.Status.Credentials {
		if u.Status.Credentials[i].ID == credID {
			u.Status.Credentials[i].SignCount = count
			return s.client.Status().Update(ctx, u)
		}
	}
	return ErrUserNotFound
}

// RemoveCredential refuses to strip an account of its last way in.
//
// Same class of guard as the admission webhook's, applied here because authd
// is the only writer of status.credentials: revoking the final key of a
// passkey-only account would lock its owner out with no recovery short of
// kubectl.
func (s *Store) RemoveCredential(ctx context.Context, u *framev1alpha1.FrameUser, credID string) error {
	kept := make([]framev1alpha1.WebAuthnCredential, 0, len(u.Status.Credentials))
	found := false
	for _, c := range u.Status.Credentials {
		if c.ID == credID {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		return ErrUserNotFound
	}
	if len(kept) == 0 && u.Spec.PasswordAuth != framev1alpha1.PasswordEnabled {
		return fmt.Errorf("refusing to remove the last key of %s: password sign-in is disabled, so the account would become unreachable", u.Spec.Email)
	}
	u.Status.Credentials = kept
	return s.client.Status().Update(ctx, u)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/authd/ -v`
Expected: all store tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/authd/store.go internal/authd/store_test.go
git commit -m "feat(authd): FrameUser store with last-credential guard"
```

---

### Task 7: WebAuthn registration and assertion

**Files:**
- Create: `internal/authd/webauthn.go`
- Create: `internal/authd/webauthn_test.go`

**Interfaces:**
- Consumes: `Store`, `ErrUserNotFound` (Task 6); `ChallengeCodec` (Task 4); `framev1alpha1.WebAuthnCredential` (Task 1).
- Produces: `NewAuthenticator(rpID, rpOrigin string, store *Store, codec *ChallengeCodec) (*Authenticator, error)` with methods `BeginRegistration(ctx, u *framev1alpha1.FrameUser) (options []byte, sealedChallenge string, err error)`, `FinishRegistration(ctx, u *framev1alpha1.FrameUser, sealedChallenge string, response []byte, label string) error`, `BeginLogin(ctx) (options []byte, sealedChallenge string, err error)`, and `FinishLogin(ctx, sealedChallenge string, response []byte) (*framev1alpha1.FrameUser, error)`. `ErrCounterRegression` is returned when the authenticator counter fails to advance.

- [ ] **Step 1: Write the failing tests**

Create `internal/authd/webauthn_test.go`:

```go
package authd

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

func testAuthenticator(t *testing.T, users ...*framev1alpha1.FrameUser) *Authenticator {
	t.Helper()
	a, err := NewAuthenticator("example.com", "https://example.com", storeWith(t, users...), testCodec())
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	return a
}

func TestBeginLoginIsUsernameless(t *testing.T) {
	a := testAuthenticator(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin,
		framev1alpha1.WebAuthnCredential{ID: "cred-1", PublicKey: "pk"}))

	opts, sealed, err := a.BeginLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if sealed == "" {
		t.Fatal("no sealed challenge returned")
	}
	var parsed struct {
		PublicKey struct {
			AllowCredentials []any  `json:"allowCredentials"`
			Challenge        string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// No allowCredentials: the browser picks the resident key. Populating it
	// would leak which credentials exist to an unauthenticated caller.
	if len(parsed.PublicKey.AllowCredentials) != 0 {
		t.Fatalf("allowCredentials was populated: %v", parsed.PublicKey.AllowCredentials)
	}
	if parsed.PublicKey.Challenge == "" {
		t.Fatal("no challenge in options")
	}
}

func TestFinishLoginRejectsForgedChallenge(t *testing.T) {
	a := testAuthenticator(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
	_, err := a.FinishLogin(context.Background(), "forged.challenge", []byte(`{}`))
	if err == nil {
		t.Fatal("a forged sealed challenge was accepted")
	}
}

func TestFinishLoginRejectsExpiredChallenge(t *testing.T) {
	a := testAuthenticator(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
	expired, _ := testCodec().Seal([]byte(`{"challenge":"x"}`), -1)
	if _, err := a.FinishLogin(context.Background(), expired, []byte(`{}`)); err == nil {
		t.Fatal("an expired challenge was accepted")
	}
}

func TestBeginRegistrationSealsChallenge(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	a := testAuthenticator(t, u)
	opts, sealed, err := a.BeginRegistration(context.Background(), u)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if len(opts) == 0 || sealed == "" {
		t.Fatal("empty options or challenge")
	}
	if _, err := testCodec().Open(sealed); err != nil {
		t.Fatalf("sealed challenge does not verify: %v", err)
	}
}

func TestCounterRegressionIsDistinguishable(t *testing.T) {
	// ErrCounterRegression must be its own error so the HTTP layer can log it
	// for investigation while still refusing the login. It must never lead to
	// deleting the credential: the check runs before signature verification,
	// so anyone who learns a credentialId could otherwise revoke it.
	if ErrCounterRegression == nil {
		t.Fatal("ErrCounterRegression is not defined")
	}
	if !errors.Is(ErrCounterRegression, ErrCounterRegression) {
		t.Fatal("ErrCounterRegression is not comparable with errors.Is")
	}
}
```

- [ ] **Step 2: Add the dependency and run to verify failure**

Run:

```bash
go get github.com/go-webauthn/webauthn@v0.17.4
go test ./internal/authd/ -run TestBeginLogin -v
```

Expected: compile failure — `undefined: NewAuthenticator`.

- [ ] **Step 3: Implement**

Create `internal/authd/webauthn.go`:

```go
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

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

// challengeTTL bounds how long a ceremony may take. Long enough to find the
// key in a drawer, short enough that a captured challenge is worthless.
const challengeTTL = 5 * time.Minute

// ErrCounterRegression signals an authenticator counter that failed to
// advance — the library's clone/replay signal.
//
// It is deliberately a distinct error so the caller can log it for manual
// investigation while refusing the login. It must never trigger deleting the
// credential: go-webauthn performs this check before verifying the signature,
// so an attacker who knows a credentialId — not a secret — could otherwise
// revoke someone else's key without authenticating.
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
			ID:              id,
			PublicKey:       pk,
			Authenticator:   webauthn.Authenticator{SignCount: c.SignCount},
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
		if strings.Contains(err.Error(), "cloned") || strings.Contains(err.Error(), "counter") {
			return nil, fmt.Errorf("%w: %v", ErrCounterRegression, err)
		}
		return nil, fmt.Errorf("verifying assertion: %w", err)
	}

	credID := base64.RawURLEncoding.EncodeToString(cred.ID)
	if err := a.store.UpdateSignCount(ctx, matched, credID, cred.Authenticator.SignCount); err != nil {
		return nil, fmt.Errorf("recording sign count: %w", err)
	}
	return matched, nil
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
	sealed, err := a.codec.Seal(encoded, challengeTTL)
	if err != nil {
		return nil, "", fmt.Errorf("sealing challenge: %w", err)
	}
	return raw, sealed, nil
}

func (a *Authenticator) openSession(sealed string) (*webauthn.SessionData, error) {
	payload, err := a.codec.Open(sealed)
	if err != nil {
		return nil, err
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(payload, &session); err != nil {
		return nil, fmt.Errorf("decoding challenge: %w", err)
	}
	return &session, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/authd/ -v`
Expected: all WebAuthn tests PASS. If `BeginDiscoverableLogin` or `ValidateDiscoverableLogin` has a different signature in v0.17.4, run `go doc github.com/go-webauthn/webauthn/webauthn.WebAuthn` and adapt — the behaviour required is fixed even if the spelling moved.

- [ ] **Step 5: Commit**

```bash
git add internal/authd/webauthn.go internal/authd/webauthn_test.go go.mod go.sum
git commit -m "feat(authd): usernameless WebAuthn registration and login"
```

---

### Task 8: HTTP server, sessions and bootstrap

**Files:**
- Create: `internal/authd/server.go`
- Create: `internal/authd/server_test.go`
- Create: `cmd/authd/main.go`

**Interfaces:**
- Consumes: everything from Tasks 3-7.
- Produces: `NewServer(cfg ServerConfig) (*Server, error)` implementing `http.Handler`, with `ServerConfig{Store *Store, Auth *Authenticator, Issuer *Issuer, Codec *ChallengeCodec, BootstrapSecret string, Namespace string, TokenTTL time.Duration}`.

Routes: `GET /.well-known/openid-configuration`, `GET /keys`, `POST /auth/bootstrap`, `POST /auth/login/begin`, `POST /auth/login/finish`, `POST /auth/login/password`, `POST /auth/register/begin`, `POST /auth/register/finish`, `POST /auth/token`, `POST /auth/logout`, `GET /healthz`.

- [ ] **Step 1: Write the failing tests**

Create `internal/authd/server_test.go`:

```go
package authd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/authd/ -run TestDiscovery -v`
Expected: compile failure — `undefined: NewServer`.

- [ ] **Step 3: Implement the server**

Create `internal/authd/server.go`. Key points the tests pin down: the session cookie is `HttpOnly`, `Secure`, `SameSite=Strict`; `/auth/bootstrap` returns 404 once any user exists and 401 on a wrong token, compared with `subtle.ConstantTimeCompare`; password login returns an identical 401 body for an unknown email and a wrong password; `/auth/token` requires a valid session cookie and returns the minted ID token in the JSON body, never in a cookie.

```go
package authd

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

const sessionCookie = "frame_session"

type ServerConfig struct {
	Store           *Store
	Auth            *Authenticator
	Issuer          *Issuer
	Codec           *ChallengeCodec
	BootstrapSecret string
	Namespace       string
	TokenTTL        time.Duration
	SessionTTL      time.Duration
}

type Server struct {
	cfg ServerConfig
	mux *http.ServeMux
}

func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /.well-known/openid-configuration", s.handleDiscovery)
	s.mux.HandleFunc("GET /keys", s.handleJWKS)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	s.mux.HandleFunc("POST /auth/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("POST /auth/login/password", s.handlePasswordLogin)
	s.mux.HandleFunc("POST /auth/token", s.handleToken)
	s.mux.HandleFunc("POST /auth/logout", s.handleLogout)
	s.mux.HandleFunc("POST /auth/login/begin", s.handleLoginBegin)
	s.mux.HandleFunc("POST /auth/login/finish", s.handleLoginFinish)
	s.mux.HandleFunc("POST /auth/register/begin", s.handleRegisterBegin)
	s.mux.HandleFunc("POST /auth/register/finish", s.handleRegisterFinish)
	return s, nil
}

const challengeCookie = "frame_challenge"

// setChallenge parks the sealed ceremony state in a short-lived cookie. Same
// attributes as the session cookie: the challenge is signed, but there is no
// reason to let script read it either.
func (s *Server) setChallenge(w http.ResponseWriter, sealed string) {
	http.SetCookie(w, &http.Cookie{
		Name:     challengeCookie,
		Value:    sealed,
		Path:     "/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(challengeTTL.Seconds()),
	})
}

func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	options, sealed, err := s.cfg.Auth.BeginLogin(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setChallenge(w, sealed)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(options)
}

func (s *Server) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(challengeCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	u, err := s.cfg.Auth.FinishLogin(r.Context(), c.Value, body)
	if err != nil {
		// A counter regression means a possible cloned authenticator. It is
		// logged loudly and refused, but the credential is deliberately left
		// in place — see ErrCounterRegression for why revoking here would be
		// an unauthenticated denial-of-service.
		if errors.Is(err, ErrCounterRegression) {
			slog.Error("possible cloned authenticator: sign counter did not advance", "error", err)
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.setSession(w, u)
	w.WriteHeader(http.StatusNoContent)
}

// handleRegisterBegin enrols an additional key for whoever is already signed
// in. Enrolling for someone else is not possible here by construction: the
// account comes from the session cookie, never from the request body.
func (s *Server) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	options, sealed, err := s.cfg.Auth.BeginRegistration(r.Context(), u)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setChallenge(w, sealed)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(options)
}

func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionUser(w, r)
	if !ok {
		return
	}
	c, err := r.Cookie(challengeCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	label := r.URL.Query().Get("label")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.cfg.Auth.FinishRegistration(r.Context(), u, c.Value, body, label); err != nil {
		http.Error(w, "registration failed", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// sessionUser resolves the signed-in account, writing the 401 itself so every
// caller is a two-liner that cannot forget to stop on failure.
func (s *Server) sessionUser(w http.ResponseWriter, r *http.Request) (*framev1alpha1.FrameUser, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	email, err := s.cfg.Codec.Open(c.Value)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), string(email))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return u, true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	body, err := s.cfg.Issuer.Discovery()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	body, err := s.cfg.Issuer.JWKS()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	// Closed for good the moment an account exists. Checked before the token
	// so a leaked bootstrap secret is worthless after first use.
	if n, err := s.cfg.Store.AdminCount(r.Context()); err != nil || n > 0 {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Token string `json:"token"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Token), []byte(s.cfg.BootstrapSecret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Creating the FrameUser and deleting the bootstrap Secret happen here,
	// then the caller proceeds to /auth/register/begin to enrol a key.
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), body.Email)
	// One response for every failure: unknown account, password disabled, and
	// wrong password are indistinguishable, so this endpoint cannot be used to
	// enumerate who has an account.
	if err != nil || u.Spec.PasswordAuth != framev1alpha1.PasswordEnabled ||
		!VerifyPassword(u.Spec.PasswordHash, body.Password) {
		if err != nil && !errors.Is(err, ErrUserNotFound) {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.setSession(w, u)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setSession(w http.ResponseWriter, u *framev1alpha1.FrameUser) {
	sealed, err := s.cfg.Codec.Seal([]byte(u.Spec.Email), s.cfg.SessionTTL)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sealed,
		Path:     "/",
		HttpOnly: true, // unreadable from JavaScript: an XSS cannot steal the session
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	email, err := s.cfg.Codec.Open(c.Value)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	u, err := s.cfg.Store.ByEmail(r.Context(), string(email))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Minted fresh from the current role, so a demotion takes effect within one
	// token lifetime instead of lasting as long as the session.
	token, err := s.cfg.Issuer.Mint(u.Spec.Email, u.Spec.Role, s.cfg.TokenTTL)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Returned in the body, never as a cookie: the browser keeps it in memory
	// only, so it is never persisted where a later XSS could read it.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id_token":   token,
		"expires_in": int(s.cfg.TokenTTL.Seconds()),
	})
}
```

Then create `cmd/authd/main.go` reading configuration from the environment (`OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `RP_ID`, `RP_ORIGIN`, `NAMESPACE`, `TOKEN_TTL`), loading the ES256 signing key and the challenge HMAC key from a mounted Secret, building a controller-runtime client, and serving TLS on `:8443`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/authd/ -v && go build ./cmd/authd`
Expected: all server tests PASS and the binary builds.

- [ ] **Step 5: Commit**

```bash
git add internal/authd/server.go internal/authd/server_test.go cmd/authd/main.go
git commit -m "feat(authd): HTTP surface, sessions and one-shot bootstrap"
```

---

### Task 9: Deploy authd alongside the existing UI

**Files:**
- Create: `deploy/kubernetes/base/authd/deployment.yaml`
- Create: `deploy/kubernetes/base/authd/service.yaml`
- Create: `deploy/kubernetes/base/authd/rbac.yaml`
- Create: `deploy/kubernetes/base/authd/certificate.yaml`
- Create: `Dockerfile.authd`
- Modify: `deploy/kubernetes/base/kustomization.yaml` (add the `authd/` resources)
- Modify: `Makefile` (add `docker-build-authd` and `docker-push-authd`, mirroring `docker-build-ui`)

**Interfaces:**
- Consumes: the `authd` binary from Task 8.
- Produces: Service `cluster-control-auth` in namespace `cluster-control` on port 443, serving TLS from the `frame-auth-tls` Secret. Secret `frame-auth-ca` holds the CA that Stage 2 places on the k3s node.

Nothing in the UI changes. This task only proves `authd` runs and serves what the apiserver will later consume.

- [ ] **Step 1: Write the cert-manager chain**

Create `deploy/kubernetes/base/authd/certificate.yaml`:

```yaml
# A dedicated CA rather than a self-signed leaf: the apiserver is given this CA
# via --oidc-ca-file in Stage 2, so the serving certificate must be re-issuable
# without touching the node again.
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: frame-auth-ca
  namespace: cluster-control
spec:
  isCA: true
  commonName: frame-auth-ca
  secretName: frame-auth-ca
  duration: 87600h
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: neura-selfsigned
    kind: ClusterIssuer
    group: cert-manager.io
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: frame-auth-ca
  namespace: cluster-control
spec:
  ca:
    secretName: frame-auth-ca
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: frame-auth-tls
  namespace: cluster-control
spec:
  secretName: frame-auth-tls
  duration: 2160h
  renewBefore: 360h
  privateKey:
    algorithm: ECDSA
    size: 256
  dnsNames:
    - cluster-control-auth.cluster-control.svc
    - cluster-control-auth.cluster-control.svc.cluster.local
  issuerRef:
    name: frame-auth-ca
    kind: Issuer
```

- [ ] **Step 2: Write the RBAC**

Create `deploy/kubernetes/base/authd/rbac.yaml`. `authd` never holds the permissions it grants — the ID token carries those, and the apiserver checks them against the user.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: cluster-control-auth
  namespace: cluster-control
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: cluster-control-auth
  namespace: cluster-control
rules:
  # Read accounts to authenticate; write status to record credentials and
  # rotate sign counters. Create is for bootstrap only — the admission webhook
  # is what stops it being abused to mint a second admin.
  - apiGroups: [frame.plume-labs.io]
    resources: [frameusers]
    verbs: [get, list, watch, create]
  - apiGroups: [frame.plume-labs.io]
    resources: [frameusers/status]
    verbs: [get, patch, update]
  # Delete the one-shot bootstrap Secret after it is consumed.
  - apiGroups: [""]
    resources: [secrets]
    resourceNames: [frame-auth-bootstrap]
    verbs: [get, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: cluster-control-auth
  namespace: cluster-control
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: cluster-control-auth
subjects:
  - kind: ServiceAccount
    name: cluster-control-auth
    namespace: cluster-control
```

- [ ] **Step 3: Write the Deployment, Service and Dockerfile**

`deploy/kubernetes/base/authd/deployment.yaml`: two replicas, `serviceAccountName: cluster-control-auth`, container port 8443, `frame-auth-tls` and the signing-key Secret `frame-auth-keys` mounted read-only, `readOnlyRootFilesystem: true`, `runAsNonRoot: true`, probes on `/healthz`. `deploy/kubernetes/base/authd/service.yaml`: `cluster-control-auth`, port 443 → 8443. `Dockerfile.authd`: mirror `Dockerfile.controller` — `golang:1.26` builder producing `/authd`, `gcr.io/distroless/static:nonroot` runtime.

Add to `deploy/kubernetes/base/kustomization.yaml`, after `config.yaml`:

```yaml
  - authd/certificate.yaml
  - authd/rbac.yaml
  - authd/service.yaml
  - authd/deployment.yaml
```

- [ ] **Step 4: Generate the signing keys Secret**

Run:

```bash
openssl ecparam -name prime256v1 -genkey -noout -out /tmp/authd-es256.pem
kubectl -n cluster-control create secret generic frame-auth-keys \
  --from-file=signing.pem=/tmp/authd-es256.pem \
  --from-literal=challenge-hmac="$(openssl rand -hex 32)"
kubectl -n cluster-control create secret generic frame-auth-bootstrap \
  --from-literal=token="$(openssl rand -hex 32)"
shred -u /tmp/authd-es256.pem
```

These are cluster state, not git: never commit them.

- [ ] **Step 5: Build, push and deploy**

Run:

```bash
export KUBECONFIG=$HOME/Neura/.test-cluster/kubeconfig-neura-test.yaml
SHA=$(git rev-parse --short HEAD)
podman build -f Dockerfile.authd -t 192.168.2.201:30500/cluster-control-auth:$SHA .
podman push --tls-verify=false 192.168.2.201:30500/cluster-control-auth:$SHA
kubectl apply -k deploy/kubernetes/base/
kubectl -n cluster-control set image deploy/cluster-control-auth authd=192.168.2.201:30500/cluster-control-auth:$SHA
kubectl -n cluster-control rollout status deploy/cluster-control-auth --timeout=180s
```

Expected: rollout succeeds, 2/2 pods Running.

- [ ] **Step 6: Verify what the apiserver will consume**

Run:

```bash
kubectl -n cluster-control run oidc-probe --rm -i --restart=Never --image=curlimages/curl:latest -- \
  sh -c 'curl -sk https://cluster-control-auth.cluster-control.svc/.well-known/openid-configuration; echo; \
         curl -sk https://cluster-control-auth.cluster-control.svc/keys'
```

Expected: a discovery document whose `issuer` is `https://cluster-control-auth.cluster-control.svc` and whose `jwks_uri` ends in `/keys`, followed by a JWKS containing exactly one public ES256 key. No private key material in either.

- [ ] **Step 7: Verify the UI is untouched**

Run:

```bash
curl -sS -o /dev/null -w "ui %{http_code}\n" http://192.168.2.201:30880/
curl -sS -o /dev/null -w "api %{http_code}\n" http://192.168.2.201:30880/api/v1/nodes
kubectl -n cluster-control get deploy cluster-control-ui -o jsonpath='{.spec.template.spec.containers[*].name}{"\n"}'
```

Expected: both 200, and the UI deployment still lists `ui kube-proxy-api` — Stage 1 must not have disturbed the existing path.

- [ ] **Step 8: Commit**

```bash
git add deploy/kubernetes/base/authd deploy/kubernetes/base/kustomization.yaml \
        Dockerfile.authd Makefile
git commit -m "feat(deploy): run authd alongside the UI, nothing switched over"
```

---

## Stage 1 exit criteria

- `make test` passes, including the webhook envtest specs.
- `authd` serves a valid discovery document and JWKS over TLS inside the cluster.
- A `FrameUser` can be created and the webhook refuses removing the last admin, verified with `kubectl`.
- The UI and its ServiceAccount path are byte-for-byte unchanged.

Stage 2 (apiserver configuration via the privileged DaemonSet) and Stage 3 (UI switch-over) each get their own plan, written once the stage before them is verified.
