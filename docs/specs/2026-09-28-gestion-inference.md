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
  - Clé API imposée dans llama.cpp (`LLAMA_API_KEY`), voir §4.1.
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
implicite. Le Reconcile remplace **en bloc** labels, `nodeSelector`, `env`,
`args`, `volumeMounts` et `securityContext`, et réécrit `resources` clé par
clé (`setContainerResources`) : sont perdus
`--metrics` (scraping, console), `--jinja` (appels d'outils), `-ngl`,
`LLAMA_CACHE` ; le label `engine` n'a aucun consommateur (sans risque).
`resources` : le provider pose `requests.cpu = limits.cpu` et une limite
mémoire = `Size` — pour un MoE aux experts en mmap sur CPU, risque de thrash ou
d'OOM. Exigence du lot 4 : **le provider préserve les `resources`
existantes** (alternative : étendre `Sizing`, qui ne sait exprimer ni
requests ≠ limits ni l'absence de limite mémoire). **Acceptation : parité du gabarit de pod**, diff installé /
Reconcile avec une **liste fermée d'écarts permis** (propriétaire, annotation
de digest, `securityContext`, `automountServiceAccountToken: false`, `emptyDir`
`/tmp` et son montage, `priorityClassName`, `nodeSelector`, `-m`, cache en
lecture seule ; **le conteneur proxy du §4.1**, reconnu comme conteneur du
provider et jamais compté dans `ForeignContainers`, avec le montage du Secret
de clé ; l'hôte et le port de llama.cpp, `127.0.0.1:8081`) ; tout autre écart
échoue.

**Chemin de retour** (avant toute migration) : manifestes du Deployment **et**
du Service, recalés sur le vivant, versionnés chez l'exploitant. Les trois
objets provisionnés (Deployment, Service, Secret de clé) portent
l'`ownerReference` du `FrameService`. **Deux protections obligatoires** :
`deletionPolicy: Retain` (sous `Delete`, `reconcileDelete` les supprime
explicitement, même avec `orphan`) **et** `kubectl delete frameservice <nom>
--cascade=orphan` (sinon le GC les supprime). Puis **`kubectl replace -f`**
du Deployment et du Service, qui efface les `ownerReferences` restantes — pas
`apply`, dont la fusion 3 voies garderait les ajouts de Frame (conteneur
proxy, uid 65532, lecture seule, `priorityClassName`, `nodeSelector`). Le
Secret de clé reste orphelin : à garder tant qu'un consommateur la présente
(sans le proxy, llama.cpp l'ignore), à supprimer ensuite. Les copies projetées (`projectTo`) disparaissent ;
si le delete bloque (finalizer retenu par un namespace non autorisé), ne pas
lancer `replace` avant qu'il soit terminé. Retour rejoué sur un cluster de test d'abord.

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
  `FrameService` et chaque `projectTo` : la clé vit toujours dans un Secret
  (§4.1) — écriture en production, avec l'accord de l'exploitant.

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

1. **Proxy d'authentification (tranché le 2026-09-28).** Ni clé dans
   llama.cpp (sous clé, `/metrics` et `/props` répondent 401 sur `master` :
   panneau console, PodMonitor et EPP cassés), ni `auth: none`. Frame fournit
   un **proxy minimal** — un binaire Go du dépôt (`net/http` +
   `httputil.ReverseProxy`, aucune dépendance nouvelle) — qui vérifie la clé
   devant llama.cpp :
   - **Ports.** Le proxy prend **8080, port nommé `http`** ; llama.cpp passe en
     `--host 127.0.0.1 --port 8081`, **sans `LLAMA_API_KEY`**. Rien ne change
     pour l'`InferencePool` (souvent rendu par un chart, `targetPorts: [8080]`),
     le `Service`, la PodMonitor (port `http`), la `readinessProbe` ni la
     console. Le proxy **relaie** `/health` à llama.cpp — sinon le pod serait
     `Ready` avant le chargement du modèle.
   - **127.0.0.1 borne le contournement.** `port-forward` et `exec`, qui
     atteignent le loopback, sont réservés à des identités qui lisent déjà le
     Secret : acceptable. `pods/proxy` (pod-proxy de la console) vise l'IP du
     pod, donc le proxy, jamais 8081.
   - **Lecture interne** : liste fermée `GET /health`, `/metrics`, `/props`,
     exemptée de clé, pour la PodMonitor, l'EPP et la console. Correspondance
     **exacte** sur le chemin nettoyé ; rejet de `..`, `//`, `%2e`, `%2f`
     (sinon `GET /props/../slots` passerait) ; `HEAD` non exempté ; requête
     exemptée avec corps refusée. `/slots`, le proxy CORS de llama.cpp et tout
     le reste exigent la clé. `/props` expose `model_path`, `chat_template`,
     `build_info`, `cors_proxy_enabled` : fuite faible, acceptée. La route de
     la gateway (`PathPrefix /`) rend aussi `/metrics` et `/props` lisibles
     sans clé par tout ce qui joint la gateway (ClusterIP, sans Ingress) :
     accepté.
   - **Clés.** Elles vivent dans le Secret du `FrameService` (`ensureAPIKey`),
     montées en fichier, relues à chaud. Comparaison en temps constant **sur
     les N clés sans sortie anticipée** ; `Authorization` retiré avant
     llama.cpp ; jamais la clé ni son empreinte dans les journaux, seulement
     l'id du consommateur ; rotation par chevauchement de 2 clés ;
     `ReadHeaderTimeout` posé. Chaque Secret projeté par consommateur garde la
     clé de donnée `apiKey`, contrat déjà lu par les consommateurs.
   - **NetworkPolicy** (aucune aujourd'hui dans un namespace d'inférence
     typique ; rattachée au **lot 4**) : port 8080 ouvert à la gateway, l'EPP,
     Prometheus, l'apiserver (pod-proxy de la console) et les sondes du
     kubelet. Un consommateur déclaré qui appelle le pod **en direct**
     contourne la gateway, l'EPP et les priorités : à éviter, autorisé
     seulement par une règle nommée.
   - **Consommateurs hors catalogue.** Tout client qui appelle la gateway sans
     lire le catalogue (URL et nom de modèle en configuration) prendra des 401
     au lot 4 : les énumérer avant la migration (l'annexe du site en tient
     la liste). **Ordre imposé**, parce que `ensureAPIKey` réutilise un Secret
     `<nom>-inference-key` déjà rempli au lieu d'en générer un :
     1. pré-créer le Secret de clé `<nom>-inference-key` ;
     2. le copier dans le namespace de chaque consommateur ;
     3. déployer les consommateurs avec `Authorization` sur tous leurs chemins
        (sans effet tant que le proxy n'existe pas : llama.cpp ignore l'en-tête) ;
     4. seulement ensuite, migrer côté Frame.
     Critère de fermeture : zéro 401 dans les journaux du proxy à la migration.
   - `Bind()` publie l'adresse de la gateway (§4.3), ou celle du proxy pour un
     service sans gateway.

   | Point | Options | Recommandation |
   |---|---|---|
   | Forme | sidecar dans le pod du serveur ; composant partagé devant la gateway | **sidecar** : l'auth s'arrête au pod, avec ou sans gateway ; pas de point unique de panne ; le provider possède déjà le gabarit. Le proxy prend le port que tout vise déjà (8080) |
   | `Authorization` via la gateway (§4.2) | la gateway transmet l'en-tête ; la gateway authentifie elle-même | **transmission**, à prouver sur un cluster de test ; si elle échoue, repli sur le composant partagé devant la gateway |
   | Streaming SSE, délai 900 s | — | `FlushInterval: -1` (vidage immédiat) ; pas de `WriteTimeout` serveur ; délai de requête ≥ celui de la route (900 s), c'est la gateway qui coupe ; déconnexion du client → annulation vers llama.cpp. Test : un flux de 20 min sans perte de jeton |
   | Clés | une clé unique ; une par consommateur | **une par consommateur** (`spec.binding.consumers[]`, un Secret projeté chacun) : révocation isolée, journal par consommateur. Le lot d'entrée n'en crée qu'une, mais le format du Secret porte N clés |
   | Lot | dans le lot 4 ; lot dédié avant | **lot 4a dédié** : binaire, image, tests (clé, chemins exemptés, SSE, reprise à chaud) livrés et audités seuls ; le lot 4 le câble. Pas de sidecar posé sur un serveur adopté (ce serait réécrire son gabarit en B) |

   Réserve : le comportement 401 est vérifié sur `master`, pas sur le build
   déployé ; il ne change plus la décision.
2. **À vérifier (sans l'exploitant, sur un cluster de test)** : la gateway
   transmet-elle `Authorization` jusqu'au pod ?
3. **Endpoint publié = la gateway.** `Bind()` publie l'adresse directe du
   `Service` : suivre le catalogue contournerait gateway, EPP et priorités.
   Le lot 4 publie la gateway (paramètre) avant toute migration.
4. **`runtimeClassName` est requis** là où le runtime GPU l'exige : paramètre
   du provider, ou dérivé du `FrameNode`.
5. **PriorityClass et classe de service.** Les `frame-*` n'existent que si un
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
| **4a. Proxy d'authentification** | Binaire Go, image, tests §4.1 ; rien de câblé | faible | lève le blocage clé / métriques |
| **4. Provider capable** | Reprise par nom et parité du gabarit (§2.1), `deletionPolicy: Retain`, `-m` sur chemin résolu, `extraArgs` bornés (`--metrics`, `--jinja`, `-ngl`), `env`, labels et `resources` conservés, endpoint publié = gateway, sidecar proxy du lot 4a, `cpuMoeLayers`, `threads`, `parallel`, `alias`, `runtimeClassName`, `suspend`, mode CPU, `Recreate`, `lastKnownGood`, pré-chargement ; prérequis §2.4 et §4 ; migration du serveur de chat en fenêtre, retour rejoué avant | moyen | une source de vérité |
| **5. Réglages depuis la console** | Contexte, déchargement, changement de modèle (admins), webhook par champ, purge du cache | moyen, borné par admission + retour auto | la demande, entière |
| **6. Vision** | Vision en `FrameService` CPU ; TEI reste adopté | faible | — |

**Exigence sur les lots 3 à 5 : ne rien fermer qui empêche le §7.** Un schéma
unique est choisi **avant le lot 3**, parmi `spec.models[]`,
`spec.models[].draft` et `draftModel`. **Recommandation : un `FrameService` =
un serveur = un modèle cible + un brouillon optionnel (`parameters.draftModel`)** ;
N modèles = N `FrameService`, dont l'admission somme les parts sur la carte
(une fois la carte partageable, voir §7).
Raisons : `parameters` est une table de chaînes, commune à tous les providers ;
chaque modèle garde son cycle de vie (redémarrage, bascule) et son alias ; l'EPP
choisit déjà entre services. L'estimateur additionne des parts par instance au
lieu de supposer une carte entière.

## 6. Décisions de l'exploitant (2026-09-28)

1. **Tranché : oui.** `FrameService` en cible, adoption transitoire (B).
2. **Tranché : proxy authentifié par clé fourni par Frame** (§4.1).
3. **Tranché : oui pour l'instant.** La coupure d'un chargement (plusieurs
   minutes) est acceptée à la demande ; à terme, ressources par modèle et
   plusieurs modèles (§7).
4. **Tranché : chantier à part.**
5. **Tranché : oui.** `SchedulingPolicy` et classe du nœud GPU au lot 4
   (§4.5), écriture en production avec l'accord de l'exploitant.

## 7. Au-delà (lots 7 et suivants)

- **Ressources par modèle.** Chaque instance reçoit sa part de VRAM et de RAM,
  calculée par l'estimateur (§2.3) et vérifiée par l'admission sur la somme des
  instances d'une carte ; `status.measured` calibre chaque part.
- **Prérequis : une carte partageable.** Le provider demande
  `nvidia.com/gpu: 1`, soit une carte exclusive : sommer des parts suppose le
  time-slicing, MPS ou une ressource fractionnée exposée par le plugin GPU.
- **Plusieurs instances ou modèles en parallèle** sur une ou plusieurs cartes,
  chacun derrière son alias, l'EPP choisissant entre elles. Le `Recreate`
  n'est plus imposé quand la VRAM libre permet de démarrer la nouvelle instance
  avant d'arrêter l'ancienne : la bascule devient sans coupure.
- **Speculative decoding.** Modèle brouillon chargé par le même serveur
  (`-md`, même GPU, `-ngld`, `--draft-max`/`--draft-min`). Estimation :
  poids cible + poids brouillon (part sur GPU selon `-ngld`) + KV cible (`-c`)
  + KV brouillon (`-cd`) × slots. `FrameModel` décrit le couple
  cible / brouillon compatible (même vocabulaire, vérifié dans les en-têtes
  GGUF) ; le `FrameService` choisit un brouillon parmi ceux déclarés.
