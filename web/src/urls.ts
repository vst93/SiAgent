/**
 * Resolve app URLs against the directory the application is served from.
 *
 * SiAgent can be mounted below a reverse-proxy prefix such as /siagent/.
 * Never hard-code leading-slash API or asset paths in the browser bundle.
 */
const base = (() => {
  const current = new URL(window.location.href);
  if (!current.pathname.endsWith("/") && !current.pathname.endsWith(".html")) {
    current.pathname += "/";
  }
  return new URL("./", current);
})();

export function appURL(path = ""): string {
  return new URL(path.replace(/^\/+/, ""), base).href;
}
