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

// Command release-sync copies signed Hearthrack releases from the release
// registry into FrameRelease objects, and is the only thing that says a release
// is verified (docs/specs/2026-11-24-mise-a-jour-en-un-clic.md section 3.2).
//
// It reads the release artifact (hearthrack-release:<version>) from an OCI
// registry, checks the keyless cosign signature of the manifest and of every
// image it names against the pinned build workflow identity, and only then
// writes the FrameRelease. The updater, which runs under another
// ServiceAccount, reads that word and cannot write it. Nothing here touches a
// workload.
//
// A pass that cannot load the Sigstore trust root verifies nothing, so no
// release is offered: failing to check is never a reason to trust.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/releasesync"
)

// passTimeout bounds one pass over the registry.
const passTimeout = 10 * time.Minute

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(framev1beta1.AddToScheme(scheme))
}

func main() {
	var (
		namespace   string
		repo        string
		githubRepo  string
		trustedRoot string
		interval    time.Duration
		maturation  time.Duration
		once        bool
		probeAddr   string
	)
	flag.StringVar(&namespace, "namespace", "frame-system", "Namespace the FrameRelease objects are written to.")
	flag.StringVar(&repo, "repository", "ghcr.io/plume-labs/hearthrack-release",
		"OCI repository of the release artifacts.")
	flag.StringVar(&githubRepo, "signer-repository", "Plume-Labs/frame",
		"GitHub repository whose build workflow signs releases (the pinned identity).")
	flag.StringVar(&trustedRoot, "trusted-root", "",
		"Sigstore trusted root file; empty fetches the public-good root. Set it where there is no outbound access.")
	flag.DurationVar(&interval, "interval", 15*time.Minute, "Time between two passes over the registry.")
	flag.DurationVar(&maturation, "maturation", 48*time.Hour,
		"How long a release must have been published before it is offered; 0 offers it at once.")
	flag.BoolVar(&once, "once", false, "Run one pass and exit.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"Address the probe endpoint binds to; empty disables it.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("release-sync")

	if interval <= 0 || maturation < 0 {
		log.Info("Invalid timing: interval must be positive and maturation not negative")
		os.Exit(2)
	}
	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Could not create the Kubernetes client")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ready := make(chan struct{})
	// lastPass is when a pass last finished, failed or not: a pass that hangs
	// (a registry or a TUF mirror that never answers) stops it moving, and the
	// liveness probe then restarts the pod.
	var lastPass atomic.Int64
	lastPass.Store(time.Now().UnixNano())
	if probeAddr != "" {
		serveProbes(ctx, probeAddr, ready, func() bool {
			return time.Since(time.Unix(0, lastPass.Load())) < 2*interval+passTimeout
		})
	}

	var readyOnce sync.Once
	registryOpts := []remote.Option{remote.WithAuthFromKeychain(authn.DefaultKeychain)}
	pass := func() {
		defer func() { lastPass.Store(time.Now().UnixNano()) }()
		pctx, cancel := context.WithTimeout(ctx, passTimeout)
		defer cancel()
		// The trust root is read again every pass, so a rotation of the
		// Sigstore keys is picked up without a restart.
		material, err := releasesync.LoadTrustedRoot(trustedRoot)
		if err != nil {
			log.Error(err, "Could not load the Sigstore trust root, nothing is verified this pass")
			return
		}
		s := &releasesync.Syncer{
			Client:    c,
			Namespace: namespace,
			Registry:  &releasesync.OCIRegistry{Repo: repo, Options: registryOpts},
			Verifier: &releasesync.CosignVerifier{
				Repo:       repo,
				GitHubRepo: githubRepo,
				Material:   material,
				Options:    registryOpts,
			},
			Maturation: maturation,
		}
		res, err := s.Sync(pctx)
		if err != nil {
			log.Error(err, "Release sync pass failed")
			return
		}
		readyOnce.Do(func() { close(ready) })
		log.Info("Release sync pass done",
			"created", res.Created, "verified", res.Verified, "waiting", res.Waiting, "rejected", len(res.Rejected))
		for v, why := range res.Rejected {
			log.Info("Release not offered", "version", v, "reason", why)
		}
	}

	pass()
	if once {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		}
	}
}

// serveProbes answers /healthz while passes keep finishing and /readyz once one
// has succeeded.
func serveProbes(ctx context.Context, addr string, ready <-chan struct{}, alive func() bool) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if alive() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-ready:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ctrl.Log.WithName("release-sync").Error(err, "Probe server stopped")
		}
	}()
}
