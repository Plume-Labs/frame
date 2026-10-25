package main

import (
	"testing"

	"github.com/Plume-Labs/frame/internal/authd"
)

// setRequiredEnv fills every variable loadEnvConfig insists on, so a test
// only has to state the one it is about.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"OIDC_ISSUER_URL": "https://auth.example.com",
		"OIDC_CLIENT_ID":  "frame-ui",
		"RP_ID":           "example.com",
		"RP_ORIGIN":       "https://example.com",
		"NAMESPACE":       "cluster-control",
		"TOKEN_TTL":       "15m",
	} {
		t.Setenv(k, v)
	}
}

func TestUserVerificationDefaultsToLog(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("WEBAUTHN_USER_VERIFICATION", "")
	cfg, err := loadEnvConfig()
	if err != nil {
		t.Fatalf("loadEnvConfig: %v", err)
	}
	if cfg.userVerification != authd.UserVerificationLog {
		t.Fatalf("userVerification = %q, want %q", cfg.userVerification, authd.UserVerificationLog)
	}
}

func TestUserVerificationCanBeRequired(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("WEBAUTHN_USER_VERIFICATION", "required")
	cfg, err := loadEnvConfig()
	if err != nil {
		t.Fatalf("loadEnvConfig: %v", err)
	}
	if cfg.userVerification != authd.UserVerificationRequired {
		t.Fatalf("userVerification = %q, want %q", cfg.userVerification, authd.UserVerificationRequired)
	}
}

// A misspelt value must stop authd at start-up rather than run it in the
// weaker mode the operator was trying to leave.
func TestUserVerificationRejectsAnUnknownValue(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("WEBAUTHN_USER_VERIFICATION", "Required ")
	if _, err := loadEnvConfig(); err == nil {
		t.Fatal("loadEnvConfig accepted an unknown WEBAUTHN_USER_VERIFICATION")
	}
}
