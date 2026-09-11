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

// Command frame-bootstrap installs node zero: the one machine no operator
// running inside a cluster could ever install, because the cluster it would
// run in does not exist yet.
//
// kubeadm, talosctl and k3sup all have this same shape -- a binary run from
// somewhere else for the first node, after which the tool moves into the
// cluster it just made. This is that binary for Frame. It is also the
// recovery path: on a single-node cluster, the day that cluster dies is a
// day that exists, and there is nothing left running inside it to fix it.
//
// It consumes internal/provision exactly the way the FrameInstall
// controller does (internal/controller/frame/frameinstall_controller.go):
// the same Spec, the same Deps, the same Install call. The only things it
// supplies that the controller does not are the things a Kubernetes object
// stood in for there and cannot here -- a flat config file instead of a
// CRD, a NodeChecker built directly against client-go instead of the
// controller's own manager cache, and a LocalImageStore that calls
// FetchBase and Remaster itself instead of talking to frame-provisiond's
// build API, because there is no cluster to host one.
//
// This command may import Kubernetes; internal/provision may not
// (internal/provision/imports_test.go enforces that). That asymmetry is
// the whole reason the package boundary exists: an operator inside the
// cluster it is creating cannot create it.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/rmocq/frame/internal/provision"
)

// bootstrapSSHUser is the account the preseed creates on every installed
// machine (internal/provision/preseed.go), the same one the FrameInstall
// controller authenticates as (frameInstallSSHUser there).
const bootstrapSSHUser = "frame"

// mediaAddr is where this process serves the installer image and preseed
// to the BMC. Fixed, not a flag: Config.MediaBaseURL is the address the BMC
// reaches this same listener at, and the port the two describe has to
// agree by construction the same way frame-provisiond's MEDIA_ADDR and
// MEDIA_URL do (cmd/provisiond/main.go).
const mediaAddr = ":8081"

// defaultPhaseTimeouts and defaultPoll mirror the FrameInstall controller's
// own values (internal/controller/frame/frameinstall_controller.go): the
// same budgets, because a machine does not install any faster from a
// laptop than it does from a pod, and there is no reason for node zero to
// run under a different contract than every node after it.
var defaultPhaseTimeouts = map[provision.Phase]time.Duration{
	provision.PhasePending:       30 * time.Second,
	provision.PhasePreparing:     10 * time.Minute,
	provision.PhaseMediaAttached: 2 * time.Minute,
	provision.PhaseInstalling:    20 * time.Minute,
	provision.PhaseJoining:       5 * time.Minute,
	provision.PhaseReady:         5 * time.Minute,
}

const defaultPoll = 5 * time.Second

