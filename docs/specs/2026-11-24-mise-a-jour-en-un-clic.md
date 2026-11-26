# Mettre à jour Hearthrack en un clic depuis la console : spécification

**Date :** 2026-11-24 · **Base :** `main` = `d11fec2`
**Statut :** **validée par Romuald le 24/11** (les 3 choix du §7). Implémentation par PR, une à la fois en CI.
**Demande :** « Comment font les clients Hearthrack ? » Un client ne doit jamais voir Argo CD, `kubectl` ni un port-forward.

## 1. Constat

Le 24/11, passer la console à `223eb20` a demandé : un build à la main sur une VM, un push vers le registre local, puis Argo CD (port-forward, App Diff, sync sélective sans Prune, kubeconfig admin). Un client ne fera rien de cela.

Ce qui existe déjà :

- **Réglages → Mises à jour** (`src/components/UpdatesView.tsx`, `src/lib/updates.ts`) est **en lecture seule** : version en cours, composants, « est-ce le bon moment » (jobs, installations, remplacements d'OSD, composants non prêts), et une carte « Préparer » qui produit un texte de commandes à copier. Elle ne met rien à jour : `buildPreparation` le dit dans son en-tête.
- **« Ajouter une application »** (`updates.apps`, `src/lib/frame-config.ts`) étend le même écran à d'autres espaces de noms (Fusora, namespace `neura`), toujours en lecture + préparation.
- **Livraison** : `deploy/gitops/argocd/applications/frame.yaml` (sync manuelle, deux sources `config/default` et `deploy/kubernetes/overlays/production`). Le chart Helm n'installe que l'opérateur et provisiond ; la console, uiproxy, authd et l'agent passent par kustomize avec des noms d'images locaux (`cluster-control:latest`, `frame-uiproxy:latest`).
- **CI** : `build.yml` construit les six images, les pousse sur GHCR, les **signe en cosign sans clé** (identité du workflow) et attache SBOM et provenance. Aucune release n'existe encore (voir `docs/publication-chart-et-image.md`). Un `upgrade-e2e.yml` existe déjà.

Donc : le signal (« il y a une version, voici l'état ») est là ; **l'acte** (appliquer, surveiller, revenir en arrière) n'existe pas, et la source de vérité d'une version (un manifeste signé) non plus.

## 2. Objectif et non-objectifs

**Objectif.** Dans Réglages → Mises à jour, un administrateur voit « version X disponible (canal stable) », lit les notes, clique **Mettre à jour**, suit l'avancement, et peut cliquer **Revenir à la version précédente**. Argo, Helm et `kubectl` sont invisibles. La console, l'opérateur et les autres composants sont mis à jour par le même geste.

**Non-objectifs (v1).** Mise à jour de l'OS des nœuds ou de k3s/Kubernetes ; retour arrière de migrations de données d'applications ; mise à jour sans interruption de la console (une coupure de quelques secondes est acceptée et annoncée).

## 3. Design

### 3.1 Le manifeste de release signé (la source de vérité)

Chaque tag `v*` publie, en plus des images, un **artefact OCI « release »** : `ghcr.io/plume-labs/hearthrack-release:<version>` (et les étiquettes mobiles de canal `stable` et `beta`). Il contient un `release.json` :

```
version, channel, publishedAt, notes (court, FR/EN),
images:   { controller, ui, uiproxy, authd, agent, provisiond } -> digests sha256 (multi-arch),
crds:     digest du paquet de CRD,
from:     version minimale d'où l'on peut partir (« min-from »),
rollbackSafeTo: plus ancienne version vers laquelle revenir sans perte,
requires: { downtime: bool, notes: [...] }
```

Il est **signé en cosign sans clé**, comme les images, avec l'identité épinglée `Plume-Labs/frame/.github/workflows/build.yml@refs/tags/v*`. Les images sont référencées **par digest, jamais par tag** (un tag se déplace).

### 3.2 Le moteur : un « updater » minuscule, pas Argo

Un petit binaire Go, `frame-updater`, dans `frame-system`, avec son propre compte de service. **Il remplace Argo pour le client**, il ne l'enveloppe pas :

1. Lit le canal (`FrameRelease`, objet en lecture seule tenu à jour par un **synchroniseur de releases** distinct, avec son propre compte de service : lui seul écrit `status.verified` ; l'updater le lit sans jamais l'écrire ; livré : `cmd/release-sync`, dans l'image de l'updater, déploiement `deploy/kubernetes/release-sync/`) : toutes les 15 min par défaut (`--interval`), ou à la demande depuis la console.
2. **Vérifie** la signature du manifeste puis celle de chaque image (bibliothèque `sigstore-go`, racine de confiance embarquée : pas d'appel réseau obligatoire en air-gap).
3. À la demande d'un administrateur (`FrameUpdate`, §3.3) : contrôles préalables, application, surveillance, retour arrière automatique en cas d'échec.
4. N'applique **que** : changement des digests d'images des charges **nommées** de Hearthrack, et mise à jour des CRD. **Jamais de prune**, jamais de suppression, jamais d'objet hors de sa liste blanche. C'est la leçon d'Argo avec élagage (sync sélective, App Diff à la main le 24/11).

Pourquoi pas Argo/Flux comme moteur : il faudrait l'installer, l'exposer et lui donner un cluster-admin chez chaque client ; sa logique de dérive et d'élagage est exactement ce qu'on veut éviter ici ; et le client n'a pas de dépôt Git à nous confier. Pourquoi pas la mise à jour par l'opérateur seul : voir §4.1 (il ne peut pas se secourir lui-même).

### 3.3 L'API et la console

- **`FrameUpdate`** (CRD, v1beta1, dans `frame-system`) : `spec.targetVersion`, `spec.force` (bool), `spec.rollback` (bool). `status` : phase, étape en cours, composants faits/restants, version précédente, horodatages, raison d'échec. **C'est la seule surface** : la console ne touche ni Deployment ni image.
- **Droits.** Création de `FrameUpdate` réservée au rôle **admin** (règles CEL dans le CRD, pas de webhook, + RBAC : rôles admin et viewer seulement) avec **ré-authentification par passkey** (authd, comme pour les gestes irréversibles du remplacement d'OSD). Journalisée dans Tasks avec la personne.
- **Console.** Le bouton remplace la carte « Préparer » qui devient « mode expert » (utile aux sites GitOps). Réutilise `disturbances()` : s'il y a des jobs, une installation ou un remplacement d'OSD en cours, le bouton est désactivé, avec **Forcer** après saisie de la version à confirmer. Affiche notes, version, avancement par composant, historique (5 dernières), bouton retour arrière.

### 3.4 Déroulé d'une mise à jour

1. Contrôles : signatures, `min-from`, capacité du cluster (surge possible, voir §4.6), activité en cours (§3.3), sauvegarde de l'état (copie des CR Hearthrack en `ConfigMap` de l'espace `frame-system`, ou Velero s'il est présent).
2. CRD (avant-compatibles), puis **opérateur**, **provisiond**, **authd**, **uiproxy**, **agent** (DaemonSet, déploiement roulant), puis **console en dernier**, puis l'updater lui-même (§4.1).
3. Après chaque étape : porte de santé (prêt, sonde, absence de redémarrages, lease de l'opérateur, certificat du webhook). Échec = arrêt, retour aux digests précédents, statut `RolledBack` avec la raison.
4. La console prévient avant, puis se reconnecte seule (suivi par sondage du statut, §4.2).

### 3.5 Canaux, signature, air-gap

- **Canaux** : `stable` (défaut), `beta` (opt-in). Pas de canal « nightly » dans l'UI. Un **délai de maturation** (par défaut 48 h après la publication) évite d'avaler une version retirée.
- **Air-gap / registre local** (cas de Romuald : `192.168.2.201:30500`) : l'updater sait **copier** les images vérifiées vers un registre local configuré (`updates.registry`), puis déployer les digests locaux. Hors ligne : import d'un **paquet de release** (archive OCI signée, déposée depuis la console), vérifié avec la racine de confiance embarquée.
- **Chaîne d'approvisionnement** : la signature est vérifiée **avant** toute écriture ; un échec de vérification ne se contourne pas, même avec `force`.

### 3.6 Coexistence avec GitOps

Si un client (ou nous) garde Argo/Flux, l'updater détecte les étiquettes de gestion (`argocd.argoproj.io/instance`, `kustomize.toolkit.fluxcd.io/name`) et passe en **mode proposition** : il n'écrit plus, il affiche le diff et le texte à appliquer (le comportement actuel). Argo disparaît de l'installation par défaut ; il reste un choix.

### 3.7 Les autres applications (Fusora)

Même moteur, plus tard : un manifeste de release par application (même format, mêmes signatures), déclaré via « Ajouter une application ». Hors périmètre du chantier bêta, mais le format §3.1 est écrit pour le permettre.

## 4. Risques

| # | Risque | Parade |
|---|---|---|
| 4.1 | **L'opérateur et l'updater se mettent à jour eux-mêmes** : le processus qui pilote disparaît au milieu de l'opération. | Machine à états **dans `FrameUpdate.status`**, idempotente, reprise par le nouveau pod. **Supervision croisée** : l'updater met à jour l'opérateur, l'opérateur met à jour l'updater, jamais les deux à la fois. Si le nouveau pod ne devient pas prêt dans la limite, l'autre recharge les digests précédents. L'updater est volontairement minuscule et change rarement. |
| 4.2 | **La console se redémarre pendant que l'utilisateur la regarde.** | Console mise à jour en dernier ; suivi par `FrameUpdate` via uiproxy déjà à jour ; bandeau « reconnexion en cours » ; la mise à jour continue même si le navigateur ferme. |
| 4.3 | **CRD / webhook : décalage de versions** entre l'ancien et le nouveau. | Règle de compatibilité N-1 (CRD avant-compatibles, appliquées avant l'opérateur) ; `min-from` bloque les sauts non supportés ; webhook de conversion testé en e2e. |
| 4.4 | **Retour arrière impossible** (migration de données, CRD qui ne revient pas). | `rollbackSafeTo` dans le manifeste ; au-delà, le bouton est grisé avec la raison. À écrire en toutes lettres dans « Limites connues » : le retour arrière ne défait pas les données. |
| 4.5 | **Air-gap et registre local** : tags mouvants, registre sans signature. | Digests partout ; copie vérifiée avant écriture ; signatures conservées en référents OCI. |
| 4.6 | **Cluster serré en CPU** : un déploiement roulant a besoin d'une place de surge (le cluster de Romuald est à ~15,3 / 16 de requests). | Contrôle de capacité à l'étape 1 ; stratégie `maxSurge: 0, maxUnavailable: 1` pour les charges à un réplica, avec coupure annoncée. |
| 4.7 | **Chaîne d'approvisionnement** : canal compromis, version retirée. | Signature à identité épinglée, délai de maturation, liste de versions révoquées dans le manifeste du canal, pas de `force` sur la signature. |
| 4.8 | **Droits trop larges** : l'updater écrit des CRD et des Deployments. | RBAC borné par `resourceNames` et espaces de noms ; pas de `cluster-admin` ; revue du chemin d'écriture dans l'audit. **Limite connue (audit du 26/11)** : RBAC ne borne pas un `patch` à un champ ; qui détient le jeton peut réécrire toute la spec des Deployments nommés (dont l'opérateur et l'agent privilégié), donc le compte vaut en pratique cluster-admin. La vraie borne est la signature de la release. **Parade livrée** : `deploy/kubernetes/updater/admission-policy.yaml`, une ValidatingAdmissionPolicy (CEL) limitée à ce compte, qui fige tout champ du pod et des conteneurs sauf `containers[*].image` et exige un digest sha256 ; jouée contre un vrai API server par `test/updateradmission`, qui échoue aussi si une version de Kubernetes ajoute un champ que la politique ne compare pas. Elle ne borne pas le registre d'origine (un miroir local change le préfixe) : le digest lie le contenu. |
| 4.9 | **Conflit avec Argo/Flux** : le selfHeal défait la mise à jour. | §3.6 : détection, mode proposition. |
| 4.10 | Une mise à jour pendant un geste critique (Ceph, OSD). | `disturbances()`, plus blocage dur sur le remplacement d'OSD même avec `force`. |

## 5. Découpage en PR (une à la fois en CI, deux runners `frame-ci`)

| PR | Contenu | Dépend de | Notes |
|---|---|---|---|
| **P1** | **Manifeste de release signé** : étape du workflow `build.yml` sur tag `v*` qui produit et signe `release.json` ; script `hack/verify-release.sh`. Corrige au passage l'écart « images de l'overlay sans `newName`/`newTag` » de `docs/publication-chart-et-image.md`. | rien (réutilise B3) | Chemin sensible (`.github/`) : relecture Romuald. |
| **P2** | **API** : CRD `FrameRelease` (lecture seule) et `FrameUpdate`, webhook de validation, RBAC admin. Pas de logique. | P1 | Chemins sensibles (`config/`, `charts/`) : relecture Romuald. |
| **P3** | **Updater, mode sans écriture** : lecture du canal, vérification, contrôles préalables, calcul du plan. Tests `envtest`. | P2 | Le dry-run existe avant l'écriture. |
| **P4** | **Updater, écriture et retour arrière** : application ordonnée des digests, portes de santé, `RolledBack`, journal Tasks. | P3 | Cas d'échec testés (image qui ne démarre pas). |
| **P5** | **Console** : bouton, avancement, historique, retour arrière ; la carte « Préparer » passe en mode expert ; textes FR/EN. | P2 (maquette dès P2) | |
| **P6** | **Mise à jour de soi-même** : supervision croisée, reprise après redémarrage, e2e Kind sur `upgrade-e2e.yml` (n → n+1, puis échec forcé, puis retour arrière). | P4 | **Critère de sortie du bloquant bêta.** |
| **P7** | **Air-gap et registre local** : `updates.registry`, copie vérifiée, import d'un paquet de release. | P4 | Indispensable à notre propre cluster ; souhaitable pour la bêta publique. |
| **P8** | **Coexistence GitOps** (mode proposition) et retrait d'Argo de l'installation par défaut ; doc `docs/upgrading.md`. | P4 | |
| **P9** | **Autres applications (Fusora)** sur le même moteur. | P6 | Après la bêta. |

Chemin court bêta : **P1 → P6**, soit six PR. P7 à P9 ensuite.

## 6. Place dans le plan bêta

Ajouté comme **bloquant B10** dans `2026-11-16-plan-beta-frame.md` : « la console se met à jour toute seule, sans `kubectl` ni Argo ». Cela **déplace** « MAJ de Frame depuis l'UI » de la section « hors bêta » (§2.3, action 1B) vers les bloquants, et retire de la page « Limites connues » (B6) la mention « pas de rollback d'application » pour les composants Hearthrack.

Calendrier : P1 s'ajoute naturellement à B3 (même pipeline de release, avant Noël). P2 à P6 tiennent difficilement avant la bêta privée du 26/01 si le gel du 21/12 au 04/01 est respecté. **Reco** : P1 en décembre, P2 à P6 du 05/01 au 15/02, et la bêta privée glisse au plus tard au 15/02 (publique le 02/03 inchangée). Sinon, un contournement documenté « mise à jour manuelle » pour la bêta privée uniquement.

## 7. Décisions (Romuald, 24/11)

1. **Plus d'Argo chez le client** : oui. Argo reste une option GitOps (§3.6).
2. **B10 bloque la bêta privée** : oui. Elle peut glisser du 26/01 au 15/02 au pire.
3. **Délai de maturation** : 48 h chez les clients, **0 sur le cluster de Romuald**.

Reste : P1 (`.github/`) et P2 (`config/`, `charts/`) touchent des chemins sensibles et attendent la relecture de Romuald.

### Vérification des signatures (livrée avec `cmd/release-sync`)

- Identité acceptée pour le manifeste d'une version : le workflow `build.yml` de `Plume-Labs/frame` **sur le tag de cette version** (`@refs/tags/v<version>`), émetteur GitHub Actions. Pour une image : le même workflow sur n'importe quel tag de version (une image est signée par le tag qui l'a construite). Un build de `main` ne vaut jamais release.
- Seule la disposition de `cosign sign` v3 est acceptée : un bundle Sigstore attaché au digest (référents OCI). Elle est vérifiée sur une vraie signature publique (sujet, émetteur, dépôt et déclencheur faux refusés : `go test -tags live`). La vérification passe par sigstore-go seul (pas de bibliothèque cosign, donc pas d'OpenPGP dans le binaire) ; le certificat Fulcio doit porter le dépôt `Plume-Labs/frame` et le déclencheur `push`, et l'énoncé signé doit être celui de `cosign sign` (aucune autre attestation de la même identité ne vaut release).
- Le manifeste lu est relu **par le digest de l'artefact signé** et doit être octet pour octet le contenu signé.
- Racine de confiance Sigstore : récupérée par TUF à chaque passage, ou fichier fourni (`--trusted-root`) pour un site sans accès sortant ; si elle ne se charge pas, rien n'est vérifié, donc rien n'est offert.
