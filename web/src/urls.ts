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

/**
 * Absolute address of this instance for people outside the browser session,
 * such as a channel platform posting a callback. The administrator may publish
 * the site under a different origin or prefix than the one they browse, so the
 * server reports it and this module prefers it over the current location.
 */
let publicBase = "";

export function setPublicBase(value: string | undefined): void {
  publicBase = (value ?? "").trim().replace(/\/+$/, "");
}

export function externalURL(path = ""): string {
  const clean = path.replace(/^\/+/, "");
  if (publicBase) return new URL(clean, `${publicBase}/`).href;
  return appURL(clean);
}
