# Slate

Isolated dev workspaces via Docker. HTTPS out of the box.

Each workspace gets its own database, services, and HTTPS URL, spun up from a git worktree.

## Installation

Slate is a single Go binary, installed with [Go 1.26+](https://go.dev/dl/). Running it needs Docker ([OrbStack](https://orbstack.dev) recommended on macOS, Docker Engine on Linux) and Git.

```sh
go install github.com/devtime-ltd/slate@latest
```

Ensure it's available on your path (add to .bashrc, .zshrc, etc). `go install` puts it in `$GOBIN`, or `$GOPATH/bin` when that is unset, which is `~/go/bin` by default:

```sh
export PATH="$HOME/go/bin:$PATH"
```

## Quick Start

```sh
slate setup              # once: HTTPS proxy + *.test DNS + CA cert
slate init laravel       # in your project: creates slate.yml
slate new my-feature     # creates workspace with containers + HTTPS
```

Open `https://your-project--my-feature.test` and start developing.

## Commands

```
Workspace lifecycle:
  slate new <name>                Create a new workspace (containers + HTTPS)
  slate up [name]                 Start/refresh a workspace
  slate down [name]               Stop (preserves data)
  slate restart [name] [service]  Restart workspace or single service
  slate rm [name]                 Destroy workspace (containers, volumes, worktree)
  slate done [name]               Destroy a workspace once its work has landed
  slate prune                     Delete orphaned workspace branches and worktrees
  slate ls [--all]                List workspaces (current project or all registered)
  slate wait [name]               Block until a background provision finishes

Tools:
  slate setup                     One-time host setup (proxy + DNS + CA cert + secret key)
  slate teardown                  Remove all slate infrastructure
  slate doctor                    Check dependencies
  slate version                   Print the build version, commit and date
  slate brief                     Print an agent-facing cheatsheet for this project
  slate open [name]               Open workspace URL in browser
  slate path [name]               Print workspace path (pipeable, --open)
  slate pwd                       Print the project's main checkout (pipeable)
  slate cd [name]                 Spawn a sub-shell rooted at the workspace dir
  slate where [project[@ws]]      Print a project or workspace directory (pipeable)
  slate code [name]               Open workspace in your editor
  slate shell [name]              Bash shell in app container
  slate agent [name]              Run the agent command in a workspace (see Agent)
  slate exec [-s svc] -- <cmd>    Run an arbitrary command in a container (-i for a TTY)
  slate logs [name] [svc]         Tail logs (default: all services)
  slate proxy                     Manage the HTTPS proxy
  slate dns                       Manage the *.test DNS resolver

Scaffold tools (from slate.yml), for Laravel:
  slate composer <args>     slate artisan <args>     slate tinker
  slate pint <args>         slate pest <args>
  slate npm <args>          slate npx <args>
  slate mysql [name]        Print DB connection info (--open, --url)
```

`slate <command> --help` documents each command's flags.

### Targeting a workspace

Omit the workspace name and slate uses the one you're inside, or pops a picker over the project's workspaces. From outside any worktree, or in scripts, CI and agents, set `SLATE_WORKSPACE=<name>` (honoured by every command that targets a workspace; `slate where` takes its target as an argument instead) or pass `-w <name>` to the lifecycle and utility commands. The scaffold tools pass every argument through, including `-w`, so target those with `SLATE_WORKSPACE`.

`--project <name>` targets a project other than the current directory's, by its registry name (`slate ls --all`).

### Jumping between projects

`slate where [project[@workspace]]` prints a project's main checkout or a workspace inside it. When no registered project matches, it looks under `dev_root` (default `~/Development`, set in the global config) for a directory of that name. A process can't change its parent shell's directory, so `slate shellenv` prints a `dev` function that does, with tab completion over project and workspace names. One line in your rc file:

```sh
eval "$(slate shellenv zsh)"    # ~/.zshrc, after compinit
eval "$(slate shellenv bash)"   # ~/.bashrc
```

Then `dev project` or `dev project@my-feature`.

### Creating and destroying

- `slate new` branches from the repo's default branch as `slate/<name>`. `-b` names the branch, `--base <ref>` forks from another ref, `--base-head` forks from the main checkout's current HEAD.
- `--adopt` carries the main checkout's uncommitted changes into the new worktree. `--bare` creates the worktree without containers; the first `slate up` provisions it.
- `--bg` forks the slow phase (build + lifecycle) to the background so editing can start at once. `slate ls` shows `provisioning` (or `failed`), `slate exec` and the scaffold tools wait for it, and `slate wait` blocks until it finishes. A configured `new:` hook backgrounds provisioning automatically.
- `slate up --fresh` recreates containers and volumes (code preserved); `--build` forces an image rebuild.
- `slate done` destroys a workspace only after proving its work landed (clean worktree, branch merged by ancestry or a merged PR). If it can't, it refuses with reasons; `slate rm -f` remains the force path.
- `slate rm` warns about uncommitted changes before confirming. If your shell was inside the workspace, slate exits a slate-spawned shell or drops you into one at the main checkout.

### One-off commands

`slate exec` runs anything else inside a workspace container, without a TTY by default so it's safe in scripts and agents:

```sh
slate exec -- ./vendor/bin/phpstan analyse
slate exec -s vite -- npm run build                   # another service (default: app)
slate exec -i -- php artisan tinker                   # allocate a TTY
slate exec --pause-workers -- php artisan migrate:fresh --seed
```

`--pause-workers` stops the queue worker for the duration: a live `queue:work` deadlocks against `migrate:fresh`.

## How It Works

Each `slate new` creates a git worktree and spins up the Docker containers your scaffold defines (e.g. PHP + Apache, MySQL, Vite, queue worker, Mailpit). A Caddy proxy terminates HTTPS so you get real `.test` URLs.

```
Host                          Containers (per workspace)
┌──────────────────────┐      ┌───────────────────────────┐
│ Your editor          │      │ app (PHP/Node/Ruby)       │
│ Git worktrees        │ ───► │ database (MySQL/Postgres) │
│ Slate CLI            │      │ vite/assets               │
│ HTTPS proxy (Caddy)  │      │ queue worker              │
└──────────────────────┘      │ mailpit                   │
                              └───────────────────────────┘
```

Source code is bind-mounted from the host. Package installs run inside containers, so a compromised dependency can't reach your SSH keys, cloud credentials, or browser password stores. Dependency caches live in each workspace under `.slate/`.

On first `slate new`, slate adds `.slate/workspaces/` to your project's `.gitignore`.

## Project Config

A single `slate.yml` in your project root:

```yaml
scaffold: laravel
```

That's it for most projects. The scaffold provides the Docker image, services, lifecycle scripts, and tool commands. When no built-in scaffold fits, define one inline; see [Inline scaffolds](#inline-scaffolds).

Each workspace uses the `slate.yml` in its own worktree, so a branch can change config and test it with `slate up` before merging. Exceptions:

- `project:` always comes from the main checkout, so a branch can't change a workspace's identity.
- `agent:`, `new:` and `up:` run on the host and only ever come from the main checkout (see [Agent](#agent)).
- Keys that reach the host (`scaffold:`, `files:`, `database:`, `env:`, `node_image:`, `apt_packages:`, `php_extensions:`, `php_ini:`) come from **committed** content on the workspace branch, never the working copy, so a compromised dependency can't rewrite them and wait for your next `slate up`. Uncommitted edits to them get a note; commit them on the branch to test.

Swapping `scaffold:` usually wants `slate up --fresh`.

### Customisation

```yaml
scaffold: laravel

project: my-project # default: directory basename
base_branch: develop # default: the repo's default branch
base_from_head: true # fork from the main checkout's HEAD instead

apt_packages: [ghostscript, imagemagick, libmagickwand-dev]
php_extensions: [imagick]
node_image: node:22 # default: node:24 (laravel), node:24-slim (nextjs)
php_ini:
  memory_limit: 1024M # defaults: memory_limit=512M, upload/post 100M

setup: | # lifecycle hook override
  composer config http-basic.nova.laravel.com "$NOVA_USER" "$NOVA_KEY"
  {{SCAFFOLD_DEFAULT}}

env:
  CUSTOM_VAR: value
  ANALYTICS_DB: "{{DB_NAME:analytics}}"
  REDIS_PASSWORD: "{{GEN_PASSWORD:redis}}"

files: # host files mounted into containers
  ~/.npmrc: /home/node/.npmrc
```

Pin a `node_image` whose bundled npm satisfies your `engines.npm`, or a mismatched npm rewrites the lockfile on every start. `apt_packages`, `php_extensions` and `php_ini` build into the image, so an existing workspace picks them up on `slate up --build`.

Lifecycle hooks run inside the containers, with worker services paused for the duration:

- **`setup`** runs on every `slate new` and `slate up` (default: install deps + migrate).
- **`fresh`** runs after `setup` on `slate new` and `slate up --fresh` (default: fresh DB seed).
- `{{SCAFFOLD_DEFAULT}}` injects the scaffold's default script at that point. A `retry <cmd>` helper (3 attempts) wraps flaky network steps.

Placeholders, expanded at workspace creation: `{{GEN_PASSWORD:salt}}` derives a per-workspace password from your installation's secret key; `{{DB_NAME:label}}` gives a safe database name.

### Custom tools

Scaffolds register their tool commands automatically. Add your own, which run a command in a container:

```yaml
tools:
  mycommand:
    service: app
    command: [php, my-script.php]
```

## Agent

At an interactive terminal, `slate new` and `slate up` drop you into the workspace through two hooks: `new:` fires straight after the worktree exists, with provisioning forked to the background behind it; `up:` fires once provisioning finishes. `agent:` is the command `slate agent [name]` runs; point the hooks at it to land in your agent:

```yaml
agent:
  - claude --name "{{PROJECT}}--{{WORKSPACE}}" # first run
  - claude --continue # thereafter
new: slate agent
up: slate agent
```

`agent:` is a single command or a `[first-run, thereafter]` pair; the first-run variant runs on the workspace's first agent entry, however that entry is reached. With `new:` configured, `slate new foo` creates the worktree in seconds and runs the hook at once, so the agent starts while the containers come up; `slate exec` and the scaffold tools wait for provisioning, so the agent's first container command simply blocks instead of failing. `slate agent myws -- "review the open PR"` passes arguments through to the agent command (`{{ARGS}}` marks where they land).

All three run in the worktree via `sh -c` with `{{WORKSPACE}}`, `{{PROJECT}}` and `{{HOSTNAME}}` expanded and `SLATE_WORKSPACE`, `SLATE_PROJECT`, `SLATE_FRESH` and `SLATE_PROVISIONING` set. They can be anything:

```yaml
up: tmux new-session -A -s {{HOSTNAME}} 'slate agent'
```

These commands run on your **host**, so your normal login, skills, MCPs and git access apply. For that reason they are read only from the **main checkout's** `slate.yml`: the worktree is container-writable, and a compromised dependency could otherwise edit it and wait for your next slate command. Workspace-side edits to them are inert and get a note.

An `agent:` command that exits within three seconds can't have hosted a session (a `claude --continue` with nothing to continue does exactly this), so slate treats it as a failed launch: it reports what ran, retries the first-run variant if the thereafter one bailed, and leaves a shell in the workspace so a wrapping tmux session survives with the diagnostic on screen. A session that crashes with a non-zero exit gets the same held shell. `slate agent --help` has the details; `SLATE_AGENT_MIN_RUNTIME=0` disables the check.

### Local overrides

An uncommitted `slate.local.yml` next to the main checkout's `slate.yml` overrides the host-side keys per developer: `agent:`, `new:`, `up:`, `doctor:` and `brief:`. Each key present replaces the committed value wholesale; any other key is an error. Slate adds it to `.gitignore` on the next `slate new`. It is read only from the main checkout, never a worktree; mounting the main checkout itself into a container (an inline scaffold using `${MAIN_ROOT}`) makes it container-writable and voids that protection, as it does for `slate.yml`. A typical use is pinning agent sessions to an account switcher without pushing that onto other developers:

```yaml
agent:
  - cswap run -- claude --name "{{PROJECT}}--{{WORKSPACE}}"
  - cswap run -- claude --continue
```

### Project checks and notes

`doctor:` is a map of named host checks appended to `slate doctor`; a non-zero exit renders as a warning and never affects the result. `brief:` is a host command whose stdout is appended to `slate brief` under a `## Project notes` heading. Each gets 10 seconds.

```yaml
doctor:
  vpn: ping -c1 -W1 10.0.0.1

brief: |
  echo "**Account:** $(cswap status --json | jq -r '.accounts[] | select(.active) | .email')"
```

### Slate for agents

Slate is designed to be driven by an LLM on the host: every workspace command honours `SLATE_WORKSPACE`, non-interactive contexts fail fast with instructions instead of prompting, container commands wait for an in-flight provision, and `slate brief` prints a project-aware cheatsheet for your `CLAUDE.md` or `AGENTS.md`.

## Scaffolds

| Scaffold                    | Stack                                  | Services                         |
| --------------------------- | -------------------------------------- | -------------------------------- |
| `laravel`                   | PHP 8.3 + Apache, MySQL, Vite, Mailpit | app, queue, mysql, vite, mailpit |
| `nextjs`                    | Node 24, PostgreSQL, Mailpit           | app, postgres, mailpit           |
| [inline](#inline-scaffolds) | Bring your own compose file            | User-defined                     |

### Inline scaffolds

Give `scaffold:` a map instead of a name:

```yaml
scaffold:
  compose: ./slate/compose.yaml
  subdomains:
    "@": { service: app, port: 8081 } # <project>--<ws>.test
    warden: { service: warden, port: 8080 } # warden.<project>--<ws>.test
```

`compose` is a committed, project-relative compose file (read from committed content, like the other host-reaching keys). Follow the built-in scaffolds' conventions: bind-mount the worktree as `..:/app`, publish ports without host numbers (`ports: ["8081"]`), and optionally mount `${SLATE_ENTRYPOINT}` as the entrypoint for UID mapping. `${MAIN_ROOT}`, `${APP_UID}` and `${APP_GID}` interpolate. `subdomains` declares the HTTPS routes, with `"@"` as the main hostname. `setup:`, `fresh:`, `tools:`, `env:` and `files:` work as usual; inline scaffolds have no default hooks. Services that bind-mount `/app` are treated like the built-ins' app services: the first is the primary, the rest are paused while the lifecycle runs.

A compose file named `*.tmpl` runs through Go's text/template with `vars:` as input (`.Project`, `.Workspace`, `.Hostname`, `.HasMainEnv`, `.Database`, `.Vars`). Reach for it only for structural differences, such as conditionally including a service.

`slate init inline` writes a starter `slate.yml`.

### Vite over HTTPS (Laravel)

Vite is served over a proxied HTTPS subdomain (`https://vite.<project>--<workspace>.test`). Add [`@devtime-ltd/vite-plugin-slate`](vite-plugin-slate) to your `vite.config.js` so assets and HMR load without mixed-content blocks:

```js
import slate from "@devtime-ltd/vite-plugin-slate";

export default defineConfig({
	plugins: [
		laravel({
			/* ... */
		}),
		slate(),
	],
});
```

The plugin reads `VITE_DEV_SERVER_URL`, which slate sets in the workspace, and is a no-op outside slate.

## Global Config

`~/.config/slate/config.yml` (all optional):

```yaml
http_port: 80
https_port: 443
tls: true # false for HTTP-only (no certs needed)
secret_key: <generated> # auto-generated on first `slate setup`
editor: code # for `slate code` (prompted on first use)
auto_cd: true # `slate new` and `slate up` drop into a shell at the workspace
dev_root: ~/Development # where `slate where` looks for projects not in the registry
```

The project registry lives at `~/.config/slate/projects`.

## DNS and networking

`slate setup` makes `*.test` resolve locally by running a small dnsmasq container on `127.0.0.1:53`. On macOS it writes `/etc/resolver/test` (one `sudo` prompt); on Linux, point your system resolver at `127.0.0.1` for `*.test` yourself. If `*.test` already resolves, slate leaves it alone.

Every running workspace takes one Docker network, and Docker's default address pools cap how many you can run at once: 32 on stock Docker Engine, 30 on OrbStack. `slate doctor` reports the budget and `slate down` reclaims networks left behind by workspaces stopped outside slate. To raise the ceiling, use a smaller subnet size in the daemon config (`orb config docker` on OrbStack, `/etc/docker/daemon.json` on Docker Engine):

```json
{ "default-address-pools": [{ "base": "10.99.0.0/16", "size": 27 }] }
```

Pick a base that doesn't overlap anything you route to, such as a VPN or office LAN.
