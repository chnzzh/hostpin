# One-click backup and restore

[简体中文](zh-CN/backup-restore.md) · English

**Admin → Backup & restore** exports, imports, and reloads a complete SQLite
site. It is intended for server moves, disaster recovery, and pre-upgrade
snapshots.

The encrypted `.hostpin-backup` container includes a consistent SQLite online
snapshot, the active master key, theme assets, and every durable database
record. It excludes machine-specific YAML/environment configuration, binaries,
reverse-proxy configuration, DNS, and TLS certificates.

Export requires the current administrator password plus a separate backup
passphrase of at least 12 characters. The payload has a SHA-256 file manifest
and is chunk-authenticated with AES-256-GCM using an Argon2id-derived key. The
passphrase is never stored and cannot be recovered.

Import requires the file, its passphrase, the current administrator password,
and the literal confirmation `RESTORE`. Before staging anything, Hostpin checks
the encrypted framing, ZIP paths and limits, every checksum, SQLite
`quick_check`, the schema version, required tables, and whether the bundled
master key decrypts protected database fields.

After validation, Hostpin drains and closes the active control plane, replaces
the data, and starts again inside the same process. Restored browser sessions
are deliberately deleted, while Agent identities and credentials remain. The
previous database, master key, and themes are retained under timestamped
`.pre-restore-*` names for manual rollback.

When moving servers, configure the new host's `public_url`, HTTPS, proxy, and
service first. Keeping the same DNS name lets Agents reconnect without changes;
a new panel origin requires updating the `endpoint` in each Agent's
`agent.json`. An externally configured `HOSTPIN_MASTER_KEY` must match the
backup key or import is rejected.

PostgreSQL deployments continue to use `pg_dump --format=custom` and
`pg_restore`, with `master.key` and `themes/` backed up separately. The UI does
not assume that the Hostpin process has database-creation privileges or a local
`pg_dump` binary.

