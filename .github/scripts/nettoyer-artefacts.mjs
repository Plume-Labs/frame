// Nettoie le stockage Actions du dépôt : artefacts périmés ou orphelins, caches de PR closes.
// Sans dépendance (Node 24, fetch). Jeton : GITHUB_TOKEN avec `actions: write` + `pull-requests: read`.
//
// Règles pour un artefact (la première qui s'applique supprime) :
//   1. plus vieux que AGE_MAX_JOURS (défaut 5) ;
//   2. issu d'une branche qui n'est ni la branche par défaut ni la tête d'une PR ouverte
//      (PR fusionnée ou fermée, branche supprimée), et vieux de plus de AGE_MIN_HEURES (défaut 2),
//      pour ne jamais toucher un job encore en cours.
// Caches : seuls ceux d'une PR close (refs/pull/N/merge) partent ; GitHub évince déjà le reste.
//
// DRY_RUN=true : liste sans rien supprimer.

const jeton = process.env.GITHUB_TOKEN;
const depot = process.env.GITHUB_REPOSITORY;
const ageMaxJours = Number(process.env.AGE_MAX_JOURS ?? 5);
const ageMinHeures = Number(process.env.AGE_MIN_HEURES ?? 2);
const aVide = process.env.DRY_RUN === "true";

if (!jeton || !depot) {
  console.error("GITHUB_TOKEN et GITHUB_REPOSITORY sont requis.");
  process.exit(1);
}

const base = `https://api.github.com/repos/${depot}`;
const entetes = {
  Authorization: `Bearer ${jeton}`,
  Accept: "application/vnd.github+json",
  "X-GitHub-Api-Version": "2022-11-28",
};

async function api(chemin, options = {}) {
  const reponse = await fetch(`${base}${chemin}`, { headers: entetes, ...options });
  if (!reponse.ok && !(options.method === "DELETE" && reponse.status === 404)) {
    throw new Error(`${options.method ?? "GET"} ${chemin} → ${reponse.status} ${await reponse.text()}`);
  }
  return reponse.status === 204 || options.method === "DELETE" ? null : reponse.json();
}

async function toutesLesPages(chemin, cle) {
  const resultats = [];
  for (let page = 1; ; page++) {
    const separateur = chemin.includes("?") ? "&" : "?";
    const donnees = await api(`${chemin}${separateur}per_page=100&page=${page}`);
    const lot = cle ? donnees[cle] : donnees;
    resultats.push(...lot);
    if (lot.length < 100) return resultats;
  }
}

const octets = (n) => `${(n / 1024 / 1024).toFixed(1)} Mo`;

const depotInfos = await api("");
const brancheDefaut = depotInfos.default_branch;
const prsOuvertes = await toutesLesPages("/pulls?state=open");
const branchesVivantes = new Set([brancheDefaut, ...prsOuvertes.map((pr) => pr.head.ref)]);
const numerosOuverts = new Set(prsOuvertes.map((pr) => pr.number));

const maintenant = Date.now();
let supprimes = 0;
let liberes = 0;

const artefacts = await toutesLesPages("/actions/artifacts", "artifacts");
for (const a of artefacts) {
  if (a.expired) continue;
  const ageHeures = (maintenant - Date.parse(a.created_at)) / 36e5;
  const branche = a.workflow_run?.head_branch;
  const trop_vieux = ageHeures > ageMaxJours * 24;
  const orphelin = branche && !branchesVivantes.has(branche) && ageHeures > ageMinHeures;
  if (!trop_vieux && !orphelin) continue;
  console.log(`${aVide ? "[à blanc] " : ""}artefact ${a.name} (${octets(a.size_in_bytes)}, ${branche ?? "?"}, ${trop_vieux ? "âge" : "branche close"})`);
  if (!aVide) await api(`/actions/artifacts/${a.id}`, { method: "DELETE" });
  supprimes++;
  liberes += a.size_in_bytes;
}
console.log(`Artefacts : ${supprimes} supprimés, ${octets(liberes)} libérés.`);

let cachesSupprimes = 0;
let cachesLiberes = 0;
const caches = await toutesLesPages("/actions/caches", "actions_caches");
for (const c of caches) {
  const m = /^refs\/pull\/(\d+)\/merge$/.exec(c.ref ?? "");
  if (!m || numerosOuverts.has(Number(m[1]))) continue;
  console.log(`${aVide ? "[à blanc] " : ""}cache ${c.key} (${octets(c.size_in_bytes)}, PR #${m[1]} close)`);
  if (!aVide) await api(`/actions/caches/${c.id}`, { method: "DELETE" });
  cachesSupprimes++;
  cachesLiberes += c.size_in_bytes;
}
console.log(`Caches : ${cachesSupprimes} supprimés, ${octets(cachesLiberes)} libérés.`);
