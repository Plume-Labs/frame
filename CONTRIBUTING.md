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
