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

// Command provisiond assembles per-machine Debian installer images and
// serves them. It exists because the manager image is
// gcr.io/distroless/static:nonroot, which cannot hold xorriso, and it solves
// two more problems on the way: where a ~700 MB artifact is stored, and how
// a BMC on the management network -- which cannot reach the pod network --
// fetches it.
//
// It listens on two ports, deliberately. The build API (BuildAddr) accepts
// a Spec and writes a 700 MB file; it belongs on a ClusterIP Service, which
// keeps it off the LAN but is reachable by every pod in the cluster -- so
// it is treated as unauthenticated, and the manager zeroes the cluster
// target before POSTing to it (internal/provision/server.go). The media
// listener (MediaAddr) is read-only and carries no secret by construction;
// it is the only one a NodePort exposes to the machine network. One port
// serving both would hand the LAN an endpoint that writes files.
//
// This command touches no Kubernetes API: it has nothing to watch and
// nothing to reconcile, only files to build and serve.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rmocq/frame/internal/provision"
)

type config struct {
	BuildAddr string // the build API -- ClusterIP only, never the LAN (but every pod can reach a ClusterIP)
	MediaAddr string // the media listener -- the one a NodePort exposes
	ImagesDir string
	// MediaURL is the address the BMC, on the management network, reaches
	// the media listener at -- not MediaAddr, which is where this process
	// binds. It has no default: every image BuildHandler builds bakes this
	// address into its own boot arguments as where to fetch its preseed
	// from, so a wrong or missing value is not a degraded start, it is
	// every subsequent install hanging with nothing to read.
	MediaURL string
}

func configFromEnv(get func(string) string) config {
	return config{
		BuildAddr: or(get("BUILD_ADDR"), ":8080"),
		MediaAddr: or(get("MEDIA_ADDR"), ":8081"),
		ImagesDir: or(get("IMAGES_DIR"), "/var/lib/frame/images"),
		MediaURL:  get("MEDIA_URL"),
	}
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// validateMediaURL refuses anything that is not a usable http(s) base URL.
// Checking presence alone only catches a missing value; it says nothing
// about a value that is wrong in a way that only shows up on real
// hardware later -- a typo'd scheme (ftp://, or http:/ missing a slash),
// or a bare hostname with no scheme at all, which url.Parse accepts
// without error and puts entirely into Path, leaving both Scheme and Host
// empty. Every one of those still starts provisiond today and fails only
// once a built image asks the BMC to fetch a preseed from it and finds
// nothing there -- exactly the failure mode this whole task exists to
// close for the boot arguments; this closes the same class of mistake for
// the address they're built from.
func validateMediaURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("MEDIA_URL is not set: every built image bakes this address into its own boot arguments, so provisiond refuses to start rather than build images that fetch their preseed from nowhere")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("MEDIA_URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("MEDIA_URL %q: scheme must be http or https, got %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("MEDIA_URL %q: has no host", raw)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "provisiond:", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := configFromEnv(os.Getenv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := validateMediaURL(cfg.MediaURL); err != nil {
		return err
	}

	// The media mux adds one route MediaHandler itself does not: a
	// readinessProbe target. It is registered as its own exact pattern,
	// alongside MediaHandler's own "GET /iso/{name}", never as a trailing-
	// slash pattern that could absorb anything else -- the same rule
	// MediaHandler follows internally.
	mediaMux := http.NewServeMux()
	mediaMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// nil for now: this task only adds the write route's plumbing to
	// MediaHandler itself. Task 6 builds the shared BeaconStore and hands it
	// to both listeners here.
	mediaMux.Handle("/", provision.MediaHandler(cfg.ImagesDir, nil))

	buildSrv := &http.Server{
		Addr: cfg.BuildAddr,
		// nil for now, same as MediaHandler above: this task only adds the
		// read route's plumbing to BuildHandler itself. Task 6 builds the
		// shared BeaconStore and hands it to both listeners here.
		Handler:           provision.BuildHandler(cfg.ImagesDir, provision.DefaultBase(), cfg.MediaURL, nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	mediaSrv := &http.Server{
		Addr:              cfg.MediaAddr,
		Handler:           mediaMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 2)
	go func() {
		log.Info("build API listening", "addr", cfg.BuildAddr, "dir", cfg.ImagesDir)
		if err := buildSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- fmt.Errorf("build API: %w", err)
			return
		}
		errs <- nil
	}()
	go func() {
		log.Info("media listener listening", "addr", cfg.MediaAddr, "dir", cfg.ImagesDir)
		if err := mediaSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- fmt.Errorf("media listener: %w", err)
			return
		}
		errs <- nil
	}()

	// Exactly two values are ever sent to errs over this function's life,
	// one per goroutine. consumed tracks how many of those two the select
	// below already took, so the drain loop after it reads exactly the
	// remainder -- never blocking forever on a channel nothing more will
	// ever send to.
	var runErr error
	consumed := 0
	select {
	case <-ctx.Done():
	case runErr = <-errs:
		// One server died on its own terms; shut the other down too rather
		// than leaving a half-running process.
		consumed++
	}

	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := errors.Join(buildSrv.Shutdown(sctx), mediaSrv.Shutdown(sctx))

	// Drain the remaining goroutine result(s) so neither leaks or blocks on
	// a full channel; Shutdown above is what makes ListenAndServe return in
	// the ctx.Done() case.
	for ; consumed < 2; consumed++ {
		if err := <-errs; err != nil && runErr == nil {
			runErr = err
		}
	}

	return errors.Join(runErr, shutdownErr)
}
