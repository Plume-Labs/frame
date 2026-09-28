# Gérer l'inférence depuis la console — spécification

**Date :** 2026-09-28 (révisée après relecture) · **Base :** `main` = `dd2e381`
**Statut :** proposition, à valider. Aucun code.
**Demande :** « pouvoir gérer l'inférence et pas juste un triste aperçu ».

Les détails propres à un site vont dans une annexe tenue par l'exploitant.

## 1. Constat

- **La console ne fait que lire.** `InferenceView` et `InferenceOverviewView`
  affichent les métriques llama.cpp (`/metrics`, `/props`), DCGM et TEI, par le
  pod-proxy de `frame-uiproxy`. Aucune action.
- **Le serveur réel ne passe pas par Frame.** Les serveurs de modèles d'un
  déploiement typique (llama.cpp de chat sur GPU, llama.cpp de vision, TEI pour
  les embeddings, une gateway Gateway API + EPP) sont posés par
  `deploy/scripts/inference-up.sh` et `llm-d-up.sh`, par des manifestes
  appliqués à la main ou par un `kubectl patch`. Rien ne réconcilie : l'objet
  vivant et le fichier qui le décrit divergent sans que personne ne le voie.
- **Le provider `FrameService` `inference` ne sait pas servir ce qui tourne.**
  - Catalogue codé en dur (`catalog.go` : trois modèles denses).
  - Estimation VRAM dense : `poids + KV(ctx)` sur toutes les couches. Faux pour
    un MoE dont les experts sont déchargés sur le CPU (`-ncmoe`) et pour une
    attention hybride où seules certaines couches ont un cache qui grandit.
  - Poids attendus en `-m /models/<model>.gguf` sur un PVC pré-rempli
    (sinon `ModelCacheMissing`) : ne correspond pas à la disposition d'un cache
    `-hf` (`models--org--repo/snapshots/<rev>/…`). Pas de `-ncmoe`,
    `--threads`, `--parallel`, `--alias`, ni `runtimeClassName`.
  - `setContainer` et `setModelCacheVolume` cherchent **leur** conteneur
    (`llama-cpp`) et **leur** volume (`model-cache`) par nom et ajoutent s'ils
    ne les trouvent pas : sur un Deployment existant dont le conteneur porte un
    autre nom, le Reconcile ajoute un second conteneur (même port, un second
    GPU demandé) et empile les volumes.
  - `desiredReplicas = 1` constant : pas d'arrêt possible, et un `scale` direct
    serait défait au Reconcile suivant.
  - Clé API imposée (`LLAMA_API_KEY`), voir §4.1.
  - Le pod reçoit `priorityClassName: frame-<classe>` (`priority.go`) et un
    `nodeSelector` sur `frame.plume-labs.io/service-class` : ces PriorityClass
    n'existent que si un `SchedulingPolicy` les a créées, sinon l'admission
    refuse le pod.
- **Droits.** Le rôle `cluster-control-workload-operator` (patch
  deployments et `/scale`) est lié par namespace, dont celui de l'inférence.
  Le manager n'écrit de Secret que là où `frame-manager-secret-writer` est lié ;
  l'overlay de production n'en lie aucun.

## 2. Décisions à prendre

### 2.1 Adopter l'existant ou migrer vers des FrameService

| Option | Pour | Contre |
|---|---|---|
| A. Statu quo + boutons génériques | rien à construire | deux sources de vérité, dérive, aucun garde-fou VRAM |
| B. Adoption sans migration : Frame pilote des Deployments existants déclarés dans `frame-config` | pas de bascule, pas de coupure | Frame écrit dans des objets qu'il ne possède pas ; la dérive reste ; pas de CRD pour porter l'intention |
| C. Migration : `FrameService` source de vérité, provider étendu | une intention déclarée, validée, réconciliée ; admission VRAM ; un consommateur peut lire les `FrameService` | provider à étendre ; une coupure par service migré |

**Recommandation : C comme cible, B comme étape transitoire limitée aux
actions sans paramètre** (redémarrer, arrêter, démarrer — lot 1). B n'écrit que
l'annotation `restartedAt` et `/scale`. Limite à écrire en clair dans la
console : `spec.replicas` appartient au gestionnaire `kubectl-client-side-apply`
(managedFields) — **tout `kubectl apply` ultérieur du manifeste ou du script
d'installation défait « Arrêter » sans bruit** et relance le serveur. Tout ce
qui change un argument (modèle, contexte, déchargement) attend C : le faire en
B, ce serait réécrire `args` d'un Deployment appliqué ailleurs.

