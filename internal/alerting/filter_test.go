package alerting

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func alertNamed(name, sev, namespace string) *framev1beta1.FrameAlert {
	return &framev1beta1.FrameAlert{
		ObjectMeta: metav1.ObjectMeta{Name: "fa-ab"},
		Spec:       framev1beta1.FrameAlertSpec{Fingerprint: "ab", AlertName: name, Severity: sev, Namespace: namespace},
	}
}

func TestMatches(t *testing.T) {
	meta := framev1beta1.AlertFilter{ExcludeAlertNames: []string{"Watchdog", "InfoInhibitor"}}
	cases := []struct {
		name string
		f    framev1beta1.AlertFilter
		a    *framev1beta1.FrameAlert
		want bool
	}{
		{"empty filter takes everything", framev1beta1.AlertFilter{}, alertNamed("Watchdog", "none", ""), true},
		{"excluded name", meta, alertNamed("Watchdog", "none", ""), false},
		{"other name passes", meta, alertNamed("KubeCPUOvercommit", "warning", ""), true},
		{"severity kept", framev1beta1.AlertFilter{Severities: []string{"critical"}}, alertNamed("X", "critical", ""), true},
		{"severity dropped", framev1beta1.AlertFilter{Severities: []string{"critical"}}, alertNamed("X", "warning", ""), false},
		{"namespace kept", framev1beta1.AlertFilter{Namespaces: []string{"neura"}}, alertNamed("X", "warning", "neura"), true},
		{"namespace dropped", framev1beta1.AlertFilter{Namespaces: []string{"neura"}}, alertNamed("X", "warning", "rook-ceph"), false},
		{"namespace filter drops cluster-wide alerts", framev1beta1.AlertFilter{Namespaces: []string{"neura"}}, alertNamed("X", "warning", ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.f, tc.a); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
