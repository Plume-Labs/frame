# Lot 3 — Stockage : conception

**Date :** 2026-09-13
**Statut :** approuvé, non implémenté
**Base :** `main` = `dcd0039`

## 1. Ce que ce lot livre

Frame n'a aujourd'hui aucun objet qui parle de stockage. Onze types
personnalisés, pas un seul. Le cluster porte trois StorageClasses
(`local-path` par défaut, `ceph-rbd`, `ceph-bucket`), dix-neuf PVC, et un
cluster Rook Ceph vieux de cinquante jours **en HEALTH_WARN que rien
n'affiche**. L'exploitant apprend l'état de son stockage en tapant `kubectl`.

Le modèle de Proxmox VE est repris pour ce qu'il a de juste : des **entrées
typées**, des **types de contenu** qui disent à quoi une entrée a le droit de
servir, une **disponibilité par nœud**, et un **indicateur partagé/local**.
PVE y ajoute une vue « Disques » par nœud, séparée des entrées de stockage.
Frame reprend cette séparation.

Trois volets, décidés ensemble :

- **A — inventaire physique des disques** : ce que la machine porte vraiment.
- **B — santé** : l'état du stockage, y compris celui que personne ne regarde.
- **C — modifications déclaratives** : déclarer une entrée, et fabriquer les
  disques qui la nourrissent.

## 2. Deux types, et un troisième qui n'existe pas

### 2.1 `FrameStorage` — l'entrée, déclarative et durable

L'équivalent d'une ligne de `/etc/pve/storage.cfg`. Elle vit longtemps, son
statut est rafraîchi par scrutation, et elle ne détruit rien.

```yaml
apiVersion: frame.plume-labs.io/v1beta1
kind: FrameStorage
spec:
  type: ceph-rbd | ceph-bucket | local-path
  content: [workload, model, backup, artifact, scratch]   # liste, pas valeur unique
  storageClassName: ceph-rbd
  adoptExisting: false
  nodes: []            # vide = tous les nœuds ; sinon liste explicite
status:
  shared: true         # DÉRIVÉ du type, jamais déclaré
  phase: Ready | Degraded | Unknown
  capacity:
    usable: "1.2Ti"    # utilisable, jamais brut
    used: "480Gi"
  claims:
    total: 16
    labelled: 0        # l'écart entre la politique et le réel
  conditions: []
```

**Types retenus : `ceph-rbd`, `ceph-bucket`, `local-path`.** NFS est
explicitement hors de ce lot — rien dans le parc ne le réclame, et il ouvre
une grammaire de montage que personne n'a demandée.

