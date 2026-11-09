import { expect, test } from "bun:test";

import { HerdsmanError } from "@herdsman/sdk";

import { bindHost, explain, notServed, services } from "./index.ts";

function recorder(reply: Response) {
  const calls: { url: string; init: RequestInit }[] = [];
  const send = (url: string, init: RequestInit) => {
    calls.push({ url, init });
    return Promise.resolve(reply);
  };
  return { calls, send };
}
const ok = () => new Response("{}", { headers: { "Content-Type": "application/json" } });

test("calls the bound host with the device secret and no cookies", async () => {
  const { calls, send } = recorder(ok());
  bindHost({ url: "http://host:1", device: "secret" }, send);
  await services().routes.list({});
  expect(calls[0]?.url).toBe("http://host:1/rpc/herdsman.daemon.v1.RouteService/List");
  expect(new Headers(calls[0]?.init.headers).get("Authorization")).toBe("Bearer secret");
  expect(calls[0]?.init.credentials).toBe("omit");
});

test("sends no Authorization before pairing", async () => {
  const { calls, send } = recorder(ok());
  bindHost({ url: "http://host:1" }, send);
  await services().projects.list({});
  expect(new Headers(calls[0]?.init.headers).has("Authorization")).toBe(false);
});

test("names what the daemon does not serve", () => {
  expect(notServed(new HerdsmanError("unimplemented", "no"))).toBe(true);
  expect(notServed(new HerdsmanError("internal", "no"))).toBe(false);
  expect(notServed(new Error("no"))).toBe(false);
});

test.each([
  [
    new HerdsmanError("unauthenticated", "x"),
    "This browser is not paired with the host. Open the link that herdsman web printed.",
  ],
  [new HerdsmanError("permission_denied", "only here"), "only here"],
  [new HerdsmanError("permission_denied", ""), "The host refused this browser."],
  [new HerdsmanError("unavailable", "gone"), "The daemon cannot be reached: gone"],
  [new HerdsmanError("not_found", "no such route"), "no such route"],
  [new HerdsmanError("internal", ""), "The call failed: internal"],
  [new Error("plain"), "plain"],
  ["text", "text"],
])("explains %p", (error, sentence) => {
  expect(explain(error)).toBe(sentence);
});
