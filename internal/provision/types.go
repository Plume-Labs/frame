// Package provision installs an operating system on a machine and joins it to
// a cluster. It imports nothing from Kubernetes: its two consumers are a
// controller, which runs inside a cluster, and a command, which runs when no
// cluster exists yet. The interfaces below are what those consumers implement.
package provision

type Spec struct {
	UID          string // proves this system came out of this image
	Hostname     string
	Network      Network
	Layout       Layout
	SSHPublicKey string // an authorized_keys line; never a private key
	Cluster      ClusterTarget
}

type Network struct {
	Address string // CIDR, e.g. "192.168.2.210/24"
	Gateway string
	DNS     []string
	VLAN    int
	Bond    string
}

type LayoutKind string

const (
	LayoutSingleDisk LayoutKind = "single-disk"
	LayoutMirror     LayoutKind = "mirror"
	LayoutRaw        LayoutKind = "raw"
)

type Layout struct {
	Kind  LayoutKind
	Disks []Disk
	Raw   string // a partman recipe; only when Kind == LayoutRaw
}

type Disk struct {
	ByID      string // "/dev/disk/by-id/..."
	SizeBytes int64  // asserted on the machine before partitioning
}

type ClusterMode string

const (
	ClusterInit ClusterMode = "init"
	ClusterJoin ClusterMode = "join"
)

type ClusterTarget struct {
	Mode       ClusterMode
	ServerURL  string // only for ClusterJoin
	JoinToken  string // only for ClusterJoin; never enters the image
	K3sVersion string
}
