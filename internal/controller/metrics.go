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
	)
}
