# Release Notes

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
