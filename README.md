# Slate

Isolated dev workspaces powered by Docker. One command to start, HTTPS out of the box, containers you never have to think about.

Your code stays on the host, everything else runs in Docker containers. Each workspace gets its own database, services, and HTTPS URL, spun up from a git worktree in seconds.

## Installation

Slate is a single Go binary. Building it requires [Go 1.26+](https://go.dev/dl/); the scaffold templates are embedded at compile time, so there are no other build-time dependencies.

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

Tools:
  slate setup                     One-time host setup (proxy + DNS + CA cert + secret key)
  slate teardown                  Remove all slate infrastructure
  slate doctor                    Check dependencies
  slate brief                     Print an agent-facing cheatsheet for this project
  slate open [name]               Open workspace URL in browser
  slate path [name]               Print workspace path (pipeable, --open)
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
- `slate new <name> --bg`: fork the slow phase (build + lifecycle) to the background; the fast phase (worktree + scaffold) runs inline so editing can start immediately. Progress is captured in `.slate/workspaces/<name>/.slate/provision.log` and surfaced as `provisioning` in `slate ls` (`failed` if it errors). While a bg provision is in flight, `slate up` and `slate restart` refuse to touch the workspace; `slate rm` aborts it as an escape hatch.
- `slate new <name> --cd` / `--cd=false`: opt in or out of dropping into a shell at the new workspace. Default comes from `auto_cd` in `~/.config/slate/config.yml` (default `true`), suppressed when stdio isn't an interactive terminal so scripts/CI/agents never block on a spawned shell. With `--bg` the shell is spawned immediately; without, after provisioning finishes.
- `slate new <name> --adopt`: carry your uncommitted changes from the main checkout into the new worktree (tracked changes patched in, untracked files copied). The main checkout is left untouched.
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

That's it for most projects. The scaffold provides sensible defaults for the Docker image, services, lifecycle scripts, and available tool commands.

Each workspace uses the `slate.yml` in its **own worktree** when present, so a branch can change config (packages, hooks, tools, agent) and test it with `slate up` before merging; slate prints a note whenever a workspace's config differs from the main checkout's. The one exception is `project:`, which is always taken from the main checkout so a branch can't change the workspace's identity (hostname, compose project, database names). Heavier changes like swapping `scaffold:` usually want `slate up --fresh`.

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

## Agent + up: what `new`/`up` drop you into

When provisioning finishes at an interactive terminal, slate drops you into the workspace (the `auto_cd` behaviour): it runs the `up` hook if configured, then a shell. `agent` defines the command `slate agent [name]` runs; point `up` at it to land in your agent after every `new`/`up`:

```yaml
agent:
  - claude --name "{{PROJECT}}--{{WORKSPACE}}"   # first run (SLATE_FRESH=1)
  - claude --continue                            # thereafter
up: slate agent
```

`agent` is either a single command or a `[first-run, thereafter]` pair; the first-run variant is picked when `SLATE_FRESH=1`, which slate sets when `up` fires from a freshly provisioned `slate new`. With the pair above, every new workspace starts a claude session named `<project>--<workspace>` (resumable later with `claude --resume <name>`), and re-entry continues where you left off.

Both commands run in the worktree via `sh -c` with `{{WORKSPACE}}`, `{{PROJECT}}`, and `{{HOSTNAME}}` expanded and `SLATE_WORKSPACE`, `SLATE_PROJECT`, `SLATE_FRESH` in the environment. `slate agent` with no `agent:` configured is an error. `up` can be anything:

```yaml
# a session that survives your terminal app and allows multiple attachments
up: tmux new-session -A -s {{HOSTNAME}} 'slate agent'
```

(`tmux new -A` re-attaches an existing server session, which keeps its original environment, so `SLATE_FRESH` only reaches `slate agent` on the session that created the server.)

These commands execute on your **host**: your normal claude login, skills, MCPs, and git access all apply. Because of that, `agent` and `up` are always read from the **main checkout's** `slate.yml` and never from the workspace copy: the worktree is writable by container code, so a compromised dependency could otherwise edit `slate.yml` and wait for your next slate command. Workspace-side edits to these fields are inert and get a note saying so; land them in the main checkout to take effect. The blast-radius protection stays where it always was, in the containers that run the app and its dependency installs.

### Slate for agents

Slate is designed to be driven by an LLM running on the host:

- Every command honours `SLATE_WORKSPACE=<name>`, so agents never depend on a cwd or an interactive picker.
- Non-interactive contexts fail fast with instructions instead of prompting (`slate up missing-ws` errors rather than asking to create; `slate exec` runs without a TTY and forwards stdin).
- `slate brief` prints a project-aware markdown cheatsheet (workspace targeting, tools, URLs, the container test-database gotcha) for pasting into your `CLAUDE.md`/`AGENTS.md`.

## Scaffolds

| Scaffold | Stack | Services |
|----------|-------|----------|
| `laravel` | PHP 8.3 + Apache, MySQL, Vite, Mailpit | app, queue, mysql, vite, mailpit |
| `nextjs` | Node 22, PostgreSQL, Mailpit | app, postgres, mailpit |
| `none` | Bring your own compose.yaml | User-defined |

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
