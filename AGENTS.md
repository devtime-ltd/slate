# Slate - AI Agent Context

For the user-facing surface (commands, flags, `slate.yml` shape, scaffolds, global config) read **`README.md`** first. This file covers internals, design decisions, and roadmap that aren't appropriate in the README.

## Overview

Slate is a CLI tool for creating isolated, ephemeral dev workspaces using Docker containers and git worktrees.

**Primary motivation:** blast-radius reduction against supply-chain attacks. All package installs (composer, npm, pip, bundle) and code execution run inside containers with no access to the host's `~/.ssh`, cloud credentials, browser password stores, etc.

**Stack:** Go (cobra for CLI, `embed` for scaffolds, lipgloss for table output), Docker Compose for orchestration, Caddy (containerised) for HTTPS proxy.

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
├── scaffolds/        go:embed'd scaffolds (laravel, nextjs)
└── main.go
```

**Per-project bootstrap:** just `slate.yml` (created by `slate init <scaffold>`). Docker infrastructure (compose.yaml, Dockerfile, .dockerignore) is generated at runtime into `.slate/` (gitignored) from the embedded scaffold, or from the project's committed compose file when `scaffold:` is an inline map (see README).

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

## Agent (`agent:`) + new/up hooks (`new:`/`up:`)

`agent:` is the command `slate agent [name]` runs in the worktree: a single
command or a `[first-run, thereafter]` pair (first-run picked on the
workspace's first entry: no `.slate/agent-started` marker yet, plus either
SLATE_FRESH=1 or a bare workspace). Two hooks fire it (or anything else):
`new:` runs straight after `slate new`'s fast phase and its presence
auto-backgrounds provisioning (the point: enter an agent session while
containers come up); `up:` runs after provisioning finishes before dropping
to the shell, and is never run on the bg path. Both sit behind the
auto_cd/--cd TTY gate, and all three commands run through `sh -c` with
{{WORKSPACE}}/{{PROJECT}}/{{HOSTNAME}} expanded and
SLATE_WORKSPACE/SLATE_PROJECT/SLATE_FRESH/SLATE_PROVISIONING set
(PROVISIONING=1 when the lockfile shows a live bg provision at launch).
They execute on the HOST by design (the user's own login/skills/MCPs/git
apply), so all three fields are pinned to the MAIN checkout's slate.yml: the
worktree copy is container-writable and must never drive host execution.
Workspace-side edits to them are inert and surface a note
(`config.HostExecPinned`).

`slate agent` distrusts a command that returns too fast to have hosted a
session, because such a command can still exit 0 and so reads as a clean quit:
`runHostCommandDetail` times the run and `hostRun.bailed()` compares it against
`agentMinRuntime()` (3s; SLATE_AGENT_MIN_RUNTIME overrides, 0 disables). A
bailed launch skips the agent-started marker, so the failure isn't baked into
the next entry's variant choice; a failed first-run launch additionally writes
`firstRunPendingMarker` (`.slate/agent-first-run-pending`), which `agentFresh`
honours ahead of SLATE_FRESH/bareness, because those signals don't survive to
the next invocation and the owed first-run entry would otherwise fall through
to the thereafter variant. A bail of the thereafter variant retries the
first-run one once (the stale `claude --continue` shape: it presumed a session
the workspace hasn't got, and exits 0 or 1 depending on the claude build);
signal deaths and 126/127 don't retry, the first being the launch stopped from
outside, the second a config problem whose surfaced error a retry would mask. Every run's outcome lands in the
workspace's `.slate/agent-last-run` (`recordAgentRun`), because an exit outside
the bail window returns cleanly and takes any enclosing tmux session with it,
leaving no other evidence. All host-side `.slate` marker writes and removals go
through a pinned directory fd (`openSlateDir`: O_NOFOLLOW, then
openat/unlinkat that never re-walk the path), because the worktree is
container-writable and a planted or concurrently-swapped link could otherwise
redirect the operation to files outside the workspace. `holdWorkspaceOpen` then leaves a
shell in the workspace rather than returning, because the documented tmux
recipe makes `slate agent` the session's only command and a return would take
the session and the diagnostic with it; `--no-hold` and non-TTY invocations
return the error instead. The same hold covers an unusable `agent:`
(`agentUnconfiguredError`, which names the main checkout's slate.yml and calls
out a workspace-only `agent:`), and `hookNeedsAgentNote` warns when a hook
shells out to `slate agent` with none configured.

Sessions entered mid-provision stay safe via `cmd/wait.go`: `slate wait`
blocks on the provisioning lockfile and reports the outcome (log tail on
failure), and exec/shell/tool commands call `awaitProvision` before touching
containers. A stale `.failed` marker deliberately doesn't block those
commands (debugging a half-provisioned workspace is legitimate); only a live
or died-in-flight provision does.

Related trust rule: `scaffold:`, `files:`, `database:`, and `env:`
(host-reaching config: they can mount host files, define containers, or
interpolate into compose files, env via the `--env-file` role of
.env.container) resolve from the workspace branch's
committed slate.yml via the main .git (`workspace.CommittedFile`; containers
can't commit because the .git mount is read-only), falling back to the main
checkout. The worktree's working copy is never trusted for them
(`config.trustedConfig` / `config.TrustPinned`).

## Scaffold interface checklist

When adding a new scaffold, implement every method on `internal/scaffold.Scaffold` plus the generation half (`FS`/`FileMap`/`RenderDockerfile`, the unexported `embeddedScaffold` interface). The non-obvious one:

- **`AppLikeServices() []string`** lists compose services that share the `/app` bind (the workspace dir bind-mounted into the container). First entry is the primary; `GenerateFileMounts` puts `/app/*` file mount targets on it alone and shared (non-`/app`) mounts on every listed service. Laravel returns `["app", "queue"]`, nextjs returns `["app"]`. nil is reserved for inline scaffolds and means "derive from the rendered compose file" (`compose.AppLikeServices`); built-ins must return a non-empty list.

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

**DNS resolution:** done — `slate setup` runs a dnsmasq container (`slate-dns`) on
`127.0.0.1:53` answering `*.test` → `127.0.0.1`, and on macOS writes `/etc/resolver/test`
(see `cmd/dns.go`). Skipped when `*.test` already resolves. Linux still needs the user to
point their system resolver at `127.0.0.1`. A future embedded resolver could fold this into
the binary.

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
- [x] `slate exec` - generic exec into any service (`-s <service>`, `-i` for TTY)
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
| Worktree slate.yml authoritative, `project:` pinned to main | Branches can test config changes before merging without forking a workspace's identity mid-life |
| Agent/new/up run host-side (no in-container agent) | The user's own tooling/credentials apply; slate stays vendor-neutral; containers keep isolating app code and deps |
| Hash-suffix on DB names | Safe across all databases (max 63 chars), unique per project+workspace+label |
| `--bg` fast/slow split | Fast phase (worktree+scaffold) runs inline so editing starts immediately; slow phase (build+lifecycle) detaches with `Setsid` and survives parent close |
| Lockfile-driven status (not in-memory) | Survives slate restarts, visible across shells, single source of truth for concurrency guards |

## Coding Conventions

- Go standard project layout. `internal/` for non-exported packages.
- cobra for CLI commands. Each command in its own file under `cmd/`.
- Errors returned, not panicked. User-facing errors should be clear and actionable.
- Scaffolds are embedded via `go:embed`. New scaffolds add a directory under `scaffolds/` and an embed var in `scaffolds/embed.go`. Inline scaffolds (`scaffold:` as a map in slate.yml) resolve through `scaffold.Resolve`, never the registry.
- Unit tests for pure functions (config, scaffold, workspace). CI runs `go test + go vet + go build`.
- Commit messages follow Conventional Commits (feat/fix/refactor/docs/chore).
- Do not duplicate the README. If user-facing content needs updating, edit `README.md` and link from here.
