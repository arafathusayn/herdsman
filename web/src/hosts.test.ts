import { expect, test } from "bun:test";

import {
  blockedReason,
  defaultName,
  type Hosts,
  loadHosts,
  localNetworkHint,
  normalizeUrl,
  parseLink,
  saveHosts,
  upsert,
} from "./hosts.ts";

test("host URLs get a scheme that fits the address", () => {
  const cases: [string, string | null][] = [
    ["127.0.0.1:7878", "http://127.0.0.1:7878"],
    ["localhost", "http://localhost"],
    ["192.168.1.20:7878/", "http://192.168.1.20:7878"],
    ["100.101.7.3:7878", "http://100.101.7.3:7878"],
    ["devbox:7878", "http://devbox:7878"],
    ["[::1]:7878", "http://[::1]:7878"],
    ["box.example.com", "https://box.example.com"],
    ["https://box.example.com/herdsman/", "https://box.example.com/herdsman"],
    ["http://box.example.com:8080", "http://box.example.com:8080"],
    ["ftp://box.example.com", null],
    ["https://user:pw@box.example.com", null],
    ["", null],
    ["   ", null],
  ];
  for (const [input, want] of cases) expect([input, normalizeUrl(input)]).toEqual([input, want]);
});

test("pairing links name the host or default to the page", () => {
  const tok = "a".repeat(48);
  expect(parseLink(`http://127.0.0.1:7878/#t=${tok}`, "https://app.example.com")).toEqual({
    url: "http://127.0.0.1:7878",
    token: tok,
  });
  expect(
    parseLink(`https://app.example.com/#host=${encodeURIComponent("http://10.0.0.5:7878")}&t=${tok}`, "https://x"),
  ).toEqual({ url: "http://10.0.0.5:7878", token: tok });
  // A bare fragment is read against the page.
  expect(parseLink(`#t=${tok}`, "http://localhost:7878")).toEqual({ url: "http://localhost:7878", token: tok });
  expect(parseLink("http://127.0.0.1:7878/", "http://x")).toBeNull();
  expect(parseLink("http://127.0.0.1:7878/#t=nothex!", "http://x")).toBeNull();
});

test("hosts are named, kept and made current", () => {
  const store = new Map<string, string>();
  globalThis.localStorage = {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  } as Storage;
  expect(loadHosts()).toEqual({ hosts: [], current: "" });
  const hs: Hosts = { hosts: [], current: "" };
  const a = upsert(hs, "http://127.0.0.1:7878");
  const b = upsert(hs, "https://box.example.com", "Box");
  expect([a.name, b.name, hs.current]).toEqual(["This computer", "Box", b.id]);
  expect(upsert(hs, "http://127.0.0.1:7878").id).toBe(a.id);
  expect(hs.current).toBe(a.id);
  expect(saveHosts(hs)).toBe(true);
  expect(loadHosts()).toEqual(hs);
  // A damaged entry is dropped; a missing current falls back to the first host.
  store.set("herdsman.hosts", JSON.stringify({ hosts: [{ id: "x", url: "not a url" }, b], current: "gone" }));
  expect(loadHosts()).toEqual({ hosts: [b], current: b.id });
  store.set("herdsman.hosts", "{");
  expect(loadHosts()).toEqual({ hosts: [], current: "" });
  expect(defaultName("http://127.0.0.1:9000")).toBe("This computer :9000");
  expect(defaultName("http://192.168.1.20:7878")).toBe("192.168.1.20:7878");
});

test("an https page cannot reach plain-http hosts beyond this computer", () => {
  expect(blockedReason("https:", "http://127.0.0.1:7878")).toBe("");
  expect(blockedReason("https:", "http://localhost:7878")).toBe("");
  expect(blockedReason("https:", "http://192.168.1.20:7878")).not.toBe("");
  expect(blockedReason("http:", "http://192.168.1.20:7878")).toBe("");
  expect(blockedReason("https:", "https://box.example.com")).toBe("");
});

// A public page reaches this machine or a private address only after the
// browser's local network prompt; the server's own page needs no prompt.
test("a public page that cannot reach a private host hints at the local network prompt", () => {
  expect(localNetworkHint("herdsman.arafat.ai", "http://127.0.0.1:7878")).toContain("local network");
  expect(localNetworkHint("herdsman.arafat.ai", "https://box.tailnet.ts.net")).toBe("");
  expect(localNetworkHint("herdsman.arafat.ai", "http://100.101.7.3:7878")).toContain("local network");
  expect(localNetworkHint("127.0.0.1", "http://127.0.0.1:7878")).toBe("");
  expect(localNetworkHint("192.168.1.20", "http://192.168.1.20:7878")).toBe("");
});

test("a host list the browser cannot store reports it", () => {
  const before = globalThis.localStorage;
  globalThis.localStorage = {
    getItem: () => null,
    setItem: () => {
      throw new DOMException("blocked", "SecurityError");
    },
    removeItem: () => {},
  } as unknown as Storage;
  try {
    expect(saveHosts({ hosts: [], current: "" })).toBe(false);
  } finally {
    globalThis.localStorage = before;
  }
});
