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
	salt, want, memory, time, threads, ok := parsePasswordHash(encoded)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(plain), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// hashIsUsable reports whether encoded is a well-formed, non-empty argon2id
// PHC hash — the same parse VerifyPassword performs, without paying its
// argon2id computation cost. handlePasswordLogin folds this into `usable` so
// an account with spec.passwordAuth: enabled but an empty or unparseable
// spec.passwordHash falls onto the same dummy-hash, full-cost verification
// path as an unknown email, instead of returning in microseconds through
// VerifyPassword's own malformed-input fast path — which would otherwise
// positively identify such an account by timing alone, the same class of
// oracle TestUnknownEmailAndWrongPasswordAreIndistinguishable guards
// against. Nothing writes spec.passwordHash yet (a future set-password
// endpoint will), so today this only ever sees a fully-formed hash or an
// empty string, but the check is general.
func hashIsUsable(encoded string) bool {
	_, _, _, _, _, ok := parsePasswordHash(encoded)
	return ok
}

// parsePasswordHash parses a PHC-format argon2id string into the pieces
// VerifyPassword needs to check it, without doing the argon2id computation
// itself. ok is false for anything that isn't a well-formed argon2id hash,
// including an empty string.
func parsePasswordHash(encoded string) (salt, want []byte, memory, time uint32, threads uint8, ok bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, false
	}
	var version, m, t, p int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, nil, 0, 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return nil, nil, 0, 0, 0, false
	}
	s, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, 0, 0, 0, false
	}
	w, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(w) == 0 {
		return nil, nil, 0, 0, 0, false
	}
	return s, w, uint32(m), uint32(t), uint8(p), true
}
