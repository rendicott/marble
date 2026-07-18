# Marble — contributor / coding-agent notes

## Runtime ownership

Prefer running Marble as a **user systemd** service. Do **not** leave a long-lived harness process owned by a coding agent (port/`marble.lock` conflicts).

| | |
|--|--|
| Unit | `systemctl --user … marble-harness` |
| Binary | `./bin/marble-harness` (build output; gitignored) |
| Memory | `~/.marble` (default) |
| Workspace | set by the operator (often `$HOME` or a project root) |
| UI | `http://127.0.0.1:8080` |

### After code or static UI changes

```bash
go build -o bin/marble-harness ./cmd/marble-harness
systemctl --user restart marble-harness
```

Static files under `internal/web/static/` are **embedded at build time**.

### Do not

- Start a second harness against the same `--memory` directory
- Commit secrets, machine-specific Tailscale hostnames, or absolute personal home paths
- Check in `bin/`, `~/.marble`, or `~/.config/marble/env`

### Module path

```
github.com/rendicott/marble
```
