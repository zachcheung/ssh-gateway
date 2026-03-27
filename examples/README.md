# Examples

Compose examples for different deployment scenarios.

| [compose.single.yml](compose.single.yml)           | Single project                 |
| -------------------------------------------------- | ------------------------------ |
| [compose.single-bind.yml](compose.single-bind.yml) | Single project, bind mounts    |
| [compose.multi.yml](compose.multi.yml)             | Multiple projects              |
| [compose.multi-bind.yml](compose.multi-bind.yml)   | Multiple projects, bind mounts |

Host keys and `authorized_keys` are persisted automatically via Docker volumes. The bind variants mount host directories instead, making them visible on the host for backup or version control.

## Bind mount directory structure

Single project:

```
.
├── compose.yml
├── config.yaml
├── home/
│   ├── alice/.ssh/authorized_keys
│   └── bob/.ssh/authorized_keys
└── ssh/
    ├── ssh_host_ecdsa_key
    ├── ssh_host_ed25519_key
    └── ssh_host_rsa_key
```

Multiple projects:

```
.
├── compose.yml
├── alpha/
│   ├── config.yaml
│   ├── home/
│   │   ├── alice/.ssh/authorized_keys
│   │   └── bob/.ssh/authorized_keys
│   └── ssh/
│       └── ...
└── beta/
    ├── config.yaml
    ├── home/
    │   └── alice/.ssh/authorized_keys
    └── ssh/
        └── ...
```
