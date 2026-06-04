# Slate - AI Agent Context

For the user-facing surface (commands, flags, `slate.yml` shape, scaffolds, global config) read **`README.md`** first. This file covers internals, design decisions, and roadmap that aren't appropriate in the README.

## Overview

Slate is a CLI tool for creating isolated, ephemeral dev workspaces using Docker containers and git worktrees.

**Primary motivation:** blast-radius reduction against supply-chain attacks. All package installs (composer, npm, pip, bundle) and code execution run inside containers with no access to the host's `~/.ssh`, cloud credentials, browser password stores, etc.

**Stack:** Go (cobra for CLI, `embed` for templates, lipgloss for table output), Docker Compose for orchestration, Caddy (containerised) for HTTPS proxy.

**Repo:** `github.com/devtime-ltd/slate`

## Architecture

```
slate (Go binary)
├── cmd/              Cobra commands (one file per command)
├── internal/
│   ├── config/       Global config + project config + project registry
│   ├── workspace/    Git worktree management, name validation, path resolution
│   ├── compose/      Docker Compose orchestration wrapper
│   ├── proxy/        Caddy container proxy + route registration
│   └── scaffold/     Scaffold interface + per-scaffold implementations
├── templates/        go:embed templates per scaffold (laravel, nextjs)
└── main.go
```

**Per-project bootstrap:** just `slate.yml` (created by `slate init <scaffold>`). Docker infrastructure (compose.yaml, Dockerfile, .dockerignore) is generated at runtime into `.slate/` (gitignored) from embedded templates.

**Key internal conventions:**
- Compose project: `slate__{project}--{workspace}` (double underscore)
- Workspaces dir: `{project}/.slate/workspaces/{name}`
- Caches per workspace: `.slate/composer/` and `.slate/npm-cache/` (bind-mounted, not Docker volumes)
- DB name: `sanitised_workspace[_label]_6charhash` (max 44 chars, safe across all databases)
- Password derivation: `HMAC-SHA256(secret_key, "project:workspace:salt")` truncated to 24 base64url chars
- Per-workspace state derived from git, Docker, and the Caddy API at runtime (no extra state files).

**State files** (all under `~/.config/slate/` and `~/.local/share/slate/`):
- `~/.config/slate/config.yml`: global config (ports, TLS, secret key, editor, auto_cd)
- `~/.config/slate/projects`: project registry (one `name=path` line per project)
- `~/.local/share/slate/entrypoint.sh`: bind-mounted into containers
- `~/.local/share/slate/slate-ca.crt`: extracted Caddy CA after `slate proxy trust`

## Background provisioning (`--bg`)

`slate new|up --bg` forks a hidden `_provision` subcommand with `Setsid` so it survives the parent shell closing. The forked worker:
1. Writes `.slate/<workspace>/.slate/provisioning` with its PID.
2. Runs the slow phase (optional `compose down -v`, `compose up`, lifecycle script, queue restart, proxy register).
3. On success removes the lock; on error renames it to `.slate/provisioning.failed`.

`slate ls` consults the lockfile (PID liveness via `signal(0)`, Unix-only) to render yellow "provisioning" or red "failed". `slate up`/`restart` refuse while a live lock exists; `slate rm` SIGTERMs the lock pid as the escape hatch.

The foreground `runWorkspaceLifecycle` writes the same lock, so concurrent `slate ls` calls see in-flight foreground runs too.

## Tool System

The Scaffold interface exposes a `Tools() map[string]config.Tool` method. `Tool` is an interface with two concrete implementations:

- **`ExecTool{Service, Command}`**: runs a command inside a container (composer, artisan, npm, etc.)
- **`DBTool{Service, Port, Scheme, User, PasswordSalt}`**: generates a DB connection URL (mysql, psql, etc.) with `--open` and `--url` flags

User-defined tools in `slate.yml` are always exec tools. Scaffolds can mix both.

## Scaffold interface checklist

When adding a new scaffold, implement every method on `internal/scaffold.Scaffold`. The non-obvious one:

