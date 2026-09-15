package alerting

import (
	"slices"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Matches applies a subscription filter. A namespace filter drops alerts
// that carry no namespace: a tenant scoped to its namespaces must not see
// cluster-wide infrastructure alerts.
func Matches(f framev1beta1.AlertFilter, a *framev1beta1.FrameAlert) bool {
	if slices.Contains(f.ExcludeAlertNames, a.Spec.AlertName) {
		return false
	}
	if len(f.Severities) > 0 && !slices.Contains(f.Severities, a.Spec.Severity) {
		return false
	}
	if len(f.Namespaces) > 0 && !slices.Contains(f.Namespaces, a.Spec.Namespace) {
		return false
	}
	return true
}
