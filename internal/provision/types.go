// Package provision installs an operating system on a machine and joins it to
// a cluster. It imports nothing from Kubernetes: its two consumers are a
// controller, which runs inside a cluster, and a command, which runs when no
// cluster exists yet. The interfaces below are what those consumers implement.
package provision

import (
	"context"
	"time"
)

type Spec struct {
	UID          string // proves this system came out of this image
	Hostname     string
	Network      Network
	Layout       Layout
	SSHPublicKey string // an authorized_keys line; never a private key
	Cluster      ClusterTarget
}

// Network, Layout, Disk and ClusterTarget carry yaml tags even though this
// package has no YAML consumer of its own (Spec travels as JSON through
// server.go's build API, which ignores them). cmd/bootstrap's Config embeds
// these types directly, and its config file is the one file in the whole
// lot a human hand-writes -- with no struct tags, the keys they would have
// to type are the lowercased, unseparated Go field names (serverurl,
// jointoken, k3sversion), discoverable only by reading this source file.
// The moment they write that file is the moment a cluster is gone and
// they are rebuilding node zero from a laptop; guessing key names then is
// the worst possible time. Chosen to match Config's own tags (config.go):
// lowerCamelCase, acronyms kept upper (byID, sizeBytes, serverURL).
type Network struct {
	Address string   `yaml:"address"` // CIDR, e.g. "192.168.2.210/24"
	Gateway string   `yaml:"gateway"`
	DNS     []string `yaml:"dns"`
}

type LayoutKind string

const (
	LayoutSingleDisk LayoutKind = "single-disk"
	LayoutMirror     LayoutKind = "mirror"
	LayoutRaw        LayoutKind = "raw"
)

type Layout struct {
	Kind  LayoutKind `yaml:"kind"`
	Disks []Disk     `yaml:"disks"`
	Raw   string     `yaml:"raw"` // a partman recipe; only when Kind == LayoutRaw
}

type Disk struct {
	ByID      string `yaml:"byID"`      // "/dev/disk/by-id/..."
	SizeBytes int64  `yaml:"sizeBytes"` // asserted on the machine before partitioning
}

type ClusterMode string

const (
	ClusterInit ClusterMode = "init"
	ClusterJoin ClusterMode = "join"
)

type ClusterTarget struct {
	Mode       ClusterMode `yaml:"mode"`
	ServerURL  string      `yaml:"serverURL"` // only for ClusterJoin
	JoinToken  string      `yaml:"joinToken"` // only for ClusterJoin; never enters the image
	K3sVersion string      `yaml:"k3sVersion"`
}

// Phase is where an installation stands in its lifecycle. Every transition is
// tied to a signal that can be read, not inferred from elapsed time -- except
// by a phase running out of its own budget, which is itself a read signal.
type Phase string

const (
	PhasePending       Phase = "Pending"
	PhasePreparing     Phase = "Preparing"
	PhaseMediaAttached Phase = "MediaAttached"
	PhaseInstalling    Phase = "Installing"
	PhaseInstalled     Phase = "Installed"
	PhaseJoining       Phase = "Joining"
	PhaseReady         Phase = "Ready"
	PhaseFailed        Phase = "Failed"
)

// BMC is the out-of-band interface Install drives: attaching installer
// media, setting a one-time boot override, and power-cycling the machine.
type BMC interface {
	Serial(ctx context.Context) (string, error)
	InsertMedia(ctx context.Context, url string) error
	MediaInserted(ctx context.Context) (bool, error)
	EjectMedia(ctx context.Context) error
	SetBootOnce(ctx context.Context, target, mode string) error // "Cd", "UEFI"|"Legacy"
	ClearBootOverride(ctx context.Context) error
	Reset(ctx context.Context, resetType string) error
}

// ImageStore builds the per-machine installer image Install attaches, and
// removes it once the machine no longer needs it.
type ImageStore interface {
	Build(ctx context.Context, s Spec) (url, token string, err error)
	Remove(ctx context.Context, token string) error
}

// NodeChecker answers whether the named node is Ready in the target cluster
// -- which, when Spec.Cluster.Mode is ClusterInit, is not the cluster Frame
// itself runs in.
//
// A nil kubeconfig means: check the cluster Frame itself is running in,
// rather than the one this installation just started. That is the case for
// Spec.Cluster.Mode == ClusterJoin, where the joined node becomes part of an
// existing cluster and Join produces no kubeconfig of its own for it.
type NodeChecker interface {
	NodeReady(ctx context.Context, kubeconfig []byte, name string) (bool, error)
}

// Deps are the collaborators Install drives.
type Deps struct {
	BMC    BMC
	Images ImageStore
	SSH    SSHClient
	Nodes  NodeChecker
	Report func(Phase) // called on entering each phase; may be nil
}

// Options configures one Install call.
type Options struct {
	BootMode      string // "UEFI" or "Legacy"; never inherited from the machine
	SSHUser       string // "frame"
	SSHKey        []byte // the private half; never enters an image
	NodeAddress   string // the address the installed system answers on
	ConfirmSerial string // must equal BMC.Serial(); the destructive guard
	PhaseTimeout  map[Phase]time.Duration
	Poll          time.Duration
}

// Result is what Install produces, win or lose: the phase it ended in, and
// -- on failure -- the phase it failed in, so the caller can say where
// without re-deriving it from the returned error.
type Result struct {
	Phase       Phase
	FailedPhase Phase
	HostKey     string
	Kubeconfig  []byte
	NodeName    string
}