- **`AppLikeServices() []string`** lists compose services that share the `/app` bind (the workspace dir bind-mounted into the container). First entry is the primary; `GenerateFileMounts` puts `/app/*` file mount targets on it alone and shared (non-`/app`) mounts on every listed service. Laravel returns `["app", "queue"]`, nextjs returns `["app"]`. Returning an empty slice while file mounts are configured is treated as a scaffold bug and errors out.

## File mount handling

`GenerateFileMounts` writes a compose override (`.slate/compose.files.yaml`) declaring bind mounts for each `DefaultFiles` + user `files:` entry. Two rules worth knowing:

- **`/app/*` targets only attach to `AppLikeServices()[0]`.** `/app` is bind-mounted on every app-like service, so the same host file backs the mountpoint in every container; declaring the same `/app/*` mount on more than one races at container-create time.
- **Symlink defenses are layered.** Before any write, `.slate`, `compose.files.yaml`, and `.slate/files` are `Lstat`-checked and refused if any is a symlink. `.slate/files` is then `RemoveAll`'d before re-creation so any pre-existing `file_N` symlink can't redirect the subsequent `WriteFile`.

## Current State

Implemented commands match the README. Status notes beyond what the README covers:

- `--project <name>` works from anywhere on disk (including outside any git repo).
- No end-to-end CI tests against Docker yet.
- The `attach` command (TUI log viewer) is not yet implemented; see Roadmap.

## Roadmap

### P1 - Embedded Caddy

Current state: external Caddy container managed by slate, auto-detected on `proxy start`.

**Goal:** embed Caddy as a Go library (`github.com/caddyserver/caddy/v2`) so the slate binary IS the proxy. No external container, no docker dep for the proxy itself.

- Reverse proxy routing via Caddy's in-process API
- Automatic local CA (like mkcert built-in)
- `slate setup` installs CA trust
- Falls back to external Caddy detection if the user already has one running

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

- [ ] **Configurable hostname format** (global + per-project): default `{{.Project}}--{{.Workspace}}.test`; allow custom TLDs like `.localhost`, `.dev`, real domains.
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
| Git worktrees (not clones) | Instant creation, shared `.git/`, lightweight |
| Workspaces under `.slate/workspaces/` | Co-located with the project, no external dirs |
| `.slate/` generated at runtime | Projects only commit `slate.yml`, Docker infra managed by slate |
| Caches in `.slate/` (not Docker volumes) | Avoids root-ownership issues with named volumes on non-existent paths |
| Tool interface (ExecTool, DBTool) | Type-safe, no mixed fields, extensible for new tool kinds |
| Scaffold-defined tools | Each scaffold owns its commands; adding a scaffold doesn't touch other code |
| Project registry with stable names | Names assigned at registration time, survive removals |
| Caddy container (now) → embedded Caddy (P1) | Container removes Caddy install dep today; embedded removes container too |
| Per-installation secret key for passwords | Same workspace name gives different passwords across installations |
| Hash-suffix on DB names | Safe across all databases (max 63 chars), unique per project+workspace+label |
| `--bg` fast/slow split | Fast phase (worktree+scaffold) runs inline so editing starts immediately; slow phase (build+lifecycle) detaches with `Setsid` and survives parent close |
| Lockfile-driven status (not in-memory) | Survives slate restarts, visible across shells, single source of truth for concurrency guards |

## Coding Conventions

- Go standard project layout. `internal/` for non-exported packages.
- cobra for CLI commands. Each command in its own file under `cmd/`.
- Errors returned, not panicked. User-facing errors should be clear and actionable.
- Templates use `go:embed`. New scaffolds add a directory under `templates/` and an embed var in `templates/embed.go`.
- Unit tests for pure functions (config, scaffold, workspace). CI runs `go test + go vet + go build`.
- Commit messages follow Conventional Commits (feat/fix/refactor/docs/chore).
- Do not duplicate the README. If user-facing content needs updating, edit `README.md` and link from here.
