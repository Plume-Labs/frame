# Lot 3 — Stockage — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Donner à Frame un modèle de stockage typé à la Proxmox VE — entrées déclaratives `FrameStorage`, inventaire disque à deux sources jointes par numéro de série, et un seul geste destructif (`FrameDiskClaim`) protégé par cinq gardes.

**Architecture:** Trois couches indépendantes qui se rejoignent tard. (1) La source BMC : le lot 1 a laissé `Inventory.Drives` vide faute de matériel ; les captures existent maintenant, on implémente le parcours OEM SmartStorage et on remplit le champ existant. (2) La source nœud : l'agent déjà déployé gagne une lecture disque et la publie dans `FrameMachine.status.storage.observed`. (3) La jointure, fonction pure sur numéro de série, produit `divergences[]` — que ni le BMC ni l'OS ne peut produire seul. Par-dessus, `FrameStorage` décrit les entrées de stockage et `FrameDiskClaim` est le seul objet qui détruit des données.

**Tech Stack:** Go 1.23+ / controller-runtime / kubebuilder v4, envtest + Ginkgo pour les contrôleurs, `go test` standard pour les paquets purs, React 19 + Vite + vitest (`environment: 'node'`) pour la console, Helm chart `charts/frame` + kustomize `config/`.

**Spec:** `docs/superpowers/specs/2026-09-13-storage-design.md` — à lire avant la première tâche. Le plan argumente depuis elle ; tout conflit se tranche en faveur de la spec.

## Global Constraints

- **`v1beta1` est gelé** : pas de webhook de conversion, pas de nouvelle version d'API. Tout nouveau type est créé directement en `v1beta1` avec `+kubebuilder:storageversion`.
- **Frame ne supprime jamais un PV ni un PVC**, dans aucune branche, quel que soit l'état de l'objet qui le référence.
- **La capacité est rendue utilisable, jamais brute seule.** `status.capacity.usable` d'abord ; le brut ne peut apparaître qu'à côté d'elle.
- **`shared` est dérivé du type, jamais déclaré.** Il vit en `status`, pas en `spec`.
- **La clé de jointure entre les deux sources disque est le numéro de série, et rien d'autre.** Jamais le nom Redfish, jamais le chemin `by-id`, jamais `sdX`.
- **Deux chaînes vides ne sont jamais égales pour une garde.** Toute comparaison d'identité refuse explicitement le cas vide des deux côtés.
- **Chaque garde nommée en §4 et §5 de la spec livre sa preuve par mutation** : retirer la garde, montrer le test rouge, la remettre. La preuve est consignée dans le rapport de tâche.
- **`make helm-parity` s'arrête à sa première section rouge** et son `jq -S` trie les clés d'objet mais **pas l'ordre des tableaux** : l'ordre des blocs RBAC compte. Le ClusterRole manager du chart (`charts/frame/templates/rbac-manager.yaml`) est maintenu à la main et doit être mis à jour pour chaque nouveau type, sinon l'informer est interdit au démarrage.
- **vitest tourne en `environment: 'node'` avec `include: ['src/**/*.test.ts']`** : aucun `.tsx` n'est jamais exécuté. Toute décision d'écran vit dans `src/lib/` et y est testée.
- **Piège d'enregistrement Ginkgo** : `k8sClient` est rempli par `BeforeSuite` dans `TestControllers` (`internal/controller/frame/suite_test.go`). Un `func TestX` de premier niveau qui trie avant `suite_test.go` panique sur un client nil. Les tests de contrôleur s'écrivent en blocs Ginkgo, pas en `func Test…`.
- **`bin/crd-render/` ne doit jamais être appliqué au cluster** : sa sortie bascule la conversion CRD vers un webhook pointant sur un service inexistant. C'est une cible de test envtest, rien d'autre.
- **Le hook pre-commit est cassé dans ce worktree** (lint-staged sans configuration) : commiter avec `--no-verify`.
- Ne jamais ajouter de trailer `Co-Authored-By` aux commits.

---

## File Structure

**Source BMC (tâches 1-2)**
- `internal/redfish/types.go` — modifié : `Drive` gagne `SerialNumber`, `Location`, `MediaType`, `StatusReasons`.
- `internal/redfish/decode_storage.go` — créé : les documents OEM SmartStorage et leur décodage. Seule responsabilité : la forme JSON de l'arbre HPE.
- `internal/redfish/client.go` — modifié : `readSmartStorage` remplit `Inventory.Drives` depuis le lien **découvert** `Oem.Hp.links.SmartStorage.href`.
- `api/frame/v1beta1/framemachine_types.go` — modifié : `DriveInfo` gagne les mêmes champs.

**Source nœud (tâches 3-5)**
- `internal/agent/disks.go` — créé : lecture `lsblk` via le `CommandRunner` existant, sans Kubernetes.
- `internal/agent/diskstatus.go` — créé : le patch de `FrameMachine.status.storage.observed`, résolution par `spec.nodeRef`.
- `api/frame/v1beta1/framemachine_types.go` — modifié : `MachineStorage`, `ObservedDisk`, `DiskDivergence`.

**La jointure (tâche 4)**
- `internal/storage/join.go` — créé : fonction pure, aucun import Kubernetes au-delà des types d'API. C'est le cœur de §3.

**Les entrées (tâches 6-7, 9)**
- `api/frame/v1beta1/framestorage_types.go` — créé.
- `internal/controller/frame/framestorage_controller.go` — créé.
- `internal/webhook/frame/v1beta1/framestorage_webhook.go` — créé : la garde d'adoption.
- `internal/webhook/core/v1/pvc_webhook.go` — créé : l'application des types de contenu sur les PVC.

**Le geste destructif (tâche 8)**
- `api/frame/v1beta1/framediskclaim_types.go` — créé.
- `internal/controller/frame/framediskclaim_controller.go` — créé : les cinq gardes.

**Déploiement (tâche 10)**
- `charts/frame/templates/rbac-manager.yaml`, `rbac-tier-roles.yaml`, `webhookconfigurations.yaml`, `config/crd/`, `config/rbac/`.

**Console (tâche 11)**
- `src/lib/storage.ts` + `src/lib/storage.test.ts` — créés : toutes les décisions d'écran.
- `src/components/FrameStorageView.tsx`, `src/components/MachineDisksPanel.tsx` — créés.
- `src/components/ClusterStorageView.tsx` — modifié : motifs du WARN, capacité utilisable avant le brut.

**Documentation (tâche 12)**
- `docs/storage.md` — créé.

---

### Task 1: Le BMC rend enfin ses disques

Le lot 1 a laissé `Inventory.Drives` vide, avec ce commentaire dans `internal/redfish/client.go` : un iLO4 expose ses disques sous l'arbre OEM `SmartStorage`, pas sous les collections standard `Storage/Drives`, et deviner ce chemin sans machine est « how a plausible-looking wrong implementation ships ». Les captures de la vraie machine existent maintenant sous `internal/redfish/testdata/ilo4-real/smartstorage_*`. Cette tâche implémente le parcours.

**Le chemin se découvre, il ne se devine pas.** `systems_1_degraded.json` porte `Oem.Hp.links.SmartStorage.href = "/redfish/v1/Systems/1/SmartStorage/"`. Une implémentation qui code ce littéral en dur passerait tous les tests écrits contre la capture réelle — c'est le piège exact que `TestClearLogPostsTheDiscoveredTarget` documente pour `ClearLog`. Le test de découverte ci-dessous déplace donc le href vers un chemin qu'aucune devinette ne produirait.

**Files:**
- Create: `internal/redfish/decode_storage.go`
- Modify: `internal/redfish/types.go` (struct `Drive`, lignes 58-63)
- Modify: `internal/redfish/client.go` (le bloc `Inventory:` lignes 238-250, et une nouvelle méthode `readSmartStorage`)
- Modify: `internal/redfish/decode.go` (ajouter `SmartStorage` aux liens OEM du document système)
- Test: `internal/redfish/real_hardware_test.go`

**Interfaces:**
- Consumes: rien.
- Produces: `redfish.Drive{Name, Model, SizeGB, Protocol, Health, SerialNumber, Location, MediaType, StatusReasons []string}` — la tâche 2 mappe exactement ces champs vers l'API.

- [ ] **Step 1: Écrire le test de découverte (il doit échouer)**

Ajouter à `internal/redfish/real_hardware_test.go` :

```go
// movedSmartStorageRoot is a path the real machine does not use. A client
// that discovers SmartStorage from Oem.Hp.links.SmartStorage.href finds the
// drives here; one that hardcodes "/redfish/v1/Systems/1/SmartStorage/"
// finds a 404 and reports zero drives. Without this move both
// implementations pass, because on the captured hardware the guessed path
// and the advertised one are byte-identical — the same coincidence that
// hid the ClearLog defect (see TestClearLogPostsTheDiscoveredTarget).
const movedSmartStorageRoot = "/redfish/v1/Systems/1/Oem/Hp/SmartStorageRelocated/"

func TestSmartStorageRootIsDiscoveredNotGuessed(t *testing.T) {
	systemDoc, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", "systems_1_degraded.json"))
	if err != nil {
		t.Fatalf("fixture systems_1_degraded.json: %v", err)
	}
	moved := strings.ReplaceAll(
		string(systemDoc),
		"/redfish/v1/Systems/1/SmartStorage/",
		movedSmartStorageRoot,
	)

	guessed := make(chan struct{}, 1)
	mux := http.NewServeMux()
	serve := func(path, file string) {
		body, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", file))
		if err != nil {
			t.Fatalf("fixture %s: %v", file, err)
		}
		mux.HandleFunc(path+"{$}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	serve("/redfish/v1/", "service_root.json")
	serve("/redfish/v1/Systems/", "systems.json")
	mux.HandleFunc("/redfish/v1/Systems/1/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(moved))
	})
	serve(movedSmartStorageRoot, "smartstorage_degraded.json")
	serve("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/", "smartstorage_arraycontrollers_degraded.json")
	serve("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/", "smartstorage_arraycontrollers_0_degraded.json")
	serve("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/", "smartstorage_diskdrives_degraded.json")
	for i := 0; i < 8; i++ {
		serve(
			fmt.Sprintf("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/%d/", i),
			fmt.Sprintf("smartstorage_diskdrive_%d_degraded.json", i),
		)
	}
	// The guessed root gets its own handler so a regression is a failure,
	// not a silent 404 that merely yields zero drives.
	mux.HandleFunc("/redfish/v1/Systems/1/SmartStorage/{$}", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case guessed <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "u", "p", &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test server
	snap, err := c.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	select {
	case <-guessed:
		t.Fatal("client fetched the hardcoded SmartStorage path; it must read Oem.Hp.links.SmartStorage.href")
	default:
	}
	if len(snap.Inventory.Drives) != 8 {
		t.Fatalf("Drives = %d, want 8", len(snap.Inventory.Drives))
	}
}
```

- [ ] **Step 2: Lancer le test, vérifier qu'il échoue**

