package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	frameJobCompleted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_framejob_completed_total",
		Help: "Total number of FrameJobs that reached Completed phase.",
	})
	frameJobFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_framejob_failed_total",
		Help: "Total number of FrameJobs that reached Failed phase.",
	})

	talosUpgradeRequested = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_talosupgrade_requested_total",
		Help: "Total number of Talos upgrade gRPC calls submitted.",
	})
	talosUpgradeAlreadyAtVersion = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_talosupgrade_alreadyatversion_total",
		Help: "Total number of Talos upgrade calls that found the node already at target version.",
	})
	talosUpgradeFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_talosupgrade_failed_total",
		Help: "Total number of Talos upgrade gRPC calls that returned an error.",
	})

	talosConfigApplied = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_talosmachineconfig_applied_total",
		Help: "Total number of successful Talos ApplyConfiguration gRPC calls.",
	})
	talosConfigFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_talosmachineconfig_failed_total",
		Help: "Total number of Talos ApplyConfiguration gRPC calls that returned an error.",
	})

	schedulingPolicyApplied = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_schedulingpolicy_applied_total",
		Help: "Total number of successful SchedulingPolicy reconciles.",
	})

	// schedulingPolicyRefused counts the cluster-scoped objects — a
	// PriorityClass or a scheduler Queue — that this controller declined to
	// create, adopt or delete on behalf of a SchedulingPolicy. A non-zero
	// value is not an error rate: it is a count of cluster-scoped objects a
	// namespaced SchedulingPolicy asked for and did not get.
	//
	// One counter for both resources rather than two, because the refusal is
	// one mechanism (claim, refuse, release) applied twice; the reason label
	// already says which resource — PriorityClassNotOwned, QueueReserved and
	// so on. The name lost its priorityclass_ infix when the Queue joined it,
	// which is a rename of a metric that has never been released.
	schedulingPolicyRefused = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "frame_schedulingpolicy_refused_total",
		Help: "Total number of SchedulingPolicy reconciles that refused to act on a cluster-scoped PriorityClass or Queue, by reason.",
	}, []string{"reason"})
)

func init() {
	metrics.Registry.MustRegister(
		frameJobCompleted,
		frameJobFailed,
		talosUpgradeRequested,
		talosUpgradeAlreadyAtVersion,
		talosUpgradeFailed,
		talosConfigApplied,
		talosConfigFailed,
		schedulingPolicyApplied,
		schedulingPolicyRefused,
	)
}
