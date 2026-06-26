# @devtime-ltd/vite-plugin-slate

A Vite plugin that makes the dev server work behind [slate](https://github.com/devtime-ltd/slate)'s HTTPS proxy.

Inside a slate workspace, Vite is reached over a proxied HTTPS subdomain (e.g.
`https://vite.app--feat.test`) rather than `http://0.0.0.0:5173`. Without this,
the page (served over HTTPS) requests assets over plain HTTP — browsers block the
mixed content — and Vite 5+ rejects the proxied `Host` header with a 403.

This plugin reads the `VITE_DEV_SERVER_URL` env var that slate sets in the
workspace and configures Vite's `server.origin`, `allowedHosts`, `cors`, and
`hmr` accordingly, so assets and HMR load over HTTPS.

It is a **no-op when `VITE_DEV_SERVER_URL` is unset**, so running `npm run dev`
outside slate behaves exactly as before.

## Install

```sh
npm i -D @devtime-ltd/vite-plugin-slate
```

## Usage

```js
// vite.config.js
import { defineConfig } from "vite";
import laravel from "laravel-vite-plugin";
import slate from "@devtime-ltd/vite-plugin-slate";

export default defineConfig({
  plugins: [
    laravel({ /* ... */ }),
    slate(),
  ],
});
```

That's it — no configuration. The plugin only applies during `vite serve` (dev).

## What it sets

When `VITE_DEV_SERVER_URL=https://vite.app--feat.test`:

```js
server: {
  origin: "https://vite.app--feat.test", // dev-server URL written to the hot file
  cors: true,                            // allow the cross-origin app host to load assets
  allowedHosts: ["vite.app--feat.test"], // accept the proxied Host header
  hmr: { host: "vite.app--feat.test", protocol: "wss", clientPort: 443 },
  watch: { usePolling: false },          // native fs events; see below
}
```

These are merged into your existing `server` config.

### Filesystem watching

slate's runtime (OrbStack) forwards native filesystem events into the container,
so Vite doesn't need to poll the worktree — and polling burns a full CPU core per
workspace, which adds up fast when you run several projects at once. The plugin
therefore disables polling under slate, **overriding** a `server.watch.usePolling: true`
in your own config (which is typically a Docker Desktop workaround and unnecessary
here).

If your Docker runtime doesn't forward fs events and HMR stops noticing changes,
opt back in:

```js
slate({ poll: true })
```

## License

MIT
