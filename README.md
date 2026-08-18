# Slate

Isolated dev workspaces powered by Docker. One command to start, HTTPS out of the box, containers you never have to think about.

Your code stays on the host, everything else runs in Docker containers. Each workspace gets its own database, services, and HTTPS URL, spun up from a git worktree in seconds.

## Installation

Slate is a single Go binary. Building it requires [Go 1.26+](https://go.dev/dl/); the scaffolds are embedded at compile time, so there are no other build-time dependencies.

To **run** slate you also need Docker ([OrbStack](https://orbstack.dev) on macOS, recommended, or Docker Engine on Linux) and Git. See [Requirements](#requirements) for details.

```sh
# Install straight from source into $GOBIN (usually ~/go/bin)
go install github.com/devtime-ltd/slate@latest
```

Or build from a checkout:

```sh
git clone https://github.com/devtime-ltd/slate.git
cd slate
go build -o slate .          # produces ./slate
# optionally move it onto your PATH:
sudo mv slate /usr/local/bin/
```

Make sure the install target is on your `PATH` (for `go install`, add `$(go env GOBIN)`, or `$(go env GOPATH)/bin` if `GOBIN` is unset). Verify with:

```sh
slate doctor             # checks Docker, Git, and proxy status
```

## Quick Start

```sh
# One-time setup
slate setup              # starts the HTTPS proxy + *.test DNS, installs CA cert

# In your project
slate init laravel       # creates slate.yml
slate new my-feature     # creates workspace with containers + HTTPS
```

Open `https://your-project--my-feature.test` and start developing.

## Commands

```
Workspace lifecycle:
  slate new <name>                Create a new workspace (containers + HTTPS)
  slate up [name]                 Start/refresh a workspace (offers to create if missing)
  slate down [name]               Stop (preserves data)
  slate restart [name] [service]  Restart workspace or single service
  slate rm [name]                 Destroy workspace (containers, volumes, worktree)
  slate ls [--all]                List workspaces (current project or all registered)
  slate wait [name]               Block until a background provision finishes

Tools:
  slate setup                     One-time host setup (proxy + DNS + CA cert + secret key)
  slate teardown                  Remove all slate infrastructure
  slate doctor                    Check dependencies
  slate brief                     Print an agent-facing cheatsheet for this project
  slate open [name]               Open workspace URL in browser
  slate path [name]               Print workspace path (pipeable, --open)
  slate pwd                       Print the project's main checkout (pipeable)
  slate cd [name]                 Spawn a sub-shell rooted at the workspace dir
  slate code [name]               Open workspace in your editor
  slate shell [name]              Bash shell in app container
  slate agent [name]              Run the agent command in a workspace (see Agent)
  slate exec [-s svc] -- <cmd>    Run an arbitrary command in a container (-i for a TTY)
  slate logs [name] [svc]         Tail logs (default: all services)
  slate proxy                     Manage the HTTPS proxy
  slate dns                       Manage the *.test DNS resolver

Scaffold tools (from slate.yml):
  Available commands depend on your scaffold. For Laravel:
  slate composer <args>     slate artisan <args>     slate tinker
  slate pint <args>         slate pest <args>
  slate npm <args>          slate npx <args>
  slate mysql [name]        Print DB connection info (--open, --url)
```

Omit the workspace name on any command that takes one: if you're inside a workspace it's used, otherwise slate pops an interactive picker over the project's workspaces.

To target a workspace explicitly (from outside any worktree, or in non-interactive contexts like scripts, CI, and agents), set `SLATE_WORKSPACE=<name>` (honoured by every command, including the scaffold tools) or pass `-w/--workspace <name>` to the lifecycle/utility commands. Examples: `SLATE_WORKSPACE=api slate artisan migrate`, `slate -w api logs`. The scaffold tools (`artisan`, `composer`, `npm`, …) pass **every** argument straight through to the tool, including the tool's own `-w` (e.g. npm workspaces), so target those with `SLATE_WORKSPACE`, not `-w`.

Add `--project <name>` to any command to target a project other than the current directory's. The project name comes from the registry (`slate ls --all`).

### Useful flags

- `slate new <name> -b <branch>`: custom branch name (default: `slate/<name>`).
- `slate new <name> --bg`: fork the slow phase (build + lifecycle) to the background; the fast phase (worktree + scaffold) runs inline so editing can start immediately. Progress is captured in `.slate/workspaces/<name>/.slate/provision.log` and surfaced as `provisioning` in `slate ls` (`failed` if it errors). While a bg provision is in flight, `slate up` and `slate restart` refuse to touch the workspace; `slate exec`, `slate shell`, and the scaffold tools wait for it instead of failing; `slate wait` blocks until it finishes (non-zero exit + log tail on failure); `slate rm` aborts it as an escape hatch. A configured `new:` hook backgrounds provisioning automatically, no flag needed (see [Agent](#agent--new--up-what-newup-drop-you-into)).
- `slate new <name> --cd` / `--cd=false`: opt in or out of dropping into a shell at the new workspace. Default comes from `auto_cd` in `~/.config/slate/config.yml` (default `true`), suppressed when stdio isn't an interactive terminal so scripts/CI/agents never block on a spawned shell. With `--bg` the shell is spawned immediately; without, after provisioning finishes.
- `slate new <name> --adopt`: carry your uncommitted changes from the main checkout into the new worktree (tracked changes patched in, untracked files copied). The main checkout is left untouched.
- `slate new <name> --bare`: worktree + scaffold only, no containers; for quick edits that don't need a running app. Shown as `bare` in `slate ls`; the first `slate up` provisions it with the fresh-workspace lifecycle. Hooks don't fire.
- `slate up [name] --fresh`: recreate containers + volumes (worktree code preserved) and run the new-workspace lifecycle.
- `slate up [name] --build`: force image rebuild.
- `slate rm [name]`: warns if the worktree has uncommitted changes (`3 modified, 1 untracked`) before asking for confirmation; `-f` skips the prompt but still warns to stderr. If your shell's cwd was inside the workspace being destroyed and you're in a slate-spawned shell (auto-cd, `slate cd`), slate exits it so you pop straight back to the shell you came from, history intact; otherwise it drops you into a sub-shell at the project's main checkout (type `exit` to return).

### One-off commands: `slate exec`

The scaffold tools cover the everyday commands; `slate exec` runs anything else inside a workspace container:

```sh
slate exec -- ./vendor/bin/phpstan analyse
slate exec -- php artisan migrate --force
slate exec -s vite -- npm run build      # target a different service (default: app)
slate exec -i -- php artisan tinker      # allocate a TTY for REPLs and prompts
```

- Runs **without a TTY** by default, so it's safe in scripts, CI, and agents. stdin is still forwarded, so you can pipe input in. Pass `-i/--interactive` when the command needs a terminal.
- Flag parsing stops at the first positional, so the target command's own flags pass straight through (`slate exec ./vendor/bin/phpstan analyse --memory-limit=1G`); the `--` is optional but makes intent clear. Slate's own flags (`-s`, `-i`, `-w`) go before the command.
- The workspace is selected like everywhere else: `-w/--workspace`, `SLATE_WORKSPACE`, or the current directory.

## How It Works

Each `slate new` creates a git worktree and spins up a set of Docker containers defined by your scaffold (e.g. PHP + Apache, MySQL, Vite, queue worker, Mailpit). A reverse proxy handles HTTPS termination so you get real `.test` URLs.

```
Host                          Containers (per workspace)
┌──────────────────────┐      ┌──────────────────────────┐
│ Your editor          │      │ app (PHP/Node/Ruby)      │
│ Git worktrees        │ ───► │ database (MySQL/Postgres) │
│ Slate CLI            │      │ vite/assets              │
│ HTTPS proxy (Caddy)  │      │ queue worker             │
└──────────────────────┘      │ mailpit                  │
                              └──────────────────────────┘
```

Source code is bind-mounted from the host. Package installs (`composer install`, `npm install`) run inside containers so compromised dependencies can't access your SSH keys, cloud credentials, or browser password stores. Dependency caches live inside each workspace at `.slate/composer/` and `.slate/npm-cache/`.

Install steps in the default lifecycle run through a `retry` helper (3 attempts, linear backoff) so transient registry blips don't fail the whole provision. For private packages, or to dodge GitHub's unauthenticated rate limits, mount a Composer `auth.json` with a token via the `files:` config (see [Customisation](#customisation)).

On first `slate new`, slate appends `.slate/workspaces/` to your project's `.gitignore` so workspace worktrees don't pollute the main checkout's status.

## Project Config

A single `slate.yml` in your project root:

```yaml
scaffold: laravel
```

That's it for most projects. The scaffold provides sensible defaults for the Docker image, services, lifecycle scripts, and available tool commands. When no built-in scaffold fits, a project can define one inline instead; see [Inline scaffolds](#inline-scaffolds).

Each workspace uses the `slate.yml` in its **own worktree** when present, so a branch can change config (packages, hooks, tools) and test it with `slate up` before merging; slate prints a note whenever a workspace's config differs from the main checkout's. Exceptions:

- `project:` is always taken from the main checkout so a branch can't change the workspace's identity (hostname, compose project, database names).
- `agent:`, `new:`, and `up:` run on the host and only ever come from the main checkout (see [Agent](#agent--new--up-what-newup-drop-you-into)).
- `scaffold:`, `files:`, `database:`, and `env:` (the latter two interpolate into compose files) can reach host resources, so they come from **committed content on the workspace branch** (containers can't commit; the `.git` mount is read-only) or, when the branch doesn't commit a `slate.yml`, from the main checkout. Uncommitted worktree edits to them are inert and get a note; commit them on the branch to test. This keeps a rewritten worktree config (e.g. by a compromised dependency) from mounting host files into containers on your next `slate up`.

Heavier changes like swapping `scaffold:` usually want `slate up --fresh`.

### Customisation

```yaml
scaffold: laravel

# Optional: override the auto-derived project name (from directory basename)
project: my-project

# Extra packages for the Docker image
apt_packages: [ghostscript, imagemagick, libmagickwand-dev]
php_extensions: [imagick]

# PHP ini overrides (laravel). Defaults: memory_limit=512M, upload/post 100M.
php_ini:
  memory_limit: 1024M

# Override lifecycle hooks (optional)
setup: |
  composer config http-basic.nova.laravel.com "$NOVA_USER" "$NOVA_KEY"
  {{SCAFFOLD_DEFAULT}}

# Extra env vars for workspaces (supports placeholders)
env:
  CUSTOM_VAR: value
  ANALYTICS_DB: "{{DB_NAME:analytics}}"
  REDIS_PASSWORD: "{{GEN_PASSWORD:redis}}"

# Mount host files into containers (e.g. for auth)
files:
  ~/.npmrc: /home/node/.npmrc
```

Lifecycle hooks (run inside the containers):
- **`setup`**: runs on every `slate new` and `slate up` (default: install deps + migrate)
- **`fresh`**: runs after `setup` on `slate new` and `slate up --fresh` (default: fresh DB seed)
- Use `{{SCAFFOLD_DEFAULT}}` to inject the scaffold's defaults at any point in your override
- A `retry <cmd>` shell helper is available inside hooks (3 attempts, linear backoff); wrap any flaky network step, e.g. `retry composer install`

Placeholders (expanded at workspace creation):
- `{{SCAFFOLD_DEFAULT}}`: scaffold's default script (lifecycle hooks only)
- `{{GEN_PASSWORD:salt}}`: derived per-workspace password from your installation's secret key
- `{{DB_NAME:label}}`: safe database name (`workspace_label_hash`, max 44 chars)

### Custom tools

Scaffolds register tool commands automatically (e.g. Laravel provides `composer`, `artisan`, `pint`, `pest`, `mysql`). Override or add your own:

```yaml
tools:
  mycommand:
    service: app
    command: [php, my-script.php]
```

User-defined tools in `slate.yml` are always exec tools (run a command in a container).

## Agent + new + up: what `new`/`up` drop you into

At an interactive terminal, slate drops you into the workspace (the `auto_cd` behaviour) through two hooks: `new:` fires straight after `slate new`'s fast phase, with provisioning forked to the background behind it; `up:` fires once provisioning finishes. Then a shell. `agent` defines the command `slate agent [name]` runs; point the hooks at it to land in your agent:

```yaml
agent:
  - claude --name "{{PROJECT}}--{{WORKSPACE}}"   # first run (SLATE_FRESH=1)
  - claude --continue                            # thereafter
new: slate agent   # slate new: runs immediately, containers provision behind it
up: slate agent    # slate up: runs after provisioning finishes
```

`agent` is either a single command or a `[first-run, thereafter]` pair; the first-run variant is picked when `SLATE_FRESH=1`, which slate sets for hooks fired from a fresh `slate new`. With the pair above, every new workspace starts a claude session named `<project>--<workspace>` (resumable later with `claude --resume <name>`), and re-entry continues where you left off.

With `new:` configured, `slate new foo` needs no flags and no waiting: the worktree and scaffold are created inline (seconds), provisioning forks to the background, and the hook runs immediately, so you brief your agent while the containers come up. The hook's session gets `SLATE_PROVISIONING=1` (0 otherwise) so tooling can tell it started mid-provision, and `slate exec` plus the scaffold tools block on the in-flight provision automatically, so the agent's first container command simply waits instead of failing. `slate wait` is the explicit check (instant when ready, non-zero exit with the log tail when provisioning failed); the `slate brief` cheatsheet tells your agent about it. Both hooks sit behind the same interactive-terminal gate as `auto_cd`, so scripts and CI invoking `slate new` still provision synchronously.

All three commands run in the worktree via `sh -c` with `{{WORKSPACE}}`, `{{PROJECT}}`, and `{{HOSTNAME}}` expanded and `SLATE_WORKSPACE`, `SLATE_PROJECT`, `SLATE_FRESH`, `SLATE_PROVISIONING` in the environment. `slate agent` with no `agent:` configured is an error. The hooks can be anything:

```yaml
# a session that survives your terminal app and allows multiple attachments
up: tmux new-session -A -s {{HOSTNAME}} 'slate agent'
```

(`tmux new -A` re-attaches an existing server session, which keeps its original environment, so `SLATE_FRESH` only reaches `slate agent` on the session that created the server.)

These commands execute on your **host**: your normal claude login, skills, MCPs, and git access all apply. Because of that, `agent`, `new`, and `up` are always read from the **main checkout's** `slate.yml` and never from the workspace copy: the worktree is writable by container code, so a compromised dependency could otherwise edit `slate.yml` and wait for your next slate command. Workspace-side edits to these fields are inert and get a note saying so; land them in the main checkout to take effect. The blast-radius protection stays where it always was, in the containers that run the app and its dependency installs.

### Slate for agents

Slate is designed to be driven by an LLM running on the host:

- Every command honours `SLATE_WORKSPACE=<name>`, so agents never depend on a cwd or an interactive picker.
- Non-interactive contexts fail fast with instructions instead of prompting (`slate up missing-ws` errors rather than asking to create; `slate exec` runs without a TTY and forwards stdin).
- Container commands self-synchronise with background provisioning: `slate exec` and the scaffold tools wait for an in-flight provision, and `slate wait` makes the check explicit.
- `slate brief` prints a project-aware markdown cheatsheet (workspace targeting, tools, URLs, the container test-database gotcha) for pasting into your `CLAUDE.md`/`AGENTS.md`.

## Scaffolds

| Scaffold | Stack | Services |
|----------|-------|----------|
| `laravel` | PHP 8.3 + Apache, MySQL, Vite, Mailpit | app, queue, mysql, vite, mailpit |
| `nextjs` | Node 22, PostgreSQL, Mailpit | app, postgres, mailpit |
| [inline](#inline-scaffolds) | Bring your own compose file | User-defined |

### Inline scaffolds

When no built-in scaffold fits, define one inline by giving `scaffold:` a map instead of a name:

```yaml
scaffold:
  compose: ./slate/compose.yaml
  subdomains:
    "@":    { service: app, port: 8081 }     # the main <project>--<ws>.test (DNS-style apex)
    warden: { service: warden, port: 8080 }  # warden.<project>--<ws>.test
```

- **`compose`** is a committed, project-relative compose file, copied into each workspace's `.slate/compose.yaml`. Its content comes from the workspace branch's committed copy, or the main checkout when the branch doesn't commit one, never from the worktree's working files (a compose file defines mounts, so it's host-reaching config; see [Project Config](#project-config)). Follow the conventions the built-in scaffolds use: bind-mount the worktree as `..:/app`, publish container ports without host numbers (`ports: ["8081"]`) so Docker assigns free ones, and optionally mount `${SLATE_ENTRYPOINT}` as the entrypoint for slate's UID mapping. `${MAIN_ROOT}`, `${APP_UID}`, and `${APP_GID}` interpolate as usual. The Dockerfile is committed too and referenced directly (`build: {context: .., dockerfile: slate/Dockerfile}`).
- **`subdomains`** declares the HTTPS routes: which service and container port each hostname proxies to. `"@"` is the apex, i.e. the main workspace hostname (quoted, since YAML reserves a bare `@`).
- Everything else stays in the ordinary keys: `setup:` / `fresh:` (inline scaffolds have no defaults), `tools:`, `env:`, `files:`.
- Services that bind-mount `/app` get the built-ins' app-like treatment: `app` is the primary (runs the lifecycle, default target for `slate exec`), the rest are restarted after each setup run since they don't hot-reload.

Name the compose file with a `.tmpl` extension to run it through Go's text/template on the way in, with `vars:` as free-form input:

```yaml
scaffold:
  compose: ./slate/compose.yaml.tmpl
  vars:
    with_warden: true
```

Template data: `.Project`, `.Workspace`, `.Hostname`, `.HasMainEnv`, `.Database`, and `.Vars`. Most per-workspace variance doesn't need this; compose `${...}` interpolation and `env:` placeholders (`{{DB_NAME:label}}`, `{{GEN_PASSWORD:salt}}`, which share the `{{...}}` syntax and belong in `env:` values, not `.tmpl` files) already cover values. Reach for a template only for structural differences, like conditionally including a service.

`slate init inline` writes a starter slate.yml. The legacy `scaffold: none` still parses and behaves as an inline scaffold with no compose file.

### Vite over HTTPS (Laravel)

Inside a workspace, Vite is served over a proxied HTTPS subdomain
(`https://vite.<project>--<workspace>.test`), not `http://0.0.0.0:5173`. To load
assets and HMR over HTTPS without mixed-content blocks or Vite's host check, add
[`@devtime-ltd/vite-plugin-slate`](vite-plugin-slate) to your `vite.config.js`:

```sh
npm i -D @devtime-ltd/vite-plugin-slate
```

```js
import slate from "@devtime-ltd/vite-plugin-slate";

export default defineConfig({
  plugins: [laravel({ /* ... */ }), slate()],
});
```

slate sets `VITE_DEV_SERVER_URL` in the workspace; the plugin reads it to point
Vite's `origin`/`allowedHosts`/`cors`/`hmr` at the proxy. It's a no-op when that
var is unset, so `npm run dev` outside slate is unaffected.

## Global Config

`~/.config/slate/config.yml` (all optional):

```yaml
http_port: 80           # default: 80
https_port: 443         # default: 443
tls: true               # false for HTTP-only (no certs needed)
secret_key: <generated> # auto-generated on first `slate setup`
editor: code            # default editor for `slate code` (prompted on first use)
auto_cd: true           # default: true. When true, `slate new` and `slate up`
                        # drop into a shell at the workspace dir when ready.
                        # Override per-invocation with --cd / --cd=false.
```

The registered projects index lives at `~/.config/slate/projects` (one `name=path` entry per line, names assigned at registration and stable across removals).

## Requirements

- Docker (via [OrbStack](https://orbstack.dev) on macOS, or Docker Engine on Linux)
- Git

That's it. Everything else is managed by slate.

`slate setup` also makes `*.test` resolve locally by running a small dnsmasq container on `127.0.0.1:53` and pointing `/etc/resolver/test` at it (one `sudo` prompt, macOS). On Linux the container runs the same way, but you point your system resolver (systemd-resolved/NetworkManager) at `127.0.0.1` for `*.test` yourself. If `*.test` already resolves (e.g. you run your own dnsmasq), slate leaves it alone.

### Docker network limits

Every running workspace takes one Docker network, and Docker allocates each network a subnet from its default address pools. Those pools, not memory or disk, are what caps how many workspaces you can run at once: stock Docker Engine defines 32 (`172.17.0.0/12` at `/16`, plus `192.168.0.0/16` at `/20`) and OrbStack 30. Past that, `slate up` fails with `all predefined address pools have been fully subnetted`.

Slate reclaims what it can. `slate down` sweeps networks left behind by workspaces stopped outside slate (a reboot, an OrbStack restart, a manual `docker stop`), a failed `slate up` retries once after sweeping, and `slate doctor` reports the budget:

```
  ✔ docker address pools (6 of 30 networks in use, 1 reclaimable)
```

If you keep more workspaces than that, raise the ceiling in the Docker daemon config. A workspace needs a handful of addresses, not the 254 a `/24` gives it, so a smaller subnet size buys far more networks from the same space. On OrbStack edit `~/.orbstack/config/docker.json` via `orb config docker`, which restarts the engine; on Docker Engine, `/etc/docker/daemon.json`:

```json
{ "default-address-pools": [{ "base": "10.99.0.0/16", "size": 27 }] }
```

That gives 2048 networks of 29 usable addresses each. Pick a base that does not overlap anything you route to, such as a VPN or an office LAN, and note that existing networks keep their current subnets until they are recreated.