// mediaStartGrace is how long this command waits after starting the media
// listener before trusting it came up. A port already in use, or any other
// bind failure, fails inside this window rather than as a twenty-minute
// MediaAttached timeout with nothing in the log to say the BMC was never
// serving anything to fetch in the first place.
const mediaStartGrace = 200 * time.Millisecond

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "frame bootstrap:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to the bootstrap config file")
	flag.Parse()

	// Checked before the config is even read: a missing tool must fail in
	// the first second, not twenty minutes into an install that was always
	// going to fail at the Remaster step.
	if _, err := exec.LookPath("xorriso"); err != nil {
		return errors.New("frame bootstrap needs xorriso (apt install xorriso)")
	}

	if strings.TrimSpace(*configPath) == "" {
		return errors.New("-config is required")
	}
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		return err
	}

	sshKey, err := os.ReadFile(cfg.SSHKeyPath)
	if err != nil {
		return fmt.Errorf("reading SSH private key %s: %w", cfg.SSHKeyPath, err)
	}
	sshPub, err := os.ReadFile(cfg.SSHKeyPath + ".pub")
	if err != nil {
		return fmt.Errorf("reading SSH public key %s.pub: %w", cfg.SSHKeyPath, err)
	}

	uid, err := newInstallUID()
	if err != nil {
		return err
	}

	imagesDir, err := os.MkdirTemp("", "frame-bootstrap-images-")
	if err != nil {
		return fmt.Errorf("creating a scratch directory for installer images: %w", err)
	}
	defer func() { _ = os.RemoveAll(imagesDir) }()

	mediaSrv := &http.Server{
		Addr:              mediaAddr,
		Handler:           provision.MediaHandler(imagesDir),
		ReadHeaderTimeout: 10 * time.Second,
	}
	mediaErrs := make(chan error, 1)
	go func() {
		if err := mediaSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			mediaErrs <- err
			return
		}
		mediaErrs <- nil
	}()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mediaSrv.Shutdown(sctx)
	}()
	select {
	case err := <-mediaErrs:
		if err != nil {
			return fmt.Errorf("media listener on %s: %w", mediaAddr, err)
		}
		return fmt.Errorf("media listener on %s exited immediately", mediaAddr)
	case <-time.After(mediaStartGrace):
		// Still running after the grace window; proceed.
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.BMC.InsecureSkipVerify} //nolint:gosec // operator-configured, matches the controller's identical construction
	bmc := provision.NewRedfishBMC("https://"+cfg.BMC.Address, cfg.BMC.Username, cfg.BMC.Password, tlsCfg)

	images := &provision.LocalImageStore{
		Dir:      imagesDir,
		Base:     provision.DefaultBase(),
		MediaURL: cfg.MediaBaseURL,
	}

	deps := provision.Deps{
		BMC:    bmc,
		Images: images,
		SSH:    provision.NewSSHClient(),
		Nodes:  kubeconfigNodeChecker{},
		Report: func(p provision.Phase) { fmt.Fprintf(os.Stdout, "frame bootstrap: %s\n", p) },
	}

	spec := provision.Spec{
		UID:          uid,
		Hostname:     cfg.Hostname,
		Network:      cfg.Network,
		Layout:       cfg.Layout,
		SSHPublicKey: strings.TrimSpace(string(sshPub)),
		Cluster:      cfg.Cluster,
	}

	opts := provision.Options{
		BootMode:      cfg.BootMode,
		SSHUser:       bootstrapSSHUser,
		SSHKey:        sshKey,
		NodeAddress:   addressWithoutCIDR(cfg.Network.Address),
		ConfirmSerial: cfg.ConfirmSerial,
		PhaseTimeout:  defaultPhaseTimeouts,
		Poll:          defaultPoll,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, installErr := provision.Install(ctx, deps, spec, opts)
	if installErr != nil {
		if res.FailedPhase != "" {
			return fmt.Errorf("install failed in phase %s: %w", res.FailedPhase, installErr)
		}
		return installErr
	}

	if len(res.Kubeconfig) == 0 {
		return fmt.Errorf("install reached %s with no kubeconfig produced; nothing to write to %s", res.Phase, cfg.Out)
	}
	if err := os.WriteFile(cfg.Out, res.Kubeconfig, 0o600); err != nil {
		return fmt.Errorf("writing kubeconfig to %s: %w", cfg.Out, err)
	}

	fmt.Printf("frame bootstrap: node %s is Ready; kubeconfig written to %s\n", res.NodeName, cfg.Out)
	return nil
}

// addressWithoutCIDR strips a "/NN" suffix from a CIDR address, the same
// way the FrameInstall controller's own addressWithoutCIDR does
// (internal/controller/frame/frameinstall_controller.go): Options.
// NodeAddress is a bare address the installer dials, not the CIDR notation
// Network.Address carries for the preseed's netcfg directives.
func addressWithoutCIDR(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

// newInstallUID generates the value Spec.UID carries: proof, checked over
// SSH by provision.WaitForOurSystem, that the machine answering at
// NodeAddress is the one this exact call installed and not merely
// something already listening at that address. 16 random bytes, hex
// encoded -- the same shape server.go's newToken uses for an image token,
// for the same reason: unguessable and cheap to compare.
func newInstallUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating install UID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// kubeconfigNodeChecker is the provision.NodeChecker frame bootstrap
// supplies. It is deliberately narrower than the FrameInstall controller's
// own ClusterNodeChecker (internal/controller/frame/
// frameinstall_controller.go): that type falls back to Frame's own
// in-cluster client when kubeconfig is nil, because a controller always has
// one. frame bootstrap does not -- there is no cluster Frame runs in, which
// is the entire reason this command exists -- so the nil-kubeconfig branch
// of provision.NodeChecker's documented contract has nothing to fall back
// to and is refused outright rather than reaching for a client that cannot
// exist yet.
type kubeconfigNodeChecker struct{}

func (kubeconfigNodeChecker) NodeReady(ctx context.Context, kubeconfig []byte, name string) (bool, error) {
	if kubeconfig == nil {
		return false, fmt.Errorf(
			"no kubeconfig to check node %q against: frame bootstrap has no cluster of its own to fall back to", name)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return false, fmt.Errorf("parsing the new cluster's kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return false, fmt.Errorf("building a client for the new cluster: %w", err)
	}
	node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	return nodeIsReady(node), nil
}

func nodeIsReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
