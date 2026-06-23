# Release Notes

## v0.6.2

- Fix: host keys with unsafe permissions (e.g. 0644 from a manually
  populated volume) caused sshd to reject all keys and exit with a cryptic
  "no hostkeys available -- exiting" error; `GenerateHostKeys` now detects
  and corrects the mode to 0600 on startup, logging a warning

## v0.6.1

- Fix: container recreation triggered spurious `user added` notifications
  because `/etc/passwd`, `/etc/shadow`, `/etc/group` were reset to the image
  defaults; symlink these files into `/var/lib/ssh-gateway-users/` (a Docker
  named volume) so the user database persists across `docker compose up
  --force-recreate` — a clean recreation now produces zero changes

## v0.6.0

- Add `log_endpoint` config field: forward structured JSON logs to an external
  processor (`tcp://`, `udp://`, `http://`, `https://`); stdout always receives
  text logs, the endpoint receives JSON in parallel
- Each reconcile emits a `phase: start` and `phase: end` record sharing an
  `event_id` (random 8-byte hex), plus per-change records between them
  (`user added`, `user removed`, `key added`, `key removed`); the `changes`
  field on the end record allows processors to suppress no-op notifications
- `trigger` field (`startup` / `reload` / `periodic`) carried on every reconcile
  log record for filtering by event source
- Endpoint writer is initialised before the startup reconcile so first-deploy
  events are not silently dropped
- Fix: `readAuthorizedKeys` included the managed-by header comment in key
  comparison, causing every reload to report spurious key changes

## v0.5.0

- Support multiple config file locations searched in priority order:
  `config.yaml`, `config.yml`, `config/config.yaml`, `config/config.yml`; the `config/` subdirectory layout allows
  mounting only the config directory (`./config:/etc/ssh-gateway/config:ro`)
  instead of the full `/etc/ssh-gateway` tree
- fsnotify watcher resolves the config path once at startup and watches
  only that file; starts in discovery mode across all candidate directories
  if no config is found at startup, then locks onto the first file that
  appears
- Move `sshd_config` to `/etc/ssh/sshd_config` so it lives alongside the
  host keys in the same volume; on startup the file is written only if
  absent, or replaced with a warning if it has drifted from the built-in
- Add `keep_sshd_config` config field (default `false`): when `true`,
  an existing `/etc/ssh/sshd_config` is never touched on startup,
  letting operators tune sshd (connection limits, timeouts, banner, etc.)

## v0.4.0

- Add `fetch_keys_on_reload` config field (default `false`): config reloads
  and container restarts now preserve existing provider/URL keys instead of
  re-fetching, limiting key changes to the periodic timer or an explicit opt-in
- Annotate `authorized_keys` with `# ssh-gateway:source=…` markers so the
  reconciler tracks key origin (inline / url / provider) across runs; sshd
  ignores comment lines
- Sources removed from config are evicted on any reload; sources new to the
  config are fetched even without `fetch_keys_on_reload`
- Files with no source markers (pre-upgrade or manually written) fall back to
  a full fetch for backward compatibility
- Log per-key `key added` / `key removed` events with source and SHA256
  fingerprint matching sshd's `Accepted publickey` format for direct
  correlation; replaces the coarse "updated keys old=N new=M" message
- Add `# This file is managed by ssh-gateway. Do not edit manually.` header
  to every managed `authorized_keys` file

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
