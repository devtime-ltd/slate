// When a workspace runs under slate, VITE_DEV_SERVER_URL holds the proxied HTTPS
// URL for the Vite dev server (e.g. https://vite.app--feat.test). Vite otherwise
// advertises http://0.0.0.0:5173 in its hot file (mixed-content on an HTTPS page)
// and rejects the proxied Host header with a 403. This points Vite at the proxy
// URL and allows the host. No-op when the var is unset, so a plain `npm run dev`
// outside slate is unaffected.
export default function slate() {
  return {
    name: "vite-plugin-slate",
    apply: "serve",
    config() {
      const devServerUrl = process.env.VITE_DEV_SERVER_URL;
      if (!devServerUrl) return {};

      const url = new URL(devServerUrl);
      const secure = url.protocol === "https:";
      const clientPort = Number(url.port) || (secure ? 443 : 80);

      return {
        server: {
          origin: devServerUrl,
          cors: true,
          allowedHosts: [url.hostname],
          hmr: {
            host: url.hostname,
            protocol: secure ? "wss" : "ws",
            clientPort,
          },
        },
      };
    },
  };
}
