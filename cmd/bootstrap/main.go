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
// With cluster.mode: join it reinstalls a machine and has it rejoin a
// cluster that still exists, as a k3s agent. That is the controller's job
// whenever the controller can do it; this path is for when it cannot -- the
// machine being reinstalled is the one the controller runs on, or the
// cluster is too degraded to host it. The Ready phase then checks the node
// through the existing cluster's kubeconfig, named in the config, and no
// kubeconfig is produced.
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
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/gothshoot/frame/internal/provision"
)

// bootstrapSSHUser is the account the autoinstall identity section creates
// on every installed machine (internal/provision/autoinstall.go), the same
// one the FrameInstall
// controller authenticates as (frameInstallSSHUser there).
const bootstrapSSHUser = "frame"

// mediaAddr is where this process serves the installer image and seed
// to the BMC. Fixed, not a flag: Config.MediaBaseURL is the address the BMC
// reaches this same listener at.
//
// The two have to name the same port. That used to be asserted here as
// holding "by construction" and nothing checked it -- the construction
// being one const and one hand-written YAML field, which is not
// construction at all. validateMediaBaseURL (config.go) now compares them,
// because a mismatch produces the same twenty-minute silence a wrong host
// does.
const mediaAddr = ":8081"

// defaultPhaseTimeouts and defaultPoll mirror the FrameInstall controller's
// own values (internal/controller/frame/frameinstall_controller.go): the
// same budgets, because a machine does not install any faster from a
// laptop than it does from a pod, and there is no reason for node zero to
// run under a different contract than every node after it.
var defaultPhaseTimeouts = map[provision.Phase]time.Duration{
	provision.PhasePending:       30 * time.Second,
	provision.PhasePreparing:     30 * time.Minute,
	provision.PhaseMediaAttached: 2 * time.Minute,
	provision.PhaseInstalling:    45 * time.Minute,
	provision.PhaseJoining:       5 * time.Minute,
	provision.PhaseReady:         5 * time.Minute,
}

const defaultPoll = 5 * time.Second

// phaseTimeouts returns the budgets this run uses: defaultPhaseTimeouts,
// with Installing replaced by installTimeout when that is set. A fresh map
// every call, never defaultPhaseTimeouts itself -- writing the override
// into the package-level map would make it every later caller's default
// too, including a test's.
func phaseTimeouts(installTimeout time.Duration) map[provision.Phase]time.Duration {
	out := make(map[provision.Phase]time.Duration, len(defaultPhaseTimeouts))
	maps.Copy(out, defaultPhaseTimeouts)
	if installTimeout > 0 {
		out[provision.PhaseInstalling] = installTimeout
	}
	return out
}

// mediaStartGrace is how long this command waits after starting the media
// listener before trusting it came up. A port already in use, or any other
// bind failure, fails inside this window rather than as a twenty-minute
// MediaAttached timeout with nothing in the log to say the BMC was never
// serving anything to fetch in the first place.
const mediaStartGrace = 200 * time.Millisecond

// baseISOURLEnv names the variable that swaps where the base ISO is
// downloaded from -- a closer mirror when releases.ubuntu.com is slow --
// and nothing else. It is read here, not in internal/provision, because
// that package also serves the controller, whose image source must not
// depend on the environment of whichever pod happens to run it.
const baseISOURLEnv = "FRAME_BASE_ISO_URL"

