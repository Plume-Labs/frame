# Gérer l'inférence depuis la console — spécification

**Date :** 2026-09-28
**Statut :** proposition, à valider. Aucun code.
**Base :** `main` = `dd2e381`
**Demande :** « pouvoir gérer l'inférence et pas juste un triste aperçu ».

Les détails propres à un site (nœuds, mesures, écarts relevés) n'ont pas leur
place dans ce dépôt public : ils vont dans une annexe tenue par l'exploitant.

## 1. Constat

- **La console ne fait que lire.** `InferenceView` et `InferenceOverviewView`
  affichent les métriques llama.cpp (`/metrics`, `/props`), DCGM et TEI, par le
  pod-proxy de `frame-uiproxy`. Aucune action.
- **Le serveur réel ne passe pas par Frame.** Les serveurs de modèles d'un
  déploiement typique (llama.cpp de chat sur GPU, llama.cpp de vision, TEI pour
  les embeddings sur CPU, une gateway Gateway API + EPP) sont posés par
  `deploy/scripts/inference-up.sh` et `llm-d-up.sh`, ou par des manifestes
  appliqués à la main. Rien ne réconcilie : l'objet vivant et le fichier qui le
  décrit divergent sans que personne ne le voie.
- **Le provider `FrameService` `inference` ne sait pas servir ce qui tourne.**
  - Catalogue codé en dur (`catalog.go` : trois modèles denses), qui ignore
    le modèle effectivement servi.
  - Estimation VRAM dense : `poids + KV(ctx)` sur toutes les couches. Faux pour
    un MoE dont les experts sont déchargés sur le CPU (`-ncmoe`) et pour une
    attention hybride où seules certaines couches ont un cache qui grandit.
  - Poids attendus en `/models/<model>.gguf` sur un PVC pré-rempli
    (`resolveModelCache`, sinon `ModelCacheMissing`). Pas de `-hf`, pas de
    `-ncmoe`, `--threads`, `--parallel`, `--alias`, ni `runtimeClassName`.
  - `desiredReplicas = 1` constant : pas d'arrêt possible, et un `scale` direct
    serait défait au Reconcile suivant.
  - Clé API imposée (`LLAMA_API_KEY`) : une gateway et un EPP qui appellent
    sans clé casseraient à la migration (à vérifier : `/metrics` sous clé).
- **Droits.** Le rôle `cluster-control-workload-operator` (patch
  deployments et `/scale`) est lié par namespace, dont celui de l'inférence.
  Le manager n'écrit de Secret que là où `frame-manager-secret-writer` est lié ;
  l'overlay de production n'en lie aucun.

## 2. Décisions à prendre

### 2.1 Adopter l'existant ou migrer vers des FrameService

| Option | Pour | Contre |
|---|---|---|
| A. Statu quo + boutons génériques | rien à construire | deux sources de vérité, dérive, aucun garde-fou VRAM |
| B. Adoption sans migration : Frame pilote des Deployments existants repérés par label `frame.plume-labs.io/inference=<rôle>` | pas de bascule, pas de coupure | Frame écrit dans des objets qu'il ne possède pas : conflits de propriété de champs avec `kubectl apply`/Helm, la dérive reste, pas de CRD pour porter l'intention |
| C. Migration : `FrameService` source de vérité, provider étendu | une intention déclarée, validée, réconciliée ; admission VRAM ; le consommateur lit déjà les `FrameService` | provider à étendre ; une coupure par service migré |

**Recommandation : C comme cible, B comme étape transitoire limitée aux
actions sans paramètre** (redémarrer, arrêter, démarrer — lot 1). B n'écrit que
l'annotation `restartedAt` et `/scale`, deux champs qu'aucun autre gestionnaire
ne revendique : pas de conflit. Tout ce qui change un argument (modèle,
contexte, déchargement) attend C : le faire en B, ce serait réécrire
`args` d'un Deployment appliqué ailleurs — la dérive qu'on veut supprimer.

Ordre de migration : le serveur de chat GPU d'abord (celui qu'on veut régler),
puis la vision (même moteur, mode CPU `-ngl 0` à ajouter). **TEI reste adopté
(B)** : autre moteur, sur CPU, rien à régler hors redémarrage ; un provider
`embeddings` ne se justifiera que le jour où l'on voudra changer de modèle
d'embeddings — ce qui impose une ré-indexation, donc un autre chantier.

