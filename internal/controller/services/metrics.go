package services

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	frameServiceReady = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_frameservice_ready_total",
		Help: "Total number of FrameService reconciles that reached Ready.",
	})
	frameServiceProvisionFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_frameservice_provision_failed_total",
		Help: "Total number of FrameService provisioning attempts that returned an error.",
	})
	frameServiceUnknownType = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "frame_frameservice_unknown_type_total",
		Help: "Total number of FrameService reconciles for a type no provider answers to.",
	})
)

func init() {
	metrics.Registry.MustRegister(
		frameServiceReady, frameServiceProvisionFailed, frameServiceUnknownType)
}
