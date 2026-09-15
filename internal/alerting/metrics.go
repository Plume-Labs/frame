package alerting

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	receiverRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "frame_alert_receiver_requests_total",
		Help: "Alertmanager webhook requests, by HTTP answer.",
	}, []string{"code"})
	alertsReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "frame_alerts_received_total",
		Help: "Alerts received from Alertmanager, by state.",
	}, []string{"state"})
	deliveries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "frame_alert_deliveries_total",
		Help: "Relay attempts to subscriptions, by result (delivered, retry, permanent).",
	}, []string{"subscription", "result"})
	pendingDeliveries = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frame_alert_pending_deliveries",
		Help: "Alerts whose current state is not yet delivered to the subscription.",
	}, []string{"subscription"})
)

func init() {
	metrics.Registry.MustRegister(receiverRequests, alertsReceived, deliveries, pendingDeliveries)
}
