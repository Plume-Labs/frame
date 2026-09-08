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
