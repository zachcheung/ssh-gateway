# Release Notes

## v0.3.4

- Fix: per-user errors (add, remove, write keys) now warn and continue
  instead of aborting the reconcile — one bad user no longer blocks others
- Fix: `writeAuthorizedKeys` ensures `.ssh` directory exists before writing,
  recovering gracefully when a user's home was migrated without `.ssh/`

## v0.3.3

- Fix GID collision: user personal group GID now allocated via `nextGID()`
  instead of `gid := uid` — the two scans are independent and could return
  the same value, causing the user's primary group to appear as `ssh-gateway`
- Fix orphaned home directory: `os.RemoveAll` now runs before group/passwd
  removal in `removeUser` so a deletion failure keeps the user visible to the
  next reconcile for retry, rather than leaving an unmanaged home forever
- Fix double-reconcile on file save: 200ms debounce window collapses
  rapid fsnotify events (truncate + write) into a single reconcile
- Fix `authorized_keys` options prefix handling: `keyType()` now scans all
  tokens in a key line so `no-pty ssh-ed25519 ...` validates and filters
  correctly; previously the options prefix caused the key to be misidentified
- Fix `authorized_keys` written with stray newline when user has no valid keys;
  now writes a 0-byte file to explicitly revoke access
- Fix examples using single-file config bind mount which breaks fsnotify;
  mount the config directory instead (`- .:/etc/ssh-gateway:ro`)

## v0.3.2

- Fix: validate fetched SSH public keys — drop lines that don't start with a
  recognised key type prefix, preventing auth redirect HTML or error pages
  from being written to `authorized_keys`
- Add version logging at startup; version is set via build-time `-ldflags`

## v0.3.1

- Fix hardened sshd_config being bypassed when `/etc/ssh` is bind-mounted:
  embed config in binary, always write to `/etc/sshd_config` (outside all
  user volume mounts), start sshd with `-f /etc/sshd_config`

## v0.3.0

- Auto-reload config via fsnotify: file writes trigger reconcile automatically without SIGHUP, works with Docker bind mounts and named volumes
- Add `reconcile_interval` config field for periodic key re-fetch from `key_provider` or URL keys (opt-in, minimum 5s)
- Harden sshd: `AllowGroups ssh-gateway` restricts login to managed users only, `LoginGraceTime 30`, `ClientAliveInterval 15`/`ClientAliveCountMax 3` to clean up dead sessions
- Fix `AllowGroups` regression: ssh-gateway group GID was hardcoded to 999, colliding with Alpine's built-in `ping` group; now allocated dynamically
- Structured logging via `log/slog`; set `LOG_LEVEL=debug` to see per-reconcile detail
- Add integration tests for fsnotify auto-reload and periodic key rotation

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