Ordre de migration : le serveur de chat GPU d'abord, puis la vision (même
moteur, mode CPU `-ngl 0` à ajouter). **TEI reste adopté (B)** : autre moteur,
rien à régler hors redémarrage ; changer de modèle d'embeddings impose une
ré-indexation, donc un autre chantier.

**Reprise d'un Deployment existant (bloquant du lot 4).** Un `FrameService` qui
porte le nom et le label `app` du Deployment (sélecteur immuable, identique)
le reprend via `SetControllerReference`. Pour éviter un pod à deux conteneurs,
le provider **reprend par nom** (paramètres `containerName` / `cacheVolumeName`,
défauts `llama-cpp` / `model-cache`) plutôt que d'élaguer, et refuse (condition
`ForeignContainers`) tout conteneur étranger restant — aucune suppression
implicite. Le Reconcile remplace **en bloc** labels du gabarit, `nodeSelector`, `env`, `args` et
`securityContext` : tout ce que le serveur installé porte et que le provider
ne sait pas produire est perdu (`--metrics` — scraping et panneau console —,
`--jinja` — appels d'outils —, `-ngl`, `LLAMA_CACHE`, labels annexes). Ces
paramètres entrent au lot 4. **Critère d'acceptation : parité du gabarit de
pod** — diff entre le gabarit installé et celui que produit le Reconcile, avec
une **liste fermée d'écarts permis** (propriétaire, annotation de digest,
`securityContext`, `priorityClassName`, `nodeSelector`, chemin `-m`, montage en
lecture seule). Tout autre écart fait échouer le test.

**Chemin de retour** (obligatoire avant toute migration) : le manifeste
d'avant, **d'abord recalé sur l'objet vivant**, reste versionné chez
l'exploitant. Le `FrameService` porte `deletionPolicy: Retain` (sous `Delete`,
`reconcileDelete` supprime Deployment et Service, orphelins ou non). Retour =
suppression du `FrameService`, puis **`kubectl replace -f`** du manifeste — pas
`apply` : la fusion 3 voies d'un apply client garderait ce que Frame a ajouté
(`LLAMA_API_KEY` → 401, uid 65532, rootfs et `/models` en lecture seule,
`priorityClassName`, `nodeSelector`), et `-hf` ne démarrerait pas. Le retour
est rejoué sur un cluster de test avant toute migration en production.

### 2.2 Actions depuis la console

« Coupure » = durée sans réponse du LLM. Le serveur GPU tourne en `Recreate`
(une seule carte : l'ancien pod doit libérer le GPU avant que le nouveau
démarre), donc toute action qui recrée le pod coupe le service pendant tout le
chargement — plusieurs minutes pour un gros MoE.

| Action | Effet | Coupure | Confirmation | Rôle |
|---|---|---|---|---|
| Redémarrer | `restartedAt` sur le gabarit | un chargement (depuis le cache) | modale : requêtes en cours, durée attendue (dernier chargement mesuré) | operators |
| Arrêter | B : `/scale` à 0 ; C : `spec.suspend: true` | jusqu'au démarrage | saisir le nom du service ; raison obligatoire | operators |
| Démarrer | inverse | un chargement | simple | operators |
| Mettre à l'échelle | GPU : **0 ou 1 seulement** ; CPU : 1..N | aucune à la hausse | simple | operators |
| Régler le contexte (`-c`) | C : `parameters.contextLength` | un chargement | modale : estimation VRAM, écart au budget déclaré par le consommateur | admins |
| Régler le déchargement MoE (`-ncmoe`) | C : `parameters.cpuMoeLayers` | un chargement | idem ; débit attendu (moins de couches GPU = plus lent) | admins |
| Changer de modèle | C : `parameters.model` → entrée du catalogue | un chargement (téléchargement fait avant, §2.4) | saisir le nom ; « le LLM du consommateur est coupé ~N min » | admins |

**Hors boutons** : gateway et EPP, dont les champs appartiennent à d'autres
gestionnaires (SSA du contrôleur de gateway, Helm).

Un réglage refusé par l'admission ne touche rien. Un réglage admis qui ne
démarre pas (OOM CUDA, `CrashLoopBackOff`, pas `Ready` après
`progressDeadline`) **revient seul** aux derniers paramètres sains
(`status.lastKnownGood`), et le dit dans les conditions. Une variable à la
fois : jamais modèle et contexte dans le même geste.

