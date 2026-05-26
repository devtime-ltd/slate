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
  slate new <name>       Create a new workspace (containers + HTTPS)
  slate up [name]        Start/refresh a workspace
  slate down [name]      Stop (preserves data)
  slate rm <name>        Destroy everything
  slate ls               List workspaces

Development (available commands depend on your scaffold):
  slate composer <args>  slate artisan <args>   slate npm <args>
  slate pint <args>      slate pest <args>      slate tinker
  slate shell [name]     slate logs [service]

Tools:
  slate setup            One-time host setup (proxy + CA cert)
  slate doctor           Check dependencies
  slate tableplus [name] Open TablePlus to workspace DB
```

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

Source code is bind-mounted from the host. Package installs (`composer install`, `npm install`) run inside containers so compromised dependencies can't access your SSH keys, cloud credentials, or browser password stores.

## Project Config

A single `slate.yml` in your project root:

```yaml
scaffold: laravel
```

That's it for most projects. The scaffold provides sensible defaults for the Docker image, services, lifecycle scripts, and available tool commands.

### Customisation

```yaml
scaffold: laravel

# Extra packages for the Docker image
apt_packages: [ghostscript, imagemagick, libmagickwand-dev]
php_extensions: [imagick]

# Override lifecycle hooks (optional)
up: |
  composer config http-basic.nova.laravel.com "$NOVA_USER" "$NOVA_KEY"
  {{SCAFFOLD_DEFAULT}}

# Extra env vars for workspaces
env:
  CUSTOM_VAR: value
```

Lifecycle hooks:
- **`up`**: runs on every `slate new` and `slate up` (default: install deps + migrate)
- **`new`**: runs on `slate new` only, before `up` (default: fresh DB seed)
- Use `{{SCAFFOLD_DEFAULT}}` to inject the scaffold's defaults at any point in your override

### Custom tools

Scaffolds register tool commands automatically (e.g. Laravel provides `composer`, `artisan`, `pint`, `pest`). Override or add your own:

```yaml
tools:
  mycommand:
    service: app
    command: [php, my-script.php]
```

## Scaffolds

| Scaffold | Stack | Services |
|----------|-------|----------|
| `laravel` | PHP 8.3 + Apache, MySQL, Vite, Mailpit | app, queue, mysql, vite, mailpit |
| `nextjs` | Node 22, PostgreSQL, Mailpit | app, postgres, mailpit |
| `none` | Bring your own compose.yaml | User-defined |

## Global Config

`~/.config/slate/config.yml` (all optional):

```yaml
http_port: 80       # default: auto-detect available port
https_port: 443     # default: auto-detect available port
tls: true           # false for HTTP-only (no certs needed)
proxy: auto          # auto | caddy
```

## Requirements

- Docker (via [OrbStack](https://orbstack.dev) on macOS, or Docker Engine on Linux)
- Git

That's it. Everything else is managed by slate.
