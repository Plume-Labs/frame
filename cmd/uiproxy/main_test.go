package main

import "testing"

func TestConfigFromEnvRejectsAMissingIssuer(t *testing.T) {
	_, err := configFromEnv(func(k string) string {
		switch k {
		case "JWKS_URL":
			return "https://authd/keys"
		case "OIDC_CLIENT_ID":
			return "frame-ui"
		}
		return ""
	})
	if err == nil {
		t.Fatal("accepted a configuration with no issuer — every token would then be trusted by name only")
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	cfg, err := configFromEnv(func(k string) string {
		switch k {
		case "JWKS_URL":
			return "https://authd/keys"
		case "OIDC_ISSUER_URL":
			return "https://authd"
		case "OIDC_CLIENT_ID":
			return "frame-ui"
		case "JWKS_CA_FILE":
			return "/etc/frame-auth-ca/ca.crt"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:8001" {
		t.Fatalf("Listen = %q", cfg.Listen)
	}
	if cfg.GroupPrefix != "frame:" {
		t.Fatalf("GroupPrefix = %q", cfg.GroupPrefix)
	}
	if cfg.TaskNamespace != "frame-system" {
		t.Fatalf("TaskNamespace = %q", cfg.TaskNamespace)
	}
	if cfg.Retention.Hours() != 168 {
		t.Fatalf("Retention = %v", cfg.Retention)
	}
}

// C2 of the whole-branch review: main passed a nil http.Client, so the
// verifier fetched authd's JWKS with the system root pool from a
// distroless/static image. Every fetch failed `x509: certificate signed by
// unknown authority` and the proxy 401'd every request in the cluster. The
// variable is required so that a deployment which forgets it fails at
// container start with a message, rather than by refusing everyone.
func TestConfigFromEnvRequiresAJWKSCA(t *testing.T) {
	_, err := configFromEnv(func(k string) string {
		switch k {
		case "JWKS_URL":
			return "https://authd/keys"
		case "OIDC_ISSUER_URL":
			return "https://authd"
		case "OIDC_CLIENT_ID":
			return "frame-ui"
		}
		return ""
	})
	if err == nil {
		t.Fatal("accepted a configuration with no JWKS CA — every JWKS fetch would fail on an unknown authority and the proxy would 401 everything")
	}
}