### 2.3 Catalogue

| Option | Pour | Contre |
|---|---|---|
| Code (`catalog.go`) | revu en PR | une release par modèle : on ne « gère » rien |
| ConfigMap | éditable | aucun schéma, aucun statut, aucune validation |
| **CRD `FrameModel`** (cluster, `services.plume-labs.io`) | schéma, statut mesuré, RBAC admin-only, lisible par le webhook | une CRD de plus |

**Recommandation : CRD `FrameModel`** (le catalogue du code passe dans
`config/samples` ; nom qui évite `InferenceModel` de Gateway API).

- `spec.source` : `hf: <dépôt>:<quant>` ; `spec.sha256` figé une fois lu.
- `status.geometry` : relevée **par Frame dans l'en-tête GGUF** (métadonnées
  et table des tenseurs en tête de fichier, requête HTTP `Range`), jamais
  saisie à la main.
- `status.measured[]` : VRAM relevée par DCGM après chaque démarrage réussi,
  par couple (contexte, `cpuMoeLayers`).
- **Sécurité de la source** : liste d'hôtes autorisés (flag du manager,
  défaut `huggingface.co` et son CDN) ; taille totale lue dans l'en-tête et
  **plafonnée avant tout Job** ; lecture d'en-tête et téléchargement partent
  d'un pod, pas du manager. Borner sa sortie à ces hôtes ne se fait pas par
  NetworkPolicy standard (filtre IP, CDN sans IP fixe). Choix : politique FQDN
  (exige Cilium comme CNI) ou **proxy sortant à liste d'autorisation**
  (recommandé : indépendant du CNI).

**Estimer la VRAM d'un GGUF MoE avec déchargement**, par somme des tailles de
tenseurs lues dans l'en-tête :

```
VRAM ≈ W_commun_GPU                              # sortie, attention, normes, experts
                                                 # partagés, routeurs ; SANS token_embd
                                                 # (llama.cpp le garde sur le CPU)
     + Σ experts des couches restées sur GPU     # blk.i.ffn_{gate,up,down}_exps, i ≥ cpuMoeLayers
     + KV(ctx) = 2 × ctx × Σ_{couches à attention complète} (n_kv_head × head_dim) × octets(type KV)
     + état fixe des couches à attention linéaire
     + tampon de calcul(ctx, ubatch)             # mesuré, ne s'estime pas proprement
     + contexte CUDA (~300 Mio par carte)
```

`-ncmoe N` renvoie sur CPU les experts des N **premières** couches ; les tailles
par couche peuvent différer (quants dynamiques). Tampon de calcul et contexte
CUDA se calibrent par carte avec `status.measured`. Admission : estimation +
5 %, refus motivé avec la plus grande valeur admissible (« contexte max 40 960
à `cpuMoeLayers` 24 »). Une mesure du même couple prime sur l'estimation.
**RAM hôte** : experts déchargés + `token_embd` doivent tenir dans
l'allocatable du nœud **moins les requests des autres pods** qui y tournent.

### 2.4 Cache des modèles

| Option | Démarrage | Remarque |
|---|---|---|
| `-hf` sans volume persistant | téléchargement à **chaque** démarrage | inacceptable pour le chat |
| `-hf` dans `LLAMA_CACHE` sur PVC en écriture | ne télécharge qu'au premier démarrage d'un modèle | la coupure d'un changement de modèle inclut le téléchargement |
| **PVC + pré-chargement** | coupure = chargement seul | recommandé |

**Recommandation : PVC local au nœud GPU + `Job` de pré-chargement.** Un PVC
local lie le pod au nœud : acceptable tant qu'il n'y a qu'une carte, et dit
dans la console. Au changement de modèle, le Job télécharge dans le cache
pendant que l'ancien modèle sert encore (même nœud : un RWO se partage entre
pods du nœud), vérifie le `sha256`, puis la bascule a lieu.

Trois conditions, à vérifier avant le lot 4 :
1. **`-m <chemin résolu>` par défaut, pas `-hf`.** `-hf` écrit dans le cache
   (`refs/`) et consulte le hub à chaque démarrage : il ne tient pas sur un
   montage en lecture seule et rend le démarrage dépendant du réseau. Le
   provider résout le chemin `snapshots/<rev>/…` (écrit par le Job dans le
   statut) et passe `-m`.
