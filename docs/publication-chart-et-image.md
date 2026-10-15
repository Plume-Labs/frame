# Publier les images et le chart Helm de Frame

Procédure pour publier une version de Frame : les images de conteneur (opérateur,
UI et les autres), puis le chart `charts/frame`. Chaque étape est exécutée par un
**humain** ; aucun agent ne pousse, ne publie ni n'écrit sur un cluster.

Rédigé le 2026-09-24 sur la branche `fix/ci-green`. Les exemples utilisent
`VERSION=1.0.0` ; remplacer par la version publiée.

## 1. Ce qui existe aujourd'hui

### Les images que le chart référence

| Valeur du chart | Défaut | Construite depuis | Publiée ? |
|---|---|---|---|
| `image.repository` / `image.tag` (opérateur) | aucun dépôt (`required`), tag = `.Chart.AppVersion` (`1.0.0`, sans `v`) | `Dockerfile.controller` | non |
| `provisiond.image.repository` / `.tag` | `frame-provisiond:latest` (nom local) | `Dockerfile.provisiond` | non |

Le chart n'installe que l'opérateur et provisiond. Les autres images servent
l'installation kustomize (`deploy/kubernetes/...`, `config/...`) :

| Image (nom dans les manifestes) | Construite depuis | Cible `make` |
|---|---|---|
| `cluster-control:latest` (UI) | `Dockerfile` (racine) | `docker-build-ui` / `docker-push-ui` |
| `frame-uiproxy:latest` | `Dockerfile.uiproxy` | `docker-build-uiproxy` / `docker-push-uiproxy` |
| `cluster-control-auth:latest` (authd) | `Dockerfile.authd` | `docker-build-authd` / `docker-push-authd` |
| `frame-agent:latest` (DaemonSet node-tuning) | `Dockerfile.agent` | `docker-build-agent` / `docker-push-agent` |

Aucune n'est publiée. L'overlay `deploy/kubernetes/overlays/production`, que le
job `Release` rend en `cluster-control-vX.Y.Z.yaml`, déclare `cluster-control`
et `frame-uiproxy` sans `newName`/`newTag` : le manifeste attaché à une release
référence donc des images qui n'existent sur aucun registre. Ce défaut n'est pas
corrigé ici.

### Ce que fait la CI sur un tag `v*` (avant cette branche)

`.github/workflows/build.yml`, job `Build` : il construit **uniquement le
`Dockerfile` racine, c'est-à-dire l'UI**, et le pousse sous
`ghcr.io/plume-labs/frame` (`IMAGE_NAME: ${{ github.repository }}`), aux tags
`1.0.0`, `1.0` et `sha-…`. Deux défauts :

- le nom nu du dépôt porte l'UI, là où un lecteur attend l'opérateur ;
- l'opérateur et provisiond, les deux seules images du chart, ne sont jamais
  poussés.

Et le tag `v1.0.0` préparé localement pointe sur `release/v1.0.0`, qui contient
encore ce workflow : un tag poussé exécute le workflow **tel qu'il est dans le
commit taggé**, pas celui de `main`.

### Le correctif proposé (commit sur `fix/ci-green`, jamais exécuté)

Le job `Build` devient une matrice sur les cinq Dockerfiles que le job `scan`
contrôle déjà (et dont dépend `Release`) : il pousse
`ghcr.io/plume-labs/frame-{controller,ui,authd,uiproxy,provisiond}` avec les
mêmes tags (`{{version}}` = `1.0.0`, `{{major}}.{{minor}}`, `sha-…`, branche).
`Dockerfile.agent` n'est ni scanné ni poussé : l'ajouter au scan d'abord, puis
au build. Vérifié avec `actionlint`, jamais lancé : il ne publie que depuis
GitHub.

## 2. Conventions proposées

- Images : `ghcr.io/plume-labs/frame-<nom>:<version sans v>`.
- Chart : OCI, `oci://ghcr.io/plume-labs/charts/frame`, version = `version` de
  `Chart.yaml`.

Pourquoi un chart OCI sur GHCR plutôt qu'un dépôt Helm classique (`index.yaml`
sur GitHub Pages, chart-releaser) :

- **Même registre, même accès que les images.** Un seul jeton (`write:packages`),
  une seule visibilité à régler, un seul endroit où chercher ce qu'une version
  publie. Un dépôt `gh-pages` ajouterait une branche, un `index.yaml` à
  régénérer et un workflow détenant `contents: write`, précisément le droit que
  `build.yml` réduit au job `Release`.
