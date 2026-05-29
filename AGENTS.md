# Slate - AI Agent Context

## Overview

Slate is a CLI tool for creating isolated, ephemeral dev workspaces using Docker containers and git worktrees. Each workspace gets its own containers, database, and HTTPS URL.

**Primary motivation:** blast-radius reduction against supply-chain attacks. All package installs (composer, npm, pip, bundle) and code execution run inside containers with no access to the host's `~/.ssh`, cloud credentials, browser password stores, etc.

**Stack:** Go (cobra for CLI, embed for templates, lipgloss for table output), Docker Compose for orchestration, Caddy (containerised) for HTTPS proxy.

**Repo:** `github.com/devtime-ltd/slate`

## Architecture

```
slate (Go binary)
├── cmd/              Cobra commands (new, up, down, rm, ls, init, doctor, etc)
├── internal/
│   ├── config/       Global (~/.config/slate/config.yml) + project (slate.yml) config + project registry
│   ├── workspace/    Git worktree management, name validation, path resolution
│   ├── compose/      Docker Compose orchestration wrapper
│   ├── proxy/        Caddy container proxy + route registration
│   └── scaffold/     Scaffold interface + per-scaffold implementations
├── templates/        go:embed templates per scaffold (laravel, nextjs)
└── main.go
```

**Per-project bootstrap:** just `slate.yml` (created by `slate init <scaffold>`). Docker infrastructure (compose.yaml, Dockerfile, .dockerignore) is generated at runtime into `.slate/` (gitignored) from embedded templates.

**Key conventions:**
- Hostnames: `{project}--{workspace}.test`, subdomains: `vite.{hostname}.test`, `mailpit.{hostname}.test`
- DB names: `{workspace}_{hash}` (sanitised, truncated to 18 chars, 6-char SHA256 hash). Labelled: `{workspace}_{label}_{hash}`.
- Branches: `slate/{workspace}`
- Compose project: `slate__{project}--{workspace}` (double underscore)
- Workspaces dir: `{project}/.slate/workspaces/{name}`
- Caches per workspace: `.slate/composer/` and `.slate/npm-cache/` (bind-mounted, no Docker volumes)
- State files (all under `~/.config/slate/` and `~/.local/share/slate/`):
  - `~/.config/slate/config.yml` — global config (ports, TLS, secret key)
  - `~/.config/slate/projects` — project registry (one `name=path` line per project)
  - `~/.local/share/slate/entrypoint.sh` — bind-mounted into containers
  - `~/.local/share/slate/slate-ca.crt` — extracted Caddy CA after `slate proxy trust`
- Per-workspace state derived from git worktree, Docker, and the Caddy API at runtime.

## Tool System

The Scaffold interface exposes a `Tools() map[string]config.Tool` method. `Tool` is an interface with two concrete implementations:

- **`ExecTool{Service, Command}`** — runs a command inside a container (composer, artisan, npm, etc.)
- **`DBTool{Service, Port, Scheme, User, PasswordSalt}`** — generates a DB connection URL (mysql, psql, etc.) with `--open` and `--url` flags

User-defined tools in `slate.yml` are always exec tools. Scaffolds can mix both.

## Placeholders

Expanded at workspace creation time inside `slate.yml`:

- `{{SCAFFOLD_DEFAULT}}` — scaffold's default script (lifecycle hooks only)
- `{{GEN_PASSWORD:salt}}` — derived password: `HMAC-SHA256(secret_key, "project:workspace:salt")` truncated to 24 base64url chars
- `{{DB_NAME:label}}` — safe DB name: `sanitised_workspace[_label]_6charhash`, max 44 chars

## Current State

Working commands:
- Workspace: `new`, `up` (auto-creates if missing), `down`, `rm` (-f), `restart`, `ls` (--all)
- Tools: `setup`, `teardown`, `doctor`, `init`, `cache`, `proxy`, `shell`, `logs`, `open`
- Scaffold-registered: `composer`, `artisan`, `pint`, `pest`, `npm`, `npx`, `tinker`, `mysql` (Laravel); `npm`, `npx`, `prisma`, `psql` (Next.js)
- Persistent flag: `--project <name>` targets any registered project from anywhere

