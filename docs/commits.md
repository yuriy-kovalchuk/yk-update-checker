# Commit Messages

Format: `type: short description` (72 chars max on first line)

No scopes: `fix(auth): …` is rejected. The type is the only prefix, and the
subject says what changed.

| Type | Use when |
|------|----------|
| `feat` | adding a new feature |
| `fix` | fixing a bug |
| `refactor` | restructuring code without behaviour change |
| `test` | adding or updating tests |
| `docs` | documentation only |
| `build` | Makefile, Dockerfile, dependencies |
| `ci` | GitHub Actions, CI config |
| `chore` | maintenance (version bumps, generated files) |
| `perf` | performance improvement |
| `style` | formatting, whitespace, no logic change |

Breaking changes: append `!` — `feat!: ...` — and explain in the body.

## Enforcement

`make install-hooks` points git at `.githooks/`, which enforces the same format in
two places:

- `commit-msg` — checks the subject against the type list, the 72-character limit, and rejects scopes.
- `pre-push` — re-checks every subject in the range being pushed to `master`, validates `v*` tag names, then runs `go test -race`.

Both hooks exempt the subjects git generates for itself, which nobody can format:
`Merge …` and `Revert "…"`. If you want a revert with a normal subject, use
`git revert --no-commit` and commit it as `fix:`/`chore:` with the detail in the body.

Body is free-form. Bullet points work well for multi-part commits:

```
feat: add config drift detection

- periodic comparison of running vs desired machine config via Talos API
- opt-out per node via spec.driftDetection=false (default: true)
- offline nodes skipped silently, no status change
```
