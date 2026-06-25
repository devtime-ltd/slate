import { test } from "node:test";
import assert from "node:assert/strict";
import slate from "./index.js";

test("plugin metadata", () => {
  const plugin = slate();
  assert.equal(plugin.name, "vite-plugin-slate");
  assert.equal(plugin.apply, "serve");
});

test("no-op without VITE_DEV_SERVER_URL", () => {
  delete process.env.VITE_DEV_SERVER_URL;
  assert.deepEqual(slate().config(), {});
});

test("configures server from an https dev server url", () => {
  process.env.VITE_DEV_SERVER_URL = "https://vite.app--feat.test";
  const { server } = slate().config();
  assert.equal(server.origin, "https://vite.app--feat.test");
  assert.equal(server.cors, true);
  assert.deepEqual(server.allowedHosts, ["vite.app--feat.test"]);
  assert.deepEqual(server.hmr, {
    host: "vite.app--feat.test",
    protocol: "wss",
    clientPort: 443,
  });
});

test("honours a non-default https port", () => {
  process.env.VITE_DEV_SERVER_URL = "https://vite.app--feat.test:8443";
  assert.equal(slate().config().server.hmr.clientPort, 8443);
});

test("falls back to ws for an http dev server url", () => {
  process.env.VITE_DEV_SERVER_URL = "http://vite.app--feat.test:8080";
  const { hmr } = slate().config().server;
  assert.equal(hmr.protocol, "ws");
  assert.equal(hmr.clientPort, 8080);
});