Run: `cd /home/rmocq/frame-storage && go test ./internal/redfish/ -run TestSmartStorageRootIsDiscoveredNotGuessed -v`
Expected: FAIL — `Drives = 0, want 8` (le champ est laissé vide aujourd'hui).

- [ ] **Step 3: Écrire le second test, celui qui fixe les valeurs**

Le premier test prouve la découverte, pas l'exactitude : huit disques vides le satisferaient. Celui-ci épingle le disque qui porte toute §3.

```go
// TestSmartStorageReadsTheSATADriveVerbatim pins the one drive the whole
// storage lot turns on: bay 2I:6:8, serial W4722RRA, the machine's only
// SATA disk and the only one Linux does not see. The BMC reports it
// Health OK — nothing in this document says it is masked — which is why
// the divergence in internal/storage.Join is computed from two sources
// and never read from one.
func TestSmartStorageReadsTheSATADriveVerbatim(t *testing.T) {
	srv := serveRealSmartStorage(t)
	c := New(srv.URL, "u", "p", &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test server
	snap, err := c.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	var got *Drive
	for i := range snap.Inventory.Drives {
		if snap.Inventory.Drives[i].SerialNumber == "W4722RRA" {
			got = &snap.Inventory.Drives[i]
		}
	}
	if got == nil {
		t.Fatalf("no drive with serial W4722RRA among %d drives", len(snap.Inventory.Drives))
	}
	if got.Location != "2I:6:8" {
		t.Errorf("Location = %q, want 2I:6:8", got.Location)
	}
	if got.Model != "MM1000GFJTE" {
		t.Errorf("Model = %q, want MM1000GFJTE", got.Model)
	}
	if got.Protocol != "SATA" {
		t.Errorf("Protocol = %q, want SATA (it is the machine's only SATA disk)", got.Protocol)
	}
	if got.MediaType != "HDD" {
		t.Errorf("MediaType = %q, want HDD", got.MediaType)
	}
	if got.SizeGB != 1000 {
		t.Errorf("SizeGB = %d, want 1000", got.SizeGB)
	}
	if got.Health != "OK" {
		t.Errorf("Health = %q, want OK — the BMC does not know this disk is masked", got.Health)
	}
	if len(got.StatusReasons) != 1 || got.StatusReasons[0] != "None" {
		t.Errorf("StatusReasons = %v, want [None]", got.StatusReasons)
	}
}

// serveRealSmartStorage routes the unmodified ilo4-real SmartStorage
// captures, for tests about values rather than about discovery.
func serveRealSmartStorage(t *testing.T) *httptest.Server {
	t.Helper()
	routes := map[string]string{
		"/redfish/v1/":                                                      "service_root.json",
		"/redfish/v1/Systems/":                                              "systems.json",
		"/redfish/v1/Systems/1/":                                            "systems_1_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/":                               "smartstorage_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/":              "smartstorage_arraycontrollers_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/":            "smartstorage_arraycontrollers_0_degraded.json",
		"/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/": "smartstorage_diskdrives_degraded.json",
	}
	for i := 0; i < 8; i++ {
		routes[fmt.Sprintf("/redfish/v1/Systems/1/SmartStorage/ArrayControllers/0/DiskDrives/%d/", i)] =
			fmt.Sprintf("smartstorage_diskdrive_%d_degraded.json", i)
	}
	return serveFixturesFrom(t, "ilo4-real", routes)
}
```

- [ ] **Step 4: Lancer les deux tests, vérifier qu'ils échouent**

Run: `go test ./internal/redfish/ -run 'TestSmartStorage' -v`
Expected: FAIL les deux.

- [ ] **Step 5: Étendre le type `Drive`**

Dans `internal/redfish/types.go`, remplacer la struct `Drive` :

```go
type Drive struct {
	Name     string
	Model    string
	SizeGB   int32
	Protocol string
	Health   string

	// SerialNumber is the join key against what the node's own agent sees
	// (internal/storage.Join). A Redfish drive name and a /dev/disk/by-id
	// path are different namespaces; matching on anything but the serial
	// matches nothing.
	SerialNumber string

	// Location is the physical bay in the controller's own
	// ControllerPort:Box:Bay format, e.g. "2I:6:8" — what a human reads on
	// the chassis before pulling a disk.
	Location string

	// MediaType is HDD or SSD as the controller reports it.
	MediaType string

	// StatusReasons is the controller's own explanation of the drive's
	// state. On the captured hardware every drive, including the one Linux
	// cannot see, reports ["None"]: the BMC has no field that says a disk
	// is masked by residual logical-unit metadata. That absence is why the
	// divergence is computed, never read.
	StatusReasons []string
}
```

- [ ] **Step 6: Écrire le décodage OEM**

Créer `internal/redfish/decode_storage.go` :

```go
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

package redfish

// HPE's SmartStorage tree is OEM, not standard Redfish, and it spells its
// link objects in lowercase "links" with an "href" inside — not the
// standard "Links"/"@odata.id". Both spellings appear on the same machine,
// which is why these documents get their own decode file rather than
// reusing decode.go's shapes.

type hpLink struct {
	Href string `json:"href"`
}

// smartStorageDoc is /redfish/v1/Systems/{id}/SmartStorage/.
type smartStorageDoc struct {
	Links struct {
		ArrayControllers hpLink `json:"ArrayControllers"`
		HostBusAdapters  hpLink `json:"HostBusAdapters"`
	} `json:"links"`
}

// hpCollection is any HpSmartStorage*Collection: the members are standard
// @odata.id entries even though the link objects around them are not.
type hpCollection struct {
	Members []struct {
		ODataID string `json:"@odata.id"`
	} `json:"Members"`
}

// arrayControllerDoc is one ArrayControllers/{id}/ document. PhysicalDrives
// is the collection of every drive attached to the controller, configured
// or not; UnconfiguredDrives is a subset of it and is deliberately not
// walked — on the captured machine all eight drives appear in both, and
// reading the subset would silently drop any drive that is part of a
// logical volume.
type arrayControllerDoc struct {
	Links struct {
		PhysicalDrives hpLink `json:"PhysicalDrives"`
	} `json:"links"`
}

// diskDriveDoc is one DiskDrives/{id}/ document.
type diskDriveDoc struct {
	Model                  string   `json:"Model"`
	SerialNumber           string   `json:"SerialNumber"`
	Location               string   `json:"Location"`
	CapacityGB             int32    `json:"CapacityGB"`
	InterfaceType          string   `json:"InterfaceType"`
	MediaType              string   `json:"MediaType"`
	DiskDriveStatusReasons []string `json:"DiskDriveStatusReasons"`
	Status                 struct {
		Health string `json:"Health"`
		State  string `json:"State"`
	} `json:"Status"`
}
```

- [ ] **Step 7: Ajouter le lien OEM au document système**

Dans `internal/redfish/decode.go`, la struct du document système porte déjà `Oem.Hp`. Y ajouter le lien SmartStorage, en respectant la casse OEM (`links`, minuscule) :

```go
// Inside the existing Oem.Hp struct, alongside PostState:
Links struct {
	SmartStorage struct {
		Href string `json:"href"`
	} `json:"SmartStorage"`
} `json:"links"`
```

Vérifier le nom exact de la struct englobante avant d'éditer : `grep -n 'Hp struct' internal/redfish/decode.go`.

- [ ] **Step 8: Implémenter le parcours dans le client**

Dans `internal/redfish/client.go`, remplacer le commentaire « Drives is deliberately left empty » par un appel, après la construction de `snap` et avant `readManager` :

```go
	// Drives come from HPE's OEM SmartStorage tree, whose root is read
	// from the system document's own Oem.Hp.links.SmartStorage.href rather
	// than assembled from a template: see
	// TestSmartStorageRootIsDiscoveredNotGuessed for why a hardcoded path
	// passes every test written against the capture and is still wrong.
	//
	// A machine with no SmartStorage tree (any non-HPE BMC) leaves Drives
	// empty and is not an error: an inventory missing a section is a
	// machine this client does not know how to read, not a probe failure,
	// and failing here would take the whole snapshot — power state,
	// sensors, event log — down with it.
	if err := c.readSmartStorage(ctx, sys.Oem.Hp.Links.SmartStorage.Href, snap); err != nil {
		return nil, fmt.Errorf("redfish: read SmartStorage: %w", err)
	}
```

Et la méthode, à la fin du fichier :

```go
// readSmartStorage walks HPE's OEM storage tree and fills snap.Inventory.Drives.
//
// An empty root (no SmartStorage link on the system document) returns nil
// with no drives: see the call site. A root that is advertised but does not
// answer is an error, because that is a BMC saying it has a tree and then
// refusing to serve it.
func (c *client) readSmartStorage(ctx context.Context, root string, snap *Snapshot) error {
	if root == "" {
		return nil
	}

	var ss smartStorageDoc
	if err := c.get(ctx, root, &ss); err != nil {
		return fmt.Errorf("smart storage root %s: %w", root, err)
	}
	if ss.Links.ArrayControllers.Href == "" {
		return nil
	}

	var controllers hpCollection
	if err := c.get(ctx, ss.Links.ArrayControllers.Href, &controllers); err != nil {
		return fmt.Errorf("array controllers: %w", err)
	}

	for _, ctrl := range controllers.Members {
		var ac arrayControllerDoc
		if err := c.get(ctx, ctrl.ODataID, &ac); err != nil {
			return fmt.Errorf("array controller %s: %w", ctrl.ODataID, err)
		}
		if ac.Links.PhysicalDrives.Href == "" {
			continue
		}

		var drives hpCollection
		if err := c.get(ctx, ac.Links.PhysicalDrives.Href, &drives); err != nil {
			return fmt.Errorf("physical drives of %s: %w", ctrl.ODataID, err)
		}

		for _, d := range drives.Members {
			var doc diskDriveDoc
			if err := c.get(ctx, d.ODataID, &doc); err != nil {
				return fmt.Errorf("disk drive %s: %w", d.ODataID, err)
			}
			snap.Inventory.Drives = append(snap.Inventory.Drives, Drive{
				Name:          doc.Location,
				Model:         doc.Model,
				SizeGB:        doc.CapacityGB,
				Protocol:      doc.InterfaceType,
				Health:        doc.Status.Health,
				SerialNumber:  doc.SerialNumber,
				Location:      doc.Location,
				MediaType:     doc.MediaType,
				StatusReasons: doc.DiskDriveStatusReasons,
			})
		}
	}
	return nil
}
```

- [ ] **Step 9: Lancer les tests, vérifier qu'ils passent**

Run: `go test ./internal/redfish/ -v`
Expected: PASS, y compris tous les tests préexistants du paquet.

- [ ] **Step 10: Preuve par mutation de la découverte**

Remplacer dans `readSmartStorage` l'appel `c.get(ctx, root, &ss)` par `c.get(ctx, "/redfish/v1/Systems/1/SmartStorage/", &ss)` — la devinette. Lancer :

Run: `go test ./internal/redfish/ -run TestSmartStorageRootIsDiscoveredNotGuessed -v`
Expected: FAIL — `client fetched the hardcoded SmartStorage path`.

Remettre le code correct, relancer, vérifier PASS. Consigner les deux sorties dans le rapport de tâche.

- [ ] **Step 11: Commit**

```bash
git add internal/redfish/
git commit --no-verify -m "feat(redfish): lire les disques sous l'arbre OEM SmartStorage, chemin decouvert"
```

---

### Task 2: Les champs disque remontent jusqu'à l'API

Le champ `DriveInfo` du lot 1 ne porte **pas** de numéro de série : tant qu'il ne le porte pas, la jointure de §3 est impossible. Cette tâche l'ajoute et le fait remplir par le contrôleur.

**Files:**
- Modify: `api/frame/v1beta1/framemachine_types.go` (struct `DriveInfo`, lignes 152-163)
- Modify: `internal/controller/frame/framemachine_controller.go` (là où la snapshot Redfish devient un `MachineInventory`)
- Test: `internal/controller/frame/framemachine_v1beta1_schema_test.go`

**Interfaces:**
- Consumes: `redfish.Drive{SerialNumber, Location, MediaType, StatusReasons}` (tâche 1).
- Produces: `framev1beta1.DriveInfo{Name, Model, SizeGB, Protocol, Health, SerialNumber, Location, MediaType, StatusReasons}` — la tâche 4 joint sur `SerialNumber`.

- [ ] **Step 1: Écrire le test de schéma (il doit échouer)**

Trouver d'abord la forme des tests existants : `grep -n 'func\|It(' internal/controller/frame/framemachine_v1beta1_schema_test.go | head -20`. Ces tests posent un objet contre le CRD rendu et vérifient que les champs survivent à l'aller-retour. Ajouter, dans le même style et le même bloc Ginkgo (jamais un `func Test…` de premier niveau — voir les contraintes globales) :

```go
It("garde le numero de serie, la baie et les motifs d'etat d'un disque", func(ctx SpecContext) {
	m := &framev1beta1.FrameMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "drive-fields", Namespace: "default"},
		Spec:       framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{Address: "https://bmc.invalid"}},
	}
	Expect(k8sClient.Create(ctx, m)).To(Succeed())

	m.Status.Inventory = &framev1beta1.MachineInventory{
		Drives: []framev1beta1.DriveInfo{{
			Name:          "2I:6:8",
			Model:         "MM1000GFJTE",
			SizeGB:        1000,
			Protocol:      "SATA",
			Health:        "OK",
			SerialNumber:  "W4722RRA",
			Location:      "2I:6:8",
			MediaType:     "HDD",
			StatusReasons: []string{"None"},
		}},
	}
	Expect(k8sClient.Status().Update(ctx, m)).To(Succeed())

	var back framev1beta1.FrameMachine
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(m), &back)).To(Succeed())
	Expect(back.Status.Inventory.Drives).To(HaveLen(1))
	d := back.Status.Inventory.Drives[0]
	// The serial is the join key: a CRD that drops it silently makes
	// every divergence in internal/storage.Join read "os-only".
	Expect(d.SerialNumber).To(Equal("W4722RRA"))
	Expect(d.Location).To(Equal("2I:6:8"))
	Expect(d.MediaType).To(Equal("HDD"))
	Expect(d.StatusReasons).To(Equal([]string{"None"}))
})
```

Vérifier le nom exact du champ BMC obligatoire dans `FrameMachineSpec` avant d'écrire le `Create` : `grep -n 'BMC\|Address' api/frame/v1beta1/framemachine_types.go | head`. Si un autre champ est requis, le remplir — un `Create` refusé pour une raison hors sujet est un test qui ne teste rien.

- [ ] **Step 2: Lancer le test, vérifier qu'il échoue**

Run: `make test` puis lire la sortie du paquet `internal/controller/frame`.
Expected: échec de compilation — `unknown field SerialNumber in struct literal`.

- [ ] **Step 3: Étendre `DriveInfo`**

Dans `api/frame/v1beta1/framemachine_types.go`, remplacer la struct :

```go
// DriveInfo describes one drive the BMC can see. It is half of the storage
// picture: what the node's own kernel sees lives in
// FrameMachineStatus.Storage.Observed, and the two are joined on
// SerialNumber alone (internal/storage.Join).
type DriveInfo struct {
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	Model  string `json:"model,omitempty"`
	SizeGB int32  `json:"sizeGB,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	Protocol string `json:"protocol,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	Health string `json:"health,omitempty"`

	// SerialNumber is the only key this drive can be matched on. A Redfish
	// drive name and a /dev/disk/by-id path are different namespaces.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	SerialNumber string `json:"serialNumber,omitempty"`

	// Location is the physical bay, in the controller's
	// ControllerPort:Box:Bay format (e.g. "2I:6:8") — the string a human
	// reads on the chassis before pulling a disk.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Location string `json:"location,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=32
	MediaType string `json:"mediaType,omitempty"`

	// StatusReasons is what the controller says about this drive's state.
	// On the captured ML350 G9 all eight drives report ["None"], including
	// the one the host cannot see: the BMC has no field that admits a disk
	// is masked. That is why divergence is computed from two sources.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	StatusReasons []string `json:"statusReasons,omitempty"`
}
```

- [ ] **Step 4: Mapper la snapshot vers l'API**

Trouver le mappage existant : `grep -n 'DriveInfo' internal/controller/frame/framemachine_controller.go`. S'il n'y a aucun mappage (le lot 1 ne remplissait jamais `Drives`), l'ajouter là où `MachineInventory` est construit depuis `snap.Inventory` :

```go
	drives := make([]framev1beta1.DriveInfo, 0, len(snap.Inventory.Drives))
	for _, d := range snap.Inventory.Drives {
		drives = append(drives, framev1beta1.DriveInfo{
			Name:          d.Name,
			Model:         d.Model,
			SizeGB:        d.SizeGB,
			Protocol:      d.Protocol,
			Health:        d.Health,
			SerialNumber:  d.SerialNumber,
			Location:      d.Location,
			MediaType:     d.MediaType,
			StatusReasons: d.StatusReasons,
		})
	}
	inv.Drives = drives
```

Adapter le nom de la variable d'inventaire à ce que le fichier utilise réellement.

- [ ] **Step 5: Régénérer et relancer**

Run: `make manifests generate && make test`
Expected: PASS.

- [ ] **Step 6: Preuve par mutation**

Retirer `SerialNumber: d.SerialNumber,` du mappage de l'étape 4, lancer `make test`, montrer le test de schéma rouge, remettre. Consigner dans le rapport.

- [ ] **Step 7: Commit**

```bash
git add api/ internal/controller/frame/ config/crd/ charts/
git commit --no-verify -m "feat(framemachine): le disque BMC porte son numero de serie, sa baie et ses motifs"
```

---

### Task 3: Les deux sources, et l'écart entre elles

Le cœur de §3. Une fonction pure : deux listes entrent, trois listes sortent. Aucune des deux sources n'est modifiée, aucune n'est préférée à l'autre.

**Files:**
- Modify: `api/frame/v1beta1/framemachine_types.go` (nouveaux types + champ `Storage` dans `FrameMachineStatus`)
- Create: `internal/storage/join.go`
- Create: `internal/storage/join_test.go`

**Interfaces:**
- Consumes: `framev1beta1.DriveInfo` (tâche 2).
- Produces: `framev1beta1.ObservedDisk{Path, SerialNumber, SizeGB, Occupancy}`, `framev1beta1.DiskDivergence{SerialNumber, Reason, Detail}`, `framev1beta1.MachineStorage{Observed, Divergences, ObservedAt}`, et `storage.Join(bmc []framev1beta1.DriveInfo, observed []framev1beta1.ObservedDisk) []framev1beta1.DiskDivergence`.

- [ ] **Step 1: Écrire les tests de la jointure (ils doivent échouer)**

Créer `internal/storage/join_test.go` :

```go
package storage

import (
	"testing"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func bmc(serial, location string) framev1beta1.DriveInfo {
	return framev1beta1.DriveInfo{SerialNumber: serial, Location: location, Health: "OK", SizeGB: 1000}
}

func os(serial, path string) framev1beta1.ObservedDisk {
	return framev1beta1.ObservedDisk{SerialNumber: serial, Path: path, SizeGB: 1000, Occupancy: "free"}
}

// TestJoinFindsTheMaskedDisk is the ML350 G9, reduced to three disks: the
// BMC reports all three, the kernel sees two. The BMC says Health OK about
// all three — nothing in its own document admits the third is masked — so
// the only way this fact exists at all is the join.
func TestJoinFindsTheMaskedDisk(t *testing.T) {
	got := Join(
		[]framev1beta1.DriveInfo{bmc("KZK245ZG", "1I:6:4"), bmc("L0JG1VJJ", "2I:6:5"), bmc("W4722RRA", "2I:6:8")},
		[]framev1beta1.ObservedDisk{os("KZK245ZG", "/dev/sdc"), os("L0JG1VJJ", "/dev/sdd")},
	)
	if len(got) != 1 {
		t.Fatalf("got %d divergences, want 1: %+v", len(got), got)
	}
	if got[0].SerialNumber != "W4722RRA" {
		t.Errorf("SerialNumber = %q, want W4722RRA", got[0].SerialNumber)
	}
	if got[0].Reason != "bmc-only" {
		t.Errorf("Reason = %q, want bmc-only", got[0].Reason)
	}
}

func TestJoinReportsADiskTheBMCDoesNotKnow(t *testing.T) {
	got := Join(
		[]framev1beta1.DriveInfo{bmc("KZK245ZG", "1I:6:4")},
		[]framev1beta1.ObservedDisk{os("KZK245ZG", "/dev/sdc"), os("NVME123", "/dev/nvme0n1")},
	)
	if len(got) != 1 || got[0].SerialNumber != "NVME123" || got[0].Reason != "os-only" {
		t.Fatalf("got %+v, want one os-only divergence for NVME123", got)
	}
}

func TestJoinReportsASizeMismatch(t *testing.T) {
	b := bmc("KZK245ZG", "1I:6:4")
	b.SizeGB = 1200
	o := os("KZK245ZG", "/dev/sdc")
	o.SizeGB = 1000

	got := Join([]framev1beta1.DriveInfo{b}, []framev1beta1.ObservedDisk{o})
	if len(got) != 1 || got[0].Reason != "mismatch" {
		t.Fatalf("got %+v, want one mismatch", got)
	}
	if got[0].Detail == "" {
		t.Error("a mismatch with no detail says two numbers differ without saying which")
	}
}

func TestJoinIsSilentWhenBothSourcesAgree(t *testing.T) {
	got := Join(
		[]framev1beta1.DriveInfo{bmc("KZK245ZG", "1I:6:4"), bmc("L0JG1VJJ", "2I:6:5")},
		[]framev1beta1.ObservedDisk{os("L0JG1VJJ", "/dev/sdd"), os("KZK245ZG", "/dev/sdc")},
	)
	if len(got) != 0 {
		t.Fatalf("got %+v, want none — order is not a divergence", got)
	}
}

// TestJoinNeverMatchesTwoBlanks is the whole family of defects this lot
// exists to not repeat: a check that passes for a reason other than the one
// it names. Two empty serials are equal in Go, so a naive map join pairs a
// BMC drive whose serial the controller failed to read with an OS disk
// whose serial lsblk failed to read, and reports agreement between two
// disks that were never identified at all.
func TestJoinNeverMatchesTwoBlanks(t *testing.T) {
	got := Join(
		[]framev1beta1.DriveInfo{bmc("", "1I:6:4")},
		[]framev1beta1.ObservedDisk{os("", "/dev/sdc")},
	)
	if len(got) != 2 {
		t.Fatalf("got %d divergences, want 2 (one per unidentified disk): %+v", len(got), got)
	}
	reasons := map[string]bool{}
	for _, d := range got {
		reasons[d.Reason] = true
	}
	if !reasons["bmc-only"] || !reasons["os-only"] {
		t.Fatalf("got reasons %v, want both bmc-only and os-only", reasons)
	}
}

// TestJoinReportsAnEmptyOSListAsTotalDivergence: an agent that has never
// reported is not a machine with no disks. Every BMC drive comes back
// bmc-only, which is what makes FrameDiskClaim's third guard refuse.
func TestJoinReportsAnEmptyOSListAsTotalDivergence(t *testing.T) {
	got := Join([]framev1beta1.DriveInfo{bmc("KZK245ZG", "1I:6:4"), bmc("L0JG1VJJ", "2I:6:5")}, nil)
	if len(got) != 2 {
		t.Fatalf("got %d divergences, want 2", len(got))
	}
}
```

- [ ] **Step 2: Lancer les tests, vérifier qu'ils échouent**

Run: `go test ./internal/storage/ -v`
Expected: échec de compilation — le paquet n'existe pas.

- [ ] **Step 3: Ajouter les types d'API**

Dans `api/frame/v1beta1/framemachine_types.go`, après `DriveInfo` :

```go
// ObservedDisk is one whole disk as the node's own kernel reports it —
// the second of the two storage sources. It is never merged with
// DriveInfo: the gap between them is the datum (see
// docs/superpowers/specs/2026-09-13-storage-design.md §3).
type ObservedDisk struct {
	// Path is the stable /dev/disk/by-id path where one exists, and the
	// kernel name otherwise. It is never an sdX name in a destructive
	// context: sdX ordering changed on all three boots of the machine this
	// design was written against.
	// +kubebuilder:validation:MaxLength=512
	Path string `json:"path,omitempty"`

	// SerialNumber is the join key. An entry never carries an empty one:
	// the agent drops a disk it could not identify rather than publish a
	// blank that matches every other blank.
	// +kubebuilder:validation:MaxLength=128
	SerialNumber string `json:"serialNumber,omitempty"`

	SizeGB int32 `json:"sizeGB,omitempty"`

	// Occupancy is what is using the disk: free, mounted, partitioned,
	// lvm-pv, ceph-osd, or in-use for a signature the agent does not
	// recognise. Anything but "free" makes FrameDiskClaim refuse.
	// +kubebuilder:validation:Enum=free;mounted;partitioned;lvm-pv;ceph-osd;in-use
	// +kubebuilder:validation:MaxLength=32
	Occupancy string `json:"occupancy,omitempty"`
}

// DiskDivergence is one disagreement between the BMC's list and the node's.
// It is computed by internal/storage.Join and never read from either source:
// on the captured ML350 G9 the BMC reports Health OK and
// DiskDriveStatusReasons ["None"] for the very disk the kernel cannot see.
type DiskDivergence struct {
	// +kubebuilder:validation:MaxLength=128
	SerialNumber string `json:"serialNumber,omitempty"`

	// Reason is bmc-only (the BMC lists it, the kernel does not),
	// os-only (the reverse), or mismatch (both list it, disagreeing).
	// +kubebuilder:validation:Enum=bmc-only;os-only;mismatch
	Reason string `json:"reason,omitempty"`

	// Detail names what differs, for mismatch. A divergence with no detail
	// says two numbers differ without saying which.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Detail string `json:"detail,omitempty"`
}

// MachineStorage is the node-side half of the storage picture plus the
// computed gap. The BMC-side half stays where the previous lot put it,
// in Inventory.Drives.
type MachineStorage struct {
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Observed []ObservedDisk `json:"observed,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=64
	Divergences []DiskDivergence `json:"divergences,omitempty"`

	// ObservedAt is when the agent last reported. A nil value means never,
	// and never is not the same as "no disks" — FrameDiskClaim refuses on
	// both, deliberately, but the screens must be able to tell them apart.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}
```

Et dans `FrameMachineStatus`, à côté de `Inventory` :

```go
	// Storage carries what the node itself reports about its disks, and the
	// divergences against Inventory.Drives. Two writers touch this object's
	// status — the machine controller and the node agent — so the agent
	// writes nothing but this field (see internal/agent/diskstatus.go).
	// +optional
	Storage *MachineStorage `json:"storage,omitempty"`
```

- [ ] **Step 4: Implémenter la jointure**

Créer `internal/storage/join.go` :

```go
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

// Package storage joins Frame's two sources of truth about a machine's
// disks. It imports nothing but the API types: the join is the argument of
// docs/superpowers/specs/2026-09-13-storage-design.md §3, and an argument
// that needs a cluster to run is an argument nobody checks.
package storage

import (
	"fmt"
	"sort"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Join returns every disagreement between what the BMC lists and what the
// node's kernel reports, matched on serial number and nothing else.
//
// An unidentified disk — one whose serial is empty on either side — is
// never matched, not even against another unidentified disk. Two empty
// strings are equal in Go, so a plain map join reports agreement between
// two disks that were never identified at all; each comes back as its own
// divergence instead.
//
// The result is sorted by serial then reason, so a status field does not
// churn on map iteration order.
func Join(bmc []framev1beta1.DriveInfo, observed []framev1beta1.ObservedDisk) []framev1beta1.DiskDivergence {
	byBMC := make(map[string]framev1beta1.DriveInfo, len(bmc))
	var out []framev1beta1.DiskDivergence

	for _, d := range bmc {
		if d.SerialNumber == "" {
			out = append(out, framev1beta1.DiskDivergence{
				Reason: "bmc-only",
				Detail: fmt.Sprintf("bay %s reports no serial number, so it cannot be matched", d.Location),
			})
			continue
		}
		byBMC[d.SerialNumber] = d
	}

	seen := make(map[string]bool, len(observed))
	for _, o := range observed {
		if o.SerialNumber == "" {
			out = append(out, framev1beta1.DiskDivergence{
				Reason: "os-only",
				Detail: fmt.Sprintf("%s reports no serial number, so it cannot be matched", o.Path),
			})
			continue
		}
		seen[o.SerialNumber] = true

		b, ok := byBMC[o.SerialNumber]
		if !ok {
			out = append(out, framev1beta1.DiskDivergence{
				SerialNumber: o.SerialNumber,
				Reason:       "os-only",
				Detail:       fmt.Sprintf("%s is not in the BMC's drive list", o.Path),
			})
			continue
		}
		if b.SizeGB != o.SizeGB {
			out = append(out, framev1beta1.DiskDivergence{
				SerialNumber: o.SerialNumber,
				Reason:       "mismatch",
				Detail:       fmt.Sprintf("BMC says %d GB, node says %d GB", b.SizeGB, o.SizeGB),
			})
		}
	}

	for _, d := range bmc {
		if d.SerialNumber == "" || seen[d.SerialNumber] {
			continue
		}
		out = append(out, framev1beta1.DiskDivergence{
			SerialNumber: d.SerialNumber,
			Reason:       "bmc-only",
			Detail:       fmt.Sprintf("bay %s: the BMC lists it, the node's kernel does not", d.Location),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SerialNumber != out[j].SerialNumber {
			return out[i].SerialNumber < out[j].SerialNumber
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}
```

- [ ] **Step 5: Régénérer et lancer**

Run: `make manifests generate && go test ./internal/storage/ -v`
Expected: PASS.

- [ ] **Step 6: Preuve par mutation de la règle des sérials vides**

Retirer les deux blocs `if …SerialNumber == ""` (en laissant la clé vide entrer dans la map), lancer `go test ./internal/storage/ -run TestJoinNeverMatchesTwoBlanks -v`, montrer le rouge — l'implémentation naïve rend 0 divergence au lieu de 2 —, remettre. Consigner les deux sorties.

- [ ] **Step 7: Commit**

```bash
git add api/ internal/storage/ config/crd/ charts/
git commit --no-verify -m "feat(storage): joindre les deux sources disque sur le serial, jamais sur le vide"
```

---

### Task 4: L'agent lit les disques du nœud

L'agent tourne déjà sur chaque nœud (`internal/agent`, DaemonSet `deploy/kubernetes/base/node-tuning-agent/daemonset.yaml`) et sait exécuter une commande dans les namespaces de PID 1 via `HostCommandRunner`. Cette tâche lui ajoute la lecture disque — un paquet pur, sans Kubernetes, comme `Observe`.

**Pourquoi `lsblk` et pas `/sys`.** La spec demande le numéro de série *et* ce qui occupe le disque : montage, PV LVM, OSD Ceph, table de partitions. `/sys/block` donne la taille mais pas le type de système de fichiers ni les signatures ; les reconstituer à la main revient à réécrire `blkid` moins bien. `lsblk -J` rend tout d'un coup, en JSON, et il est présent sur toute Debian.

**Files:**
- Create: `internal/agent/disks.go`
- Create: `internal/agent/disks_test.go`

**Interfaces:**
- Consumes: `agent.CommandRunner` (existant, `internal/agent/systemd.go`) et `framev1beta1.ObservedDisk{Path, SerialNumber, SizeGB, Occupancy}` (tâche 3).
- Produces: `agent.ObserveDisks(runner CommandRunner) ([]framev1beta1.ObservedDisk, error)` — la tâche 5 l'appelle.

- [ ] **Step 1: Écrire les tests (ils doivent échouer)**

Créer `internal/agent/disks_test.go` :

```go
package agent

import (
	"errors"
	"testing"
)

// fakeRunner returns a canned lsblk payload and records the argv it was
// given, so the test can assert both the parsing and the invocation.
type fakeRunner struct {
	out  string
	err  error
	name string
	args []string
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	f.name = name
	f.args = args
	return f.out, f.err
}

// lsblkG9 is trimmed from the ML350 G9: three 300 GB disks, four 1200 GB
// disks, and no 1000 GB disk at all — the MM1000GFJTE the BMC reports is
// masked by residual logical-unit metadata and never reaches the kernel.
// sdc carries a Ceph OSD, sda is mounted, sdb is untouched.
const lsblkG9 = `{
  "blockdevices": [
    {"name":"sda","path":"/dev/sda","serial":"S420YJWS0000K6319L3R","size":300000000000,"type":"disk","fstype":null,"mountpoint":null,
     "children":[{"name":"sda1","path":"/dev/sda1","serial":null,"size":299000000000,"type":"part","fstype":"ext4","mountpoint":"/"}]},
    {"name":"sdb","path":"/dev/sdb","serial":"S421NQ0S0000K645A8C8","size":300000000000,"type":"disk","fstype":null,"mountpoint":null},
    {"name":"sdc","path":"/dev/sdc","serial":"KZK245ZG","size":1200000000000,"type":"disk","fstype":"ceph_bluestore","mountpoint":null}
  ]
}`

func TestObserveDisksJoinsOnSerialAndReportsOccupancy(t *testing.T) {
	r := &fakeRunner{out: lsblkG9}
	disks, err := ObserveDisks(r)
	if err != nil {
		t.Fatalf("ObserveDisks: %v", err)
	}
	if len(disks) != 3 {
		t.Fatalf("got %d disks, want 3 (partitions are not disks)", len(disks))
	}

	by := map[string]int{}
	for i, d := range disks {
		by[d.SerialNumber] = i
	}

	sda := disks[by["S420YJWS0000K6319L3R"]]
	if sda.Occupancy != "mounted" {
		t.Errorf("sda Occupancy = %q, want mounted (a child is mounted at /)", sda.Occupancy)
	}
	if sda.SizeGB != 300 {
		t.Errorf("sda SizeGB = %d, want 300", sda.SizeGB)
	}

	sdb := disks[by["S421NQ0S0000K645A8C8"]]
	if sdb.Occupancy != "free" {
		t.Errorf("sdb Occupancy = %q, want free", sdb.Occupancy)
	}

	sdc := disks[by["KZK245ZG"]]
	if sdc.Occupancy != "ceph-osd" {
		t.Errorf("sdc Occupancy = %q, want ceph-osd", sdc.Occupancy)
	}
}

// TestObserveDisksNeverReturnsAnEmptySerial is the guard behind guard 1 of
// the spec: FrameDiskClaim compares a hand-typed serial against these
// entries, and two empty strings are equal. A disk whose serial lsblk could
// not read must not enter the list wearing "" — it would match any claim
// that also left the field blank, and that is how a disk gets wiped with no
// confirmation.
func TestObserveDisksNeverReturnsAnEmptySerial(t *testing.T) {
	r := &fakeRunner{out: `{"blockdevices":[
	  {"name":"sda","path":"/dev/sda","serial":null,"size":300000000000,"type":"disk","fstype":null,"mountpoint":null},
	  {"name":"sdb","path":"/dev/sdb","serial":"KZK39VSH","size":1200000000000,"type":"disk","fstype":null,"mountpoint":null}
	]}`}
	disks, err := ObserveDisks(r)
	if err != nil {
		t.Fatalf("ObserveDisks: %v", err)
	}
	for _, d := range disks {
		if d.SerialNumber == "" {
			t.Fatalf("disk %s entered the inventory with an empty serial", d.Path)
		}
	}
	if len(disks) != 1 {
		t.Fatalf("got %d disks, want 1 (the serial-less disk is dropped)", len(disks))
	}
	if disks[0].SerialNumber != "KZK39VSH" {
		t.Fatalf("kept the wrong disk: %q", disks[0].SerialNumber)
	}
}

func TestObserveDisksSurfacesRunnerFailure(t *testing.T) {
	r := &fakeRunner{err: errors.New("nsenter: permission denied")}
	if _, err := ObserveDisks(r); err == nil {
		t.Fatal("want an error when lsblk cannot run; a failed read must not look like a node with no disks")
	}
}

func TestObserveDisksAsksForTheFieldsItParses(t *testing.T) {
	r := &fakeRunner{out: `{"blockdevices":[]}`}
	if _, err := ObserveDisks(r); err != nil {
		t.Fatalf("ObserveDisks: %v", err)
	}
	if r.name != "lsblk" {
		t.Fatalf("ran %q, want lsblk", r.name)
	}
	joined := ""
	for _, a := range r.args {
		joined += a + " "
	}
	for _, want := range []string{"-J", "SERIAL", "PATH", "FSTYPE", "MOUNTPOINT", "SIZE", "TYPE"} {
		if !contains(joined, want) {
			t.Errorf("argv %q does not request %s, but the parser reads it", joined, want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Lancer les tests, vérifier qu'ils échouent**

Run: `go test ./internal/agent/ -run TestObserveDisks -v`
Expected: échec de compilation — `undefined: ObserveDisks`.

- [ ] **Step 3: Implémenter**

Créer `internal/agent/disks.go` :

```go
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

package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// lsblkColumns are the columns ObserveDisks parses. They are listed here,
// once, so the argv and the parser cannot drift apart — a column dropped
// from the request but still read from the JSON yields a zero value that
// looks exactly like a disk with nothing on it.
const lsblkColumns = "NAME,PATH,SERIAL,SIZE,TYPE,FSTYPE,MOUNTPOINT"

type lsblkDevice struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Serial     *string       `json:"serial"`
	Size       int64         `json:"size"`
	Type       string        `json:"type"`
	FSType     *string       `json:"fstype"`
	MountPoint *string       `json:"mountpoint"`
	Children   []lsblkDevice `json:"children"`
}

// ObserveDisks reads the node's block devices through runner and returns one
// entry per whole disk.
//
// Three rules, each with a test:
//
//  1. Only type "disk" is returned. A partition is not a thing a
//     FrameDiskClaim can claim.
//  2. A disk with no serial is dropped, not returned with an empty one.
//     FrameDiskClaim's first guard compares a hand-typed serial against
//     these entries, and two empty strings are equal — an entry wearing ""
//     would match a claim that also left the field blank.
//  3. A runner failure is an error, never an empty list. "I could not
//     look" and "there is nothing there" are the two answers that must
//     never be confused: the second authorises a wipe.
func ObserveDisks(runner CommandRunner) ([]framev1beta1.ObservedDisk, error) {
	out, err := runner.Run("lsblk", "-J", "-b", "-o", lsblkColumns)
	if err != nil {
		return nil, fmt.Errorf("running lsblk: %w", err)
	}

	var payload struct {
		BlockDevices []lsblkDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}

	disks := make([]framev1beta1.ObservedDisk, 0, len(payload.BlockDevices))
	for _, dev := range payload.BlockDevices {
		if dev.Type != "disk" {
			continue
		}
		serial := ""
		if dev.Serial != nil {
			serial = strings.TrimSpace(*dev.Serial)
		}
		if serial == "" {
			continue
		}
		disks = append(disks, framev1beta1.ObservedDisk{
			Path:         dev.Path,
			SerialNumber: serial,
			SizeGB:       int32(dev.Size / 1_000_000_000),
			Occupancy:    occupancyOf(dev),
		})
	}
	return disks, nil
}

// occupancyOf says what is using the disk, and errs towards "occupied".
// FrameDiskClaim's third guard fails closed on anything that is not "free",
// so a signature this function does not recognise must never fall through
// to "free": the default branch returns "in-use" precisely because an
// unknown filesystem is still a filesystem.
func occupancyOf(dev lsblkDevice) string {
	if dev.MountPoint != nil && *dev.MountPoint != "" {
		return "mounted"
	}
	for _, child := range dev.Children {
		if child.MountPoint != nil && *child.MountPoint != "" {
			return "mounted"
		}
	}

	fstype := ""
	if dev.FSType != nil {
		fstype = *dev.FSType
	}
	switch {
	case fstype == "":
		if len(dev.Children) > 0 {
			return "partitioned"
		}
		return "free"
	case strings.HasPrefix(fstype, "ceph"):
		return "ceph-osd"
	case fstype == "LVM2_member":
		return "lvm-pv"
	default:
		return "in-use"
	}
}
```

- [ ] **Step 4: Lancer les tests, vérifier qu'ils passent**

Run: `go test ./internal/agent/ -v`
Expected: PASS (le type `ObservedDisk` vient de la tâche 3, déjà faite).

- [ ] **Step 5: Preuve par mutation de la règle du sérial vide**

Retirer le bloc `if serial == "" { continue }`, lancer `go test ./internal/agent/ -run TestObserveDisksNeverReturnsAnEmptySerial -v`, montrer le rouge, remettre. Consigner.

- [ ] **Step 6: Preuve par mutation du repli d'occupation**

Remplacer `default: return "in-use"` par `default: return "free"`, lancer `go test ./internal/agent/ -run TestObserveDisks -v`, montrer le rouge, remettre. Si aucun test ne rougit, **ajouter le cas manquant** : un disque `"fstype":"xfs"` qui doit ressortir `in-use`. Un repli non couvert est le défaut que ce lot existe pour ne pas reproduire.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/disks.go internal/agent/disks_test.go
git commit --no-verify -m "feat(agent): lire les disques du noeud, sans jamais rendre un serial vide"
```

---

---

### Task 5: L'agent publie ses disques sans écraser le contrôleur

`FrameMachine.status` a maintenant **deux écrivains** : le contrôleur (inventaire BMC, capteurs, journal) et l'agent (disques observés). C'est exactement la situation que `internal/agent/status.go` documente pour `NodeTuning`, et son commentaire nomme le mécanisme : un patch de fusion JSON **remplace un tableau en entier**, donc une copie périmée emporte tout le reste du statut avec elle. Les deux protections y sont décrites et sont reprises ici telles quelles : relire l'objet au moment du patch, et porter une précondition de `resourceVersion`.

L'agent ne connaît que son nom de nœud. La `FrameMachine` correspondante est celle dont `spec.nodeRef` vaut ce nom.

**Files:**
- Create: `internal/agent/diskstatus.go`
- Create: `internal/agent/diskstatus_test.go`
- Modify: `cmd/agent/main.go` (ajouter l'étape à la boucle, et le doc-comment en tête)
- Modify: `deploy/kubernetes/base/node-tuning-agent/daemonset.yaml` + son RBAC (droits `framemachines` get/list/watch et `framemachines/status` patch)

**Interfaces:**
- Consumes: `agent.ObserveDisks` (tâche 4), `storage.Join` (tâche 3).
- Produces: `agent.PatchObservedDisks(ctx context.Context, c client.Client, nodeName string, disks []framev1beta1.ObservedDisk) error`.

- [ ] **Step 1: Écrire le test de non-écrasement (il doit échouer)**

Ce test vit dans la suite envtest du contrôleur, pas dans `internal/agent`, pour la raison que `status.go` donne : il faut que l'écriture réelle de l'agent croise l'écriture réelle du contrôleur. L'écrire en bloc Ginkgo dans `internal/controller/frame/framemachine_controller_test.go` :

```go
It("l'agent ecrit ses disques sans effacer l'inventaire BMC", func(ctx SpecContext) {
	m := &framev1beta1.FrameMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "two-writers", Namespace: "default"},
		Spec: framev1beta1.FrameMachineSpec{
			BMC:     framev1beta1.BMCSpec{Address: "https://bmc.invalid"},
			NodeRef: "node-two-writers",
		},
	}
	Expect(k8sClient.Create(ctx, m)).To(Succeed())

	// The controller's half.
	m.Status.Inventory = &framev1beta1.MachineInventory{
		Model:  "ProLiant ML350 Gen9",
		Drives: []framev1beta1.DriveInfo{{SerialNumber: "W4722RRA", Location: "2I:6:8", SizeGB: 1000}},
	}
	m.Status.PowerState = "On"
	Expect(k8sClient.Status().Update(ctx, m)).To(Succeed())

	// The agent's half, written from a stale copy on purpose: this is the
	// shape that silently reverted everything in the NodeTuning lot.
	Expect(agent.PatchObservedDisks(ctx, k8sClient, "node-two-writers",
		[]framev1beta1.ObservedDisk{{Path: "/dev/sdc", SerialNumber: "KZK245ZG", SizeGB: 1200, Occupancy: "free"}},
	)).To(Succeed())

	var back framev1beta1.FrameMachine
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(m), &back)).To(Succeed())

	// Both halves survive.
	Expect(back.Status.PowerState).To(Equal("On"))
	Expect(back.Status.Inventory).NotTo(BeNil())
	Expect(back.Status.Inventory.Drives).To(HaveLen(1), "the agent's patch replaced the BMC drive list")
	Expect(back.Status.Storage).NotTo(BeNil())
	Expect(back.Status.Storage.Observed).To(HaveLen(1))
	Expect(back.Status.Storage.ObservedAt).NotTo(BeNil())

	// And the join ran: the BMC lists a disk the node does not.
	Expect(back.Status.Storage.Divergences).To(HaveLen(2))
})

It("refuse d'ecrire quand aucune FrameMachine ne nomme ce noeud", func(ctx SpecContext) {
	err := agent.PatchObservedDisks(ctx, k8sClient, "node-that-no-machine-claims",
		[]framev1beta1.ObservedDisk{{Path: "/dev/sda", SerialNumber: "X1", SizeGB: 1, Occupancy: "free"}})
	Expect(err).To(HaveOccurred())
	Expect(err.Error()).To(ContainSubstring("no FrameMachine"))
})
```

- [ ] **Step 2: Lancer, vérifier l'échec**

Run: `make test`
Expected: échec de compilation — `undefined: agent.PatchObservedDisks`.

- [ ] **Step 3: Implémenter**

Créer `internal/agent/diskstatus.go` :

```go
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

package agent

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/storage"
)

// PatchObservedDisks writes disks into the status of whichever FrameMachine
// names nodeName in spec.nodeRef, and recomputes the divergences against
// that machine's BMC drive list.
//
// It writes status.storage and nothing else. FrameMachine.status has two
// writers — this one and the machine controller — and a CRD status patch is
// a JSON merge patch, which replaces an array wholesale. The same two
// mechanisms as PatchObserved are therefore load-bearing here, and neither
// is sufficient alone:
//
//  1. The object is re-read inside the retry loop rather than patched from
//     a caller's copy. The caller's copy comes from the top of a tick that
//     spends seconds in nsenter running lsblk; a patch built from it
//     reverts every field the controller wrote in between — power state,
//     sensors, event log, the whole inventory.
//
//  2. The patch carries a resourceVersion precondition, so a controller
//     write landing between the read and the patch is a 409 that
//     RetryOnConflict redoes, not a silent revert.
//
// A node no FrameMachine claims is an error, never a silent no-op: a
// machine registered without a nodeRef is a misconfiguration the operator
// has to see, and an agent that swallowed it would leave the disks screen
// permanently empty with nothing to explain why.
func PatchObservedDisks(
	ctx context.Context,
	c client.Client,
	nodeName string,
	disks []framev1beta1.ObservedDisk,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var machines framev1beta1.FrameMachineList
		if err := c.List(ctx, &machines); err != nil {
			return fmt.Errorf("listing FrameMachines: %w", err)
		}

		var target *framev1beta1.FrameMachine
		for i := range machines.Items {
			if machines.Items[i].Spec.NodeRef == nodeName {
				target = &machines.Items[i]
				break
			}
		}
		if target == nil {
			return fmt.Errorf("no FrameMachine has spec.nodeRef %q", nodeName)
		}

		base := target.DeepCopy()
		var bmcDrives []framev1beta1.DriveInfo
		if target.Status.Inventory != nil {
			bmcDrives = target.Status.Inventory.Drives
		}
		now := metav1.Now()
		target.Status.Storage = &framev1beta1.MachineStorage{
			Observed:    disks,
			Divergences: storage.Join(bmcDrives, disks),
			ObservedAt:  &now,
		}

		return c.Status().Patch(ctx, target, client.MergeFromWithOptions(
			base, client.MergeFromWithOptimisticLock{},
		))
	})
}
```

Vérifier le nom exact du champ `NodeRef` dans `FrameMachineSpec` avant d'écrire la boucle : `grep -n 'NodeRef' api/frame/v1beta1/framemachine_types.go`.

- [ ] **Step 4: Brancher dans la boucle de l'agent**

Dans `cmd/agent/main.go`, après l'étape 5 existante (`agent.Observe` / `agent.PatchObserved`), ajouter :

```go
		// 6. reads the node's own block devices and publishes them, with
		//    the divergences against the BMC's list, onto the FrameMachine
		//    that names this node. A node no machine claims logs and moves
		//    on: the tuning half of this loop must keep running on a
		//    cluster where FrameMachine is not used at all.
		disks, err := agent.ObserveDisks(agent.HostCommandRunner{})
		if err != nil {
			log.Error(err, "reading node disks")
		} else if err := agent.PatchObservedDisks(ctx, kc, nodeName, disks); err != nil {
			log.Error(err, "publishing node disks")
		}
```

Adapter `log` au logger réellement utilisé dans le fichier (`grep -n 'slog\|logger\|log\.' cmd/agent/main.go | head`), et ajouter l'étape 6 au doc-comment en tête de fichier.

- [ ] **Step 5: Étendre le RBAC de l'agent**

Dans le RBAC du DaemonSet (`grep -rn 'node-tuning-agent' deploy/kubernetes/base/ | head` puis lire le `rbac.yaml` voisin), ajouter :

```yaml
- apiGroups: ["frame.plume-labs.io"]
  resources: ["framemachines"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["frame.plume-labs.io"]
  resources: ["framemachines/status"]
  verbs: ["get", "patch", "update"]
```

- [ ] **Step 6: Lancer les tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 7: Preuve par mutation de la précondition**

Remplacer le `client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})` par `client.MergeFrom(base)` **et** construire le patch depuis une copie lue avant l'écriture du contrôleur (déplacer le `List` hors du `RetryOnConflict`). Lancer `make test`, montrer le test « sans effacer l'inventaire BMC » rouge, remettre. Si le test ne rougit pas, c'est que la fenêtre n'est pas reproduite : ajouter au test une écriture du contrôleur entre le `List` et le `Patch`, et recommencer. Un test qui ne distingue pas les deux implémentations ne prouve rien.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/ cmd/agent/ deploy/kubernetes/base/ internal/controller/frame/
git commit --no-verify -m "feat(agent): publier les disques du noeud sans ecraser l'inventaire BMC"
```

---

### Task 6: `FrameStorage` — le type et sa garde d'adoption

L'entrée déclarative de §2.1. Le point dur n'est pas le CRD : c'est que **Frame n'adopte jamais une StorageClass existante implicitement**. Le cluster porte trois classes (`local-path` par défaut, `ceph-rbd`, `ceph-bucket`) et dix-neuf PVC qui en dépendent ; une entrée créée par erreur sur `ceph-rbd` ne doit pas donner à Frame la propriété d'une classe qui porte seize volumes en production.

**Files:**
- Create: `api/frame/v1beta1/framestorage_types.go`
- Create: `internal/webhook/frame/v1beta1/framestorage_webhook.go`
- Create: `internal/webhook/frame/v1beta1/framestorage_webhook_test.go`
- Modify: `cmd/main.go` (enregistrer le webhook)
- Test: `internal/controller/frame/framestorage_v1beta1_schema_test.go`

**Interfaces:**
- Consumes: rien.
- Produces: `framev1beta1.FrameStorage` avec `Spec{Type, Content []string, StorageClassName, AdoptExisting bool, Nodes []string}` et `Status{Shared bool, Phase, Capacity StorageCapacity{Usable, Used}, Claims ClaimCounts{Total, Labelled}, Conditions}`.

- [ ] **Step 1: Écrire le type**

Créer `api/frame/v1beta1/framestorage_types.go` :

```go
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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FrameStorageSpec declares one storage entry, in the sense Proxmox VE gives
// the word: a typed, named place where volumes of declared content types can
// live, available on some set of nodes.
type FrameStorageSpec struct {
	// Type is what backs this entry. NFS and iSCSI are deliberately out of
	// this lot: neither exists in the park, and a backend nobody runs is a
	// backend nobody tests.
	// +kubebuilder:validation:Enum=ceph-rbd;ceph-bucket;local-path
	Type string `json:"type"`

	// Content is what may be stored here — a list, not a single value: a
	// Ceph pool legitimately holds both workload volumes and model
	// artifacts, and forcing a choice would just produce a second entry on
	// the same pool.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:items:Enum=workload;model;backup;artifact;scratch
	Content []string `json:"content"`

	// StorageClassName is the StorageClass this entry owns or adopts.
	// +kubebuilder:validation:MaxLength=253
	StorageClassName string `json:"storageClassName"`

	// AdoptExisting must be set explicitly for an entry naming a
	// StorageClass Frame did not create. Without it, admission refuses:
	// the cluster's existing classes carry production volumes, and an
	// entry that silently took ownership of one would delete it on its own
	// deletion. An adopted entry owns nothing and deletes nothing.
	// +optional
	AdoptExisting bool `json:"adoptExisting,omitempty"`

	// Nodes empty means every node. A non-empty list names the nodes where
	// this entry is available — the local/shared distinction made concrete.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Nodes []string `json:"nodes,omitempty"`
}

// StorageCapacity reports usable space first. Raw is optional and never
// stands alone: the park's capacity incident came from reading raw numbers
// on a pool whose replication divides them by three.
type StorageCapacity struct {
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Usable string `json:"usable,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Used string `json:"used,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Raw string `json:"raw,omitempty"`
}

// ClaimCounts is the gap between the policy and the cluster: how many PVCs
// use this entry's class, and how many of them carry the usage label the
// content-type webhook selects on. A large Total with a zero Labelled is
// the normal state on the day the webhook lands, and is meant to be read,
// not fixed automatically.
type ClaimCounts struct {
	Total    int32 `json:"total,omitempty"`
	Labelled int32 `json:"labelled,omitempty"`
}

type FrameStorageStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Shared is derived from Type, never declared. ceph-* entries are
	// shared; local-path is not. It lives in status because a user who
	// could write it could declare a local disk shared and have Frame
	// believe it.
	// +optional
	Shared bool `json:"shared,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=Ready;Degraded;Unknown
	Phase string `json:"phase,omitempty"`

	// +optional
	Capacity *StorageCapacity `json:"capacity,omitempty"`

	// +optional
	Claims *ClaimCounts `json:"claims,omitempty"`

	// Adopted records that this entry did not create its StorageClass, and
	// therefore must never delete it.
	// +optional
	Adopted bool `json:"adopted,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fstor
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=".spec.storageClassName"
// +kubebuilder:printcolumn:name="Shared",type=boolean,JSONPath=".status.shared"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Usable",type=string,JSONPath=".status.capacity.usable"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameStorage is the Schema for the framestorages API.
type FrameStorage struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec FrameStorageSpec `json:"spec,omitempty"`

	// +optional
	Status FrameStorageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameStorageList contains a list of FrameStorage.
type FrameStorageList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameStorage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameStorage{}, &FrameStorageList{})
}
```

- [ ] **Step 2: Écrire les tests du webhook d'adoption (ils doivent échouer)**

Créer `internal/webhook/frame/v1beta1/framestorage_webhook_test.go`. Regarder d'abord comment `frameuser_webhook_test.go` construit son client de test (`grep -n 'fake.NewClientBuilder\|NewFakeClient' internal/webhook/frame/v1beta1/*_test.go`) et suivre le même montage.

```go
package v1beta1

import (
	"context"
	"strings"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func validatorWith(objs ...runtime.Object) *FrameStorageCustomValidator {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = framev1beta1.AddToScheme(s)
	return &FrameStorageCustomValidator{
		Client: fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build(),
	}
}

func entry(class string, adopt bool) *framev1beta1.FrameStorage {
	return &framev1beta1.FrameStorage{
		ObjectMeta: metav1.ObjectMeta{Name: "e"},
		Spec: framev1beta1.FrameStorageSpec{
			Type:             "ceph-rbd",
			Content:          []string{"workload"},
			StorageClassName: class,
			AdoptExisting:    adopt,
		},
	}
}

func foreignClass(name string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Provisioner: "rook-ceph.rbd.csi.ceph.com",
	}
}

// TestRefusesAnExistingClassWithoutOptIn is the guard that protects the
// sixteen ceph-rbd PVCs in the cluster: an entry naming a class Frame did
// not create takes ownership of it, and an owned class is deleted with its
// entry.
func TestRefusesAnExistingClassWithoutOptIn(t *testing.T) {
	v := validatorWith(foreignClass("ceph-rbd"))
	_, err := v.ValidateCreate(context.Background(), entry("ceph-rbd", false))
	if err == nil {
		t.Fatal("want a refusal for an existing StorageClass with adoptExisting unset")
	}
	if !strings.Contains(err.Error(), "adoptExisting") {
		t.Errorf("refusal does not name the field that unblocks it: %v", err)
	}
	if !strings.Contains(err.Error(), "ceph-rbd") {
		t.Errorf("refusal does not name the class: %v", err)
	}
}

func TestAcceptsAnExistingClassWithOptIn(t *testing.T) {
	v := validatorWith(foreignClass("ceph-rbd"))
	if _, err := v.ValidateCreate(context.Background(), entry("ceph-rbd", true)); err != nil {
		t.Fatalf("adoptExisting: true must be accepted: %v", err)
	}
}

func TestAcceptsAClassThatDoesNotExistYet(t *testing.T) {
	v := validatorWith()
	if _, err := v.ValidateCreate(context.Background(), entry("frame-scratch", false)); err != nil {
		t.Fatalf("a class Frame is about to create needs no opt-in: %v", err)
	}
}

// TestRefusalDoesNotDependOnTheClassBeingCephRBD: the guard is about
// ownership, not about a hardcoded name. A test that only ever passes
// "ceph-rbd" cannot tell a real lookup from a string comparison.
func TestRefusalDoesNotDependOnTheClassBeingCephRBD(t *testing.T) {
	v := validatorWith(foreignClass("local-path"))
	e := entry("local-path", false)
	e.Spec.Type = "local-path"
	if _, err := v.ValidateCreate(context.Background(), e); err == nil {
		t.Fatal("want a refusal for local-path too")
	}
}

// TestSharedIsNotDeclarable: status.shared is derived from the type. A
// spec that could set it would let a user declare a local disk shared.
func TestSharedIsNotSettableFromSpec(t *testing.T) {
	// The compile-time guarantee is that FrameStorageSpec has no Shared
	// field; this test states it so a future addition has to delete a test
	// rather than quietly add a field.
	var spec framev1beta1.FrameStorageSpec
	_ = spec
	if got := sharedForType("ceph-rbd"); !got {
		t.Error("ceph-rbd must be shared")
	}
	if got := sharedForType("local-path"); got {
		t.Error("local-path must not be shared")
	}
}
```

- [ ] **Step 3: Lancer, vérifier l'échec**

Run: `go test ./internal/webhook/frame/v1beta1/ -run 'TestRefuses|TestAccepts|TestShared' -v`
Expected: échec de compilation — `undefined: FrameStorageCustomValidator`.

- [ ] **Step 4: Implémenter le webhook**

Créer `internal/webhook/frame/v1beta1/framestorage_webhook.go` :

```go
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

package v1beta1

import (
	"context"
	"fmt"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// SetupFrameStorageWebhookWithManager registers the webhook for FrameStorage.
func SetupFrameStorageWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &framev1beta1.FrameStorage{}).
		WithValidator(&FrameStorageCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-frame-plume-labs-io-v1beta1-framestorage,mutating=false,failurePolicy=fail,sideEffects=None,groups=frame.plume-labs.io,resources=framestorages,verbs=create;update,versions=v1beta1,name=vframestorage-v1beta1.kb.io,admissionReviewVersions=v1

type FrameStorageCustomValidator struct {
	Client client.Client
}

func (v *FrameStorageCustomValidator) ValidateCreate(ctx context.Context, obj *framev1beta1.FrameStorage) (admission.Warnings, error) {
	return nil, v.validateAdoption(ctx, obj)
}

func (v *FrameStorageCustomValidator) ValidateUpdate(ctx context.Context, _, newObj *framev1beta1.FrameStorage) (admission.Warnings, error) {
	return nil, v.validateAdoption(ctx, newObj)
}

func (v *FrameStorageCustomValidator) ValidateDelete(_ context.Context, _ *framev1beta1.FrameStorage) (admission.Warnings, error) {
	return nil, nil
}

// validateAdoption refuses an entry that names a StorageClass which already
// exists, unless it says so. A Frame-created entry owns its class and
// deletes it with itself; an entry that silently adopted ceph-rbd would
// delete the class sixteen production volumes provision from.
//
// A lookup error other than NotFound is a refusal, not a pass: "I could not
// check whether this class exists" and "this class does not exist" lead to
// opposite decisions, and only one of them is safe.
func (v *FrameStorageCustomValidator) validateAdoption(ctx context.Context, fs *framev1beta1.FrameStorage) error {
	if fs.Spec.AdoptExisting {
		return nil
	}

	var sc storagev1.StorageClass
	err := v.Client.Get(ctx, types.NamespacedName{Name: fs.Spec.StorageClassName}, &sc)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf(
			"cannot determine whether StorageClass %q already exists (%w); refusing rather than risk adopting it",
			fs.Spec.StorageClassName, err,
		)
	}

	return fmt.Errorf(
		"StorageClass %q already exists and was not created by Frame: set spec.adoptExisting to true to reference it. "+
			"An adopted entry owns nothing and deletes nothing; without the flag, Frame would take ownership of a class "+
			"that other volumes already provision from",
		fs.Spec.StorageClassName,
	)
}

// sharedForType derives status.shared from the type. It lives here, next to
// the validator, so nothing anywhere can set it from a spec.
func sharedForType(t string) bool {
	switch t {
	case "ceph-rbd", "ceph-bucket":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 5: Enregistrer le webhook**

Dans `cmd/main.go`, à côté des appels `SetupXWebhookWithManager` existants (`grep -n 'WebhookWithManager' cmd/main.go`), ajouter :

```go
	if err := webhookframev1beta1.SetupFrameStorageWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "FrameStorage")
		os.Exit(1)
	}
```

Respecter le garde-fou conditionnel existant s'il y en a un (les webhooks sont souvent derrière un `if os.Getenv("ENABLE_WEBHOOKS") != "false"`).

- [ ] **Step 6: Lancer les tests**

Run: `make manifests generate && make test`
Expected: PASS.

- [ ] **Step 7: Preuve par mutation de la garde d'adoption**

Remplacer le `case err != nil:` par `case err != nil: return nil` (l'erreur devient un laissez-passer). Écrire un test qui injecte un client dont le `Get` rend une erreur autre que NotFound — `fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{Get: func(...) error { return errors.New("apiserver down") }})` — et vérifier qu'il rougit. Remettre. Si ce test n'existe pas encore, **l'ajouter** : c'est la branche par laquelle une garde passe pour une raison autre que celle qu'elle nomme.

- [ ] **Step 8: Commit**

```bash
git add api/ internal/webhook/ cmd/main.go config/ charts/
git commit --no-verify -m "feat(framestorage): l'entree typee, et le refus d'adopter une classe existante"
```

---

### Task 7: Le contrôleur `FrameStorage`

Il ne détruit rien. Il crée la StorageClass d'une entrée qui n'en adopte pas, rafraîchit la capacité et l'écart de politique, et ne supprime jamais un PV ni un PVC.

**Files:**
- Create: `internal/controller/frame/framestorage_controller.go`
- Create: `internal/controller/frame/framestorage_controller_test.go`
- Modify: `cmd/main.go` (enregistrer le reconciler)

**Interfaces:**
- Consumes: `framev1beta1.FrameStorage` (tâche 6), `sharedForType` — **attention** : la fonction vit dans le paquet webhook ; la déplacer en `api/frame/v1beta1/framestorage_types.go` sous le nom exporté `SharedForType(t string) bool` et l'appeler depuis les deux endroits. Une deuxième copie serait deux vérités.
- Produces: `FrameStorageReconciler`.

- [ ] **Step 1: Déplacer `sharedForType` avant tout le reste**

Couper la fonction de `internal/webhook/frame/v1beta1/framestorage_webhook.go`, la coller dans `api/frame/v1beta1/framestorage_types.go` sous le nom `SharedForType`, et corriger l'appel dans le test de la tâche 6 (`sharedForType` → `framev1beta1.SharedForType`).

Run: `make test` — Expected: PASS, inchangé.

Commit :
```bash
git add api/ internal/webhook/
git commit --no-verify -m "refactor(framestorage): une seule definition de shared, dans l'API"
```

- [ ] **Step 2: Écrire les tests du contrôleur (ils doivent échouer)**

En blocs Ginkgo, dans `internal/controller/frame/framestorage_controller_test.go`. Reprendre l'ossature d'un fichier voisin (`head -40 internal/controller/frame/frameresourcequota_controller_test.go`) pour le `Describe`/`BeforeEach`.

```go
var _ = Describe("FrameStorage controller", func() {
	It("derive shared du type et ne le lit jamais du spec", func(ctx SpecContext) {
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "derived-shared"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-derived-shared", AdoptExisting: false,
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Shared).To(BeTrue())
	})

	It("compte les PVC de sa classe et ceux qui portent l'etiquette", func(ctx SpecContext) {
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "claim-gap"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-claim-gap",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		class := "frame-claim-gap"
		mk := func(name string, labels map[string]string) *corev1.PersistentVolumeClaim {
			return &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &class,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			}
		}
		Expect(k8sClient.Create(ctx, mk("gap-unlabelled-1", nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, mk("gap-unlabelled-2", nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, mk("gap-labelled", map[string]string{"frame.plume-labs.io/usage": "workload"}))).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Claims).NotTo(BeNil())
		Expect(back.Status.Claims.Total).To(Equal(int32(3)))
		Expect(back.Status.Claims.Labelled).To(Equal(int32(1)),
			"the gap between the policy and the cluster is the number that gets displayed")
	})

	It("ne compte pas les PVC d'une autre classe", func(ctx SpecContext) {
		// Without this, Total is just "every PVC in the cluster" and the
		// gap it reports belongs to no entry in particular.
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "class-scoped"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "local-path", Content: []string{"scratch"},
				StorageClassName: "frame-class-scoped",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		other := "some-other-class"
		Expect(k8sClient.Create(ctx, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "default"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &other,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		})).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Claims.Total).To(Equal(int32(0)))
	})

	It("marque une entree adoptee et ne cree pas sa classe", func(ctx SpecContext) {
		Expect(k8sClient.Create(ctx, &storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: "pre-existing"},
			Provisioner: "kubernetes.io/no-provisioner",
		})).To(Succeed())

		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "adopted"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "local-path", Content: []string{"scratch"},
				StorageClassName: "pre-existing", AdoptExisting: true,
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Adopted).To(BeTrue())

		var sc storagev1.StorageClass
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "pre-existing"}, &sc)).To(Succeed())
		// An adopted class keeps no owner reference: an owned object is
		// garbage-collected with its owner, and this one holds volumes.
		Expect(sc.OwnerReferences).To(BeEmpty())
	})
})
```

Le `reconciler` est construit dans le `BeforeEach` du fichier, comme dans les autres suites du paquet : `reconciler = &FrameStorageReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}`.

- [ ] **Step 3: Lancer, vérifier l'échec**

Run: `make test`
Expected: échec de compilation — `undefined: FrameStorageReconciler`.

- [ ] **Step 4: Implémenter le contrôleur**

Créer `internal/controller/frame/framestorage_controller.go` :

```go
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

package frame

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// usageLabel is the label the content-type webhook selects on. It is
// declared once, here, and read by the PVC webhook too: a second literal
// would be a second policy.
const usageLabel = "frame.plume-labs.io/usage"

// storageResyncInterval is how often an entry's capacity and claim counts
// are refreshed. Capacity is polled, not watched: no backend in this lot
// sends an event when a pool fills up.
const storageResyncInterval = 2 * time.Minute

// FrameStorageReconciler keeps a FrameStorage entry's status current.
//
// It creates nothing destructive and deletes nothing at all. In particular
// it never deletes a PersistentVolume or a PersistentVolumeClaim, in any
// branch: an entry is a description of where volumes may live, and removing
// the description must never remove the volumes.
type FrameStorageReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framestorages,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framestorages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framestorages/finalizers,verbs=update
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch

func (r *FrameStorageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fs framev1beta1.FrameStorage
	if err := r.Get(ctx, req.NamespacedName, &fs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	base := fs.DeepCopy()

	fs.Status.Shared = framev1beta1.SharedForType(fs.Spec.Type)
	fs.Status.ObservedGeneration = fs.Generation

	adopted, err := r.reconcileClass(ctx, &fs)
	if err != nil {
		return ctrl.Result{}, err
	}
	fs.Status.Adopted = adopted

	claims, err := r.countClaims(ctx, fs.Spec.StorageClassName)
	if err != nil {
		return ctrl.Result{}, err
	}
	fs.Status.Claims = claims

	if fs.Status.Phase == "" {
		fs.Status.Phase = "Unknown"
	}

	if err := r.Status().Patch(ctx, &fs, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching FrameStorage status: %w", err)
	}
	return ctrl.Result{RequeueAfter: storageResyncInterval}, nil
}

// reconcileClass returns whether this entry adopted its StorageClass.
//
// An adopted class gets no owner reference: an owned object is
// garbage-collected with its owner, and adopting a class means explicitly
// not taking that power over it. A class Frame creates does get one, so an
// entry cleans up exactly what it made.
func (r *FrameStorageReconciler) reconcileClass(ctx context.Context, fs *framev1beta1.FrameStorage) (bool, error) {
	var sc storagev1.StorageClass
	err := r.Get(ctx, types.NamespacedName{Name: fs.Spec.StorageClassName}, &sc)
	switch {
	case err == nil:
		return fs.Spec.AdoptExisting, nil
	case !apierrors.IsNotFound(err):
		return false, fmt.Errorf("reading StorageClass %q: %w", fs.Spec.StorageClassName, err)
	}

	provisioner, ok := provisionerFor(fs.Spec.Type)
	if !ok {
		return false, fmt.Errorf("no provisioner known for type %q", fs.Spec.Type)
	}
	created := &storagev1.StorageClass{
		ObjectMeta:  ctrl.ObjectMeta{Name: fs.Spec.StorageClassName},
		Provisioner: provisioner,
	}
	if err := ctrl.SetControllerReference(fs, created, r.Scheme); err != nil {
		return false, fmt.Errorf("setting owner on StorageClass: %w", err)
	}
	if err := r.Create(ctx, created); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("creating StorageClass %q: %w", fs.Spec.StorageClassName, err)
	}
	return false, nil
}

func provisionerFor(t string) (string, bool) {
	switch t {
	case "ceph-rbd":
		return "rook-ceph.rbd.csi.ceph.com", true
	case "ceph-bucket":
		return "rook-ceph.ceph.rook.io/bucket", true
	case "local-path":
		return "rancher.io/local-path", true
	default:
		return "", false
	}
}

// countClaims reports how many PVCs provision from this entry's class and
// how many of them carry the usage label. The gap between the two is the
// number the screens display: enforcement is opt-in per object, so an
// entry with many claims and no labels is the normal state, not a fault.
func (r *FrameStorageReconciler) countClaims(ctx context.Context, className string) (*framev1beta1.ClaimCounts, error) {
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs); err != nil {
		return nil, fmt.Errorf("listing PersistentVolumeClaims: %w", err)
	}

	counts := &framev1beta1.ClaimCounts{}
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != className {
			continue
		}
		counts.Total++
		if _, ok := pvc.Labels[usageLabel]; ok {
			counts.Labelled++
		}
	}
	return counts, nil
}

func (r *FrameStorageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameStorage{}).
		Named("framestorage").
		Complete(r)
}
```

`ctrl.ObjectMeta` n'existe pas : utiliser `metav1.ObjectMeta` et ajouter l'import. Corriger à l'implémentation ; le compilateur le dira.

- [ ] **Step 5: Enregistrer le reconciler**

Dans `cmd/main.go`, à côté des autres `SetupWithManager` :

```go
	if err := (&framecontroller.FrameStorageReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "FrameStorage")
		os.Exit(1)
	}
```

- [ ] **Step 6: Lancer les tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 6b: La phase et les motifs, pour une entrée `ceph-*`**

`Reconcile` pose aujourd'hui `Phase = "Unknown"` et n'en sort jamais. C'est le volet santé de §6a : l'entrée doit porter l'état **et ses motifs**, faute de quoi l'écran ne peut rendre que ce qu'il rend déjà.

Ajouter le test, en bloc Ginkgo dans le même fichier :

```go
It("rend Degraded avec le motif quand Ceph est en WARN", func(ctx SpecContext) {
	// A CephCluster CR, as rook publishes it. envtest has no rook CRD, so
	// the reconciler reads it unstructured and a missing CRD is not an
	// error — see the reconciler's comment.
	cc := &unstructured.Unstructured{}
	cc.SetGroupVersionKind(schema.GroupVersionKind{Group: "ceph.rook.io", Version: "v1", Kind: "CephCluster"})
	cc.SetName("rook-ceph")
	cc.SetNamespace("default")
	Expect(unstructured.SetNestedMap(cc.Object, map[string]any{
		"health": "HEALTH_WARN",
	}, "status", "ceph")).To(Succeed())

	fs := &framev1beta1.FrameStorage{
		ObjectMeta: metav1.ObjectMeta{Name: "ceph-health"},
		Spec: framev1beta1.FrameStorageSpec{
			Type: "ceph-rbd", Content: []string{"workload"},
			StorageClassName: "frame-ceph-health",
		},
	}
	Expect(k8sClient.Create(ctx, fs)).To(Succeed())

	r := &FrameStorageReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), CephHealth: func(context.Context) (string, []string, error) {
		return "HEALTH_WARN", []string{"1 pool(s) have no replicas configured"}, nil
	}}
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
	Expect(err).NotTo(HaveOccurred())

	var back framev1beta1.FrameStorage
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
	Expect(back.Status.Phase).To(Equal("Degraded"))
	cond := meta.FindStatusCondition(back.Status.Conditions, "Healthy")
	Expect(cond).NotTo(BeNil())
	Expect(cond.Status).To(Equal(metav1.ConditionFalse))
	Expect(cond.Message).To(ContainSubstring("no replicas"),
		"a degraded state without its reason cannot be acted on")
})

It("laisse Unknown une entree non-Ceph", func(ctx SpecContext) {
	fs := &framev1beta1.FrameStorage{
		ObjectMeta: metav1.ObjectMeta{Name: "local-unknown"},
		Spec: framev1beta1.FrameStorageSpec{
			Type: "local-path", Content: []string{"scratch"},
			StorageClassName: "frame-local-unknown",
		},
	}
	Expect(k8sClient.Create(ctx, fs)).To(Succeed())
	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
	Expect(err).NotTo(HaveOccurred())

	var back framev1beta1.FrameStorage
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
	// local-path has no cluster-wide health to report. Claiming Ready
	// would be asserting something nothing measured.
	Expect(back.Status.Phase).To(Equal("Unknown"))
})
```

Puis, dans le reconciler, remplacer le bloc `if fs.Status.Phase == "" { fs.Status.Phase = "Unknown" }` par :

```go
	fs.Status.Phase, err = r.reconcileHealth(ctx, &fs)
	if err != nil {
		return ctrl.Result{}, err
	}
```

et ajouter le champ et la méthode :

```go
// CephHealth reads the Ceph cluster's health and the reasons behind it.
// It is a function field rather than a direct client call so the test can
// drive it without a rook CRD in envtest, and so a cluster with no rook at
// all is a nil field rather than a permanent error.
CephHealth func(ctx context.Context) (health string, reasons []string, err error)

// reconcileHealth returns the entry's phase and sets its Healthy condition.
//
// Only ceph-* entries have a cluster-wide health to report. A local-path
// entry stays Unknown: claiming Ready would assert something nothing
// measured, and this lot exists because of checks that passed for reasons
// other than the ones they named.
func (r *FrameStorageReconciler) reconcileHealth(ctx context.Context, fs *framev1beta1.FrameStorage) (string, error) {
	if !strings.HasPrefix(fs.Spec.Type, "ceph-") || r.CephHealth == nil {
		return "Unknown", nil
	}

	health, reasons, err := r.CephHealth(ctx)
	if err != nil {
		// A health check that could not run leaves the phase Unknown and
		// says why. It never reports Ready: not knowing is not health.
		meta.SetStatusCondition(&fs.Status.Conditions, metav1.Condition{
			Type: "Healthy", Status: metav1.ConditionUnknown, Reason: "CheckFailed",
			Message: fmt.Sprintf("could not read Ceph health: %v", err),
			ObservedGeneration: fs.Generation,
		})
		return "Unknown", nil
	}

	if health == "HEALTH_OK" {
		meta.SetStatusCondition(&fs.Status.Conditions, metav1.Condition{
			Type: "Healthy", Status: metav1.ConditionTrue, Reason: "CephHealthOK",
			Message: health, ObservedGeneration: fs.Generation,
		})
		return "Ready", nil
	}

	msg := health
	if len(reasons) > 0 {
		msg = fmt.Sprintf("%s: %s", health, strings.Join(reasons, "; "))
	}
	meta.SetStatusCondition(&fs.Status.Conditions, metav1.Condition{
		Type: "Healthy", Status: metav1.ConditionFalse, Reason: "CephDegraded",
		Message: msg, ObservedGeneration: fs.Generation,
	})
	return "Degraded", nil
}
```

Dans `cmd/main.go`, brancher `CephHealth` sur une lecture non structurée du `CephCluster` (`status.ceph.health` et `status.ceph.details`), dans le namespace que la configuration d'intégration nomme. Une CRD `ceph.rook.io` absente rend une erreur, que `reconcileHealth` traduit en `Unknown` — jamais en `Ready`.

- [ ] **Step 6c: Disponibilité par nœud**

`spec.nodes` est déclaré (tâche 6) et lu par personne. Ajouter au reconciler la condition qui le rend exploitable :

```go
// Availability says where this entry can be used. An empty spec.nodes
// means everywhere; a non-empty one names the nodes, and the condition
// carries the list so a screen can render it without re-deriving the rule.
avail := "all nodes"
if len(fs.Spec.Nodes) > 0 {
	avail = strings.Join(fs.Spec.Nodes, ", ")
}
meta.SetStatusCondition(&fs.Status.Conditions, metav1.Condition{
	Type: "Available", Status: metav1.ConditionTrue, Reason: "Declared",
	Message: avail, ObservedGeneration: fs.Generation,
})
```

Et le test :

```go
It("porte la disponibilite par noeud dans une condition", func(ctx SpecContext) {
	fs := &framev1beta1.FrameStorage{
		ObjectMeta: metav1.ObjectMeta{Name: "two-nodes"},
		Spec: framev1beta1.FrameStorageSpec{
			Type: "local-path", Content: []string{"scratch"},
			StorageClassName: "frame-two-nodes",
			Nodes:            []string{"w1", "w2"},
		},
	}
	Expect(k8sClient.Create(ctx, fs)).To(Succeed())
	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
	Expect(err).NotTo(HaveOccurred())

	var back framev1beta1.FrameStorage
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
	cond := meta.FindStatusCondition(back.Status.Conditions, "Available")
	Expect(cond).NotTo(BeNil())
	Expect(cond.Message).To(Equal("w1, w2"))
})

It("dit 'all nodes' quand la liste est vide", func(ctx SpecContext) {
	fs := &framev1beta1.FrameStorage{
		ObjectMeta: metav1.ObjectMeta{Name: "all-nodes"},
		Spec: framev1beta1.FrameStorageSpec{
			Type: "ceph-rbd", Content: []string{"workload"},
			StorageClassName: "frame-all-nodes",
		},
	}
	Expect(k8sClient.Create(ctx, fs)).To(Succeed())
	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
	Expect(err).NotTo(HaveOccurred())

	var back framev1beta1.FrameStorage
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
	Expect(meta.FindStatusCondition(back.Status.Conditions, "Available").Message).To(Equal("all nodes"))
})
```

Run: `make test` — Expected: PASS.

- [ ] **Step 7: Preuve par mutation du comptage par classe**

Retirer le `if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != className { continue }`, lancer `make test`, montrer « ne compte pas les PVC d'une autre classe » rouge, remettre. Consigner.

- [ ] **Step 8: Commit**

```bash
git add internal/controller/frame/ cmd/main.go config/rbac/ charts/
git commit --no-verify -m "feat(framestorage): statut derive, classe creee ou adoptee, jamais un PVC supprime"
```

---

### Task 8: `FrameDiskClaim` — le seul geste qui détruit

Cinq gardes, chacune tirée d'un défaut réel du lot provisionnement, chacune livrée avec sa preuve par mutation. C'est la tâche la plus dangereuse du lot : elle efface des données sur une vraie machine.

**Files:**
- Create: `api/frame/v1beta1/framediskclaim_types.go`
- Create: `internal/controller/frame/framediskclaim_controller.go`
- Create: `internal/controller/frame/framediskclaim_controller_test.go`
- Modify: `cmd/main.go`

**Interfaces:**
- Consumes: `framev1beta1.ObservedDisk` (tâche 3), `FrameMachine.status.storage` (tâche 5).
- Produces: `framev1beta1.FrameDiskClaim`, `FrameDiskClaimReconciler`.

- [ ] **Step 1: Écrire le type**

Créer `api/frame/v1beta1/framediskclaim_types.go` :

```go
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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FrameDiskClaimSpec claims one physical disk for one purpose, destroying
// whatever is on it. It is modelled on FrameInstall: a one-shot act with a
// terminal phase, not a long-lived declaration that gets reconciled back
// into place. Re-running it means creating a new object.
type FrameDiskClaimSpec struct {
	// MachineRef names the FrameMachine whose disk this is.
	MachineRef LocalObjectReference `json:"machineRef"`

	// ByIDPath is the /dev/disk/by-id path of the disk. Never an sdX name:
	// on the machine this design was written against, sdX ordering changed
	// on all three boots, so a claim naming sdb would have wiped a
	// different disk on each one.
	// +kubebuilder:validation:Pattern=`^/dev/disk/by-id/.+`
	// +kubebuilder:validation:MaxLength=512
	ByIDPath string `json:"byIDPath"`

	// Serial is the disk's serial number, retyped by hand. It is matched
	// against what the node's agent reported, and the match is the
	// confirmation: a path can be stale, a serial the operator read off
	// the object they intend to destroy cannot.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Serial string `json:"serial"`

	// Destination is what the disk becomes. LVM and manual partitioning
	// are deliberately absent: each is a different destructive operation
	// with its own failure modes, and this lot ships the two that the park
	// actually needs.
	// +kubebuilder:validation:Enum=wipe;ceph-osd
	Destination string `json:"destination"`
}

type LocalObjectReference struct {
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

type FrameDiskClaimStatus struct {
	// +optional
	// +kubebuilder:validation:Enum=Pending;Claiming;Ready;Failed
	Phase string `json:"phase,omitempty"`

	// ClaimUID is written onto the machine before the destructive work
	// begins and read back before it is repeated. It lives on the node, not
	// in this controller's memory: a manager restart replayed an entire
	// destructive sequence in the previous lot because the only record that
	// it had already run was process-local.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	ClaimUID string `json:"claimUID,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`

	// +optional
	PhaseSince *metav1.Time `json:"phaseSince,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fdc
// +kubebuilder:printcolumn:name="Machine",type=string,JSONPath=".spec.machineRef.name"
// +kubebuilder:printcolumn:name="Serial",type=string,JSONPath=".spec.serial"
// +kubebuilder:printcolumn:name="Destination",type=string,JSONPath=".spec.destination"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameDiskClaim is the Schema for the framediskclaims API.
type FrameDiskClaim struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec FrameDiskClaimSpec `json:"spec,omitempty"`
	// +optional
	Status FrameDiskClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameDiskClaimList contains a list of FrameDiskClaim.
type FrameDiskClaimList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameDiskClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameDiskClaim{}, &FrameDiskClaimList{})
}
```

Si un type `LocalObjectReference` existe déjà dans le paquet (`grep -n 'LocalObjectReference' api/frame/v1beta1/*.go`), le réutiliser et supprimer la définition ci-dessus.

- [ ] **Step 2: Écrire les tests des cinq gardes (ils doivent échouer)**

`internal/controller/frame/framediskclaim_controller_test.go`, en blocs Ginkgo. Chaque `It` nomme la garde et le défaut dont elle vient.

```go
var _ = Describe("FrameDiskClaim controller", func() {
	// machineWithDisks creates a FrameMachine whose agent has reported.
	machineWithDisks := func(ctx SpecContext, name string, disks []framev1beta1.ObservedDisk) *framev1beta1.FrameMachine {
		m := &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{Address: "https://bmc.invalid"}},
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

	// GARDE 1 — le numéro de série retapé doit correspondre.
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

	// GARDE 1, la vraie — deux chaînes vides sont égales, et c'est comme ça
	// qu'un disque se fait effacer sans confirmation.
	It("refuse quand le serial est vide des deux cotes", func(ctx SpecContext) {
		machineWithDisks(ctx, "g1-blank", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-B", SerialNumber: "", SizeGB: 1200, Occupancy: "free"},
		})
		c := claim("g1-blank", "/dev/disk/by-id/scsi-B", "")
		// The CRD's MinLength=1 already refuses this at admission, which is
		// the outer layer; the controller must refuse it too, because a
		// reconciler is reached by objects written before a schema change
		// and by any path that bypasses admission.
		err := k8sClient.Create(ctx, c)
		if err == nil {
			_, rerr := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
			Expect(rerr).NotTo(HaveOccurred())
			var back framev1beta1.FrameDiskClaim
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(c), &back)).To(Succeed())
			Expect(back.Status.Phase).To(Equal("Failed"))
		}
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

	// GARDE 3 — refus fermé sur l'occupation.
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

	// GARDE 3, la moitié qu'on oublie — ne pas savoir n'est pas une
	// autorisation.
	It("refuse quand l'agent n'a jamais rapporte", func(ctx SpecContext) {
		m := &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "g3-silent", Namespace: "default"},
			Spec:       framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{Address: "https://bmc.invalid"}},
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

	It("refuse quand le rapport de l'agent est perime", func(ctx SpecContext) {
		stale := metav1.NewTime(time.Now().Add(-2 * time.Hour))
		m := &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "g3-stale", Namespace: "default"},
			Spec:       framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{Address: "https://bmc.invalid"}},
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

	// GARDE 4 — le marqueur d'unicité n'est pas dans la mémoire du process.
	It("ne rejoue pas le geste destructif apres un redemarrage du manager", func(ctx SpecContext) {
		machineWithDisks(ctx, "g4", []framev1beta1.ObservedDisk{
			{Path: "/dev/disk/by-id/scsi-G", SerialNumber: "S421NTCK0000M643K5U4", SizeGB: 300, Occupancy: "free"},
		})
		c := claim("g4", "/dev/disk/by-id/scsi-G", "S421NTCK0000M643K5U4")
		Expect(k8sClient.Create(ctx, c)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())
		Expect(wipes.Count()).To(Equal(1))

		// A fresh reconciler is a restarted manager: nothing in memory
		// survives, only what was written down.
		fresh := &FrameDiskClaimReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Wiper: wipes}
		_, err = fresh.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)})
		Expect(err).NotTo(HaveOccurred())
		Expect(wipes.Count()).To(Equal(1), "the destructive act ran twice across a manager restart")
	})

	// GARDE 5 — une erreur de nettoyage n'est pas avalée.
	It("ne passe pas Ready quand le nettoyage echoue apres le travail utile", func(ctx SpecContext) {
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
	})
})
```

Le `wipes` est une doublure déclarée dans le `BeforeEach` du fichier :

```go
// fakeWiper records every destructive call and can be told to fail its
// cleanup step, so guard 5 has something to catch.
type fakeWiper struct {
	mu          sync.Mutex
	calls       int
	cleanupErr  error
	markers     map[string]string
}

func newFakeWiper() *fakeWiper { return &fakeWiper{markers: map[string]string{}} }

func (f *fakeWiper) Count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *fakeWiper) FailCleanup(err error) { f.mu.Lock(); defer f.mu.Unlock(); f.cleanupErr = err }

// ReadMarker returns the claim UID recorded on the machine for byIDPath, or
// "" if none. This is the on-machine record guard 4 relies on.
func (f *fakeWiper) ReadMarker(_ context.Context, machine, byIDPath string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.markers[machine+"|"+byIDPath], nil
}

func (f *fakeWiper) Claim(_ context.Context, machine, byIDPath, claimUID, destination string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markers[machine+"|"+byIDPath] = claimUID
	f.calls++
	return f.cleanupErr
}
```

- [ ] **Step 3: Lancer, vérifier l'échec**

Run: `make test`
Expected: échec de compilation — `undefined: FrameDiskClaimReconciler`.

- [ ] **Step 4: Implémenter le contrôleur**

Créer `internal/controller/frame/framediskclaim_controller.go`. Les cinq gardes, dans l'ordre, avant tout appel destructif :

```go
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

package frame

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// observationMaxAge is how old the agent's disk report may be and still
// authorise a destructive act. The agent reports every 30 seconds; five
// minutes is ten missed reports, which is a node that has stopped talking.
//
// This is deliberately not configurable. A knob here is a knob that gets
// widened the one time someone is in a hurry.
const observationMaxAge = 5 * time.Minute

// Wiper performs the destructive work on the node and records that it did.
// The marker it writes lives on the machine, not in this process: in the
// previous lot a manager restart replayed an entire destructive sequence
// because the only record that it had already run was process-local. That
// was reproduced, not supposed.
type Wiper interface {
	// ReadMarker returns the claim UID already recorded for this disk on
	// this machine, or "" if none.
	ReadMarker(ctx context.Context, machine, byIDPath string) (string, error)
	// Claim writes the marker and then does the destructive work.
	Claim(ctx context.Context, machine, byIDPath, claimUID, destination string) error
}

type FrameDiskClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Wiper  Wiper
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framediskclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framediskclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framediskclaims/finalizers,verbs=update

func (r *FrameDiskClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var c framev1beta1.FrameDiskClaim
	if err := r.Get(ctx, req.NamespacedName, &c); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A terminal phase is terminal. Re-running means a new object.
	if c.Status.Phase == "Ready" || c.Status.Phase == "Failed" {
		return ctrl.Result{}, nil
	}

	disk, err := r.authorise(ctx, &c)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, &c, err.Error())
	}

	// GUARD 4: the marker is read from the machine, never from memory.
	claimUID := string(c.UID)
	existing, err := r.Wiper.ReadMarker(ctx, c.Spec.MachineRef.Name, c.Spec.ByIDPath)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading the claim marker on %s: %w", c.Spec.MachineRef.Name, err)
	}
	if existing == claimUID {
		return ctrl.Result{}, r.succeed(ctx, &c, claimUID, "already claimed by this object")
	}
	if existing != "" {
		return ctrl.Result{}, r.fail(ctx, &c,
			fmt.Sprintf("disk %s already carries claim marker %q; a second claim on a claimed disk is refused",
				c.Spec.ByIDPath, existing))
	}

	if err := r.setPhase(ctx, &c, "Claiming", claimUID, fmt.Sprintf("claiming %s", disk.Path)); err != nil {
		return ctrl.Result{}, err
	}

	// GUARD 5: the error from the destructive call is returned, never
	// swallowed. A phase does not become Ready because the failure happened
	// after the useful work.
	if err := r.Wiper.Claim(ctx, c.Spec.MachineRef.Name, c.Spec.ByIDPath, claimUID, c.Spec.Destination); err != nil {
		if serr := r.setPhase(ctx, &c, "Claiming", claimUID,
			fmt.Sprintf("claim of %s did not complete cleanly: %v", disk.Path, err)); serr != nil {
			return ctrl.Result{}, serr
		}
		return ctrl.Result{}, fmt.Errorf("claiming %s on %s: %w", c.Spec.ByIDPath, c.Spec.MachineRef.Name, err)
	}

	return ctrl.Result{}, r.succeed(ctx, &c, claimUID, fmt.Sprintf("%s claimed for %s", disk.Path, c.Spec.Destination))
}

// authorise runs guards 1 to 3 and returns the observed disk the claim
// names. Every branch that cannot positively identify a free disk is a
// refusal: not knowing is not an authorisation.
func (r *FrameDiskClaimReconciler) authorise(ctx context.Context, c *framev1beta1.FrameDiskClaim) (*framev1beta1.ObservedDisk, error) {
	// GUARD 1, first half: an empty serial matches nothing, because two
	// empty strings are equal and that is how a disk gets wiped with no
	// confirmation.
	if strings.TrimSpace(c.Spec.Serial) == "" {
		return nil, fmt.Errorf("spec.serial is empty: the retyped serial is the confirmation, and an empty one confirms nothing")
	}

	// GUARD 2: an sdX path names a different disk on every boot.
	if !strings.HasPrefix(c.Spec.ByIDPath, "/dev/disk/by-id/") {
		return nil, fmt.Errorf("spec.byIDPath %q is not under /dev/disk/by-id: sdX ordering changes across boots", c.Spec.ByIDPath)
	}

	var m framev1beta1.FrameMachine
	if err := r.Get(ctx, types.NamespacedName{Name: c.Spec.MachineRef.Name, Namespace: c.Namespace}, &m); err != nil {
		return nil, fmt.Errorf("reading FrameMachine %q: %v", c.Spec.MachineRef.Name, err)
	}

	// GUARD 3, the half that gets forgotten: a machine whose agent has
	// never reported, or reported too long ago, authorises nothing.
	if m.Status.Storage == nil || m.Status.Storage.ObservedAt == nil {
		return nil, fmt.Errorf("machine %q has never reported its disks: not knowing is not an authorisation", m.Name)
	}
	if age := time.Since(m.Status.Storage.ObservedAt.Time); age > observationMaxAge {
		return nil, fmt.Errorf("machine %q's disk report is stale (%s old, limit %s)", m.Name, age.Truncate(time.Second), observationMaxAge)
	}

	// GUARD 1, second half: the retyped serial must match an observed disk,
	// and the same disk the path names.
	for i := range m.Status.Storage.Observed {
		d := &m.Status.Storage.Observed[i]
		if d.SerialNumber == "" || d.SerialNumber != c.Spec.Serial {
			continue
		}
		if d.Path != c.Spec.ByIDPath {
			return nil, fmt.Errorf("serial %q is at %s on %s, not at the requested %s",
				c.Spec.Serial, d.Path, m.Name, c.Spec.ByIDPath)
		}
		// GUARD 3, the occupancy half: fail closed on anything but free.
		if d.Occupancy != "free" {
			return nil, fmt.Errorf("disk %s is %s; a claim destroys data and only proceeds on a free disk", d.Path, d.Occupancy)
		}
		return d, nil
	}
	return nil, fmt.Errorf("no disk with serial %q is reported on machine %q", c.Spec.Serial, m.Name)
}

func (r *FrameDiskClaimReconciler) setPhase(ctx context.Context, c *framev1beta1.FrameDiskClaim, phase, claimUID, msg string) error {
	base := c.DeepCopy()
	now := metav1.Now()
	c.Status.Phase = phase
	c.Status.PhaseSince = &now
	c.Status.ClaimUID = claimUID
	c.Status.Message = msg
	return r.Status().Patch(ctx, c, client.MergeFrom(base))
}

func (r *FrameDiskClaimReconciler) fail(ctx context.Context, c *framev1beta1.FrameDiskClaim, msg string) error {
	return r.setPhase(ctx, c, "Failed", c.Status.ClaimUID, msg)
}

func (r *FrameDiskClaimReconciler) succeed(ctx context.Context, c *framev1beta1.FrameDiskClaim, claimUID, msg string) error {
	return r.setPhase(ctx, c, "Ready", claimUID, msg)
}

func (r *FrameDiskClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameDiskClaim{}).
		Named("framediskclaim").
		Complete(r)
}
```

**La `Wiper` réelle n'est pas dans ce lot.** Le contrôleur est livré avec son interface et sa doublure de test ; l'implémentation qui exécute réellement `sgdisk`/`ceph-volume` sur le nœud est le geste physique, et elle se branche une fois les gardes prouvées. `cmd/main.go` enregistre le reconciler avec une `Wiper` qui rend une erreur explicite (« destructive backend not configured »), de sorte qu'une `FrameDiskClaim` créée sur ce déploiement échoue proprement au lieu de ne rien faire.

- [ ] **Step 5: Lancer les tests**

Run: `make manifests generate && make test`
Expected: PASS.

- [ ] **Step 6: Les cinq preuves par mutation**

Une par garde, chacune consignée dans le rapport de tâche avec la sortie rouge :

1. Retirer `if strings.TrimSpace(c.Spec.Serial) == ""` → « refuse quand le serial est vide » rouge.
2. Remplacer le test de préfixe par `strings.Contains(c.Spec.ByIDPath, "/dev/")` → le test sdX rouge (si la garde CRD le couvre déjà, retirer aussi le `+kubebuilder:validation:Pattern` et régénérer).
3. Remplacer `if d.Occupancy != "free"` par `if d.Occupancy == "mounted"` → « refuse un disque occupe » rouge ; et retirer le bloc `ObservedAt == nil` → « refuse quand l'agent n'a jamais rapporte » rouge.
4. Remplacer `r.Wiper.ReadMarker(...)` par un champ `map[string]string` du reconciler → « ne rejoue pas apres un redemarrage » rouge.
5. Remplacer le `return ... fmt.Errorf("claiming %s ...")` par `return ctrl.Result{}, r.succeed(...)` → « ne passe pas Ready quand le nettoyage echoue » rouge.

Si l'une des cinq ne rougit pas, **le test est à corriger avant le code** : une garde dont on ne peut pas montrer la mort est une garde dont on ne sait pas si elle vit.

- [ ] **Step 7: Commit**

```bash
git add api/ internal/controller/frame/ cmd/main.go config/ charts/
git commit --no-verify -m "feat(framediskclaim): cinq gardes avant le seul geste qui detruit"
```

---

### Task 9: L'application des types de contenu, sans fenêtre de casse

Le webhook de §5. Deux propriétés sont plus importantes que la règle elle-même : il ne doit **jamais** empêcher le cluster de créer un volume s'il est indisponible (`failurePolicy: Ignore`), et il ne doit **rien** faire le jour de sa pose (`objectSelector` sur l'étiquette d'usage, qu'aucun PVC ne porte).

**Files:**
- Create: `internal/webhook/core/v1/pvc_webhook.go`
- Create: `internal/webhook/core/v1/pvc_webhook_test.go`
- Modify: `cmd/main.go`
- Modify: `charts/frame/templates/webhookconfigurations.yaml`, `config/webhook/manifests.yaml`

**Interfaces:**
- Consumes: `framev1beta1.FrameStorage` (tâche 6), `usageLabel` — **attention** : la constante est définie dans `internal/controller/frame`. L'exporter depuis `api/frame/v1beta1` sous le nom `UsageLabel` et l'utiliser aux deux endroits, comme pour `SharedForType`.
- Produces: `SetupPVCWebhookWithManager(mgr ctrl.Manager) error`.

- [ ] **Step 1: Déplacer `usageLabel` dans l'API**

Le déplacer en `api/frame/v1beta1/framestorage_types.go` :

```go
// UsageLabel is the label a PersistentVolumeClaim carries to declare what
// it is for. It is also the objectSelector of the content-type webhook:
// a PVC without it is never sent to admission at all, which is what makes
// enforcement opt-in per object rather than a cluster-wide flag day.
const UsageLabel = "frame.plume-labs.io/usage"
```

Corriger l'appel dans `countClaims`. Run: `make test` — Expected: PASS inchangé. Commit.

- [ ] **Step 2: Écrire les tests (ils doivent échouer)**

Créer `internal/webhook/core/v1/pvc_webhook_test.go` :

```go
package v1

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func validatorWith(entries ...*framev1beta1.FrameStorage) *PVCCustomValidator {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = framev1beta1.AddToScheme(s)
	b := fake.NewClientBuilder().WithScheme(s)
	for _, e := range entries {
		b = b.WithObjects(e)
	}
	return &PVCCustomValidator{Client: b.Build()}
}

func storageEntry(name, class string, content ...string) *framev1beta1.FrameStorage {
	return &framev1beta1.FrameStorage{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: framev1beta1.FrameStorageSpec{
			Type: "ceph-rbd", Content: content, StorageClassName: class,
		},
	}
}

func pvc(class string, labels map[string]string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "default", Labels: labels},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &class},
	}
}

func TestRefusesAContentTypeTheEntryDoesNotDeclare(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model", "artifact"))
	_, err := v.ValidateCreate(context.Background(), pvc("ceph-rbd", map[string]string{framev1beta1.UsageLabel: "backup"}))
	if err == nil {
		t.Fatal("want a refusal: the entry does not declare backup")
	}
	// The message must name the entry and the allowed list, or the person
	// reading it has to go find both by hand.
	if !strings.Contains(err.Error(), "models") {
		t.Errorf("refusal does not name the entry: %v", err)
	}
	if !strings.Contains(err.Error(), "model") || !strings.Contains(err.Error(), "artifact") {
		t.Errorf("refusal does not list what is allowed: %v", err)
	}
}

func TestAcceptsADeclaredContentType(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model", "artifact"))
	if _, err := v.ValidateCreate(context.Background(), pvc("ceph-rbd", map[string]string{framev1beta1.UsageLabel: "model"})); err != nil {
		t.Fatalf("want acceptance: %v", err)
	}
}

// TestAcceptsAnUnlabelledClaim is the migration property: the nineteen PVCs
// already in the cluster carry no usage label. The objectSelector means
// they never reach this code at all, but the code must agree — a webhook
// that would refuse them if it ever saw them is one selector edit away
// from a cluster that cannot create volumes.
func TestAcceptsAnUnlabelledClaim(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model"))
	if _, err := v.ValidateCreate(context.Background(), pvc("ceph-rbd", nil)); err != nil {
		t.Fatalf("an unlabelled PVC must pass: %v", err)
	}
}

// TestAcceptsAClassNoEntryDescribes: a class Frame knows nothing about is
// not Frame's to police. local-path is the cluster's default class and
// three PVCs use it today.
func TestAcceptsAClassNoEntryDescribes(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model"))
	if _, err := v.ValidateCreate(context.Background(), pvc("local-path", map[string]string{framev1beta1.UsageLabel: "scratch"})); err != nil {
		t.Fatalf("a class with no FrameStorage entry must pass: %v", err)
	}
}

func TestAcceptsAClaimWithNoStorageClass(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model"))
	p := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "default",
			Labels: map[string]string{framev1beta1.UsageLabel: "model"}},
	}
	if _, err := v.ValidateCreate(context.Background(), p); err != nil {
		t.Fatalf("a PVC with no class named yet must pass: %v", err)
	}
}
```

- [ ] **Step 3: Lancer, vérifier l'échec**

Run: `go test ./internal/webhook/core/v1/ -v`
Expected: le paquet n'existe pas.

- [ ] **Step 4: Implémenter**

Créer `internal/webhook/core/v1/pvc_webhook.go` :

```go
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

package v1

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// SetupPVCWebhookWithManager registers the content-type webhook.
func SetupPVCWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &corev1.PersistentVolumeClaim{}).
		WithValidator(&PVCCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// The two non-default settings here are the whole migration story.
//
// failurePolicy=ignore: this policy governs which volumes may exist, not
// whether volumes may exist. A manager that is down, rolling, or
// unreachable must not stop the cluster from provisioning storage — a
// policy that cannot be evaluated must not stop the cluster.
//
// The objectSelector lives in the chart and kustomize manifests rather than
// in this marker (controller-gen has no marker for it): it restricts the
// webhook to PVCs carrying frame.plume-labs.io/usage. No PVC in the cluster
// carries it today, so the day this lands nothing happens — no refusal, no
// migration, and no window in which a policy arrived before the labels.
//
// +kubebuilder:webhook:path=/validate--v1-persistentvolumeclaim,mutating=false,failurePolicy=ignore,sideEffects=None,groups="",resources=persistentvolumeclaims,verbs=create;update,versions=v1,name=vpersistentvolumeclaim.kb.io,admissionReviewVersions=v1

type PVCCustomValidator struct {
	Client client.Client
}

func (v *PVCCustomValidator) ValidateCreate(ctx context.Context, obj *corev1.PersistentVolumeClaim) (admission.Warnings, error) {
	return nil, v.validate(ctx, obj)
}

func (v *PVCCustomValidator) ValidateUpdate(ctx context.Context, _, newObj *corev1.PersistentVolumeClaim) (admission.Warnings, error) {
	return nil, v.validate(ctx, newObj)
}

func (v *PVCCustomValidator) ValidateDelete(_ context.Context, _ *corev1.PersistentVolumeClaim) (admission.Warnings, error) {
	return nil, nil
}

// validate refuses a claim whose declared usage is not among the content
// types of the FrameStorage entry that owns its class.
//
// Three ways out, all of them "accept": no usage label (the claim opted
// out, and the objectSelector means it never got here anyway), no storage
// class named, and no FrameStorage entry describing that class. The last
// one matters most: local-path is the cluster's default class, and a class
// Frame does not describe is not Frame's to police.
func (v *PVCCustomValidator) validate(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	usage := pvc.Labels[framev1beta1.UsageLabel]
	if usage == "" {
		return nil
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName == "" {
		return nil
	}

	var entries framev1beta1.FrameStorageList
	if err := v.Client.List(ctx, &entries); err != nil {
		return fmt.Errorf("listing FrameStorage entries: %w", err)
	}

	for i := range entries.Items {
		e := &entries.Items[i]
		if e.Spec.StorageClassName != *pvc.Spec.StorageClassName {
			continue
		}
		for _, allowed := range e.Spec.Content {
			if allowed == usage {
				return nil
			}
		}
		return fmt.Errorf(
			"storage entry %q (class %q) accepts content types [%s]; this claim declares %q",
			e.Name, e.Spec.StorageClassName, strings.Join(e.Spec.Content, ", "), usage,
		)
	}
	return nil
}
```

- [ ] **Step 5: Enregistrer et poser l'`objectSelector`**

Dans `cmd/main.go`, ajouter `SetupPVCWebhookWithManager(mgr)` à côté des autres.

Puis, dans `charts/frame/templates/webhookconfigurations.yaml`, ajouter l'entrée au `ValidatingWebhookConfiguration`, **avec les deux propriétés que le marker ne sait pas exprimer** :

```yaml
- admissionReviewVersions:
  - v1
  clientConfig:
    {{- if not .Values.certManager.enabled }}
    caBundle: {{ .Values.webhooks.caBundle | quote }}
    {{- end }}
    service:
      name: {{ $svc }}
      namespace: {{ $ns }}
      path: /validate--v1-persistentvolumeclaim
  # A policy that cannot be evaluated must not stop the cluster from
  # creating volumes. Every other webhook in this file is failurePolicy:
  # Fail because it guards a Frame CRD nobody else writes; this one guards
  # a core type the whole cluster uses.
  failurePolicy: Ignore
  # Enforcement is opt-in per object. No PVC in the cluster carries this
  # label today, so the day this lands nothing happens.
  objectSelector:
    matchExpressions:
    - key: frame.plume-labs.io/usage
      operator: Exists
  name: vpersistentvolumeclaim.kb.io
  rules:
  - apiGroups:
    - ""
    apiVersions:
    - v1
    operations:
    - CREATE
    - UPDATE
    resources:
    - persistentvolumeclaims
  sideEffects: None
```

Poser la même entrée dans `config/webhook/manifests.yaml` — et vérifier que `make manifests` ne la réécrit pas en la privant de l'`objectSelector` : controller-gen régénère ce fichier. Si c'est le cas, l'`objectSelector` va dans un patch kustomize (`config/webhook/kustomization.yaml`), pas dans le fichier généré.

**L'ordre des entrées compte** : `make helm-parity` compare avec un `jq -S` qui trie les clés d'objet mais pas l'ordre des tableaux. Insérer la même entrée au même rang dans les deux fichiers.

- [ ] **Step 6: Lancer**

Run: `make manifests generate && make test && make helm-parity`
Expected: PASS des trois.

- [ ] **Step 7: Preuve par mutation de la propriété de migration**

Retirer `if usage == "" { return nil }`, lancer `go test ./internal/webhook/core/v1/ -run TestAcceptsAnUnlabelled -v` : le test doit rougir. Remettre.

Puis, sur le manifeste : retirer le bloc `objectSelector` du template Helm, lancer `make helm-parity` et constater qu'il **ne rougit pas** — la parité compare les deux chemins d'installation, pas la politique. Ajouter donc un test de rendu qui le couvre, dans le style des tests de chart existants (`grep -rn 'helm template' hack/ test/ | head`) ou, à défaut, une assertion dans `hack/helm-parity.sh` qui vérifie la présence de `objectSelector` et de `failurePolicy: Ignore` sur cette entrée. Un manifeste sans contrôle est un manifeste que la prochaine régénération efface.

- [ ] **Step 8: Commit**

```bash
git add internal/webhook/ cmd/main.go config/ charts/ hack/
git commit --no-verify -m "feat(webhook): appliquer les types de contenu sans jamais bloquer le cluster"
```

---

### Task 10: RBAC et parité des deux chemins d'installation

Le ClusterRole du manager dans le chart est maintenu à la main. Deux nouveaux types y sont invisibles tant que personne ne les ajoute — et un informer sans droits fait échouer le manager **au démarrage**, pas à la première requête.

**Files:**
- Modify: `charts/frame/templates/rbac-manager.yaml`
- Modify: `charts/frame/templates/rbac-tier-roles.yaml`
- Modify: `charts/frame/templates/crds.yaml` (ou le mécanisme qu'il utilise pour embarquer les CRD)
- Modify: `config/rbac/role.yaml` (généré par `make manifests`)
- Modify: `config/crd/kustomization.yaml`

- [ ] **Step 1: Régénérer le RBAC kustomize**

Run: `make manifests`
Puis lire le diff : `git diff config/rbac/role.yaml`
Expected: les règles `framestorages`, `framestorages/status`, `framestorages/finalizers`, `framediskclaims`, `framediskclaims/status`, `framediskclaims/finalizers`, `storageclasses` et `persistentvolumeclaims` apparaissent, issues des markers `+kubebuilder:rbac` des tâches 7 et 8.

- [ ] **Step 2: Reporter à la main dans le chart**

Dans `charts/frame/templates/rbac-manager.yaml`, ajouter les ressources aux blocs existants **en respectant l'ordre alphabétique interne de chaque bloc** et l'ordre des blocs eux-mêmes, tel que `config/rbac/role.yaml` les produit. Exemple pour le bloc des statuts (lignes ~160-175), qui devient :

```yaml
- apiGroups:
  - frame.plume-labs.io
  resources:
  - framediskclaims/status
  - frameinstalls/status
  - framejobs/status
  - framemachines/status
  - framenodes/status
  - frameresourcequotas/status
  - framestorages/status
  - frametasks/status
  - nodetunings/status
  - schedulingpolicies/status
  - talosmachineconfigs/status
  - talosupgrades/status
  verbs:
  - get
  - patch
  - update
```

Et ajouter le bloc pour les types core/storage que le lot introduit :

```yaml
- apiGroups:
  - ""
  resources:
  - persistentvolumeclaims
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - storage.k8s.io
  resources:
  - storageclasses
  verbs:
  - create
  - get
  - list
  - watch
```

**Frame ne demande jamais `delete` sur `persistentvolumeclaims` ni sur `persistentvolumes`.** Le droit absent est la garantie : une contrainte globale qui n'existe que dans un commentaire est une contrainte que le prochain correctif contourne sans le savoir.

- [ ] **Step 3: Ajouter les rôles de tiers**

Dans `charts/frame/templates/rbac-tier-roles.yaml`, ajouter `framestorages` et `framediskclaims` aux trois tiers, avec les mêmes verbes et la même étiquette `rbac.frame.plume-labs.io/tier` que les types voisins. **`framediskclaims` n'est pas accessible en écriture au tier `viewer` ni `editor`** : créer une `FrameDiskClaim` détruit des données, et c'est un geste d'administrateur.

- [ ] **Step 4: Vérifier la parité**

Run: `make helm-parity`
Expected: PASS. S'il échoue, lire **la première section rouge seulement** — le script s'arrête là, et les sections suivantes ne sont pas « vertes », elles ne sont pas évaluées.

- [ ] **Step 5: Prouver que l'absence de `delete` est contrôlée**

Ajouter au fichier de test de parité, ou à `hack/helm-parity.sh`, une assertion :

```bash
# Frame must never hold delete on volumes. The design says so in prose;
# this makes the prose fail a build.
if "$HELM" template charts/frame | grep -A20 'persistentvolume' | grep -qE '^\s+- delete$'; then
  echo "FAIL: the chart grants delete on persistent volumes"
  exit 1
fi
```

Puis la preuve par mutation : ajouter `- delete` au bloc `persistentvolumeclaims`, relancer, montrer le rouge, retirer.

- [ ] **Step 6: Commit**

```bash
git add config/ charts/ hack/
git commit --no-verify -m "chore(rbac): les deux nouveaux types dans les deux chemins d'installation"
```

---

### Task 11: Les écrans

Trois choses à l'écran, et toutes les décisions dans `src/lib/` — vitest tourne en `environment: 'node'` avec `include: ['src/**/*.test.ts']`, donc aucun `.tsx` n'est jamais exécuté par la suite de tests.

**Files:**
- Create: `src/lib/storage.ts`, `src/lib/storage.test.ts`
- Create: `src/components/FrameStorageView.tsx`
- Create: `src/components/MachineDisksPanel.tsx`
- Modify: `src/components/ClusterStorageView.tsx`
- Modify: `src/App.tsx` (l'onglet)
- Modify: `src/lib/frame-sdk.ts` (les lectures des deux nouveaux types)

**Interfaces:**
- Consumes: les types d'API des tâches 3, 6, 8.
- Produces: `describeDivergence`, `capacityLine`, `cephWarningReasons`, `claimGapLine` — toutes pures, toutes testées.

- [ ] **Step 1: Écrire les tests des décisions (ils doivent échouer)**

Créer `src/lib/storage.test.ts` :

```ts
import { describe, it, expect } from 'vitest'
import { describeDivergence, capacityLine, claimGapLine, cephWarningReasons } from './storage'

describe('describeDivergence', () => {
  it('nomme la baie pour un disque que seul le BMC voit', () => {
    const line = describeDivergence({
      serialNumber: 'W4722RRA',
      reason: 'bmc-only',
      detail: 'bay 2I:6:8: the BMC lists it, the node kernel does not',
    })
    expect(line).toContain('W4722RRA')
    expect(line).toContain('2I:6:8')
  })

  it('ne dit jamais qu-un disque bmc-only est en panne', () => {
    // The BMC reports Health OK for the masked disk. A screen that renders
    // a divergence as a hardware fault sends someone to replace a healthy
    // drive.
    const line = describeDivergence({ serialNumber: 'W4722RRA', reason: 'bmc-only', detail: '' })
    expect(line.toLowerCase()).not.toContain('fail')
    expect(line.toLowerCase()).not.toContain('error')
  })

  it('distingue os-only de bmc-only', () => {
    const a = describeDivergence({ serialNumber: 'X', reason: 'bmc-only', detail: '' })
    const b = describeDivergence({ serialNumber: 'X', reason: 'os-only', detail: '' })
    expect(a).not.toEqual(b)
  })
})

describe('capacityLine', () => {
  it('rend l-utilisable en premier', () => {
    const line = capacityLine({ usable: '1.2Ti', used: '480Gi', raw: '3.6Ti' })
    expect(line.indexOf('1.2Ti')).toBeLessThan(line.indexOf('3.6Ti'))
  })

  it('ne rend jamais le brut seul', () => {
    // This is the capacity incident, in one assertion: OSDs were sized on
    // a raw number while replication divided it by three.
    const line = capacityLine({ usable: '', used: '', raw: '3.6Ti' })
    expect(line).not.toEqual('3.6Ti')
    expect(line.toLowerCase()).toContain('unknown')
  })

  it('se passe du brut quand il manque', () => {
    expect(capacityLine({ usable: '1.2Ti', used: '480Gi', raw: '' })).toContain('1.2Ti')
  })
})

describe('claimGapLine', () => {
  it('rend l-ecart lisible', () => {
    expect(claimGapLine('ceph-rbd', { total: 16, labelled: 0 })).toContain('16')
    expect(claimGapLine('ceph-rbd', { total: 16, labelled: 0 })).toContain('0')
  })

  it('ne presente pas zero etiquette comme une faute', () => {
    // Enforcement is opt-in per object; the normal state on day one is
    // many claims and no labels.
    const line = claimGapLine('ceph-rbd', { total: 16, labelled: 0 }).toLowerCase()
    expect(line).not.toContain('error')
    expect(line).not.toContain('violation')
  })
})

describe('cephWarningReasons', () => {
  it('extrait les verifications en echec', () => {
    const reasons = cephWarningReasons({
      health: 'HEALTH_WARN',
      checks: {
        POOL_NO_REDUNDANCY: { summary: { message: '1 pool(s) have no replicas configured' } },
        MON_CLOCK_SKEW: { summary: { message: 'clock skew detected on mon.b' } },
      },
    })
    expect(reasons).toHaveLength(2)
    expect(reasons.join(' ')).toContain('no replicas')
  })

  it('rend une liste vide sur HEALTH_OK', () => {
    expect(cephWarningReasons({ health: 'HEALTH_OK', checks: {} })).toEqual([])
  })

  it('ne masque pas un WARN dont les motifs manquent', () => {
    // A WARN with no checks is still a WARN. Returning [] here would let
    // the screen render "healthy" for a degraded cluster.
    const reasons = cephWarningReasons({ health: 'HEALTH_WARN', checks: {} })
    expect(reasons).toHaveLength(1)
    expect(reasons[0].toLowerCase()).toContain('reason')
  })
})
```

- [ ] **Step 2: Lancer, vérifier l'échec**

Run: `npm run test -- src/lib/storage.test.ts` (vérifier le nom du script : `grep -n '"test"' package.json`)
Expected: FAIL — le module n'existe pas.

- [ ] **Step 3: Implémenter les décisions**

Créer `src/lib/storage.ts` :

```ts
export interface DiskDivergence {
  serialNumber: string
  reason: 'bmc-only' | 'os-only' | 'mismatch'
  detail: string
}

export interface StorageCapacity {
  usable: string
  used: string
  raw: string
}

export interface ClaimCounts {
  total: number
  labelled: number
}

/**
 * A one-line reading of one divergence.
 *
 * It never uses fault language for `bmc-only`. On the machine this design
 * was written against, the BMC reports Health OK and status reasons
 * ["None"] for the very disk the kernel cannot see: the disk is masked by
 * residual logical-unit metadata, not broken, and a screen that says
 * "failed" sends someone to replace a healthy drive.
 */
export function describeDivergence(d: DiskDivergence): string {
  const where = d.detail ? ` — ${d.detail}` : ''
  switch (d.reason) {
    case 'bmc-only':
      return `${d.serialNumber}: seen by the BMC, not presented to the node${where}`
    case 'os-only':
      return `${d.serialNumber}: seen by the node, absent from the BMC's list${where}`
    default:
      return `${d.serialNumber}: the two sources disagree${where}`
  }
}

