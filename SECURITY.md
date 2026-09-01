# Security policy

## Supported versions

The latest tagged release receives security fixes. Preview builds and older
minor releases may be used to reproduce a report but are not supported as a
long-term security branch.

## Reporting a vulnerability

Use the repository's **Security → Report a vulnerability** flow to open a
private GitHub Security Advisory. Do not open a public issue containing an
exploit, credential, private endpoint, database, backup, or Agent token. If
private reporting is temporarily unavailable, open a public issue asking the
maintainers for a private contact channel without disclosing the vulnerability.

Include the affected version, deployment mode, impact, and the smallest safe
reproduction. Maintainers will acknowledge a complete report as soon as
practical and coordinate disclosure after a fix is available.

Hostpin deliberately excludes remote command execution. A report showing any
path from a monitoring or theme endpoint to arbitrary server-side or agent-side
execution is considered critical.

Third-party themes execute browser code. Install only reviewed packages, keep
the administrator session separate from public theme browsing, and use the
strictest CSP compatible with the selected theme.

`.hostpin-backup` files contain the complete database, master key, and themes.
They are encrypted with a user-supplied passphrase, but should still be stored
as sensitive disaster-recovery material and never committed to source control.

The Ed25519 Agent update-signing private key is release infrastructure, not a
runtime secret. Keep an offline backup, store the CI copy only as an encrypted
GitHub Actions secret, and never commit it or include it in a Hostpin backup.
