## Summary

Describe the user-visible change and why it is needed.

## Verification

- [ ] Go tests pass (`go test ./...`)
- [ ] Go vet passes (`go vet ./...`)
- [ ] Frontend typecheck/tests/build pass when UI code changed
- [ ] SQLite and PostgreSQL migrations/tests were updated when storage changed
- [ ] Chinese and English text/docs were updated when behavior changed
- [ ] No secrets, runtime data, backups, signing keys, or release binaries are included

## Compatibility and safety

Describe protocol, migration, privacy, resource, and rollback impact. Confirm
that the change does not introduce shell, PTY, or arbitrary remote command
execution.
