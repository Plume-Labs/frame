# Relais d'alertes : Alertmanager → Frame → tenants — conception

**Date :** 2026-09-15
**Statut :** approuvé, non implémenté
**Base :** `main` = `11336da`

## 1. Ce que ce lot livre

Les alertes du cluster n'arrivent nulle part d'utile. Alertmanager les envoie
à `alert-sink`, un conteneur d'écho posé comme démonstration, et la console
les lit **en direct** dans Alertmanager : une alerte résolue disparaît sans
trace. Côté Neura, `it.incidents` est vide alors que le webhook
`POST /api/it/alerts/webhook` existe et qu'une matinée d'incident
(2026-09-15, stockage du cluster de test) a déclenché une dizaine d'alertes.

Frame devient le point d'entrée des alertes du cluster :

- **il les reçoit** d'Alertmanager ;
- **il les garde** dans un registre, un objet par alerte, avec historique ;
- **il les relaie** à chaque tenant abonné — Neura aujourd'hui — selon un
  filtre propre à l'abonnement, sans rien perdre si le tenant est injoignable.

Un envoi direct Alertmanager → Neura a été écarté (commit `3daba1b`, défait en
`11336da`) : l'infra commune appartient à Frame, et c'est à Frame de décider
ce que chaque tenant voit.

**Aucun changement de code côté Neura.** Frame renvoie le format Alertmanager
v4 que le webhook Neura lit déjà ; le jeton et le drapeau
`IT_ALERT_WEBHOOK_ENABLED` sont posés et déployés (Neura `67aa54fbe`).

## 2. Approche retenue

Tout vit **dans le manager Frame** (`frame-system/frame-controller-manager`) :

| Composant | Rôle | Réplicas |
|---|---|---|
| Récepteur HTTP (`Runnable`) | reçoit, valide, écrit `FrameAlert` | toutes |
| Contrôleur `FrameAlert` | relaie aux abonnements, purge | leader seul |

La réception est **découplée** de l'envoi : un tenant en panne ne fait jamais
échouer la requête d'Alertmanager, et l'attente d'envoi survit à un
redémarrage parce qu'elle est écrite dans l'objet.

Écartés : un binaire séparé `frame-alertrelay` (une image, un Deployment et un
circuit de build de plus pour un composant qui écrit de toute façon des
objets Frame) ; un relais synchrone dans la réception (couple l'enregistrement
à la disponibilité du tenant, statut d'envoi pauvre).

## 3. Types

Groupe `frame.plume-labs.io`, **`v1beta1` directement** : types neufs, pas
d'historique v1alpha1, donc pas de conversion. Les deux vivent dans
`frame-system` : ce sont des données d'exploitation de Frame, pas du namespace
qui déclenche l'alerte.

### 3.1 `FrameAlert`

Nom : `fa-<fingerprint>` — l'empreinte d'Alertmanager est un hachage stable des
libellés, le même objet porte donc l'alerte de l'ouverture à la résolution
sans index à maintenir.

`spec` — écrit **par le récepteur seul** :

| Champ | Type | Note |
|---|---|---|
| `fingerprint` | string | requis, non vide |
| `alertName` | string | label `alertname` |
| `severity` | string | label `severity`, vide si absent |
| `namespace` | string | label `namespace`, facultatif |
| `labels`, `annotations` | map | bornés (§4.2) |
| `startsAt` | time | |
| `endsAt` | time | vide tant que l'alerte est active |
| `generatorURL` | string | |

`status` — écrit **par le contrôleur seul** :

| Champ | Note |
|---|---|
| `state` | `Firing` \| `Resolved` |
| `lastReceivedAt` | dernière réception, mise à jour au plus toutes les 15 min |
| `deliveries[]` | une entrée par abonnement concerné : `subscription`, `deliveredState` (dernier état envoyé avec succès), `attempts`, `lastError`, `lastAttemptAt`, `permanentFailure` (bool) |

Colonnes d'affichage : `Alert`, `Severity`, `State`, `Age`.

**Écritures sur kine rares.** Alertmanager renvoie un groupe toutes les 5 min
et une alerte inchangée toutes les 4 h ; ces renvois n'écrivent rien, sauf
`lastReceivedAt` si la précédente valeur a plus de 15 min. Seules les
transitions (ouverture, résolution, réouverture) et les envois écrivent. Le
cluster de test l'exige : son datastore (kine/SQLite) sur un SLOG lent a
décroché le 2026-09-15 sous une charge disque bien plus faible qu'un flux
d'alertes non borné.

### 3.2 `FrameAlertSubscription`

`spec` :

| Champ | Type | Note |
|---|---|---|
| `url` | string | `http` ou `https`, requis |
| `tokenSecretRef` | `{name, key}` | Secret dans `frame-system` |
| `filter.excludeAlertNames` | []string | défaut `[Watchdog, InfoInhibitor]` |
| `filter.severities` | []string | vide = toutes |
| `filter.namespaces` | []string | vide = tout le cluster |
| `paused` | bool | défaut `false` |

`status` : `lastSuccessAt`, `lastError`, `pendingDeliveries`,
`observedGeneration`. Colonnes : `URL`, `Pending`, `LastSuccess`.

Le filtre `namespaces` prépare le multi-tenant ; l'abonnement Neura couvre tout
le cluster.

## 4. Réception

### 4.1 Exposition

`Runnable` du manager, port `8445`, `NeedLeaderElection() = false`. Service
`frame-alert-receiver` (ClusterIP) dans `frame-system`. Une seule route :
`POST /alertmanager` ; tout le reste répond `404`.

### 4.2 Contrôles, dans l'ordre

1. **Jeton porteur** comparé en temps constant au Secret
   `frame-system/frame-alert-receiver-token` (clé `token`), **relu à chaque
   requête** : une rotation ne demande pas de redémarrage. Absent, faux, ou
   Secret introuvable → `401`.
2. **Corps ≤ 1 Mio** → sinon `413`.
3. **Format** : `version == "4"`, au plus 100 alertes, chaque empreinte non
   vide, `status` ∈ {`firing`, `resolved`} → sinon `400`.
4. **Bornes** avant écriture : 64 entrées au plus par map, clé ≤ 256 octets,
   valeur ≤ 4096 octets, au-delà tronqué. Une alerte mal formée ne doit pas
   faire rejeter l'objet par l'apiserver.

### 4.3 Écriture et réponse

Pour chaque alerte : création de `fa-<fingerprint>` si absent, sinon mise à
jour du `spec` et du `status.state` **seulement s'ils changent** (règle des
15 min de §3.1 pour `lastReceivedAt`). Réouverture d'une alerte `Resolved` →
`Firing`, ses `deliveries` gardent leur `deliveredState` précédent, ce qui
déclenche l'envoi.