**Known gaps / active work:**
- The `attach` command (TUI log viewer) is not yet implemented. Plan: bubbletea-based tabbed UI.
- End-to-end CI tests against Docker not yet wired up.

## Roadmap

### P1 - Embedded Caddy
Current state: external Caddy container (managed by slate). Auto-detected on `proxy start`.

**Goal:** embed Caddy as a Go library (`github.com/caddyserver/caddy/v2`) so the slate binary IS the proxy. No external container, no docker dep for the proxy itself.

- Reverse proxy routing via Caddy's in-process API
- Automatic local CA (like mkcert built-in)
- `slate setup` installs CA trust
- Falls back to external Caddy/Herd detection if user already has one running

**DNS resolution:** `.test` still needs routing to 127.0.0.1. Options:
- Manage `/etc/hosts` (cross-platform, needs sudo once)
- Embedded resolver (ambitious)
- Document manual dnsmasq setup

### P2 - Additional Scaffolds

Each new scaffold is a new file under `internal/scaffold/` implementing the Scaffold interface. No edits to other packages required.

Suggested order:
1. **`rails`** - Ruby + Puma, PostgreSQL, Redis, Sidekiq
2. **`django`** - Python + gunicorn, PostgreSQL, Redis, Celery
3. **`wordpress`** - PHP + Apache, MySQL
4. **`go-service`** - Go + air, PostgreSQL
5. **`static`** - Just a web server

### P2.5 - Configuration Depth

- [ ] **Configurable hostname format** (global + per-project): `{{.Project}}--{{.Workspace}}.test` is the default; allow custom TLDs like `.localhost`, `.dev`, real domains.
- [ ] **Custom TLS certs** for non-local TLDs.
- [ ] **Entrypoint hooks**: per-project script that runs on every container start, before the main process.

### P3 - Developer Experience
- [ ] `slate attach` - bubbletea TUI tabbed log viewer with shell windows
- [ ] `slate status` - rich overview of current workspace
- [ ] `slate exec <service> <command>` - generic exec into any service
- [ ] Shell completions (cobra generates these)
- [ ] Progress bars / spinners during long operations

### P4 - Distribution
- [ ] GitHub releases with cross-compiled binaries (goreleaser)
- [ ] Homebrew tap: `brew install devtime-ltd/tap/slate`
- [ ] `slate self-update` command
- [ ] AUR package for Arch Linux

### P5 - Advanced
- [ ] `slate clone <repo-url>` - clone + init + new in one command
- [ ] `slate snapshot` / `slate restore` - DB state
- [ ] Multi-project workspaces (frontend + backend as linked services)
- [ ] CI mode: `slate ci` runs tests in a fresh workspace
- [ ] Remote workspaces (spin up on a remote Docker host)

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Git worktrees (not clones) | Instant creation, shared .git/, lightweight |
| Workspaces under .slate/workspaces/ | Co-located with the project, no external dirs |
| .slate/ generated at runtime | Projects only commit slate.yml, Docker infra managed by slate |
| Caches in .slate/ (not Docker volumes) | Avoids root-ownership issues with named volumes on non-existent paths |
| Tool interface (ExecTool, DBTool) | Type-safe, no mixed fields, extensible for new tool kinds |
| Scaffold-defined tools | Each scaffold owns its commands; adding a scaffold doesn't touch other code |
| Project registry with stable names | Names assigned at registration time, survive removals |
| Caddy container (now) → embedded Caddy (P1) | Container removes Caddy install dep today; embedded removes container too |
| Per-installation secret key for passwords | Same workspace name gives different passwords across installations |
| Hash-suffix on DB names | Safe across all databases (max 63 chars), unique per project+workspace+label |

## Coding Conventions

- Go standard project layout. `internal/` for non-exported packages.
- cobra for CLI commands. Each command in its own file under `cmd/`.
- Errors returned, not panicked. User-facing errors should be clear and actionable.
- Templates use go:embed. New scaffolds add a directory under `templates/` and an embed var in `templates/embed.go`.
- Unit tests for pure functions (config, scaffold, workspace). CI runs `go test + go vet + go build`.
- Commit messages follow conventional commits (feat/fix/refactor/docs/chore).
