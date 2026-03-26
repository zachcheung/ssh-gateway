# SSH Gateway

A Go application that runs inside Docker containers alongside sshd to provide isolated SSH jump host access per project. Each project gets its own container. Users are managed via a bind-mounted YAML config file.

## How It Works

The Go binary runs as PID 1 inside each container. On startup (and on `SIGHUP`), it reads a per-project YAML config and reconciles system users and their `authorized_keys`. It then starts and supervises sshd.

Users connect via `ProxyJump` (`ssh -J`) to reach backend servers. No shell access is granted (`ForceCommand /bin/false`).

## Config

Each project has its own `config.yaml`:

```yaml
project: 'alpha'
users:
  - name: 'alice'
    keys:
      - 'ssh-ed25519 AAAA... alice@laptop'
  - name: 'bob'
    keys:
      - 'ssh-ed25519 AAAA... bob@desktop'
```

## Build

```sh
docker build -t ssh-gateway .
```

## Run

```sh
docker run -d \
  --name ssh-gw-alpha \
  -p 2201:22 \
  -v $(pwd)/data/alpha/config.yaml:/etc/ssh-gateway/config.yaml:ro \
  -v ssh-gw-alpha-keys:/etc/ssh \
  ssh-gateway
```

The `/etc/ssh` volume persists host keys across container restarts so clients don't get host key mismatch warnings.

## Reload

After editing the config file, send `SIGHUP` to reload users without restarting:

```sh
docker kill -s HUP ssh-gw-alpha
```

## Client Usage

Connect through the gateway as a jump host:

```sh
ssh -J alice@gateway:2201 alice@backend-server
```

Or in `~/.ssh/config`:

```
Host gw-alpha
  HostName gateway
  Port 2201
  User alice

Host backend-server
  ProxyJump gw-alpha
  User alice
```

## License

[MIT](LICENSE)