`200` **seulement quand tout le lot est écrit**. Une écriture en échec →
`503`, Alertmanager rejoue le lot entier ; l'idempotence par nom le rend sans
effet de bord. Le relais n'est **jamais** fait dans cette requête.

### 4.4 Réseau et Alertmanager

- NetworkPolicy : entrée sur `8445` depuis les seuls pods Alertmanager de
  `monitoring`, sur le modèle de `deploy/kubernetes/containment/`.
- HTTP en clair dans le cluster, comme le webhook Neura aujourd'hui. Le jeton
  protège contre les autres pods ; TLS est un chantier séparé.
- `kps-values.yaml` : les receivers `default` et `critical` pointent vers
  `http://frame-alert-receiver.frame-system.svc:8445/alertmanager`,
  `send_resolved: true`, `max_alerts: 100`, jeton en
  `http_config.authorization.credentials_file` monté depuis
  `alertmanagerSpec.secrets`. `alert-sink` sort de la configuration (son
  Deployment reste, sa suppression est hors périmètre).
- Watchdog et InfoInhibitor **sont** reçues et enregistrées ; le filtrage est
  fait par abonnement, au même endroit pour tous les tenants.
- Routage vérifié avant application par `amtool config routes test`. Rappel
  appris le 2026-09-15 : dès qu'une route enfant correspond, le receiver du
  parent n'est plus utilisé — toute route ajoutée avec `continue` impose une
  route finale explicite.

### 4.5 Métriques

`frame_alert_receiver_requests_total{code}`,
`frame_alerts_received_total{state}` sur le `/metrics` existant du manager.

## 5. Relais

### 5.1 Déclencheurs

Contrôleur sur `FrameAlert`, **leader seul** (pas de double envoi). Réveillé
par : un changement de `FrameAlert` ; un changement de
`FrameAlertSubscription` (mappé vers toutes les `FrameAlert` en `Firing`, et
vers celles qui ont une livraison en attente pour cet abonnement) ; une
échéance de retentative ou de purge (`RequeueAfter`).

### 5.2 Algorithme par alerte

Pour chaque abonnement non `paused` dont le filtre accepte l'alerte, si
`deliveredState != state` et pas de `permanentFailure` :

- `POST url`, corps Alertmanager v4 à **une alerte**, `receiver` = nom de
  l'abonnement, `Authorization: Bearer <jeton relu dans le Secret>`, timeout
  10 s, **redirections refusées** (le jeton ne part pas ailleurs).
- `2xx` → `deliveredState = state`, `attempts = 0`, `lastError = ""`.
- `5xx`, erreur réseau, `408`, `429` → `attempts++`, `lastError`,
  retentative après `min(5 s × 2^(attempts-1), 10 min)`.
- autre `4xx` → `permanentFailure = true`, `lastError`, pas de retentative.
  Levée quand l'alerte change d'état ou que la `generation` de l'abonnement
  change (jeton ou URL corrigés).

### 5.3 Cas limites

| Situation | Comportement |
|---|---|
| Ouverte puis résolue avant tout envoi (`deliveredState` vide, `state = Resolved`) | envoi `firing` puis `resolved` — sinon le tenant ignore une résolution d'empreinte inconnue et l'incident n'existe nulle part |
| Réouverture après résolution | `Firing` à nouveau → envoi → le tenant ouvre un nouvel incident |
| Abonnement `paused` | rien n'est envoyé, l'attente s'accumule |
| Abonnement supprimé | ses `deliveries[]` sont retirées au prochain passage |
| Nouvel abonnement ou filtre élargi | les alertes **actives** qui entrent dans le filtre partent ; les résolues ne sont pas rejouées |
| Alerte sortie du filtre | son entrée `deliveries[]` est retirée, rien n'est envoyé |

