/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// fakeWiper records every destructive call and can be told to fail its
// cleanup step, so guard 5 has something to catch. It also tracks R15's
// completion record separately from the started marker: Claim only ever
// writes "started", and MarkComplete is what a real Wiper would call once
// the destructive work is actually done.
type fakeWiper struct {
	mu              sync.Mutex
	calls           int
	cleanupErr      error
	readMarkerErr   error
	markCompleteErr error
	markers         map[string]string
	completed       map[string]bool
}

func newFakeWiper() *fakeWiper {
	return &fakeWiper{markers: map[string]string{}, completed: map[string]bool{}}
}

func (f *fakeWiper) Count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *fakeWiper) FailCleanup(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.cleanupErr = err }

// FailReadMarker makes the next ReadMarker calls return err, standing in for
// a transient node-communication failure — R14's case, as opposed to any of
// guards 1-3's "this is unsafe" refusals.
func (f *fakeWiper) FailReadMarker(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readMarkerErr = err
}

// ReadMarker returns the claim UID recorded on the machine for byIDPath (or
// "" if none) and whether MarkComplete was ever called for it. This is the
// on-machine record guard 4 relies on, and the completion half R15 added.
func (f *fakeWiper) ReadMarker(_ context.Context, machine, byIDPath string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readMarkerErr != nil {
		return "", false, f.readMarkerErr
	}
	key := machine + "|" + byIDPath
	return f.markers[key], f.completed[key], nil
}

func (f *fakeWiper) Claim(_ context.Context, machine, byIDPath, claimUID, destination string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markers[machine+"|"+byIDPath] = claimUID
	f.calls++
	return f.cleanupErr
}

// FailMarkComplete makes the next MarkComplete call return err, matching
// the FailCleanup idiom — R16's case: the destructive work already
// succeeded, only the completion bookkeeping fails to write.
func (f *fakeWiper) FailMarkComplete(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markCompleteErr = err
}

func (f *fakeWiper) MarkComplete(_ context.Context, machine, byIDPath, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markCompleteErr != nil {
		return f.markCompleteErr
	}
	f.completed[machine+"|"+byIDPath] = true
	return nil
}