**Cinq types de contenu**, chacun désignant quelque chose de réel : `workload`
(données d'application), `model` (poids et gros caches en lecture), `backup`,
`artifact` (objets, registres), `scratch` (éphémère, reconstructible).

**`shared` est dérivé du type, pas déclaré.** Un utilisateur ne peut pas
affirmer qu'un `local-path` est partagé. Le champ est en statut, en lecture
seule.

**La capacité rapportée est utilisable, jamais brute.** L'incident de
dimensionnement du parc venait précisément de là : des OSD dimensionnés sur du
brut alors que la réplication divise par trois. Le brut peut apparaître en
second, jamais en premier, jamais seul.

**Frame n'adopte jamais une StorageClass existante implicitement.** Une
`FrameStorage` qui nomme une classe déjà présente est refusée à l'admission
sauf `adoptExisting: true`. Sans cette garde, la première `FrameStorage`
écrite à la main prendrait possession des seize PVC `ceph-rbd` et des trois
`local-path` en production.

**Propriété et suppression.** Une entrée créée par Frame possède sa
StorageClass et la supprime avec elle. Une entrée adoptée ne possède rien :
la supprimer retire la ligne de Frame et laisse la classe intacte. **Frame ne
supprime jamais un PV ni un PVC**, dans aucune branche.

### 2.2 `FrameDiskClaim` — l'acte destructif, à usage unique

Calqué sur `FrameInstall` du lot provisionnement : une machine, un disque, des
phases dont l'issue reste lisible, et aucune reprise implicite.

```yaml
apiVersion: frame.plume-labs.io/v1beta1
kind: FrameDiskClaim
spec:
  machineRef: { name: g9 }
  byIDPath: /dev/disk/by-id/scsi-3600508b1001c...   # jamais sdX
  serial: W4722RRA                                   # retapé à la main
  destination: wipe | ceph-osd
status:
  phase: Pending | Claiming | Ready | Failed
  claimUID: 9666aafc-...
  message: ""
```

Destinations de ce lot : `wipe` (rendre le disque nu et s'arrêter là) et
`ceph-osd` (le confier à Rook). Ni LVM ni partitionnement manuel.

### 2.3 Les disques ne sont pas un type

Ils n'ont pas de cycle de vie propre et ne se déclarent pas : ils sont
observés. Ils vivent dans `FrameMachine.status` (lot 1), enrichi d'un champ
alimenté par l'agent.

## 3. Deux sources de vérité sur les disques

```yaml
status:
  inventory:
    drives: []        # Redfish : baie, modèle, série, type, état  (champ du lot 1)
  storage:
    observed: []      # agent : lsblk + /dev/disk/by-id, taille, occupation
    divergences: []   # motif : bmc-only | os-only | mismatch
```

**La source BMC n'est pas un champ neuf : c'est `status.inventory.drives[]`,
posé par le lot 1 et jamais rempli.** `internal/redfish/client.go` laisse
`Inventory.Drives` vide avec un commentaire explicite — un iLO4 expose ses
disques sous l'arbre OEM `SmartStorage`, pas sous les collections standard
`Storage/Drives`, et le lot 1 a refusé de deviner ce chemin sans machine pour
le vérifier. Les captures de cette machine existent désormais
(`internal/redfish/testdata/ilo4-real/smartstorage_*`). Ce lot implémente donc
le parcours OEM et remplit le champ existant ; créer une seconde liste BMC à
côté d'une liste vide serait un doublon.

`DriveInfo` du lot 1 ne porte **pas** de numéro de série — donc la jointure de
§3 est aujourd'hui impossible. L'ajouter est un préalable, pas un détail : le
champ `SerialNumber` des disques est présent dans les captures
(`"SerialNumber": "W4722RRA"`), avec `Location` (`"2I:6:8"`), `InterfaceType`,
`MediaType` et `DiskDriveStatusReasons`.

**La clé de jointure est le numéro de série, et rien d'autre.** Le lot
provisionnement a montré qu'un nom de disque Redfish et un chemin `by-id`
sont deux espaces de noms différents ; les rapprocher par le nom ne rapproche
rien.

**Les deux sources ne sont jamais fusionnées, et l'écart est la donnée.** Le
ML350 G9 en est la démonstration : huit disques annoncés par le BMC, sept vus
par Linux, un `MM1000GFJTE` masqué par des métadonnées résiduelles. PVE ne
peut pas montrer ça, faute d'avoir deux sources. Frame en a deux, donc Frame
le doit.

**Le BMC ne sait pas qu'il ment.** Relevé sur le G9 le 2026-09-11 : les huit
disques rendent `Health: OK` et `DiskDriveStatusReasons: ["None"]`, les huit
sont dans `UnconfiguredDrives`, `LogicalDrives` est à 0. Le seul indice côté
BMC est une entrée IML `1832` de sévérité `OK`, donc informative. Aucun champ
d'état ne porte l'écart. **`divergences[]` est donc calculé par la jointure,
jamais lu dans une source.** Une entrée `bmc-only` dont l'état est `OK` est
le cas normal, pas le cas rare.

Ces chiffres viennent de `docs/g9-single-node-cluster.md` du dépôt Neura
(`main = e8a9eba82`), un journal de terrain non relu dont l'autrice signale y
avoir corrigé trois affirmations fausses en route. Ils valent comme cas de
démonstration ; **la garde 1 de §4 impose de toute façon de revérifier le
numéro de série sur la machine avant tout geste destructif.**

`observed[]` porte aussi **ce qui occupe le disque** : monté, PV LVM, OSD
Ceph, table de partitions. C'est la donnée dont dépend la garde principale de
`FrameDiskClaim`.

## 4. Les gardes de `FrameDiskClaim`

Chacune vient d'un défaut réel du lot provisionnement, pas d'une précaution
théorique.

1. **Le numéro de série est retapé dans le spec et doit correspondre à
   `observed[]`.** Vide d'un côté ou de l'autre = refus explicite, avec un
   test qui échoue si la garde est retirée. Deux chaînes vides sont égales :
   c'est ainsi qu'un disque se fait effacer sans confirmation.
2. **Le disque est désigné par son chemin `by-id`, jamais `sdX`.** Sur le G9,
   l'ordre `sdX` a changé aux trois démarrages (`sda+sdb`, `sdb+sdc`,
   `sda+sdc`).
3. **Refus fermé sur l'occupation.** Monté, PV, OSD, ou **rapport d'agent
   absent ou périmé** → on ne touche pas. Ne pas savoir n'est pas une
   autorisation.
4. **Marque d'unicité écrite sur la machine**, pas seulement en mémoire du
   contrôleur. Un redémarrage du manager rejouait toute la séquence
   destructive du lot précédent — reproduit, pas supposé.
5. **Les erreurs de nettoyage ne sont pas avalées.** Une phase ne devient pas
   `Ready` parce que l'échec s'est produit après le travail utile.

## 5. Application des types de contenu

Webhook d'admission sur les PVC :

- **`failurePolicy: Ignore`** — une politique qu'on ne peut pas évaluer ne
  doit pas arrêter le cluster. Frame s'est déjà fait mordre exactement là.
- **`objectSelector`** sur `frame.plume-labs.io/usage` — un PVC sans
  étiquette ne traverse jamais ce chemin.

Un PVC étiqueté `usage: model` sur une classe dont `content` ne contient pas
`model` est refusé, avec le nom de l'entrée et la liste autorisée dans le
message.

**Les dix-neuf PVC existants n'en portent aucune.** Le jour où le webhook est
posé, il ne se passe rien : pas de refus, pas de migration, pas de fenêtre où
le cluster ne peut plus créer de volume parce qu'une politique arrive avant
les étiquettes. L'adoption est opt-in par objet.

**L'écart est affiché, pas résorbé d'office.** `status.claims` compte les PVC
d'une classe et ceux qui sont étiquetés. L'écran montre `ceph-rbd — 16 PVC,
0 étiqueté`. Étiqueter reste un geste humain, un PVC à la fois.

## 6. Santé

**Correction portée après lecture du code.** J'avais écrit que le HEALTH_WARN
n'était affiché nulle part. C'est faux : `src/components/ClusterStorageView.tsx`
appelle `frame.cluster.ceph()`, lit `cluster.status.ceph.health`, et rend
`<Stat label="Health" …>` avec un ton `warning` sur `HEALTH_WARN`
(`healthTone`). L'écran existe, sous *Resources → Storage*. Le volet santé de
ce lot n'a donc pas à rendre Ceph visible ; il doit faire les deux choses que
cet écran ne fait pas.

**a. Le pourquoi du WARN n'est nulle part.** L'écran affiche `WARN` et rien
d'autre : ni les vérifications en échec, ni depuis quand. Un état dégradé sans
son motif ne se traite pas. La `FrameStorage` de type `ceph-*` porte donc
l'état **et ses motifs** dans ses conditions, et l'écran rend les motifs.

**b. L'écran n'affiche que du brut, et c'est le défaut à corriger.** La barre
de capacité lit `bytesUsed`/`bytesTotal` et l'étiquette dit littéralement
`GiB raw`. C'est exactement la lecture qui a produit l'incident de capacité du
parc : des OSD dimensionnés sur le brut quand la réplication divise par trois.
`FrameStorage.status.capacity.usable` est la valeur de référence (§2.1), le
brut ne peut apparaître qu'à côté d'elle, jamais seul.

Le critère de réussite du volet B est donc : sur l'écran, le motif du WARN est
lisible et la capacité utilisable est affichée avant le brut. Ni l'un ni
l'autre ne se simule par un test unitaire — les deux se vérifient à l'écran,
sur le cluster.

## 7. Contraintes globales

- **`v1beta1` est figé** : les deux types sortent en `v1beta1` seul, sans
  webhook de conversion.
- **Parité Helm/kustomize** : `make helm-parity` s'arrête à sa première
  section rouge, et son `jq -S` trie les clés d'objet mais **pas l'ordre des
  tableaux** — l'ordre des blocs RBAC compte.
- **Le RBAC manager du chart est tenu à la main** et doit être mis à jour pour
  chaque nouveau type, sans quoi l'informer est interdit au démarrage.
- **vitest tourne en `environment: 'node'` avec `include:
  ['src/**/*.test.ts']`** : aucun `.tsx` n'est jamais exécuté. Toute décision
  d'écran vit dans `src/lib/`.
- **Enregistrement Ginkgo** : `k8sClient` est rempli par `BeforeSuite` dans
  `TestControllers`. Un `func TestX` de premier niveau dans un fichier qui
  trie avant `suite_test.go` plante sur un client nul.
- **`internal/agent` existe déjà** et tourne sur chaque nœud pour NodeTuning
  (`observe.go`, `apply.go`, `systemd.go`). Le volet disques s'y greffe, il
  ne crée pas un second agent.

## 8. La leçon du lot précédent, portée ici

Treize défauts en treize déguisements, tous de la même famille : **un contrôle
qui passe pour une raison autre que celle qu'il annonce**. Aucun n'a été trouvé
par lecture ; tous par mutation.

Un contrôle sans témoin positif ne sait pas distinguer « la chose est absente »
de « j'ai regardé au mauvais endroit ». Et **un contrôle qui ne cherche que ce
qu'on vient d'ajouter ne voit jamais ce qu'on a cassé**.

Chaque garde nommée en §4 et §5 doit être accompagnée d'une preuve de
mutation : retirer la garde, montrer le test rouge, le remettre.

## 9. Hors périmètre

- NFS, iSCSI, LVM, partitionnement manuel.
- Suppression de PV ou de PVC par Frame.
- Redimensionnement de volume.
- Instantanés et sauvegarde — lot distinct.
