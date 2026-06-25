import type { Plugin } from "vite";

/**
 * Wires the Vite dev server to slate's HTTPS proxy using the VITE_DEV_SERVER_URL
 * env var that slate sets inside a workspace. No-op when that var is unset.
 */
export default function slate(): Plugin;
