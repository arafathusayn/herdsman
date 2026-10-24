// Hosts: the herdsman servers this app talks to. The app may be served by
// `herdsman web` itself (that server is the default host) or hosted on its
// own site, from where it reaches any host: this computer, a LAN or
// Tailscale address, or a public domain in front of a server. Each host
// pairs once per run of its server; its device secret is kept here.

export interface Host {
  id: string;
  /** The server's origin plus any path prefix, no trailing slash. */
  url: string;
  name: string;
  /** The secret this browser got when it paired, sent as a bearer token. */
  device?: string;
}

export interface Hosts {
  hosts: Host[];
  current: string;
}

const storeKey = "herdsman.hosts";

export function loadHosts(): Hosts {
  try {
    const v = JSON.parse(localStorage.getItem(storeKey) ?? "null") as Partial<Hosts> | null;
    const hosts = (Array.isArray(v?.hosts) ? v.hosts : []).filter(
      (x): x is Host => typeof x?.id === "string" && typeof x.url === "string" && normalizeUrl(x.url) === x.url,
    );
    const current = hosts.some((x) => x.id === v?.current) ? (v?.current ?? "") : (hosts[0]?.id ?? "");
    return { hosts, current };
  } catch {
    return { hosts: [], current: "" };
  }
}

export function saveHosts(h: Hosts): void {
  try {
    localStorage.setItem(storeKey, JSON.stringify(h));
  } catch {
    // Kept for this page only.
  }
}

const loopbackNames = new Set(["localhost", "127.0.0.1", "[::1]", "::1"]);

export function isLoopback(hostname: string): boolean {
  return loopbackNames.has(hostname) || hostname.endsWith(".localhost") || /^127\./.test(hostname);
}

/** Addresses that only a LAN, a VPN or this machine reaches: plain http is the norm there. */
function isPrivate(hostname: string): boolean {
  return (
    isLoopback(hostname) ||
    /^(10|192\.168|169\.254)\./.test(hostname) ||
    /^172\.(1[6-9]|2\d|3[01])\./.test(hostname) ||
    /^100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\./.test(hostname) || // Tailscale's CGNAT range
    hostname.endsWith(".local") ||
    /^\[(fc|fd|fe80)/i.test(hostname) ||
    !hostname.includes(".") // a bare machine name
  );
}

/**
 * A host URL from what a person types: "192.168.1.20:7878", "localhost",
 * "https://box.example.com/herdsman/". A missing scheme means http for a
 * private address and https for a public name. Null when it is no URL.
 */
export function normalizeUrl(input: string): string | null {
  let s = input.trim();
  if (!s) return null;
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(s)) {
    const hostname = s.split(/[/:?#]/)[0] ?? "";
    const bracket = /^\[[^\]]+\]/.exec(s)?.[0];
    s = `${isPrivate(bracket ?? hostname) ? "http" : "https"}://${s}`;
  }
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return null;
  }
  if ((u.protocol !== "http:" && u.protocol !== "https:") || !u.hostname || u.username || u.password) return null;
  return `${u.origin}${u.pathname.replace(/\/+$/, "")}`;
}

export interface Link {
  url: string;
  token: string;
}

/**
 * The pairing link in a URL: `herdsman web` prints http://host:port/#t=TOKEN,
 * and a hosted app's link carries the server too: #host=URL&t=TOKEN. The
 * page's own origin is the host when the link names none.
 */
export function parseLink(href: string, pageOrigin: string): Link | null {
  let u: URL;
  try {
    u = new URL(href.trim(), pageOrigin);
  } catch {
    return null;
  }
  const q = new URLSearchParams(u.hash.slice(1));
  const token = q.get("t") ?? "";
  if (!/^[a-f0-9]{16,128}$/.test(token)) return null;
  const named = q.get("host");
  const url = normalizeUrl(named ?? `${u.origin}${u.pathname}`);
  return url ? { url, token } : null;
}

/** The name a host gets until it is renamed. */
export function defaultName(url: string): string {
  const u = new URL(url);
  if (isLoopback(u.hostname)) return u.port && u.port !== "7878" ? `This computer :${u.port}` : "This computer";
  return u.port ? `${u.hostname}:${u.port}` : u.hostname;
}

/** Adds a host, or finds the one with that URL, and makes it current. */
export function upsert(hs: Hosts, url: string, name?: string): Host {
  let host = hs.hosts.find((x) => x.url === url);
  if (!host) {
    host = { id: newId(), url, name: name?.trim() || defaultName(url) };
    hs.hosts.push(host);
  } else if (name?.trim()) host.name = name.trim();
  hs.current = host.id;
  return host;
}

function newId(): string {
  const b = new Uint8Array(6);
  crypto.getRandomValues(b);
  return [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}

/**
 * Why a browser will refuse to reach the host from this page, or "". An
 * https page may not call plain http, except on loopback addresses.
 */
export function blockedReason(pageProtocol: string, url: string): string {
  const u = new URL(url);
  if (pageProtocol === "https:" && u.protocol === "http:" && !isLoopback(u.hostname))
    return "This page is served over HTTPS, so the browser blocks plain-HTTP hosts other than this computer. Put the host behind HTTPS (for example `tailscale serve`, or a proxy with `--allow-host`), or open the app from the host's own URL.";
  return "";
}