/**
 * Capacity, usable first.
 *
 * Raw never stands alone: the park's capacity incident came from sizing
 * OSDs on a raw number that replication divides by three. An entry with no
 * usable figure says so rather than fall back to the raw one.
 */
export function capacityLine(c: StorageCapacity): string {
  if (!c.usable) {
    return c.raw ? `usable unknown (raw ${c.raw})` : 'usable unknown'
  }
  const used = c.used ? `${c.used} used of ` : ''
  const raw = c.raw ? ` (raw ${c.raw})` : ''
  return `${used}${c.usable} usable${raw}`
}

/**
 * The gap between the policy and the cluster, stated as a fact rather than
 * a fault. Enforcement is opt-in per object, so many claims and no labels
 * is the normal state the day the webhook lands.
 */
export function claimGapLine(className: string, c: ClaimCounts): string {
  return `${className} — ${c.total} PVC, ${c.labelled} labelled`
}

export interface CephHealthPayload {
  health: string
  checks: Record<string, { summary?: { message?: string } }>
}

/**
 * The reasons behind a Ceph warning, which the existing storage screen does
 * not show: it renders "WARN" and nothing else, and a degraded state with
 * no reason cannot be acted on.
 *
 * A WARN whose checks are missing still returns one line. Returning an
 * empty list there would let the screen render a degraded cluster as
 * healthy — the failure mode this function exists to prevent.
 */
