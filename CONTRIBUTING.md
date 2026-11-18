# Contributing to Hearthrack

Thanks for helping. Hearthrack is licensed under [Apache-2.0](LICENSE); by
contributing you agree your work is released under that license.

## Sign your commits (DCO)

We use the [Developer Certificate of Origin](https://developercertificate.org/)
instead of a CLA. Every commit must carry a `Signed-off-by` line matching its
author:

```sh
git commit -s -m "fix(ui): explain what changed"
# forgot? amend the last one, or rebase with --signoff for several:
git commit --amend -s --no-edit
git rebase --signoff origin/main
```

The `DCO` check on pull requests fails if a commit lacks the line. Merge
commits and commits by bots (`[bot]`) are exempt.

## Before you open a pull request

- `make test` and `make lint` pass; UI changes pass `npm run build`.
- Never edit generated files (`config/crd/bases`, `zz_generated.*`): change the
  types and run `make manifests generate`.
- Security issues: do not open a public issue, see [SECURITY.md](SECURITY.md).

## Getting set up

[docs/development.md](docs/development.md) covers the two loops (the Go operator
and the React UI), and `make demo` gives you a throwaway Kind cluster with
seeded data to try a change against. `make test-upgrade` runs the N-1 → N →
N-1 upgrade check when your change touches CRDs, RBAC or the webhook.

## Opening the pull request

- Keep it to one change, with a conventional-commit title (`fix(ui): ...`,
  `feat(operator): ...`) and a description that says why, not only what.
- Add or update a test for behaviour you change; the CI runs unit, envtest,
  Kind integration, quickstart and upgrade checks.
- A change to a `v1beta1` API type is a change to a frozen API: read
  [docs/upgrading.md](docs/upgrading.md) first.
- Not sure it fits? Open an issue first. Questions and help: [SUPPORT.md](SUPPORT.md).