var _ = Describe("FrameDiskClaim controller", func() {
	var (
		reconciler *FrameDiskClaimReconciler
		wipes      *fakeWiper
	)

	BeforeEach(func() {
		wipes = newFakeWiper()
		reconciler = &FrameDiskClaimReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Wiper: wipes}
	})

	// validBMC satisfies the CEL validation on FrameMachineSpec.BMC:
	// Address must be a real, non-loopback, non-link-local IP (not a URL),
	// CredentialsRef and TLS are both required, and the object-level rule
	// demands tls.insecureSkipVerify or tls.caBundleRef. A bare
	// BMCSpec{Address: "..."} literal fails admission outright.
	validBMC := func(name string) framev1beta1.BMCSpec {
		return framev1beta1.BMCSpec{
			Address:        "192.168.2.60",
			CredentialsRef: name + "-creds",
			TLS:            framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
		}
	}

	// machineWithDisks creates a FrameMachine whose agent has reported.
	machineWithDisks := func(ctx SpecContext, name string, disks []framev1beta1.ObservedDisk) *framev1beta1.FrameMachine {
		m := &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       framev1beta1.FrameMachineSpec{BMC: validBMC(name)},
		}
		Expect(k8sClient.Create(ctx, m)).To(Succeed())
		now := metav1.Now()
		m.Status.Storage = &framev1beta1.MachineStorage{Observed: disks, ObservedAt: &now}
		Expect(k8sClient.Status().Update(ctx, m)).To(Succeed())
		return m
	}

	claim := func(machine, byID, serial string) *framev1beta1.FrameDiskClaim {
		return &framev1beta1.FrameDiskClaim{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "c-", Namespace: "default"},
			Spec: framev1beta1.FrameDiskClaimSpec{
				MachineRef:  framev1beta1.LocalObjectReference{Name: machine},
				ByIDPath:    byID,
				Serial:      serial,
				Destination: "wipe",
			},
		}
	}

	// LE CHEMIN HEUREUX — sans ce test, la suite ne prouve que les refus.
	// Le re-revieweur a remplace succeed() par fail() sur le chemin de
	// succes et les quinze specs precedentes sont restees vertes: rien ne
	// pinnait "Ready". Un disque libre, une serie qui correspond, un Wiper
	// qui reussit — doit produire Ready, un seul appel destructif, et un
	// message qui nomme la destination.
	It("autorise un disque libre dont le chemin et la serie correspondent", func(ctx SpecContext) {
		machineWithDisks(ctx, "happy-path", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-HAPPY", SerialNumber: "HAPPY0001", SizeGB: 1200, Occupancy: "free"},
		})
		c := claim("happy-path", "/dev/disk/by-id/scsi-HAPPY", "HAPPY0001")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Ready"))
		Expect(back.Status.Message).To(ContainSubstring("wipe"))
		Expect(wipes.Count()).To(Equal(1))

		// R16, test 2 of 2 — the normal path records completion as True.
		cond := meta.FindStatusCondition(back.Status.Conditions, conditionCompletionRecorded)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
	})

	// R16 — a failed MarkComplete must not undo a successful Claim. The
	// destructive work already happened; refusing Ready here would assert
	// something false about the hardware (guard 5's "the gesture itself
	// failed" does not apply — only the bookkeeping did), and returning the
	// error would requeue into a resume that reads "started, not complete"
	// and fails closed forever, rebuilding the exact incident R15 removed.
	// R16, test 1 of 2 — this must produce Ready, one destructive call, a
	// message naming the failure, and CompletionRecorded=False.
	It("passe Ready quand seul l'enregistrement de completion echoue", func(ctx SpecContext) {
		machineWithDisks(ctx, "r16-fail", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-R16", SerialNumber: "R16FAIL0001", SizeGB: 300, Occupancy: "free"},
		})
		wipes.FailMarkComplete(errors.New("could not write the completion marker"))

		c := claim("r16-fail", "/dev/disk/by-id/scsi-R16", "R16FAIL0001")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Ready"),
			"a failure to record completion says nothing about the disk, which was already claimed successfully")
		Expect(back.Status.Message).To(ContainSubstring("could not write the completion marker"))
		Expect(wipes.Count()).To(Equal(1))

		cond := meta.FindStatusCondition(back.Status.Conditions, conditionCompletionRecorded)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("MarkerWriteFailed"))
	})

	// GARDE 1 — le numero de serie retape doit correspondre.
	It("refuse quand le serial retape ne correspond a aucun disque observe", func(ctx SpecContext) {
		machineWithDisks(ctx, "g1-mismatch", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-A", SerialNumber: "KZK245ZG", SizeGB: 1200, Occupancy: "free"},
		})
		c := claim("g1-mismatch", "/dev/disk/by-id/scsi-A", "TYPO9999")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
		Expect(back.Status.Message).To(ContainSubstring("TYPO9999"))
	})

	// GARDE 1, ruling R2 — le test du serial vide ne doit rien laisser sous
	// condition. Deux couches, deux assertions inconditionnelles: le schema
	// (MinLength=1) refuse a l'admission, et le controleur refuse aussi,
	// pour le chemin qui contourne l'admission (objets ecrits avant un
	// changement de schema, ou tout appel direct de authorise).
	It("refuse un serial vide, au schema et dans le controleur", func(ctx SpecContext) {
		machineWithDisks(ctx, "g1-blank", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-B", SerialNumber: "", SizeGB: 1200, Occupancy: "free"},
		})

		// Layer 1: the schema refuses it outright.
		Expect(k8sClient.Create(ctx, claim("g1-blank", "/dev/disk/by-id/scsi-B", ""))).NotTo(Succeed())

		// Layer 2: the controller refuses it too. A reconciler is reached by
		// objects written before a schema change and by any path that
		// bypasses admission, and two empty strings are equal — which is how
		// a disk gets wiped with no confirmation.
		inMemory := claim("g1-blank", "/dev/disk/by-id/scsi-B", "")
		inMemory.Namespace = "default"
		_, err := reconciler.authorise(ctx, inMemory)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty"))
	})

	// GARDE 1, ruling R7 — une serie ambigue fait refuser plutot que de
	// cibler le premier disque venu.
	It("refuse quand deux disques portent la meme serie", func(ctx SpecContext) {
		machineWithDisks(ctx, "g1-ambiguous", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-X1", SerialNumber: "DUP123", SizeGB: 300, Occupancy: "free"},
			{Path: "/dev/disk/by-id/scsi-X2", SerialNumber: "DUP123", SizeGB: 300, Occupancy: "free"},
		})
		c := claim("g1-ambiguous", "/dev/disk/by-id/scsi-X1", "DUP123")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
		Expect(back.Status.Message).To(ContainSubstring("ambiguous"))
	})

	// GARDE 1, revue C1 — le serial peut correspondre a un disque observe
	// tout en nommant un autre chemin: le croisement chemin/serial doit
	// refuser, pas cibler le premier match de serie. Sans ce test, retirer
	// `if d.Path != c.Spec.ByIDPath { ... }` laisse toute la suite verte —
	// et ce croisement est la seule chose qui relie le chemin declare a un
	// disque reellement observe avant l'appel destructif.
	It("refuse quand le serial correspond a un disque observe a un autre chemin", func(ctx SpecContext) {
		machineWithDisks(ctx, "g1-crossed", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-X1", SerialNumber: "CROSS0001", SizeGB: 300, Occupancy: "free"},
			{Path: "/dev/disk/by-id/scsi-X2", SerialNumber: "CROSS0002", SizeGB: 300, Occupancy: "free"},
		})
		// scsi-X1's path, but scsi-X2's serial.
		c := claim("g1-crossed", "/dev/disk/by-id/scsi-X1", "CROSS0002")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
		Expect(back.Status.Message).To(ContainSubstring("not at the requested"))
	})

	// GARDE 1, revue M1 — la comparaison de serie est sensible a la casse;
	// "la confirmation retapee" n'en est une que si elle correspond
	// exactement.
	It("refuse un serial qui ne differe que par la casse", func(ctx SpecContext) {
		machineWithDisks(ctx, "g1-case", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-CASE", SerialNumber: "KZK245ZG", SizeGB: 1200, Occupancy: "free"},
		})
		c := claim("g1-case", "/dev/disk/by-id/scsi-CASE", "kzk245zg")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
	})

	// GARDE 2 — jamais un nom sdX.
	It("refuse un chemin sdX", func(ctx SpecContext) {
		machineWithDisks(ctx, "g2", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-C", SerialNumber: "KZK39VSH", SizeGB: 1200, Occupancy: "free"},
		})
		c := claim("g2", "/dev/sdb", "KZK39VSH")
		// Refused by the CRD pattern; assert that, since that is where the
		// guard lives.
		Expect(k8sClient.Create(ctx, c)).NotTo(Succeed())
	})

	// GARDE 2, revue I1 — le controleur doit refuser aussi, independamment
	// du schema. Meme idiome que R2: appeler authorise() directement avec
	// un objet en memoire qui n'est jamais passe par l'admission. Sans ce
	// test, mutiler le controleur (HasPrefix -> Contains) ne rougissait que
	// si le motif CRD etait aussi retire — ce qui ne prouvait pas grand
	// chose sur le controleur lui-meme.
	It("refuse un chemin sdX au niveau du controleur, meme hors admission", func(ctx SpecContext) {
		// Deliberately no FrameMachine created for "g2-bypass-none": if the
		// sdX guard is weakened, authorise falls through to the machine
		// lookup and fails with a "reading FrameMachine" NotFound error
		// instead — which does not mention "by-id" — so this discriminates
		// cleanly instead of coincidentally matching a later guard's message
		// (the path/serial cross-check's error text also happens to quote a
		// by-id path, which would make a same-machine version of this test
		// pass for the wrong reason).
		inMemory := claim("g2-bypass-none", "/dev/sdb", "KZK00SDX")
		inMemory.Namespace = "default"
		_, err := reconciler.authorise(ctx, inMemory)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("by-id"))
	})

	// GARDE 3 — refus ferme sur l'occupation.
	It("refuse un disque occupe", func(ctx SpecContext) {
		machineWithDisks(ctx, "g3-busy", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-D", SerialNumber: "KZK35GXG", SizeGB: 1200, Occupancy: "ceph-osd"},
		})
		c := claim("g3-busy", "/dev/disk/by-id/scsi-D", "KZK35GXG")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
		Expect(back.Status.Message).To(ContainSubstring("ceph-osd"))
	})

	// GARDE 3, revue I2 — l'occupation doit fermer sur toute valeur qui
	// n'est pas explicitement "free", y compris une chaine vide (une entree
	// jamais renseignee). Un `!= "free" && != ""` ou une liste blanche qui
	// ne nomme que "ceph-osd"/"mounted" survivrait a la suite sans ce test,
	// et autoriserait "lvm-pv" ou une occupation jamais renseignee.
	It("refuse un disque dont l'occupation est vide", func(ctx SpecContext) {
		machineWithDisks(ctx, "g3-unknown-occ", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-U", SerialNumber: "UNKNOWNOCC1", SizeGB: 300, Occupancy: ""},
		})
		c := claim("g3-unknown-occ", "/dev/disk/by-id/scsi-U", "UNKNOWNOCC1")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
	})

	// GARDE 3, la moitie qu'on oublie — ne pas savoir n'est pas une
	// autorisation.
	It("refuse quand l'agent n'a jamais rapporte", func(ctx SpecContext) {
		m := &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "g3-silent", Namespace: "default"},
			Spec:       framev1beta1.FrameMachineSpec{BMC: validBMC("g3-silent")},
		}
		Expect(k8sClient.Create(ctx, m)).To(Succeed()) // status.storage stays nil

		c := claim("g3-silent", "/dev/disk/by-id/scsi-E", "KZK245ZG")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
		Expect(back.Status.Message).To(ContainSubstring("never reported"))
	})

	// GARDE 3, la meme moitie sous un angle different — un Storage non-nil
	// dont ObservedAt est nil n'est pas un rapport, meme si Observed porte
	// des entrees. C'est la moitie que "status.storage stays nil" ci-dessus
	// ne peut pas discriminer d'une mutation qui retire seulement le
	// sous-test ObservedAt == nil: dans ce cas-la, m.Status.Storage lui-meme
	// est nil, donc le garde restant (Storage == nil) suffit encore a faire
	// rougir le test precedent sans que cette moitie du garde soit vivante.
	It("refuse quand le storage existe mais n'a pas d'horodatage", func(ctx SpecContext) {
		m := &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "g3-noage", Namespace: "default"},
			Spec:       framev1beta1.FrameMachineSpec{BMC: validBMC("g3-noage")},
		}
		Expect(k8sClient.Create(ctx, m)).To(Succeed())
		m.Status.Storage = &framev1beta1.MachineStorage{
			Observed: []framev1beta1.ObservedDisk{
				{Path: "/dev/disk/by-id/scsi-Z", SerialNumber: "NOAGE0001", SizeGB: 300, Occupancy: "free"},
			},
			// ObservedAt intentionally left nil: Storage is non-nil, but
			// carries no report time.
		}
		Expect(k8sClient.Status().Update(ctx, m)).To(Succeed())

		c := claim("g3-noage", "/dev/disk/by-id/scsi-Z", "NOAGE0001")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
		Expect(back.Status.Message).To(ContainSubstring("never reported"))
	})

	It("refuse quand le rapport de l'agent est perime", func(ctx SpecContext) {
		stale := metav1.NewTime(time.Now().Add(-2 * time.Hour))
		m := &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "g3-stale", Namespace: "default"},
			Spec:       framev1beta1.FrameMachineSpec{BMC: validBMC("g3-stale")},
		}
		Expect(k8sClient.Create(ctx, m)).To(Succeed())
		m.Status.Storage = &framev1beta1.MachineStorage{
			Observed:   []framev1beta1.ObservedDisk{{Path: "/dev/disk/by-id/scsi-F", SerialNumber: "L0JG1VJJ", SizeGB: 1200, Occupancy: "free"}},
			ObservedAt: &stale,
		}
		Expect(k8sClient.Status().Update(ctx, m)).To(Succeed())

		c := claim("g3-stale", "/dev/disk/by-id/scsi-F", "L0JG1VJJ")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
		Expect(back.Status.Message).To(ContainSubstring("stale"))
	})

	// GARDE 4, ruling R14 — une erreur de lecture transitoire du marqueur ne
	// doit pas fermer definitivement l'objet. R12/R1 avaient d'abord fait de
	// cette branche un fail() terminal (pour que les commentaires de
	// cmd/main.go disent vrai) ; c'etait une erreur, car sur un objet
	// destructeur un aleas de communication avec le noeud n'est pas une
	// declaration que la reclamation est dangereuse — contrairement aux
	// gardes 1-3, qui statuent sur la securite du geste, celle-ci ne dit
	// rien de tel et doit se representer (`err` non nil, requeue), jamais
	// verrouiller l'objet en Failed.
	It("ne ferme pas definitivement sur une erreur de lecture transitoire du marqueur", func(ctx SpecContext) {
		machineWithDisks(ctx, "r14-transient", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-R14", SerialNumber: "R14TRANSIENT", SizeGB: 300, Occupancy: "free"},
		})
		wipes.FailReadMarker(errors.New("temporary node communication error"))

		c := claim("r14-transient", "/dev/disk/by-id/scsi-R14", "R14TRANSIENT")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).To(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).NotTo(Equal("Failed"),
			"a transient marker-read failure on a destructive object must requeue, not close the claim permanently")
		Expect(wipes.Count()).To(Equal(0), "no destructive call while the marker could not even be read")
	})

	// GARDE 4, revue I3 — un marqueur appartenant a un autre objet doit
	// refuser. Sans ce test, remplacer cette branche par `succeed(...)`
	// laisse la suite verte, puisque aucun test ne pre-seme un marqueur
	// etranger.
	It("refuse quand un autre objet porte deja le marqueur", func(ctx SpecContext) {
		machineWithDisks(ctx, "g4-foreign", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-FM", SerialNumber: "FOREIGN0001", SizeGB: 300, Occupancy: "free"},
		})
		wipes.markers["g4-foreign|/dev/disk/by-id/scsi-FM"] = "some-other-claim-uid"

		c := claim("g4-foreign", "/dev/disk/by-id/scsi-FM", "FOREIGN0001")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Failed"))
		Expect(back.Status.Message).To(ContainSubstring("already carries claim marker"))
		Expect(wipes.Count()).To(Equal(0), "a disk carrying someone else's marker must never be wiped")
	})

	// GARDE 4, ruling R1 — le marqueur d'unicite n'est pas dans la memoire
	// du process. Le test doit forcer le chemin de rejeu: un manager mort
	// apres l'effacement et avant l'ecriture de la phase. Sans remettre la
	// phase a vide entre les deux Reconcile, le garde de phase terminale en
	// tete de la fonction termine seul le second appel, et supprimer
	// ReadMarker laisserait quand meme le test vert.
	It("ne rejoue pas le geste destructif apres un redemarrage du manager", func(ctx SpecContext) {
		machineWithDisks(ctx, "g4", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-G", SerialNumber: "S421NTCK0000M643K5U4", SizeGB: 300, Occupancy: "free"},
		})
		c := claim("g4", "/dev/disk/by-id/scsi-G", "S421NTCK0000M643K5U4")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())
		Expect(wipes.Count()).To(Equal(1))

		// A manager that died after the wipe and before writing Ready.
		// Without this the terminal-phase guard alone ends the second
		// reconcile, and the test stays green with ReadMarker deleted. Note
		// what this test does and does not pin: since Claim and MarkComplete
		// both succeed here (no FailCleanup armed), the resumed reconcile
		// legitimately finds the marker complete and does converge to
		// Ready — that is R15's correct behaviour for a claim that actually
		// finished, not a regression of guard 4. What guard 4 forbids is the
		// destructive call running twice, which is the only thing asserted
		// below; the "unconfirmed completion" refusal this ruling also
		// introduced is pinned separately by the guard-5 extension, where
		// Claim's cleanup fails and MarkComplete is never reached.
		var mid framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &mid)).To(Succeed())
		mid.Status.Phase = ""
		Expect(k8sClient.Status().Update(ctx, &mid)).To(Succeed())

		// A fresh reconciler is a restarted manager: nothing in memory
		// survives, only what was written down.
		fresh := &FrameDiskClaimReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Wiper: wipes}
		_, err = fresh.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())
		Expect(wipes.Count()).To(Equal(1), "the destructive act ran twice across a manager restart")

		// R15: the marker matches and MarkComplete was recorded, so this is
		// the "lost the Ready status write" case, not "died mid-gesture" —
		// pin that it actually resumes to Ready rather than being folded
		// into the unconfirmed-completion refusal regardless of `complete`.
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &mid)).To(Succeed())
		Expect(mid.Status.Phase).To(Equal("Ready"))
		// Tightened per re-review: the resume-success message must match the
		// primary path's, destination and all, so a resumed claim reads the
		// same as a fresh one.
		Expect(mid.Status.Message).To(ContainSubstring(c.Spec.Destination))
	})

	// GARDE 5 — une erreur de nettoyage n'est pas avalee. Revue C2: guard 4
	// undoes guard 5 one requeue later if the "marker matches this object"
	// branch resumes with succeed() — the marker is written by Claim()
	// before the destructive work, so a failed cleanup still leaves it
	// behind, and the very next reconcile would read it back and converge
	// to Ready if that branch trusted it. The extension below reconciles a
	// second time, cleanup still failing, and pins that it does not.
	It("ne passe pas Ready quand le nettoyage echoue apres le travail utile, meme apres un second passage", func(ctx SpecContext) {
		machineWithDisks(ctx, "g5", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-H", SerialNumber: "S420YJWS0000K6319L3R", SizeGB: 300, Occupancy: "free"},
		})
		wipes.FailCleanup(errors.New("could not remove the claim marker"))

		c := claim("g5", "/dev/disk/by-id/scsi-H", "S420YJWS0000K6319L3R")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).To(HaveOccurred())

		var back framev1beta1.FrameDiskClaim
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		Expect(back.Status.Phase).NotTo(Equal("Ready"),
			"a phase does not become Ready because the failure happened after the useful work")
		Expect(wipes.Count()).To(Equal(1))

		// The natural next step after the first reconcile returned an error:
		// controller-runtime requeues. Guard 4's own marker — written by the
		// first, failed Claim call — is now read back and matches this
		// object's UID. Per the ruling, that must mean "started, not
		// confirmed succeeded", so this pass must fail closed too, never
		// converge to Ready just because the marker looks like "already
		// done", and it must not repeat the destructive call either.
		_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
		// Tightened per re-review: NotTo(Equal("Ready")) alone would let a
		// stuck "Claiming" through, or a Failed whose message dropped the
		// inspection instruction — the one sentence this object exists to
		// deliver at this moment. Pin both: the phase is the terminal
		// refusal, and the message still tells the operator what to do.
		Expect(back.Status.Phase).To(Equal("Failed"),
			"own marker, not complete: this must be the terminal refusal, not a stuck Claiming")
		Expect(back.Status.Message).To(ContainSubstring("inspected"))
		Expect(wipes.Count()).To(Equal(1), "an unconfirmed claim must not be retried automatically")
	})
})
