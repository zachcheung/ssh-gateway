# Release Notes

## v0.2.0

- Improve reconcile logging: added/removed/updated results with key counts
- Warn when a user ends up with no keys after filtering
- Deduplicate resolved keys
- Update GitHub Actions to latest versions (checkout v6, setup-qemu v4, setup-buildx v4, login v4, metadata v6, build-push v7)
- Add test for all-keys-filtered scenario

## v0.1.1

- Fix multi-arch image build with QEMU and Buildx setup
- Skip major-only image tag for v0.x releases

## v0.1.0

Initial release.

- Containerized SSH jump host with per-project user isolation
- YAML config for users and SSH public keys
- `key_provider` support: fetch keys from GitHub, GitLab, or custom URLs
- `key_types` filtering: allow or disallow key types (ecdsa, ed25519, rsa, etc.)
- Multi-project support via `SSH_GATEWAY_PROJECT` env var
- Live reload via `SIGHUP` without container restart
- Persisted host keys and home directories across restarts
- Integration test suite via Docker Compose
