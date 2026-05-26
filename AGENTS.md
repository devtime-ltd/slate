# Slate - AI Agent Context

## Overview

Slate is a CLI tool for creating isolated, ephemeral dev workspaces using Docker containers and git worktrees. Each workspace gets its own containers, database, and HTTPS URL.

**Primary motivation:** blast-radius reduction against supply-chain attacks. All package installs (composer, npm, pip, bundle) and code execution run inside containers with no access to the host's `~/.ssh`, cloud credentials, browser password stores, etc.

**Stack:** Go (cobra for CLI, embed for templates), Docker Compose for orchestration.

**Repo:** `github.com/devtime-ltd/slate`

## Architecture

```
slate (Go binary)
├── cmd/              Cobra commands (new, up, down, rm, ls, init, doctor, etc)
├── internal/
│   ├── config/       Global (~/.config/slate/config.yml) + project (slate.yml) config
│   ├── workspace/    Git worktree management, name validation, path resolution
│   ├── compose/      Docker Compose orchestration wrapper
│   ├── proxy/        HTTPS proxy auto-detection + route registration
│   └── scaffold/     Runtime generation of .slate/ from embedded templates
├── templates/        go:embed templates per scaffold (laravel, etc)
└── main.go
```

**Per-project bootstrap:** just `slate.yml` (created by `slate init <scaffold>`). Docker infrastructure (compose.yaml, Dockerfile, .dockerignore) is generated at runtime into `.slate/` (gitignored) from embedded templates.

**Key conventions:**
- Hostnames: `{project}--{workspace}.test`, Vite: `vite.{project}--{workspace}.test`
- DB names: `{project}__{workspace}` (underscored)
- Branches: `slate/{workspace}`
- Compose project: `slate-{project}--{workspace}`
- Workspaces dir: `{project}/.slate/workspaces/{name}`
- No state files. Everything derived from git, Docker, and the proxy API at runtime.

## Current State

Working commands: `init`, `new`, `up` (with `--build`), `down`, `rm`, `ls`, `doctor`, `shell`, `logs`, `tableplus`, plus dynamic tool commands from the scaffold template (e.g. `composer`, `artisan`, `pint`, `pest`, `npm`, `npx`, `tinker` for Laravel).

**Known gaps / active work:**
- The `attach` command (TUI log viewer) is not yet implemented. Plan: bubbletea-based tabbed UI, zero tmux dependency.
- End-to-end testing of the Go binary against real projects is in progress.

## Roadmap / Wishlist

### P0 - Core (in progress)
- [ ] Embed entrypoint.sh in the binary (currently a separate file at ~/.local/share/slate/)
- [ ] End-to-end test `slate new` with Go binary against sparta-bravo
- [ ] `attach` command via bubbletea TUI (tabbed log viewer with shell/mysql windows)

### P1 - HTTPS Proxy (Embedded Caddy)
Current state: auto-detects external Caddy or Herd on the host. Both are external dependencies.

**Goal:** zero external proxy dependencies. The slate binary handles everything.

**Approach: embed Caddy as a Go library.**
- Caddy is written in Go and importable as `github.com/caddyserver/caddy/v2`
- The slate binary IS the proxy. Runs a Caddy instance on configurable host ports.
- Handles TLS via automatic local CA (like mkcert but built-in).
- Reverse proxy routing managed via Caddy's in-process API.
- `slate proxy start` / `slate proxy stop` to manage the background process.
- `slate proxy trust` installs the local CA root into the system trust store.
- Increases binary size (~30MB) but eliminates ALL external deps except Docker.
- Could also handle DNS via embedded CoreDNS or a simpler `/etc/hosts` manager.
- Configurable listen port (default 443, override for non-root / port conflicts).

**Fallback:** keep the current external Caddy/Herd detection as a compatibility path for users who already have a proxy running. Embedded Caddy is the default for new installs.

**DNS resolution:** `.test` still needs routing to 127.0.0.1. Options:
- Manage `/etc/hosts` entries (cross-platform, needs sudo once)
- Embedded dnsmasq-style resolver (ambitious, eliminates sudo)
- Document manual dnsmasq setup as fallback

### P2 - Additional Scaffolds

Testing with non-Laravel scaffolds validates the template system is truly generic.

Suggested order (each exercises a different axis):