export function cephWarningReasons(payload: CephHealthPayload): string[] {
  if (payload.health === 'HEALTH_OK') return []

  const reasons = Object.entries(payload.checks ?? {})
    .map(([code, check]) => check.summary?.message ?? code)
    .filter((m) => m.length > 0)

  if (reasons.length === 0) {
    return [`${payload.health} with no reason reported by the cluster`]
  }
  return reasons
}
```

- [ ] **Step 4: Lancer les tests**

Run: `npm run test -- src/lib/storage.test.ts`
Expected: PASS.

- [ ] **Step 5: Les écrans**

`src/components/FrameStorageView.tsx` : la liste des entrées, une carte par `FrameStorage`, avec type, classe, `shared` (du statut), phase, `capacityLine(...)` et `claimGapLine(...)`. Suivre la structure de `ServiceClassesView.tsx` pour le chargement (`useLiveResource` + `crdListPath`).

`src/components/MachineDisksPanel.tsx` : deux listes côte à côte — `status.inventory.drives` et `status.storage.observed` — puis, en dessous, les divergences rendues par `describeDivergence`. **Les deux listes ne sont jamais fusionnées à l'écran** : c'est l'écart qui est la donnée. Ajouter le panneau à `NodeDetailPanel.tsx` ou à l'onglet Hardware, là où `FrameMachine` est déjà affichée.

`src/components/ClusterStorageView.tsx` : deux modifications précises.
1. Sous le `<Stat label="Health" …>` existant, rendre `cephWarningReasons(...)` en liste quand la santé n'est pas `HEALTH_OK`.
2. Remplacer la ligne de capacité qui dit `GiB raw` par `capacityLine(...)`, en lisant l'utilisable de l'entrée `FrameStorage` correspondante quand elle existe.

La carte d'une entrée rend aussi **où elle est disponible** — `all nodes` ou la liste — depuis la condition `Available` posée par le contrôleur, et le motif de sa phase depuis la condition `Healthy`. Une entrée `Degraded` dont l'écran ne dit pas pourquoi reproduit exactement le défaut que §6a nomme.

Ajouter l'onglet dans `src/App.tsx`, sous *Resources → Storage*, à côté de l'onglet `storage` existant : `{ id: 'storage-entries', label: 'Entries' }` et `{ id: 'disks', label: 'Disks' }`.

- [ ] **Step 6: Vérifier la compilation et la suite complète**

Run: `npm run build && npm run test`
Expected: PASS des deux.

- [ ] **Step 7: Preuve par mutation de la règle du brut**

Remplacer le corps de `capacityLine` par `return c.usable || c.raw`, lancer `npm run test -- src/lib/storage.test.ts`, montrer « ne rend jamais le brut seul » rouge, remettre.

- [ ] **Step 8: Commit**

```bash
git add src/
git commit --no-verify -m "feat(ui): les entrees de stockage, l'ecart entre les deux sources, et le motif du WARN"
```

---

### Task 12: La documentation

**Files:**
- Create: `docs/storage.md`
- Modify: `docs/deployment.md` (la section « pas encore exécuté »)

- [ ] **Step 1: Écrire `docs/storage.md`**

Le document couvre, dans cet ordre : les deux types et ce que chacun fait ; les cinq types de contenu et ce que chacun désigne dans le parc ; **comment étiqueter un PVC existant**, une commande, un PVC à la fois, avec la phrase qui dit que rien ne l'exige ; les deux sources disque et comment lire une divergence — avec le cas du `MM1000GFJTE` en exemple travaillé, BMC `Health: OK` compris ; et la procédure d'une `FrameDiskClaim`, avec ses cinq refus possibles et ce que chacun veut dire.

Une section **« Ce qui n'est pas livré »** nomme : la `Wiper` réelle (le contrôleur refuse proprement sans elle), NFS, iSCSI, LVM, le partitionnement manuel, le redimensionnement, les instantanés et la sauvegarde.

- [ ] **Step 2: Mettre à jour `docs/deployment.md`**

Ajouter à la liste des choses non exécutées : le déploiement du webhook PVC (il faut un `caBundle` ou cert-manager), et le fait que l'agent a besoin de nouveaux droits RBAC avant que l'écran des disques montre quoi que ce soit.

- [ ] **Step 3: Commit**

```bash
git add docs/
git commit --no-verify -m "docs(storage): les deux types, l'ecart entre les sources, et ce qui n'est pas livre"
```

---

## Ordre d'exécution et dépendances

Les tâches se font dans l'ordre. Trois dépendances sont dures :

- **3 avant 4** — l'agent utilise `ObservedDisk`, que la tâche 3 définit.
- **3 et 4 avant 5** — le patch de statut appelle les deux.
- **6 avant 7 et avant 9** — le contrôleur et le webhook PVC lisent `FrameStorage`.

Les tâches 1-2 (BMC) et 3-5 (nœud) sont indépendantes l'une de l'autre jusqu'à la tâche 5, qui les joint.

## Ce que ce plan ne fait pas

- **La `Wiper` réelle.** Le contrôleur de la tâche 8 est livré avec ses gardes prouvées et une implémentation qui refuse. Exécuter `sgdisk` ou `ceph-volume` sur une vraie machine est le geste physique, et il se fait après, sur une machine choisie, pas depuis un plan.
- **L'étiquetage des PVC existants.** La tâche 12 documente le geste ; personne ne l'exécute automatiquement.
- **Le déploiement.** Rien de ce lot n'est déployé par ce plan. La branche se termine par une fusion locale, comme les lots précédents.