**Chemin de retour** (obligatoire avant toute migration) : le manifeste
d'avant, **d'abord recalé sur l'objet vivant**, reste versionné chez
l'exploitant. Retour = `kubectl delete frameservice <nom> --cascade=orphan`
(le Deployment survit, sans propriétaire), puis `kubectl apply` du manifeste.
Aucune coupure si les arguments sont identiques. Tant que le retour n'a pas été
joué une fois sur un cluster de test, la migration ne passe pas en production.

**Adoption par la migration.** Un `FrameService` qui porte le nom et le label
`app` du Deployment existant (sélecteur immuable, identique) le reprend :
`SetControllerReference` pose le propriétaire, le Reconcile réécrit le gabarit.
L'EPP et le `Service` continuent de sélectionner les mêmes pods. Une coupure :
un rechargement du modèle.

### 2.2 Actions depuis la console

« Coupure » = durée sans réponse du LLM. Le serveur GPU tourne en `Recreate`
(une seule carte : l'ancien pod doit libérer le GPU avant que le nouveau
démarre), donc toute action qui recrée le pod coupe le service pendant tout le
chargement.

| Action | Effet | Coupure | Confirmation | Rôle |
|---|---|---|---|---|
| Redémarrer | `restartedAt` sur le gabarit | un chargement (depuis le cache) | modale : requêtes en cours, durée attendue (dernier chargement mesuré) | operators |
| Arrêter | B : `/scale` à 0 ; C : `spec.suspend: true` | jusqu'au démarrage | saisir le nom du service ; raison obligatoire | operators |
| Démarrer | inverse | un chargement | simple | operators |
| Mettre à l'échelle | GPU : **0 ou 1 seulement**, une carte = un pod ; CPU (TEI, vision CPU) : 1..N | aucune à la hausse | simple | operators |
| Régler le contexte (`-c`) | C : `parameters.contextLength` | un chargement | modale avec l'estimation VRAM et l'écart au budget déclaré par le consommateur | admins |
| Régler le déchargement MoE (`-ncmoe`) | C : `parameters.cpuMoeLayers` | un chargement | idem ; affiche le débit attendu (moins de couches GPU = plus lent) | admins |
| Changer de modèle | C : `parameters.model` → entrée du catalogue | téléchargement (évité par le pré-chargement, §2.4) + un chargement | saisir le nom ; afficher « le LLM du consommateur est coupé ~N min » | admins |

Un réglage refusé par l'admission ne touche rien. Un réglage admis qui ne
démarre pas (OOM CUDA, `CrashLoopBackOff`, pas `Ready` après
`progressDeadline`) **revient seul** aux derniers paramètres sains
(`status.lastKnownGood`), et le dit dans les conditions. Sans ce retour,
un mauvais `-ncmoe` laisse le consommateur sans LLM jusqu'à ce qu'un humain
passe.

La console n'offre jamais le changement de modèle et de contexte dans le même
geste : une variable à la fois, pour savoir laquelle a cassé.

### 2.3 Catalogue

| Option | Pour | Contre |
|---|---|---|
| Code (`catalog.go`) | revu en PR | une release par modèle : on ne « gère » rien |
| ConfigMap | éditable | aucun schéma, aucun statut, aucune validation |
| **CRD `FrameModel`** (cluster) | schéma, statut mesuré, RBAC admin-only, lisible par le webhook | une CRD de plus |

**Recommandation : CRD `FrameModel`.** Le catalogue du code devient des
exemples dans `config/samples`. Nom choisi pour ne pas heurter `InferenceModel`
de Gateway API Inference Extension.

- `spec.source` : `hf: <dépôt>:<quant>` (ou chemin sur PVC) ; `spec.sha256`
  du fichier, figé une fois lu.
- `status.geometry` : relevée **par Frame dans l'en-tête GGUF** (métadonnées
  et table des tenseurs au début du fichier : quelques Mio via une requête
  HTTP `Range`, sans télécharger les poids) — jamais saisie à la main.
- `status.measured[]` : VRAM relevée par DCGM après chaque démarrage réussi,
  par couple (contexte, `cpuMoeLayers`).

**Estimer la VRAM d'un GGUF MoE avec déchargement.** Par somme des tailles de
tenseurs lues dans l'en-tête, pas par un « nombre de paramètres » :

```
VRAM ≈ W_commun                                  # embeddings, sortie, attention, normes,
                                                 # experts partagés, routeurs : tout sauf *_exps
     + Σ experts des couches restées sur GPU     # blk.i.ffn_{gate,up,down}_exps, i ≥ cpuMoeLayers
     + KV(ctx)  = 2 × ctx × Σ_{couches à attention complète} (n_kv_head × head_dim) × octets(type KV)
     + état fixe des couches à attention linéaire (indépendant du contexte)
     + tampon de calcul(ctx, ubatch)             # mesuré, ne s'estime pas proprement
     + contexte CUDA (~300 Mio, constant par carte)
```