- **Natif dans Helm** depuis 3.8 (`helm push`, `helm install oci://…`) ; la CI
  utilise Helm v4.2.3.
- **Adressable par digest** : une version poussée ne se réécrit pas en silence,
  alors qu'un `index.yaml` peut pointer ailleurs du jour au lendemain.
- `charts/` dans le chemin sépare le chart de l'image qui porterait sinon le même
  nom (`ghcr.io/plume-labs/frame`).

## 3. Droits et secrets nécessaires

| Pour | Quoi | Qui le détient |
|---|---|---|
| Pousser `main` et le tag | droit d'écriture sur `Plume-Labs/frame` | le mainteneur qui publie |
| Push des images par la CI | `GITHUB_TOKEN` avec `packages: write` (déjà déclaré sur le job `Build`) ; dans les réglages de l'organisation, « Workflow permissions » ne doit pas interdire l'écriture des packages | aucun secret à créer ; un propriétaire de l'organisation vérifie le réglage |
| Push manuel (images hors CI, chart) | PAT **classic** avec `write:packages` (inclut `read:packages`), autorisé pour le SSO de l'organisation si celle-ci l'impose | le mainteneur, sur son compte ; jamais committé |
| Rendre un package public | rôle *admin* du package (ou propriétaire de l'organisation) | propriétaire de `Plume-Labs` |
| Release GitHub | `gh` authentifié avec droit d'écriture, ou l'interface web | le mainteneur |

Les packages GHCR sont **privés à la création**. Tant qu'ils le restent, chaque
installation a besoin d'un `imagePullSecret` et d'un `helm registry login`.

## 4. Procédure

Toutes les commandes depuis la racine du dépôt :

```bash
cd /path/to/frame
VERSION=1.0.0
```

### Étape 0 — Faire entrer le correctif CI dans la version (humain)

Le tag doit pointer sur un commit qui contient le workflow corrigé et une CI
verte. `release/v1.0.0` et `v1.0.0` ont été préparés depuis `main` à `0ebb2ac`,
avant `fix/ci-green`. `git merge-tree` ne signale aucun conflit entre les deux
branches (vérifié le 2026-09-24).

```bash
git switch main
git merge --ff-only fix/ci-green                 # main n'a pas bougé : avance rapide
git switch release/v1.0.0
git rebase main                                   # rejoue les 3 commits de release
git tag -d v1.0.0                                 # le tag n'a jamais été poussé
git tag -a v1.0.0 -m "Frame v1.0.0" -m "See CHANGELOG.md, section 1.0.0."
git switch main
git merge --ff-only release/v1.0.0
git rev-parse main v1.0.0^{commit}               # même SHA deux fois
```

Ne jamais `git add -A` : `config/manager/kustomization.yaml` (modifié) et
`deploy/samples/test-cluster/*.sealed.yaml` (non suivis) ne font pas partie de
la version.

### Étape 1 — Pousser `main`, attendre la CI (humain)

```bash
git push origin main
```

Attendre que les quatre workflows (`Lint`, `Tests`, `E2E Tests`,
`CI/CD Pipeline`) soient verts sur ce commit. Sur `main`, le job `Build` pousse
déjà les cinq images aux tags `main` et `sha-…` : c'est la première preuve que
le correctif fonctionne, avant qu'un tag de version ne soit en jeu.

### Étape 2 — Pousser le tag : la CI publie les cinq images (humain)

```bash
git push origin v1.0.0
```

Déclenche `build.yml` : `Build` pousse
`ghcr.io/plume-labs/frame-{controller,ui,authd,uiproxy,provisiond}:1.0.0`
(et `:1.0`, `:sha-…`), puis `Release` crée la release GitHub si `build`,
`integration` et `scan` sont tous verts. Un job amont rouge fait **sauter
`Release` sans erreur visible** : vérifier son état.

Contrôle :

```bash
for n in controller ui authd uiproxy provisiond; do
  skopeo inspect --format '{{.Name}}:{{.Digest}}' docker://ghcr.io/plume-labs/frame-$n:$VERSION
done
```

### Étape 2 bis — Si la CI ne peut pas publier : pousser à la main (humain)

Depuis le commit taggé, avec podman :

```bash
git switch --detach v$VERSION
echo "$GHCR_PAT" | podman login ghcr.io -u <utilisateur-github> --password-stdin
make docker-build          docker-push          CONTAINER_TOOL=podman IMG=ghcr.io/plume-labs/frame-controller:$VERSION
make docker-build-provisiond docker-push-provisiond CONTAINER_TOOL=podman IMG_PROVISIOND=ghcr.io/plume-labs/frame-provisiond:$VERSION
make docker-build-ui       docker-push-ui       CONTAINER_TOOL=podman IMG_UI=ghcr.io/plume-labs/frame-ui:$VERSION
make docker-build-authd    docker-push-authd    CONTAINER_TOOL=podman IMG_AUTHD=ghcr.io/plume-labs/frame-authd:$VERSION
make docker-build-uiproxy  docker-push-uiproxy  CONTAINER_TOOL=podman IMG_UIPROXY=ghcr.io/plume-labs/frame-uiproxy:$VERSION
git switch main
```

Un push manuel ne porte pas le label `org.opencontainers.image.source` que
`metadata-action` ajoute en CI : lier chaque package au dépôt à la main
(Package settings → *Connect repository*), sinon il n'hérite pas de ses droits.
Aucun scan Trivy ne précède ce chemin : lancer `make scan` avant de pousser.

### Étape 2 ter — L'image agent (humain, hors CI)

Ni scannée ni poussée par la CI. Si l'installation kustomize doit la tirer d'un
registre :

```bash
make docker-build-agent docker-push-agent CONTAINER_TOOL=podman IMG_AGENT=ghcr.io/plume-labs/frame-agent:$VERSION
```

### Étape 3 — Rendre les packages publics (propriétaire de l'organisation)

github.com/orgs/Plume-Labs/packages → chaque `frame-*` → *Package settings* →
*Change visibility* → *Public*. Même chose pour `charts/frame` après l'étape 4.
Irréversible sans supprimer le package : décider avant.

### Étape 4 — Publier le chart (humain)

Aucun mécanisme n'existe dans le dépôt (pas de job, pas de cible `make`) ;
c'est une commande manuelle.

```bash
git switch --detach v$VERSION
grep -E '^(version|appVersion):' charts/frame/Chart.yaml     # doivent valoir $VERSION
helm lint charts/frame --set image.repository=ghcr.io/plume-labs/frame-controller
mkdir -p /tmp/frame-chart
helm package charts/frame -d /tmp/frame-chart                # -> frame-$VERSION.tgz
echo "$GHCR_PAT" | helm registry login ghcr.io -u <utilisateur-github> --password-stdin
helm push /tmp/frame-chart/frame-$VERSION.tgz oci://ghcr.io/plume-labs/charts
git switch main
```

Contrôle :

```bash
helm show chart oci://ghcr.io/plume-labs/charts/frame --version $VERSION
```

Installation qui en résulte (deux valeurs n'ont pas de défaut utilisable) :

```bash
helm install frame oci://ghcr.io/plume-labs/charts/frame --version $VERSION \
  --namespace frame-system --create-namespace \
  --set image.repository=ghcr.io/plume-labs/frame-controller \
  --set provisiond.image.repository=ghcr.io/plume-labs/frame-provisiond \
  --set provisiond.image.tag=$VERSION \
  --set provisiond.media.url=http://<ip-de-gestion>:30581
```

(`--set provisiond.enabled=false` à la place des trois `provisiond.*` sur un
cluster qui ne provisionnera jamais de machine.) Faire pointer les défauts de
`values.yaml` sur ces noms rendrait l'installation plus courte ; ce n'est pas
fait ici.

### Étape 5 — Release GitHub (humain)

Si `Release` a tourné, y joindre le chart ; sinon la créer. Voir
`docs/release-v1.0.0-publication.md` (branche `release/v1.0.0`), section 5,
pour les commandes `gh release upload/edit/create`.

## 5. Ce qui reste non prouvé

- Le job `Build` matriciel n'a jamais tourné : il ne s'exécute que sur GitHub.
  La première exécution sur `main` (étape 1) le prouve, sans tag en jeu.
- Que `metadata-action` mette `Plume-Labs` en minuscules : c'est déjà ce dont
  dépendait l'ancien nom `ghcr.io/plume-labs/frame`, non revérifié ici.
- Le réglage « Workflow permissions » de l'organisation n'a pas été lu.
