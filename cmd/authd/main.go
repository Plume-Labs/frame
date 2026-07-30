/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command authd serves the Cluster Control UI's authentication surface:
// WebAuthn and password login, session cookies, the OIDC discovery/JWKS
// documents the apiserver will verify tokens against, and the one-shot
// bootstrap that creates the first admin. See
// docs/superpowers/plans/2026-07-30-frame-authd-stage1.md for the design.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
	"github.com/rmocq/frame/internal/authd"
)

func main() {
	if err := run(); err != nil {
		// fmt.Fprintln, not the structured logger: run() has not necessarily
		// set anything up yet, and the point here is a message an operator
		// reads directly off `kubectl logs`, never a value that could
		// accidentally carry key material (nothing loaded here is ever
		// interpolated into these error strings — see loadSigningKey/
		// loadHMACKey/loadBootstrapToken, which wrap failures with context
		// but never the secret bytes themselves).
		fmt.Fprintln(os.Stderr, "authd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadEnvConfig()
	if err != nil {
		return err
	}

	signingKey, err := loadSigningKey(filepath.Join(cfg.keysDir, "signing.pem"))
	if err != nil {
		return fmt.Errorf("loading signing key: %w", err)
	}
	hmacKey, err := loadHMACKey(filepath.Join(cfg.keysDir, "challenge-hmac"))
	if err != nil {
		return fmt.Errorf("loading challenge key: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(cfg.tlsCertFile, cfg.tlsKeyFile)
	if err != nil {
		return fmt.Errorf("loading TLS certificate: %w", err)
	}

	scheme := clientgoscheme.Scheme
	if err := framev1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("registering scheme: %w", err)
	}
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	kc, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("building Kubernetes client: %w", err)
	}

	bootstrapToken, err := loadBootstrapToken(context.Background(), kc, cfg.bootstrapSecretName, cfg.namespace)
	if err != nil {
		return fmt.Errorf("loading bootstrap secret: %w", err)
	}

	store := authd.NewStore(kc, cfg.namespace)
	codec := authd.NewChallengeCodec(hmacKey)
	auth, err := authd.NewAuthenticator(cfg.rpID, cfg.rpOrigin, store, codec)
	if err != nil {
		return fmt.Errorf("configuring webauthn: %w", err)
	}
	issuer, err := authd.NewIssuer(cfg.issuerURL, cfg.clientID, signingKey)
	if err != nil {
		return fmt.Errorf("configuring issuer: %w", err)
	}

	srv, err := authd.NewServer(authd.ServerConfig{
		Store:               store,
		Auth:                auth,
		Issuer:              issuer,
		Codec:               codec,
		BootstrapSecret:     bootstrapToken,
		BootstrapSecretName: cfg.bootstrapSecretName,
		Client:              kc,
		Namespace:           cfg.namespace,
		TokenTTL:            cfg.tokenTTL,
	})
	if err != nil {
		return fmt.Errorf("building server: %w", err)
	}

	httpSrv := &http.Server{
		Addr:      ":8443",
		Handler:   srv,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	slog.Info("authd starting", "addr", httpSrv.Addr, "issuer", cfg.issuerURL, "namespace", cfg.namespace)
	return httpSrv.ListenAndServeTLS("", "")
}

// envConfig holds everything read from the environment. Kept as one struct so
// run() has a single place to check for load errors before touching the
// filesystem or the Kubernetes API.
type envConfig struct {
	issuerURL           string
	clientID            string
	rpID                string
	rpOrigin            string
	namespace           string
	tokenTTL            time.Duration
	keysDir             string
	tlsCertFile         string
	tlsKeyFile          string
	bootstrapSecretName string
}

func loadEnvConfig() (envConfig, error) {
	var cfg envConfig
	var err error
	for _, f := range []struct {
		key string
		set *string
	}{
		{"OIDC_ISSUER_URL", &cfg.issuerURL},
		{"OIDC_CLIENT_ID", &cfg.clientID},
		{"RP_ID", &cfg.rpID},
		{"RP_ORIGIN", &cfg.rpOrigin},
		{"NAMESPACE", &cfg.namespace},
	} {
		*f.set, err = requiredEnv(f.key)
		if err != nil {
			return envConfig{}, err
		}
	}

	ttlRaw, err := requiredEnv("TOKEN_TTL")
	if err != nil {
		return envConfig{}, err
	}
	cfg.tokenTTL, err = time.ParseDuration(ttlRaw)
	if err != nil || cfg.tokenTTL <= 0 {
		return envConfig{}, fmt.Errorf("TOKEN_TTL: invalid duration %q", ttlRaw)
	}

	cfg.keysDir = envOrDefault("KEYS_DIR", "/etc/frame-auth-keys")
	cfg.tlsCertFile = envOrDefault("TLS_CERT_FILE", "/etc/frame-auth-tls/tls.crt")
	cfg.tlsKeyFile = envOrDefault("TLS_KEY_FILE", "/etc/frame-auth-tls/tls.key")
	cfg.bootstrapSecretName = envOrDefault("BOOTSTRAP_SECRET_NAME", "frame-auth-bootstrap")
	return cfg, nil
}

func requiredEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadSigningKey reads the ES256 private key authd mints ID tokens with. It
// never returns a zero-value key: every error path here returns early, so
// run() always fails fast instead of starting a server that would sign
// tokens with a nil or empty key.
func loadSigningKey(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s does not contain a PEM block", path)
	}

	// openssl ecparam -genkey produces SEC1 ("EC PRIVATE KEY"); accept PKCS8
	// too so the key can also come from `openssl genpkey`.
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s is not a recognized EC private key", path)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s does not hold an ECDSA private key", path)
	}
	return key, nil
}

// loadHMACKey reads the challenge-signing key, stored hex-encoded (it is
// created with `openssl rand -hex 32`; see the plan's Task 9).
func loadHMACKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("%s is not valid hex", path)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	return key, nil
}

// loadBootstrapToken reads the one-shot bootstrap token straight from the
// Kubernetes API rather than a mounted file: authd's own RBAC (Task 9) grants
// get+delete on this one Secret by name, which only makes sense if authd
// fetches it itself.
//
// A missing Secret is not an error: it is the expected, permanent state once
// bootstrap has already happened (handleBootstrap deletes it on success), and
// authd must keep serving logins after that — it must not crash-loop on every
// restart of an already-bootstrapped cluster. In that case authd starts with
// an empty token, which can never match a caller-supplied one, so
// /auth/bootstrap simply stays refused (on top of the AdminCount guard, which
// is already closed by then anyway).
func loadBootstrapToken(ctx context.Context, kc client.Client, name, namespace string) (string, error) {
	var secret corev1.Secret
	err := kc.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &secret)
	switch {
	case apierrors.IsNotFound(err):
		slog.Info("bootstrap secret not found; assuming this cluster is already bootstrapped", "secret", name)
		return "", nil
	case err != nil:
		return "", fmt.Errorf("fetching secret %s/%s: %w", namespace, name, err)
	}
	token := strings.TrimSpace(string(secret.Data["token"]))
	if token == "" {
		return "", fmt.Errorf("secret %s/%s has no \"token\" key", namespace, name)
	}
	return token, nil
}