2. **Propriétaire des fichiers.** Les `models--*` déjà remplis par `-hf` en
   root sont `root:root 0755` : un Job en uid 65532 ne peut pas y écrire. Si
   la racine du cache est en 0777, il peut créer un **nouveau** `models--X` ;
   le changement de propriétaire unique (Job root, action admin tracée) ne
   concerne que les dépôts existants à compléter.
3. **Place : le disque du nœud, pas le PVC.** Un provisionneur `local-path`
   n'applique pas la taille demandée : le vrai plafond est le système de
   fichiers qui porte le répertoire. Le Job refuse si **l'espace libre de ce
   disque** (node-exporter) < taille du fichier + 10 % ; la console l'affiche
   et propose « purger un modèle non servi » (admins, confirmation). Sur une
   classe qui applique la taille, la même règle vaut sur le volume.

`ModelCacheMissing` reste la réponse à un PVC absent ; un fichier absent devient
`ModelNotPrefetched`, qui déclenche le Job au lieu de bloquer.

### 2.5 Droits

- **Qui voit** : `frame:viewers` et au-dessus, sans changement.
- **Qui clique** : authd → jeton → `frame-uiproxy`, qui retire tout
  `Impersonate-*` reçu et pose `Impersonate-User` et
  `Impersonate-Group: frame:<rôle>`. Chaque écriture laisse une `FrameTask`.
- Lot 1 : **aucun droit nouveau** ; les liaisons
  `cluster-control-workload-operator` existantes par namespace suffisent.
