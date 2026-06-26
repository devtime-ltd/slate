import type { Plugin } from "vite";

export interface SlateOptions {
  /**
   * Force filesystem polling (`server.watch.usePolling`). Defaults to `false`:
   * slate's runtime forwards native fs events into the container, so polling
   * wastes a CPU core per workspace. Set `true` only if your Docker runtime
   * doesn't forward events and HMR misses changes.
   */
  poll?: boolean;
}

/**
 * Wires the Vite dev server to slate's HTTPS proxy using the VITE_DEV_SERVER_URL
 * env var that slate sets inside a workspace. No-op when that var is unset.
 */
export default function slate(options?: SlateOptions): Plugin;
