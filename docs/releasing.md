# Maintainer release guide

[简体中文](zh-CN/releasing.md) · English

The tag is the version source. Pushing `v0.1.0` starts `.github/workflows/release.yml`,
which reruns source tests, builds every artifact, enforces the Linux Agent
resource gate, signs the update manifest, and creates the GitHub Release.

## One-time signing setup

Generate the Agent update-signing key pair once, before the first tag:

```sh
go run ./cmd/hostpin-manifest --generate-key-dir .release/update-signing
gh secret set HOSTPIN_UPDATE_PUBLIC_KEY < .release/update-signing/public.key
gh secret set HOSTPIN_UPDATE_PRIVATE_KEY < .release/update-signing/private.key
```

The key files are mode `0600` and `.release/` is ignored by Git. Keep an
offline copy of the private key. Do not rotate the pair casually: released
Agents trust the public key compiled into their binary. Manifest generation
fails if the two GitHub secrets are malformed or do not match.

The source module, default download URLs, and build metadata target
`github.com/chnzzh/hostpin`. The workflow also injects the actual
`${owner}/${repository}` release URL into server installers and Agent
auto-update logic, so test releases produced from a fork do not download
artifacts from the upstream repository.

## Release checklist

1. Update `CHANGELOG.md` and add `docs/releases/vX.Y.Z.md`.
2. Build the frontend so the checked-in embedded fallback is current.
3. Run `make release-check` with Go 1.26.x.
4. Run the browser/theme suites and manually dispatch the capacity workflow.
5. Verify there are no `.env`, database, backup, key, or release-binary files
   in `git status --ignored`.
6. Push `main` and wait for CI and capacity checks to pass.
7. Create and push the tag only after the two signing secrets exist:

```sh
git tag -s v0.1.0 -m "Hostpin v0.1.0"
git push origin v0.1.0
```

An annotated tag may be used if tag signing is unavailable. The workflow
rejects non-semantic tag names and refuses to publish on any test, build,
resource, checksum, or key-pair failure.

## Post-release verification

- Confirm 13 Agent binaries, two server binaries, 15 sidecars, `SHA256SUMS`,
  `install-server.sh`, license/notice files, the third-party license bundle, and
  `update-manifest.json` are attached.
- Run the hosted `install-server.sh` against a disposable systemd VM and verify
  both a fresh install and a rerun that preserves configuration.
- Verify `SHA256SUMS`, then run each available server binary with `version`.
- Start a fresh SQLite server, complete setup, enroll one monitor and one
  probe-only node through the served installer, and test uninstall dry-run.
- Enable signed Agent update only after the manifest and download URLs have
  been checked from an unprivileged test host.