// baseSource returns provision.DefaultBase() with its URL replaced by
// override when override is non-empty. The SHA256 is never replaced: it
// stays the integrity control, so a mirror can only be faster, not
// different.
//
// The file name must match the default's because FetchBase caches under
// filepath.Base(URL): a different name would sidestep a cached, verified
// copy, and would say nothing about whether the mirror serves the same
// release. https is required so a mirror is at least who it claims to be
// while the multi-gigabyte download runs (FetchBase also refuses a redirect
// from https to anything else); the checksum still decides.
func baseSource(override string) (provision.BaseSource, error) {
	src := provision.DefaultBase()
	if override == "" {
		return src, nil
	}
	// The value is never echoed as given: it may carry user info or a
	// query token, and the refusal goes to stderr. Messages show it
	// redacted and without query or fragment, or not at all.
	u, err := url.Parse(override)
	if err != nil {
		return provision.BaseSource{}, fmt.Errorf("%s: not a valid URL", baseISOURLEnv)
	}
	shown := *u
	shown.RawQuery, shown.Fragment, shown.RawFragment = "", "", ""
	if u.User != nil {
		// Credentials would also be printed below and in every FetchBase
		// error. A mirror needing them is not one this variable is for.
		return provision.BaseSource{}, fmt.Errorf("%s %q: must not carry credentials", baseISOURLEnv, shown.Redacted())
	}
	if u.Scheme != "https" || u.Host == "" {
		return provision.BaseSource{}, fmt.Errorf("%s %q: must be an https:// URL", baseISOURLEnv, shown.Redacted())
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return provision.BaseSource{}, fmt.Errorf("%s %q: must not carry a query or fragment", baseISOURLEnv, shown.Redacted())
	}
	if got, want := filepath.Base(override), filepath.Base(src.URL); got != want {
		return provision.BaseSource{}, fmt.Errorf("%s %q: file name %q, want %q (the checksum is pinned to that image)", baseISOURLEnv, shown.Redacted(), got, want)
	}
	src.URL = override
	return src, nil
}

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

	// Validated before anything else starts, like xorriso above: a bad
	// mirror URL must not surface as a failed download half an hour in.
	baseOverride := os.Getenv(baseISOURLEnv)
	base, err := baseSource(baseOverride)
	if err != nil {
		return err
	}
	if baseOverride != "" {
		fmt.Fprintf(os.Stderr, "base ISO from %s: %s\n", baseISOURLEnv, base.URL)
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

	mediaSrv, err := startMediaListener(mediaAddr, provision.MediaHandler(imagesDir, nil))
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mediaSrv.Shutdown(sctx)
	}()

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.BMC.InsecureSkipVerify} //nolint:gosec // operator-configured, matches the controller's identical construction
	bmc := provision.NewRedfishBMC("https://"+cfg.BMC.Address, cfg.BMC.Username, cfg.BMC.Password, tlsCfg)

	images := &provision.LocalImageStore{
		Dir:      imagesDir,
		Base:     base,
		MediaURL: cfg.MediaBaseURL,
	}

	// Nodes and Report are left to execute, which ties them together: for
	// a join, the Ready check needs to know when Joining began.
	deps := provision.Deps{
		BMC:    bmc,
		Images: images,
		SSH:    provision.NewSSHClient(),
	}

	spec := provision.Spec{
		UID:          uid,
		Hostname:     cfg.Hostname,
		Network:      cfg.Network,
		Layout:       cfg.Layout,
		SSHPublicKey: strings.TrimSpace(string(sshPub)),
		Cluster:      cfg.Cluster.ClusterTarget,
	}

	opts := provision.Options{
		BootMode:      cfg.BootMode,
		SSHUser:       bootstrapSSHUser,
		SSHKey:        sshKey,
		NodeAddress:   addressWithoutCIDR(cfg.Network.Address),
		ConfirmSerial: cfg.ConfirmSerial,
		PhaseTimeout:  phaseTimeouts(cfg.InstallTimeout),
		Poll:          defaultPoll,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return execute(ctx, os.Stdout, cfg, deps, spec, opts, provision.Install)
}

// installFunc is provision.Install's signature, so a test can drive
// execute without a BMC.
type installFunc func(context.Context, provision.Deps, provision.Spec, provision.Options) (provision.Result, error)

// execute is everything run() does once the config is loaded and the
// collaborators exist: the join preflight, the Ready checker, Install, and
// finish. Split out so the wiring a join depends on -- preflight before
// Install, the existing cluster reaching the Ready check, the config's
// mode reaching finish -- is tested end to end rather than only read.
//
// The preflight runs here, before install is called, and so before the
// BMC is touched at all: Install's first BMC access is in its Pending
// phase, after this has returned. It is the one step of frame bootstrap
// that contacts anything but the BMC before the disk is wiped, and it
// contacts only the cluster the config says the machine will rejoin.
func execute(
	ctx context.Context, stdout io.Writer, cfg Config,
	deps provision.Deps, spec provision.Spec, opts provision.Options, install installFunc,
) error {
	checker := nodeCheckerFor(cfg)
	if cfg.Cluster.Mode == provision.ClusterJoin {
		before, err := preflightJoin(ctx, stdout, cfg)
		if err != nil {
			return fmt.Errorf("join preflight, before anything was touched: %w", err)
		}
		checker.before = &before
		deps.BeforeMediaAttached = recheckNodePassword(cfg)
	}
	deps.Nodes = checker
	deps.Report = func(p provision.Phase) {
		if p == provision.PhaseJoining {
			checker.joiningSince = time.Now()
		}
		_, _ = fmt.Fprintf(stdout, "frame bootstrap: %s\n", p)
	}

	res, installErr := install(ctx, deps, spec, opts)
	return finish(stdout, cfg.Cluster.Mode, cfg.Out, res, installErr)
}

