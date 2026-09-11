# Captures Redfish d'un vrai iLO4

Relevées le **2026-09-10** sur un **HP ProLiant ML350 Gen9**, iLO 4 **v2.77**,
BIOS P92 v2.80. Corps de réponse bruts, `python3 -m json.tool` pour la mise en
forme, rien d'autre modifié.

Machine : E5-2609 v3 (1 socket), 80 Gio en 6 barrettes, 2 × 500 W, pas de GPU,
**aucun OS installé**.

## Les trois états de capteurs

| suffixe | `PowerState` | `PostState` |
|---|---|---|
| `_poweroff` | `Off` | `PowerOff` |
| `_inpost` | `On` | `InPost` (attrapé à t+8 s) |
| `_postcomplete` | `On` | `InPostDiscoveryComplete` |

**Il n'y a pas de `_running`** : la machine n'a pas d'OS, le POST s'arrête faute
de périphérique d'amorçage. Un état `_running` réel pourrait encore différer —
les capteurs seraient alimentés par un système qui tourne.

## ⚠️ Ce que ces trois fichiers démontrent

```
poweroff      capteurs Offline=0  en ligne=46 | CPU1=40 C (State=Enabled) | 0 W
inpost        capteurs Offline=0  en ligne=46 | CPU1=40 C (State=Enabled) | 0 W
postcomplete  capteurs Offline=0  en ligne=46 | CPU1=40 C (State=Enabled) | 122 W
```

**Un iLO4 qui a déjà démarré une fois retient ses dernières lectures et les
sert avec `Status.State: Enabled`, machine éteinte comprise.** Le CPU rend
40 °C alors que la machine est à l'arrêt depuis vingt minutes dans une pièce à
20 °C. C'est une valeur périmée présentée comme valide.

`Status.State: Offline` avec des zéros existe aussi, mais **seulement sur un
iLO froid** — jamais démarré depuis le branchement secteur. C'est l'état que
j'avais observé en premier, et il est trompeur : on en déduit à tort qu'un
garde sur `Status.State` suffit.

**Seul `PowerControl[0].PowerConsumedWatts` discrimine** : 0 éteint, 0 pendant
le POST, 122 machine allumée. Avec une réserve — à t+8 s de POST la
consommation est encore à 0 et met ~75 s à se peupler.

Conclusion pour un collecteur : ne rien déduire de `Status.State` ni des
valeurs. Prendre `PowerState` et `Oem.Hp.PostState` comme seule source de
vérité sur la validité d'un relevé.

## Autres écarts avec la spec

- **Slash terminal obligatoire** : `/redfish/v1/Systems/1` → `HTTP 308`.
- Service root en `ServiceRoot.1.0.0`, OEM nommé **`Hp`** (pas `Hpe`).
- Mémoire : collection `/redfish/v1/Systems/1/Memory/`, membres `proc1dimmN`.
  Champs de **l'ancien schéma** — `SizeMB`, `MaximumFrequencyMHz`, `DIMMType`,
  `Rank`, `DIMMStatus`. Pas de `CapacityMiB` ni `MemoryDeviceType`.
  `OperatingSpeedMhz` **n'est jamais rempli**, même POST terminé.
  Deux membres capturés : `proc1dimm1` (16 Go, 2R) et `proc1dimm2` (8 Go, 1R).
- Alimentations : bloc `Redundancy` incohérent — `Mode: Failover`,
  `MinNumNeeded: 2`, `Status.Health: None`. `PowerControl[0].PowerCapacityWatts`
  annonce 1000 W, qui est l'installé et non le budget sous redondance.
- `ResetType@Redfish.AllowableValues` = `On, ForceOff, ForceRestart, Nmi,
  PushPowerButton`. **Pas de `GracefulShutdown`.**
- `Oem.Hp.PowerAutoOn: Restore`.

## `reset_403_insufficient_privilege.json`

Corps exact d'un `POST .../ComputerSystem.Reset/` refusé, avec un compte
n'ayant que `LoginPriv`. **Capturé plus tôt le même jour**, avant que le compte
reçoive `VirtualPowerAndResetPriv` — il ne provient donc pas de la même passe
que les autres fichiers. Les privilèges se lisent dans `Oem.Hp.Privileges` sur
`/redfish/v1/AccountService/Accounts/<n>/`.

## Ce qui n'est pas là

Aucun contenu d'`AccountService`, aucun en-tête d'authentification, aucun jeton
de session — les fichiers ne contiennent que des corps de réponse.

`service_root.json` contient `Oem.Hp.LoginHint.HintPOSTData` avec
`"Password": "password"` : c'est un **gabarit littéral publié par l'iLO**, pas
un identifiant.

Numéros de série et adresses MAC sont conservés tels quels. Dis-le si tu
préfères les masquer.

## Pagination du journal IML — relevée le 2026-09-11

`$skip` et `$top` sont **ignorés en silence** : `HTTP 200`, toujours les 30
mêmes membres. Ne pas les utiliser, ils ne signalent pas leur inefficacité.

La pagination est **propriétaire HP** et vit dans `links` :

```json
"links": {
  "self":     { "href": ".../Entries/?page=1" },
  "NextPage": { "count": 30, "page": 2 }
}
```

`?page=N` fonctionne. `links.NextPage` est **absent sur la dernière page** —
c'est le seul terminateur fiable. `?page=7` sur 6 pages rend `HTTP 400`
`Base.0.10.QueryParameterOutOfRange` avec `MessageArgs: ["7","page","6"]`,
le troisième argument étant le numéro de page maximal.

175 entrées = 6 pages (30, 30, 30, 30, 30, 25). Ordre **croissant par Id**,
donc `?page=1` rend les **plus anciennes**. Les plus récentes sont sur la
dernière page, qu'il faut atteindre en suivant `NextPage` — ou en lisant
`Total` et en calculant, mais `count` peut varier selon les pages.

### `Members` ne contient que des liens

`Members[]` ne porte que `@odata.id`. **Les entrées complètes sont dans
`Items[]`** (`Id`, `Created`, `Severity`, `Message`, `Oem.Hp`). Un collecteur
qui itère `Members` fait 175 requêtes là où 6 suffisent.

### ⚠️ `Created` est absent sur certaines entrées

**15 des 175 entrées n'ont pas de champ `Created`** — toutes des
`POST Information` de type « DIMM could not be authenticated ». Elles portent
en revanche `Oem.Hp.Updated`. Un tri par `Created` sans garde plante ou classe
ces entrées n'importe où. `Oem.Hp.Updated` est présent partout dans cette
capture et constitue le repli.

Fichiers : `..._page2.json` (chaînage), `..._page6.json` (dernière page,
`NextPage` absent, entrées sans `Created`), `iml_entries_page_out_of_range_400.json`.