### 5.4 Purge

Suppression d'une `FrameAlert` si `state = Resolved`, `endsAt` plus vieux que
`--alert-retention-days` (défaut 14, aligné sur Velero et l'archivage WAL), **et**
aucune livraison en attente. Une alerte non livrée n'est jamais purgée.

### 5.5 Statut d'abonnement et métriques

`lastSuccessAt`, `lastError`, `pendingDeliveries` recalculés au passage sur
l'abonnement, au plus une fois par minute.
`frame_alert_deliveries_total{subscription,result}`,
`frame_alert_pending_deliveries{subscription}`.

## 6. Console

- **Onglet Alerts** : lit les `FrameAlert`. Deux vues, **Actives** et
  **Historique** (résolues, fenêtre de rétention), état d'envoi par abonnement
  sur chaque ligne.
- **Carte « Abonnements »** : URL, `lastSuccessAt`, `pendingDeliveries`,
  `lastError` en rouge si le dernier envoi a échoué.
- **Inchangés** : « Needs attention » (Overview) et les silences lisent
  toujours Alertmanager en direct — temps réel et action sur Alertmanager, pas
  registre.

## 7. Droits

Sur les étages `frame:*` existants (`rbac_tiers_test.go`) :

| Ressource | viewers / operators | admins | SA du manager |
|---|---|---|---|
| `framealerts` | get, list, watch | get, list, watch | tout |
| `framealertsubscriptions` | get, list, watch | + create, update, delete | get, list, watch, status |
| Secrets `frame-system` (jetons) | — | — | get sur les noms utilisés |

Aucun humain n'écrit une `FrameAlert`. Qui reçoit les alertes du cluster est
une décision d'admin.

## 8. Tests

- **Schéma** : `framealert_v1beta1_schema_test.go`,
  `framealertsubscription_v1beta1_schema_test.go` sur le modèle existant.
- **Récepteur** (envtest) : `401` sans jeton / jeton faux / Secret absent ;
  `413` ; `400` (version, empreinte vide, plus de 100 alertes) ; troncature des
  maps ; renvoi identique sans écriture avant 15 min ; réouverture ; `503`
  quand l'écriture échoue.
- **Relais** (`httptest.Server`) : `2xx` ; `5xx` puis `2xx` ; `429` retenté ;
  `401` définitif puis levée au changement de `generation` ; redirection
  refusée ; `firing` puis `resolved` pour une alerte jamais livrée ; `paused` ;
  suppression d'abonnement ; filtre (`excludeAlertNames`, `severities`,
  `namespaces`) ; purge bloquée tant qu'une livraison est en attente.
- **RBAC** : les lignes du tableau §7 dans `rbac_tiers_test.go`.
- **Contrôle discriminant** : chaque garde a un test qui **échoue** sur une
  version volontairement cassée (jeton ignoré, `4xx` retenté, purge sans
  condition d'attente, redirection suivie), vérifié en cassant le code avant
  de conclure.

## 9. Déploiement

Même méthode que l'opérateur le 2026-09-15 : image
`192.168.2.201:30500/frame-controller:<sha>`, `kustomize build config/default`
puis `kubectl apply`, **sans** `frame-provisiond`.

1. CRD + RBAC, puis manager.
2. Jetons scellés : `frame-system/frame-alert-receiver-token`,
   `frame-system/neura-alert-webhook` (copie du jeton déjà dans
   `neura/neura-neura-secret`), `monitoring/frame-alert-receiver-token`.
   Retrait du SealedSecret `monitoring/neura-alert-webhook` posé le matin même.
3. `FrameAlertSubscription` `neura` →
   `http://neura-neura-api.neura.svc.cluster.local:3000/api/it/alerts/webhook`,
   rangée dans `deploy/samples/test-cluster/`.
4. `helm upgrade kps` (même version de chart, valeurs en service + section
   `alertmanager` de `kps-values.yaml`), après `amtool config routes test`.

## 10. Critère de fin — en base, pas en code

- les alertes actives du cluster existent en `FrameAlert` ;
- `FrameAlertSubscription/neura` affiche un `lastSuccessAt` et
  `pendingDeliveries = 0` ;
- `it.incidents` côté Neura contient les lignes correspondantes ;
- **la première alerte résolue ferme son incident côté Neura**, observé en
  réel.

Le jalon Neura « Alertes Frame reçues dans Neura » décrit ce chemin et ne se
ferme qu'avec ces quatre constats.

## 11. Hors périmètre

TLS du récepteur et du relais ; suppression du Deployment `alert-sink` ;
silences depuis le registre ; tenants autres que Neura ; lien jalon ↔ incident
côté Neura (jalon « Lier un jalon à un incident », traité après).
