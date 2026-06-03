# Slate

Isolated dev workspaces powered by Docker. One command to start, HTTPS out of the box, containers you never have to think about.

Your code stays on the host, everything else runs in Docker containers. Each workspace gets its own database, services, and HTTPS URL, spun up from a git worktree in seconds.

## Quick Start

```sh
# One-time setup
slate setup              # starts the HTTPS proxy, installs CA cert

# In your project
slate init laravel       # creates slate.yml
slate new my-feature     # creates workspace with containers + HTTPS
```

Open `https://your-project--my-feature.test` and start developing.

## Commands

```
Workspace lifecycle:
  slate new <name>          Create a new workspace (containers + HTTPS)
  slate up [name]           Start/refresh a workspace (offers to create if missing)
  slate down [name]         Stop (preserves data)
  slate restart <name>      Restart workspace or single service
  slate rm <name>           Destroy workspace (containers, volumes, worktree)
  slate ls [--all]          List workspaces (current project or all registered)

Tools:
  slate setup               One-time host setup (proxy + CA cert + secret key)
  slate teardown            Remove all slate infrastructure
  slate doctor              Check dependencies
  slate open <name>         Open workspace URL in browser
  slate path <name>         Print workspace path (pipeable, --open)
  slate cd <name>           Spawn a sub-shell rooted at the workspace dir
  slate code <name>         Open workspace in your editor
  slate shell <name>        Bash shell in app container
  slate logs <name> [svc]   Tail logs (default: all services)
  slate proxy               Manage the HTTPS proxy

Scaffold tools (from slate.yml):
  Available commands depend on your scaffold. For Laravel:
  slate composer <args>     slate artisan <args>     slate tinker
  slate pint <args>         slate pest <args>
  slate npm <args>          slate npx <args>
  slate mysql <workspace>   Print DB connection info (--open, --url)
```

Add `--project <name>` to any command to target a project other than the
current directory's. The project name comes from the registry (`slate ls --all`).

### Useful flags

- `slate new <name> -b <branch>`: custom branch name (default: `slate/<name>`).
- `slate new <name> --bg`: fork the slow phase (build + lifecycle) to the background; the fast phase (worktree + scaffold) runs inline so editing can start immediately. Progress is captured in `.slate/workspaces/<name>/.slate/provision.log` and surfaced as `provisioning` in `slate ls` (red `failed` if it errors). While a bg provision is in flight, `slate up` and `slate restart` refuse to touch the workspace; `slate rm` aborts it as an escape hatch.
- `slate new <name> --cd` / `--cd=false`: explicitly opt in or out of dropping into a shell at the new workspace. Default comes from `auto_cd` in `~/.config/slate/config.yml` (defaults to `true`). With `--bg` the shell is spawned immediately; without, after provisioning finishes.
- `slate up [name] --fresh`: recreate containers + volumes (worktree code preserved) and run the new-workspace lifecycle.
- `slate up [name] --build`: force image rebuild.

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

On first `slate new`, slate appends `.slate/workspaces/` to your project's `.gitignore` so workspace worktrees don't pollute the main checkout's status.

## Project Config

A single `slate.yml` in your project root:

```yaml
scaffold: laravel
```

That's it for most projects. The scaffold provides sensible defaults for the Docker image, services, lifecycle scripts, and available tool commands.

### Customisation

```yaml
scaffold: laravel

# Optional: override the auto-derived project name (from directory basename)
project: my-project

# Extra packages for the Docker image
apt_packages: [ghostscript, imagemagick, libmagickwand-dev]
php_extensions: [imagick]

# Override PHP ini values for both CLI and Apache/FPM (laravel scaffold).
# Rendered as a build-time conf.d drop-in so it covers PHPUnit/Pest,
# queue workers, and request handling. Defaults (memory_limit 512M,
# upload/post 100M) apply when unset; max_execution_time is left at
# PHP's CLI default of 0 (unlimited).
php_ini:
  memory_limit: 1024M

# Override lifecycle hooks (optional)
up: |
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

Lifecycle hooks:
- **`up`**: runs on every `slate new` and `slate up` (default: install deps + migrate)
- **`new`**: runs on `slate new` only, after `up` (default: fresh DB seed)
- Use `{{SCAFFOLD_DEFAULT}}` to inject the scaffold's defaults at any point in your override

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

## Scaffolds

| Scaffold | Stack | Services |
|----------|-------|----------|
| `laravel` | PHP 8.3 + Apache, MySQL, Vite, Mailpit | app, queue, mysql, vite, mailpit |
| `nextjs` | Node 22, PostgreSQL, Mailpit | app, postgres, mailpit |
| `none` | Bring your own compose.yaml | User-defined |

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
