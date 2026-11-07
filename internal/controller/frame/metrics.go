package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/storage"
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

	// What the agents report of each link, for graphing beside node_exporter's
	// raw counters, which know nothing of Frame's window or of a bond's capacity.
	linkUtilization = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frame_link_utilization_percent",
		Help: "Last minute's utilisation of a link, in its busier direction. Absent when the link gives no speed.",
	}, []string{"machine", "interface"})
	linkBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frame_link_bytes_per_second",
		Help: "Bytes a second a link carried over the last minute.",
	}, []string{"machine", "interface", "direction"})
	linkErrors = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frame_link_errors_per_minute",
		Help: "Errors a minute on a link, over the last five minutes.",
	}, []string{"machine", "interface"})
)

// recordLinkMetrics replaces a machine's series: a link that left, or a report
// that went stale, must not stay on the graph at its last value. Keyed on the
// machine, whose name is all a reconcile has left once it is deleted.
func recordLinkMetrics(machine string, n *framev1beta1.MachineNetwork, now time.Time) {
	match := prometheus.Labels{"machine": machine}
	linkUtilization.DeletePartialMatch(match)
	linkBytes.DeletePartialMatch(match)
	linkErrors.DeletePartialMatch(match)
	if n == nil || n.ObservedAt == nil || now.Sub(n.ObservedAt.Time) > storage.AgentSilentAfter {
		return
	}
	record := func(name string, t *framev1beta1.LinkTraffic) {
		if t == nil {
			return
		}
		if t.UtilizationPercent != nil {
			linkUtilization.WithLabelValues(machine, name).Set(float64(*t.UtilizationPercent))
		}
		linkBytes.WithLabelValues(machine, name, "rx").Set(float64(t.RxBytesPerSecond))
		linkBytes.WithLabelValues(machine, name, "tx").Set(float64(t.TxBytesPerSecond))
		linkErrors.WithLabelValues(machine, name).Set(float64(t.ErrorsPerMinute))
	}
	for i := range n.Interfaces {
		record(n.Interfaces[i].Name, n.Interfaces[i].Traffic)
	}
	for i := range n.Bonds {
		record(n.Bonds[i].Name, n.Bonds[i].Traffic)
	}
}

func init() {
	metrics.Registry.MustRegister(
		frameJobCompleted,
		frameJobFailed,
		schedulingPolicyApplied,
		schedulingPolicyRefused,
		linkUtilization,
		linkBytes,
		linkErrors,
	)
}