// finish decides what happens after provision.Install returns, and is a
// function of its own so a test can drive the one ordering that matters
// here without a BMC: the kubeconfig is written BEFORE installErr is looked
// at.
//
// provision.Join returns the kubeconfig in the Joining phase, and two later
// things can still fail the install -- the Ready poll timing out, and
// cleanup failing, which a deliberate ruling made an install failure. In
// both of those the kubeconfig is populated, and returning on installErr
// first threw away the only copy of the new cluster's admin credential:
// the cluster exists, it has a node, and nobody can ever talk to it. A
// failed install whose kubeconfig is on disk can be looked at by hand; one
// whose kubeconfig is gone cannot be.
//
// A join is the other shape: provision.Join installs a k3s agent and
// produces no kubeconfig at all, so none is written or expected, and
// success is Install having reached Ready -- the node Ready in the
// existing cluster, checked through the kubeconfig the config named.
func finish(stdout io.Writer, mode provision.ClusterMode, out string, res provision.Result, installErr error) error {
	if mode == provision.ClusterJoin {
		return finishJoin(stdout, res, installErr)
	}

	var writeErr error
	if len(res.Kubeconfig) > 0 {
		if err := os.WriteFile(out, res.Kubeconfig, 0o600); err != nil {
			writeErr = fmt.Errorf("writing kubeconfig to %s: %w", out, err)
		} else {
			_, _ = fmt.Fprintf(stdout, "frame bootstrap: kubeconfig for the new cluster written to %s\n", out)
		}
	}

	if installErr != nil {
		if res.FailedPhase != "" {
			installErr = fmt.Errorf("install failed in phase %s: %w", res.FailedPhase, installErr)
		}
		if writeErr != nil {
			return fmt.Errorf("%w; additionally, %w", installErr, writeErr)
		}
		return installErr
	}
	if writeErr != nil {
		return writeErr
	}
	if len(res.Kubeconfig) == 0 {
		return fmt.Errorf("install reached %s with no kubeconfig produced; nothing to write to %s", res.Phase, out)
	}

	_, _ = fmt.Fprintf(stdout, "frame bootstrap: node %s is Ready\n", res.NodeName)
	return nil
}

// finishJoin is finish for cluster.mode: join. Nothing is written: the
// existing cluster's kubeconfig is the one the operator already has, and
// Install's Ready phase already proved the node through it. The phase is
// checked as well as the error -- Install returns a nil error only on
// reaching Ready today, and a future path that returned early without one
// must not be reported as a node that rejoined.
func finishJoin(stdout io.Writer, res provision.Result, installErr error) error {
	if installErr != nil {
		if res.FailedPhase != "" {
			return fmt.Errorf("install failed in phase %s: %w", res.FailedPhase, installErr)
		}
		return installErr
	}
	if res.Phase != provision.PhaseReady {
		return fmt.Errorf("install returned in phase %s, not Ready; node %s is not known to have "+
			"rejoined", res.Phase, res.NodeName)
	}
	_, _ = fmt.Fprintf(stdout,
		"frame bootstrap: node %s rejoined the existing cluster and is Ready there; no kubeconfig was "+
			"produced -- keep using the existing cluster's own\n",
		res.NodeName)
	return nil
}

// startMediaListener binds addr and serves handler on it, waiting up to
// mediaStartGrace to confirm the listener is actually still running before
// handing it back. Split out from run() so the race it closes -- a port
// already in use, or any other bind failure, reported immediately instead
// of surfacing as a silent, twenty-minute MediaAttached timeout with
// nothing in the log to say the BMC was never serving anything to fetch in
// the first place -- is a unit a test can drive directly, by pre-binding
// addr itself, rather than only provable in prose.
func startMediaListener(addr string, handler http.Handler) (*http.Server, error) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()
	select {
	case err := <-errs:
		if err != nil {
			return nil, fmt.Errorf("media listener on %s: %w", addr, err)
		}
		return nil, fmt.Errorf("media listener on %s exited immediately", addr)
	case <-time.After(mediaStartGrace):
		return srv, nil
	}
}

// addressWithoutCIDR strips a "/NN" suffix from a CIDR address, the same
// way the FrameInstall controller's own addressWithoutCIDR does
// (internal/controller/frame/frameinstall_controller.go): Options.
// NodeAddress is a bare address the installer dials, not the CIDR notation
// Network.Address carries for the autoinstall network section.
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
// one. frame bootstrap does not -- it runs on a laptop, in no cluster -- so
// the nil-kubeconfig branch of provision.NodeChecker's contract ("check the
// cluster the node joined") falls back to existing instead: the kubeconfig
// a join config names, read and parsed by LoadConfig.
type kubeconfigNodeChecker struct {
	existing []byte

	// before is the existing Node's identity, read by preflightJoin before
	// the machine was touched; nil for an init, where no Node existed.
	//
	// It is what stops the Ready check from believing the OLD node. A
	// rejoin reuses the Node's name, and the object -- kept on purpose, see
	// docs/deployment.md -- still carries the last status the old kubelet
	// posted. Ready=True from the machine as it was before the wipe, read
	// in the seconds before the node controller marks it Unknown, would
	// end the install as a success with nothing having rejoined.
	before *nodeIdentity
	// joiningSince is when Install entered Joining, recorded through
	// Deps.Report. A Ready condition whose heartbeat predates it was
	// posted by a kubelet that existed before k3s was installed, which on a
	// freshly wiped machine means not this one.
	joiningSince time.Time
}

