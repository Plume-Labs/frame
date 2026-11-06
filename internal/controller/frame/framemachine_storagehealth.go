package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/alerting"
	"github.com/Plume-Labs/frame/internal/storage"
)

// Labels on the FrameAlerts assessStorage raises. An alert's identity lives in
// them, not in this controller: what is open is listed from the FrameAlerts on
// each pass, so a restart loses nothing. No "namespace" label: this is
// infrastructure, and a subscription scoped to namespaces must not receive it.
const (
	storageAlertSource = "frame-storage-health"

	labelAlertName        = "alertname"
	labelSeverity         = "severity"
	labelSource           = "source"
	labelMachine          = "machine"
	labelMachineNamespace = "machine_namespace"
	labelNode             = "node"
	labelDevice           = "device"
	labelStack            = "stack"

	annotationSummary = "summary"

	// assessTimeout bounds one assessment, so a stuck read costs this pass's
	// storage verdict and not the probe's status write.
	assessTimeout = 20 * time.Second
)

// assessStorage writes the storage conditions onto fm, for the caller's status
// patch, and raises and resolves the machine's alerts. It runs on every pass
// including the ones the BMC did not answer: the node agent's reading does not
// depend on the BMC. Conditions are written here rather than by a controller
// of their own because a merge patch replaces status.conditions whole, so two
// writers would drop each other's.
//
// Failing to read the policy or the open alerts changes nothing: without them
// no conclusion is safe, and the next pass tries again. It never fails the
// probe's own status write: the policy goes through PolicyReader, because the
// cached client blocks on a kind it may not list instead of answering
// Forbidden, and nothing here outlives assessTimeout.
//
// ponytail: the pass is the probe's, so a BMC that stays down stretches it to
// the backoff (30 min at most, framemachine_sessions.go) and an alert with it.
// Split the assessment off the probe's timer if that delay ever matters.
func (r *FrameMachineReconciler) assessStorage(ctx context.Context, fm *framev1beta1.FrameMachine, inventoryFresh bool) {
	ctx, cancel := context.WithTimeout(ctx, assessTimeout)
	defer cancel()
	log := logf.FromContext(ctx)
	key := client.ObjectKeyFromObject(fm)

	reader := r.PolicyReader
	if reader == nil {
		reader = r.Client
	}
	var policy *framev1beta1.StorageHealthPolicy
	var p framev1beta1.StorageHealthPolicy
	switch err := reader.Get(ctx, types.NamespacedName{Name: framev1beta1.StorageHealthPolicyName}, &p); {
	case err == nil:
		policy = &p
	case !apierrors.IsNotFound(err):
		log.Error(err, "Reading the StorageHealthPolicy; storage left as it was", "machine", key)
		return
	}
	alerts, err := r.openStorageAlerts(ctx, key)
	if err != nil {
		log.Error(err, "Listing open storage alerts; storage left as it was", "machine", key)
		return
	}
	open := make(storage.Open, len(alerts))
	for k, fa := range alerts {
		open[k] = storage.Scope(fa.Spec.Labels[labelStack])
	}

	now := time.Now()
	a := storage.Assess(&fm.Status, storage.ThresholdsFrom(policy), inventoryFresh, open, now)
	if err := r.writeStorageAlerts(ctx, fm, &a, alerts); err != nil {
		log.Error(err, "Writing storage alerts; next pass tries again", "machine", key)
	}
	for _, check := range storage.Checks {
		v := a.Verdict(check, open)
		meta.SetStatusCondition(&fm.Status.Conditions, metav1.Condition{
			Type:               check,
			Status:             metav1.ConditionStatus(v.Status),
			Reason:             v.Reason,
			Message:            v.Message,
			ObservedGeneration: fm.Generation,
		})
	}
}