- Lot C : `ClusterRole` `frameservice-editor` (`get/list/watch/patch` sur
  `frameservices`), lié par RoleBinding dans le seul namespace d'inférence à
  `frame:operators` et `frame:admins`. RBAC ne borne pas un patch à un champ :
  le **webhook de validation** (qui voit l'identité impersonnée) n'autorise à
  `frame:operators` que `spec.suspend` et l'annotation de redémarrage ; modèle,
  contexte, déchargement exigent `frame:admins`. `FrameModel` : admins.
- **Contournement connu** : les operators gardent `patch deployments` dans ce
  namespace. Un patch direct du Deployment passe à côté du webhook ; le
  Reconcile suivant le défait, donc il ne dure pas, mais il peut couper le
  service entre-temps et n'est tracé que par la `FrameTask`. À retirer le jour
  où tous les serveurs du namespace sont des `FrameService`.
- Manager : `frame-manager-secret-writer` lié dans le namespace du
  `FrameService` et chaque `projectTo` — seulement si la clé est gardée (§4.1).

### 2.6 Garde-fous : toute bascule coupe le LLM du consommateur

- **Nom servi stable.** `--alias <nom du FrameService>` : le nom que le
  consommateur envoie (clé de routage de l'EPP) ne change plus avec les poids.
- **Budget de contexte publié.** `status.contextLength` (budget d'un slot :
  `-c` / `--parallel`). Le baisser sous le budget déclaré par le consommateur
  est refusé sauf case « je sais » (admins).
- **État explicite.** Condition `Maintenance` et annotation
  `services.plume-labs.io/maintenance` = `{raison, par, jusqu'à}` posées
  **avant** l'écriture disruptive, retirées au retour `Ready`. Bandeau console :
  *En maintenance — changement de modèle par X, retour estimé HH:MM*.
- **Fenêtre** `spec.maintenanceWindow` : hors fenêtre, confirmation renforcée,
  jamais un blocage. **Requêtes en cours** : « attendre la fin » (max 10 min).
- **Repli du consommateur.** Hors Frame : il lit `Maintenance` / `Ready` et
  met ses traitements en attente au lieu de les faire échouer.

## 3. Ce qui manquait

- **Dérive** : afficher l'écart entre l'objet vivant et son `last-applied` (lot 1).
- **Énergie** : arrêter le serveur libère GPU et RAM, pas le nœud (extinction Redfish : autre lot).
- **Statut** : durée de chargement mesurée (c'est elle que la console annonce) et
  `status.history` (5 dernières configurations, chargement, VRAM mesurée).

## 4. Exigences et décisions techniques du lot 4

1. **Clé API — décision de l'exploitant.** Sur llama.cpp `master`
   (`server-http.cpp`), sous clé seuls `/health`, `/v1/health` et les fichiers
   de l'interface restent publics : `/metrics` et `/props` répondent 401, ce qui
   casse le panneau console (pod-proxy) et le scraping de l'EPP. Deux voies :
   - **garder la clé** : la console, l'EPP et la PodMonitor doivent la
     présenter ; le writer de Secret doit être lié dans le namespace ; le
     consommateur la lit ;
   - **`auth: none`** pour un service joignable seulement via la gateway,
     borné par une NetworkPolicy (gateway, EPP, uiproxy, Prometheus). Coûte
     du code des deux côtés : `ensureAPIKey` est imposé par le provider (il
     faut un paramètre `auth`, absent aujourd'hui), et un consommateur qui
     ignore un service sans `secretRef` doit l'accepter.
   Réserve : vérifié sur `master`, pas sur le build déployé.
2. **À vérifier (sans l'exploitant, sur un cluster de test)** : la gateway
   transmet-elle `Authorization` jusqu'au pod ?
3. **`runtimeClassName` est requis** là où le runtime GPU l'exige : paramètre
   du provider, ou dérivé du `FrameNode`.
4. **PriorityClass et classe de service.** Les `frame-*` n'existent que si un
   `SchedulingPolicy` les a créées ; sinon l'admission refuse le pod. Le label
   `service-class` du nœud vient de son `FrameNode` (un étiquetage manuel
   serait défait) et la classe choisit à la fois le nœud et la priorité :
   changer la classe du `FrameNode` GPU déplace aussi les `FrameJob`. Un
   serveur installé sans `priorityClassName` tourne en priorité 0, déjà
   préemptible par toute classe supérieure ; la migration ne doit pas le
   laisser **plus bas** que les charges qui le consomment. Prérequis :
   `SchedulingPolicy` présent et classe du nœud GPU choisie, ou découpler
   priorité et placement pour ce provider.

## 5. Découpage en lots

| Lot | Contenu | Risque | Valeur |
|---|---|---|---|
| **1. Piloter l'existant** | Panneau « Service » dans `InferenceView` : modèle servi (`/props`), contexte, slots, pod, depuis quand, VRAM (DCGM), dernière `FrameTask`. Redémarrer / Arrêter / Démarrer via `restart()`/`scale()` du SDK, qui veulent un nom de Deployment alors que `frame-config.ts` déclare un sélecteur : ajouter un champ `deployment` à chaque intégration (défaut : le Deployment unique dont le sélecteur égale celui déclaré ; bouton absent sinon). Ajouter la vision (seuls chat et TEI y sont). Gateway et EPP exclus. Avertissement « un `kubectl apply` défait l'arrêt ». Aucun changement Go, aucun droit nouveau. | faible | on n'ouvre plus `kubectl` pour relancer un serveur figé |
| **2. Maintenance explicite** | Annotation + bandeau §2.6 avant chaque action du lot 1 ; « attendre la fin des requêtes » ; durée de chargement mesurée | faible | le consommateur peut se replier |
| **3. Catalogue et estimation** | CRD `FrameModel`, lecture d'en-tête bornée, estimateur §2.3, calibrage DCGM ; « tient / ne tient pas » en lecture | nul (lecture) | on sait ce qu'un réglage coûtera |
| **4. Provider capable** | Reprise par nom et parité du gabarit (§2.1), `deletionPolicy: Retain`, `-m` sur chemin résolu, `extraArgs` bornés (`--metrics`, `--jinja`, `-ngl`), `env` et labels conservés, paramètre `auth`, `cpuMoeLayers`, `threads`, `parallel`, `alias`, `runtimeClassName`, `suspend`, mode CPU, `Recreate`, `lastKnownGood`, pré-chargement ; prérequis §2.4 et §4 ; migration du serveur de chat en fenêtre, retour rejoué avant | moyen | une source de vérité |
| **5. Réglages depuis la console** | Contexte, déchargement, changement de modèle (admins), webhook par champ, purge du cache | moyen, borné par admission + retour auto | la demande, entière |
| **6. Vision** | Vision en `FrameService` CPU ; TEI reste adopté | faible | — |

## 6. Questions pour l'exploitant

1. Cible C (FrameService source de vérité) validée, avec B comme étape ?
2. Clé API (coût : lier le writer de Secret dans le namespace d'inférence en
   production, clé portée par console, EPP, PodMonitor) ou `auth: none` +
   NetworkPolicy (coût : du code côté provider et côté consommateur) (§4.1) ?
3. Coupure d'un chargement acceptée à chaque migration et à chaque réglage ?
4. Détails de site déjà présents dans ce dépôt : les laisser ou les déplacer ?
5. `SchedulingPolicy` en production et classe du nœud GPU (§4.4) : cela
   change priorités et placement de tout le cluster, pas seulement du LLM.