`-ncmoe N` renvoie sur CPU les experts des N **premières** couches : la somme
porte sur les couches restantes, dont les tailles peuvent différer (quants
dynamiques « UD- »). Le tampon de calcul et le contexte CUDA se calibrent par
carte avec `status.measured`. Admission : estimation + marge de 5 %, refus
motivé avec la plus grande valeur admissible (« contexte max 40 960 à
`cpuMoeLayers` 36 »). Une mesure existante pour le même couple prime sur
l'estimation. **La RAM hôte est aussi vérifiée** : les experts déchargés
(Σ experts des N premières couches) doivent tenir dans l'allocatable du nœud,
sinon on échange la panne GPU contre une panne mémoire du nœud.

### 2.4 Cache des modèles

| Option | Démarrage | Remarque |
|---|---|---|
| `-hf` sans volume persistant | téléchargement à **chaque** démarrage (dizaines de Gio) | inacceptable pour le chat ; déjà le cas pour un TEI sur `emptyDir` |
| `-hf` dans `LLAMA_CACHE` sur PVC | ne télécharge qu'au premier démarrage d'un modèle | la coupure d'un changement de modèle inclut le téléchargement |
| **PVC + pré-chargement** | coupure = chargement seul | recommandé |

**Recommandation : PVC par nœud GPU (`local-path` ou équivalent) + `Job` de
pré-chargement.** Un PVC local lie le pod au nœud : c'est acceptable tant
qu'il n'y a qu'une carte, et c'est dit dans la console. Au changement de
modèle, Frame lance d'abord un `Job` qui télécharge dans le cache pendant que
l'ancien modèle sert encore (même nœud : un RWO se partage entre pods d'un
même nœud), vérifie le `sha256`, puis bascule. Le serveur monte le cache
**en lecture seule**, comme aujourd'hui ; seul le Job écrit.

- Place : le Job refuse si l'espace libre < taille du fichier + 10 %. La
  console affiche l'occupation et propose « purger un modèle non servi »
  (admins, confirmation).
- Taille du PVC : ≥ 2 × le plus gros modèle du catalogue (servi + suivant).
  Un PVC ne se réduit pas ; il s'agrandit si la classe le permet.
- `ModelCacheMissing` reste la réponse à un PVC absent ; un fichier absent
  devient `ModelNotPrefetched`, qui déclenche le Job au lieu de bloquer.
- Chemin : le provider lit le chemin réel dans le cache (disposition
  `models--org--repo/snapshots/<rev>/…`), plus `/models/<nom>.gguf`.
- Droits d'écriture : le Job tourne sous l'uid du serveur (65532) ; le
  répertoire racine du cache doit lui être accessible en écriture.

### 2.5 Droits

- **Qui voit** : `frame:viewers` et au-dessus, sans changement.
- **Qui clique** : l'identité passe par authd → jeton → `frame-uiproxy`, qui
  retire tout `Impersonate-*` reçu et pose `Impersonate-User` et
  `Impersonate-Group: frame:<rôle>`. Chaque écriture laisse une `FrameTask`.
- Lot 1 (adoption) : **aucun droit nouveau** ; les liaisons
  `cluster-control-workload-operator` existantes par namespace suffisent.
