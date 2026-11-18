# Getting help with Hearthrack

Hearthrack is in public beta, developed in the open, with no support contract
or response-time guarantee. Here is where to go.

| You have... | Go to |
| --- | --- |
| A bug or a surprising behaviour | [Open a bug report](https://github.com/Plume-Labs/frame/issues/new?template=bug_report.yml) |
| An idea or a missing feature | [Open a feature request](https://github.com/Plume-Labs/frame/issues/new?template=feature_request.yml) |
| A security vulnerability | **Not a public issue.** Follow [SECURITY.md](SECURITY.md) |
| A question about installing or running it | Read [getting-started](docs/getting-started.md), the [runbook](docs/runbook.md) and [upgrading](docs/upgrading.md) first, then open an issue if they do not answer it |

## Before you open an issue

- Check [known limitations](docs/known-limitations.md): some things are unproven
  on a real cluster (the console terminal, the storage-version migration) or
  out of scope on purpose.
- Say which version you run (`kubectl -n frame-system get deploy frame-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}'`),
  your Kubernetes version, and what you expected versus what happened.
- Redact anything secret from logs and manifests. Hearthrack was called Frame
  until 2026-11, so you will still see `frame` in names, namespaces and API
  groups: see [naming](docs/naming.md).

Want to fix it yourself? See [CONTRIBUTING.md](CONTRIBUTING.md).