// openStorageAlerts lists what fires for this machine, and closes on the way
// what fires for a machine that no longer exists: a deletion missed while the
// manager was down has no other pass to catch it.
//
// Anything not Resolved counts as open, so a write that created an alert and
// failed before its state is picked up here instead of left stateless forever.
func (r *FrameMachineReconciler) openStorageAlerts(ctx context.Context, machine types.NamespacedName) (map[storage.Key]*framev1beta1.FrameAlert, error) {
	open := map[storage.Key]*framev1beta1.FrameAlert{}
	if r.Alerts == nil {
		return open, nil
	}
	var list framev1beta1.FrameAlertList
	if err := r.List(ctx, &list, client.InNamespace(r.Alerts.Namespace)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		fa := &list.Items[i]
		l := fa.Spec.Labels
		if l[labelSource] != storageAlertSource || fa.Status.State == framev1beta1.AlertStateResolved {
			continue
		}
		owner := types.NamespacedName{Name: l[labelMachine], Namespace: l[labelMachineNamespace]}
		if owner == machine {
			open[storage.Key{Check: fa.Spec.AlertName, Device: l[labelDevice]}] = fa
			continue
		}
		if err := r.Get(ctx, owner, &framev1beta1.FrameMachine{}); apierrors.IsNotFound(err) {
			if err := r.resolveStorageAlert(ctx, fa); err != nil {
				return nil, err
			}
		}
	}
	return open, nil
}

func (r *FrameMachineReconciler) writeStorageAlerts(ctx context.Context, fm *framev1beta1.FrameMachine,
	a *storage.Assessment, open map[storage.Key]*framev1beta1.FrameAlert) error {
	if r.Alerts == nil {
		return nil
	}
	now := time.Now()
	firing := make(map[storage.Key]bool, len(a.Findings))
	for i := range a.Findings {
		f := &a.Findings[i]
		firing[f.Key] = true
		startsAt := now
		if fa, ok := open[f.Key]; ok {
			startsAt = fa.Spec.StartsAt.Time
		}
		if err := r.Alerts.Record(ctx, alerting.Alert{
			Status: alerting.StatusFiring,
			Labels: map[string]string{
				labelAlertName:        f.Check,
				labelSeverity:         f.Severity,
				labelSource:           storageAlertSource,
				labelMachine:          fm.Name,
				labelMachineNamespace: fm.Namespace,
				labelNode:             fm.Spec.NodeRef,
				labelDevice:           f.Device,
				labelStack:            string(f.Scope),
			},
			Annotations: map[string]string{annotationSummary: f.Message},
			StartsAt:    startsAt,
			Fingerprint: alerting.LocalFingerprint(storageAlertSource, fm.Namespace, fm.Name, f.Check, f.Device),
		}); err != nil {
			return err
		}
	}
	for k, fa := range open {
		if firing[k] || a.Keeps(k, storage.Scope(fa.Spec.Labels[labelStack])) {
			continue
		}
		if err := r.resolveStorageAlert(ctx, fa); err != nil {
			return err
		}
	}
	return nil
}

func (r *FrameMachineReconciler) resolveStorageAlert(ctx context.Context, fa *framev1beta1.FrameAlert) error {
	return r.Alerts.Record(ctx, alerting.Alert{
		Status:      alerting.StatusResolved,
		Labels:      fa.Spec.Labels,
		Annotations: fa.Spec.Annotations,
		StartsAt:    fa.Spec.StartsAt.Time,
		EndsAt:      time.Now(),
		Fingerprint: fa.Spec.Fingerprint,
	})
}

// resolveStorageAlerts closes what a deleted machine leaves open: nothing would
// ever re-read it.
func (r *FrameMachineReconciler) resolveStorageAlerts(ctx context.Context, machine types.NamespacedName) error {
	open, err := r.openStorageAlerts(ctx, machine)
	if err != nil {
		return err
	}
	for _, fa := range open {
		if err := r.resolveStorageAlert(ctx, fa); err != nil {
			return err
		}
	}
	return nil
}
