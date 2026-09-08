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

// Command uiproxy authenticates the Frame UI's requests to the Kubernetes
// apiserver: it validates authd's token, impersonates the person it names,
// and records what they did. It replaces the `kubectl proxy` sidecar, which
// authenticated every request as the pod ServiceAccount.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/uiproxy"
)

type config struct {
	Listen        string
	JWKSURL       string
	JWKSCAFile    string
	Issuer        string
	ClientID      string
	GroupPrefix   string
	TaskNamespace string
	Retention     time.Duration
}

// jwksCASystem is the one value of JWKS_CA_FILE that is not a path: it says
// "authd's certificate chains to a public root, use the system pool".
//
// The variable is required rather than optional, and this is why. authd's
// serving certificate is issued by the in-cluster `frame-auth-ca` Issuer,
// which nothing public chains to, and the uiproxy image is
// distroless/static. Left to the system pool by default, every JWKS fetch
// fails `x509: certificate signed by unknown authority`, Verify errors, and
// the proxy answers 401 to every request in the cluster — the console is
// down and nothing in the failure names TLS trust. Requiring the variable
// turns that into a container that refuses to start with a message saying
// what is missing, and makes "public roots are fine here" a decision
// somebody wrote down.
const jwksCASystem = "system"

func configFromEnv(get func(string) string) (config, error) {
	c := config{
		Listen:        or(get("LISTEN_ADDR"), "127.0.0.1:8001"),
		JWKSURL:       get("JWKS_URL"),
		JWKSCAFile:    get("JWKS_CA_FILE"),
		Issuer:        get("OIDC_ISSUER_URL"),
		ClientID:      get("OIDC_CLIENT_ID"),
		GroupPrefix:   or(get("GROUP_PREFIX"), "frame:"),
		TaskNamespace: or(get("TASK_NAMESPACE"), "frame-system"),
		Retention:     7 * 24 * time.Hour,
	}
	if v := get("TASK_RETENTION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return config{}, fmt.Errorf("TASK_RETENTION: %w", err)
		}
		c.Retention = d
	}
	for k, v := range map[string]string{
		"JWKS_URL":        c.JWKSURL,
		"JWKS_CA_FILE":    c.JWKSCAFile,
		"OIDC_ISSUER_URL": c.Issuer,
		"OIDC_CLIENT_ID":  c.ClientID,
	} {
		if v == "" {
			return config{}, fmt.Errorf("%s is required", k)
		}
	}
	return c, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "uiproxy:", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	// The apiserver's own address and the SA credential, both from the
	// in-cluster config — the same pair kubectl proxy used.
	upstream, err := url.Parse(restCfg.Host)
	if err != nil {
		return err
	}
	transport, err := rest.TransportFor(restCfg)
	if err != nil {
		return err
	}
	if err := framev1beta1.AddToScheme(scheme.Scheme); err != nil {
		return err
	}
	c, err := client.New(restCfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return err
	}
	rec := uiproxy.NewRecorder(c, cfg.TaskNamespace, log)

	// nil means "system roots", which is wrong everywhere authd actually
	// runs — see jwksCASystem.
	var jwksClient *http.Client
	if cfg.JWKSCAFile != jwksCASystem {
		if jwksClient, err = uiproxy.HTTPClientWithCA(cfg.JWKSCAFile); err != nil {
			return err
		}
	}

	p, err := uiproxy.New(uiproxy.Options{
		Verifier:    uiproxy.NewJWKSVerifier(cfg.JWKSURL, cfg.Issuer, cfg.ClientID, jwksClient),
		Recorder:    rec,
		Upstream:    upstream,
		Transport:   transport,
		GroupPrefix: cfg.GroupPrefix,
		Log:         log,
	})
	if err != nil {
		return err
	}

	ctx := ctrl.SetupSignalHandler()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if err := rec.Purge(ctx, cfg.Retention); err != nil {
				log.Error("purge failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	srv := &http.Server{Addr: cfg.Listen, Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("listening", "addr", cfg.Listen, "issuer", cfg.Issuer)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
