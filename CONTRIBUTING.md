# Contributing to Hostpin

Thank you for helping improve Hostpin. Bug reports, documentation fixes,
platform compatibility improvements, and focused feature proposals are
welcome.

## Before opening a change

- Use GitHub Security Advisories for vulnerabilities; do not publish secrets or
  exploit details in an issue.
- Keep Hostpin's monitoring-only boundary intact. Agent tasks must remain typed
  and structured; shell, PTY, arbitrary command, and arbitrary binary URL
  fields are not accepted.
- Discuss large protocol, storage, authentication, or compatibility changes in
  an issue before implementation.

## Development

Hostpin requires Go 1.26.x and the pnpm version pinned in `web/package.json`.

```sh
corepack enable
pnpm --dir web install --frozen-lockfile
make test
make lint
make security
make build
```

Changes to storage require matching SQLite and PostgreSQL migrations and tests.
Changes to public APIs require privacy tests. Changes to Agent tasks require
tests proving that no arbitrary execution field was introduced. Frontend
changes should cover both Chinese and English and remain usable at a 320 px
viewport.

See `docs/testing.md` for integration, browser, theme, resource, and capacity
verification.

## Pull requests

- Keep each pull request scoped to one coherent change.
- Add or update tests and documentation with behavior changes.
- Do not commit runtime databases, `.env` files, backup archives, signing keys,
  generated release binaries, or dependency directories.
- Use clear commit messages and describe migration or compatibility impact.

By contributing, you agree that your contribution is licensed under the MIT
License used by this repository.