// nodeCheckerFor builds the checker run() hands Install from a loaded
// config. Its own function so the one wire that matters for a join -- the
// existing cluster's kubeconfig, read by LoadConfig, reaching the Ready
// check -- is tested from config to apiserver rather than only read.
func nodeCheckerFor(cfg Config) *kubeconfigNodeChecker {
	return &kubeconfigNodeChecker{existing: cfg.existingKubeconfig}
}

// NodeReady checks name against kubeconfig, or -- when Install passes nil,
// which it does exactly for cluster.mode: join -- against the existing
// cluster's kubeconfig. With neither, it refuses rather than reaching for a
// client that cannot exist here; LoadConfig refuses a join config with no
// kubeconfig, so that branch is a belt against a caller that builds this
// type some other way.
//
// For a rejoin (before set), Ready alone is not enough: see rejoined.
// Every "not yet" there is returned as an error rather than a bare false,
// because Install keeps polling on either and reports the last error when
// its budget runs out -- which is then the reason, not just "never Ready".
func (c *kubeconfigNodeChecker) NodeReady(ctx context.Context, kubeconfig []byte, name string) (bool, error) {
	if kubeconfig == nil {
		kubeconfig = c.existing
	}
	if kubeconfig == nil {
		return false, fmt.Errorf(
			"no kubeconfig to check node %q against: frame bootstrap has no cluster of its own to fall back to", name)
	}
	cs, err := clientsetFor(kubeconfig)
	if err != nil {
		return false, err
	}
	node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if !nodeIsReady(node) {
		return false, nil
	}
	if c.before != nil {
		if err := c.rejoined(node); err != nil {
			return false, err
		}
	}
	return true, nil
}

// rejoined says whether a Ready node is the reinstalled machine rather than
// the old one under the same name. Both boot ID and machine ID must have
// changed -- a reboot alone changes the first, and a freshly installed
// system gets a new /etc/machine-id -- and the Ready heartbeat must be
// newer than the start of Joining.
//
// The heartbeat is stamped by the node's clock and compared with this
// laptop's. A node running behind the laptop makes a real rejoin look
// stale, which fails the install on its Ready timeout -- the safe
// direction. joiningSince is truncated to the second because the heartbeat
// travels as RFC 3339 with whole seconds.
func (c *kubeconfigNodeChecker) rejoined(node *corev1.Node) error {
	info := node.Status.NodeInfo
	if info.BootID == c.before.BootID {
		return fmt.Errorf("node %s still reports the boot ID it had before the reinstall: it is the old node's status, "+
			"not the reinstalled machine", node.Name)
	}
	if info.MachineID == c.before.MachineID {
		return fmt.Errorf("node %s still reports the machine ID it had before the reinstall: this is not a freshly "+
			"installed system", node.Name)
	}
	if c.joiningSince.IsZero() {
		return fmt.Errorf("node %s: no record of when Joining began, so a Ready heartbeat cannot be dated", node.Name)
	}
	ready := readyCondition(node)
	since := c.joiningSince.Truncate(time.Second)
	if ready == nil || ready.LastHeartbeatTime.Time.Before(since) {
		return fmt.Errorf("node %s: its Ready heartbeat predates the start of Joining (%s)",
			node.Name, since.UTC().Format(time.RFC3339))
	}
	return nil
}

// clientsetFor builds a client from a kubeconfig. The parser's own error is
// not wrapped: it can quote the file, and the file holds credentials.
func clientsetFor(kubeconfig []byte) (kubernetes.Interface, error) {
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, errors.New("parsing the target cluster's kubeconfig failed (the parser's message is " +
			"withheld: it can quote the file, and the file holds credentials)")
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building a client for the target cluster: %w", err)
	}
	return cs, nil
}

func readyCondition(node *corev1.Node) *corev1.NodeCondition {
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == corev1.NodeReady {
			return &node.Status.Conditions[i]
		}
	}
	return nil
}

func nodeIsReady(node *corev1.Node) bool {
	c := readyCondition(node)
	return c != nil && c.Status == corev1.ConditionTrue
}