- Lot C : nouveau `ClusterRole` `frameservice-editor` (`get/list/watch/patch`
  sur `frameservices`), lié par **RoleBinding dans le seul namespace
  d'inférence** à `frame:operators` et `frame:admins`. RBAC ne sait pas
  borner un patch à un champ : le **webhook de validation** (qui voit déjà
  l'identité impersonnée) n'autorise à `frame:operators` que `spec.suspend` et
  l'annotation de redémarrage ; modèle, contexte et déchargement exigent
  `frame:admins`. `FrameModel` : écriture admins seulement.
- Manager : `frame-manager-secret-writer` lié par RoleBinding dans le
  namespace du `FrameService` et dans chaque `projectTo` — seulement si la
  clé API est gardée (voir §4).

### 2.6 Garde-fous : toute bascule coupe le LLM du consommateur

- **Nom servi stable.** Le provider pose `--alias <nom du FrameService>` :
  le nom que le consommateur envoie (clé de routage de l'EPP) ne change plus
  avec les poids. Sans cela, changer de modèle impose de redéployer le
  consommateur, sinon 404.
- **Budget de contexte publié.** `status.contextLength` (le budget d'un slot :
  `-c` / `--parallel`). Baisser le contexte sous le budget que le
  consommateur déclare est refusé sauf case « je sais » (admins).
- **État explicite.** Condition `Maintenance` et annotation
  `frame.plume-labs.io/maintenance` = `{raison, par, jusqu'à}` posées **avant**
  l'écriture disruptive, retirées au retour `Ready`. La console affiche un
  bandeau : *En maintenance — changement de modèle par X, retour estimé HH:MM*.
- **Fenêtre de maintenance.** `spec.maintenanceWindow` (cron + durée)
  optionnel : hors fenêtre, les actions disruptives demandent une
  confirmation renforcée (saisie du nom), jamais un blocage — un serveur
  planté doit pouvoir redémarrer à toute heure.
- **Requêtes en cours.** La modale affiche `requests_processing` et propose
  « attendre la fin » (l'action part quand il tombe à 0, délai max 10 min).
- **Repli du consommateur.** Hors Frame : le consommateur lit la condition
  `Maintenance` / `Ready` et met ses traitements en attente au lieu de les
  faire échouer (voir l'annexe du site).

## 3. Ce qui manquait

- **Recaler avant d'adopter** : comparer l'objet vivant au fichier versionné
  et afficher l'écart dans la console (« dérive ») — utile dès le lot 1.
- **Énergie** : arrêter le serveur libère le GPU et la RAM, pas le nœud. Mettre
  le nœud GPU hors tension (Redfish) est un autre lot : il héberge souvent
  d'autres pods, et le rallumer + recharger dépasse vingt minutes.
- **Durée de chargement mesurée** : Frame note la durée de chaque démarrage
  (création du pod → `Ready`) dans le statut ; c'est elle que la console
  annonce, pas une valeur en dur.
- **Historique** : les `FrameTask` suffisent pour « qui » ; `status.history`
  (5 dernières configurations avec durée de chargement et VRAM mesurée) sert
  à comparer deux réglages.

## 4. Points ouverts à vérifier avant le lot 4

1. llama.cpp sous `LLAMA_API_KEY` : `/metrics` et `/health` restent-ils
   publics ? Sinon l'EPP et la console perdent leurs métriques. Option : un
   paramètre `auth: none` pour un service joignable seulement via la gateway,
   borné par une NetworkPolicy.
2. La gateway transmet-elle `Authorization` jusqu'au pod ?
3. Le provider doit poser `runtimeClassName` (paramètre, ou dérivé du
   `FrameNode`) : sans lui, pas de GPU sur une distribution qui l'exige.

## 5. Découpage en lots

| Lot | Contenu | Risque | Valeur |
|---|---|---|---|
| **1. Piloter l'existant** | Panneau « Service » dans `InferenceView` : modèle servi (`/props`), contexte, slots, pod, depuis quand, VRAM (DCGM), dernière `FrameTask`. Boutons Redémarrer / Arrêter / Démarrer sur les serveurs déclarés dans `frame-config` (`restart()`/`scale()` du SDK existant). Modales §2.2. Aucun changement Go, aucun droit nouveau. | faible : deux champs non disputés | on n'ouvre plus `kubectl` pour relancer un serveur figé |
| **2. Maintenance explicite** | Annotation + bandeau §2.6 posés par la console avant chaque action du lot 1 ; « attendre la fin des requêtes » ; durée de chargement mesurée | faible | le consommateur peut se replier au lieu d'échouer |
| **3. Catalogue et estimation** | CRD `FrameModel`, lecture d'en-tête GGUF, estimateur MoE §2.3, calibrage DCGM ; la console affiche « tient / ne tient pas » pour la config courante et pour une config simulée — en lecture | nul (lecture) | on sait ce qu'un réglage coûtera avant de l'essayer |
| **4. Provider capable** | `-hf`/cache, `cpuMoeLayers`, `threads`, `parallel`, `alias`, `runtimeClassName`, `suspend`, mode CPU, `Recreate`, `lastKnownGood`, pré-chargement ; admission sur `FrameModel`. Migration du serveur de chat en fenêtre, retour joué avant | moyen : une coupure, retour prêt | une source de vérité |
| **5. Réglages depuis la console** | Contexte, déchargement, changement de modèle (admins), webhook par champ, purge du cache | moyen, borné par admission + retour auto | la demande, entière |
| **6. Vision, puis TEI si besoin** | Vision en `FrameService` CPU ; TEI reste adopté | faible | — |