1. **`nextjs`** - Node-only (no PHP), validates non-PHP templates work
   - Services: app (node), postgres or sqlite, mailpit optional
   - Tools: npm, npx, prisma (if used)
   - Tests: vitest or jest

2. **`rails`** - Ruby runtime, PostgreSQL, validates alternative package managers
   - Services: app (ruby + puma), postgres, redis, sidekiq, vite (if using jsbundling)
   - Tools: rails, rake, bundle, rubocop, rspec

3. **`django`** - Python runtime, validates pip/poetry
   - Services: app (gunicorn), postgres, redis, celery
   - Tools: manage.py, pytest, pip, poetry

4. **`wordpress`** - PHP but very different from Laravel
   - Services: app (apache + PHP), mysql, mailpit
   - Tools: wp-cli, composer (if using bedrock)

5. **`go-service`** - Go + air (hot reload), validates non-interpreted stacks
   - Services: app (air or go run), postgres, redis
   - Tools: go, migrate

6. **`static`** - Minimal, just a web server
   - Services: app (caddy or nginx)
   - Tools: npm (if using a build step)

### P2.5 - Configuration Depth

- [ ] **Configurable hostname format** (global + per-project):
  ```yaml
  # ~/.config/slate/config.yml (global default)
  hostname_format: "{{.Project}}--{{.Workspace}}.test"

  # slate.yml (project override)
  hostname_format: "{{.Workspace}}.{{.Project}}.localhost"
  ```
  Vite hostname derived automatically as `vite.{hostname}`. Including the TLD in the format allows `.test`, `.localhost`, `.dev`, or even real domains.

- [ ] **Custom TLS certs** for non-local TLDs:
  ```yaml
  tls:
    cert: /path/to/cert.pem
    key: /path/to/key.pem
  ```
  When using embedded Caddy with `.test`, certs are auto-generated from the local CA. Custom certs only needed for real domains.

- [ ] **Entrypoint hooks** (runs on every container start, before the main process):
  ```yaml
  # slate.yml
  entrypoint_hook: |
    # e.g. inject runtime secrets, warm caches
    php artisan config:cache
  ```
  The embedded entrypoint has a hook point that sources this. Different from `setup` (which runs via `slate up` after containers are healthy) because this runs on every container start including restarts.

### P3 - Developer Experience
- [ ] `slate status` - rich overview of current workspace (services, ports, URLs, git branch)
- [ ] `slate open` - open the workspace URL in default browser
- [ ] `slate exec <service> <command>` - generic exec into any service
- [ ] `slate restart <service>` - restart a single service + re-register proxy
- [ ] Shell completions (cobra generates these)
- [ ] Progress bars / spinners during long operations (build, install)

### P4 - Distribution
- [ ] GitHub releases with cross-compiled binaries (goreleaser)
- [ ] Homebrew tap: `brew install devtime-ltd/tap/slate`
- [ ] `slate self-update` command
- [ ] AUR package for Arch Linux

### P5 - Advanced
- [ ] `slate clone <repo-url>` - clone a repo + init + new in one command
- [ ] `slate snapshot` / `slate restore` - save/restore DB state
- [ ] Multi-project workspaces (e.g. frontend + backend as linked services)
- [ ] CI mode: `slate ci` runs tests in a fresh workspace, no proxy needed
- [ ] Remote workspaces (spin up on a remote Docker host)

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Git worktrees (not clones) | Instant creation, shared .git/, lightweight |
| .slate/ generated at runtime | Projects only commit slate.yml, Docker infra managed by slate |
| Dynamic tool commands | Templates define available tools; `slate help` adapts to the project |
| No state files | Everything derived from git + Docker + proxy API |
| Caddy preferred over Herd | Cross-platform, open source, API-driven config |
| macOS UID 1000:1000 | OrbStack/Docker Desktop virtiofs handles translation |
| vendor/node_modules APFS clone | cp -cR is near-instant on macOS, avoids full reinstall |
| Entrypoint shipped with slate | Generic env-merging logic, not project-specific |
| Branch prefix `slate/` | Clear namespace, doesn't collide with feature branches |

## Coding Conventions

- Go standard project layout. `internal/` for non-exported packages.
- cobra for CLI commands. Each command in its own file under `cmd/`.
- Errors returned, not panicked. User-facing errors should be clear and actionable.
- Templates use go:embed. New scaffolds add a directory under `templates/` and an embed var in `templates/embed.go`.
- Commit messages follow conventional commits (feat/fix/refactor/docs/chore).
