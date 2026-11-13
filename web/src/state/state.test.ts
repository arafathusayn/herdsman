import { expect, test } from "bun:test";

import { HerdsmanError } from "@herdsman/sdk";
import { createActor } from "@reactre/state";

import { pairingMachine } from "./pairing.machine.ts";
import { createPoll } from "./poll.ts";
import { registerRequest } from "./request-service.ts";
import { createRequest } from "./request.ts";
import { streamMachine } from "./stream.machine.ts";
import { valueState } from "./value.ts";
import { decodeValue, encodeValue } from "./wire.ts";

const settle = () => Bun.sleep(0);

test("request replacement aborts the old task and drops its late result", async () => {
  const pending: { signal: AbortSignal; finish: (value: string) => void }[] = [];
  const request = createRequest<number, string>(
    (_, signal) => new Promise((finish) => pending.push({ signal, finish })),
  );
  try {
    const first = request.run(1);
    const second = request.run(2);
    expect(pending[0]!.signal.aborted).toBe(true);
    expect(await first).toEqual({ status: "cancelled" });
    pending[1]!.finish("new");
    expect(await second).toEqual({ status: "done", value: "new" });
    pending[0]!.finish("old");
    await settle();
    expect(request.data()).toBe("new");
    expect(request.snapshot().state).toBe("ready");
  } finally {
    request.stop();
  }
});

test("request failure preserves SDK error semantics and can retry", async () => {
  let fail = true;
  const request = createRequest(async () => {
    if (fail) throw new HerdsmanError("unimplemented", "not served", { status: 501 });
    return { value: "ok" };
  });
  try {
    const result = await request.run(undefined);
    expect(result.status).toBe("failed");
    expect(request.error()).toBeInstanceOf(HerdsmanError);
    expect((request.error() as HerdsmanError).code).toBe("unimplemented");
    request.cancel();
    expect(request.snapshot().state).toBe("failed");
    fail = false;
    await request.run(undefined);
    expect(request.data()).toEqual({ value: "ok" });
    expect(request.error()).toBeUndefined();
    const data = request.data();
    await request.run(undefined);
    expect(request.data()).toBe(data);
  } finally {
    request.stop();
  }
});

test("stopping a request settles its caller and prevents later work", async () => {
  let signal!: AbortSignal;
  let finish!: (value: string) => void;
  const request = createRequest((_: void, current) => {
    signal = current;
    return new Promise<string>((resolve) => {
      finish = resolve;
    });
  });
  const result = request.run(undefined);
  request.stop();
  expect(signal.aborted).toBe(true);
  expect(await result).toEqual({ status: "cancelled" });
  finish("late");
  await settle();
  expect(request.data()).toBeUndefined();
  expect(await request.run(undefined)).toEqual({ status: "cancelled" });
});

test("poll wake coalesces during a read and stop cancels its work", async () => {
  const pending: { signal: AbortSignal; finish: (delay: number) => void }[] = [];
  const poll = createPoll((signal) => new Promise((finish) => pending.push({ signal, finish })));
  try {
    poll.start();
    poll.start();
    poll.wake();
    poll.wake();
    expect(pending.length).toBe(1);
    pending[0]!.finish(10000);
    await settle();
    expect(pending.length).toBe(2);
    poll.stop();
    expect(pending[1]!.signal.aborted).toBe(true);
    pending[1]!.finish(0);
    await settle();
    expect(pending.length).toBe(2);
    expect(poll.actor.snapshot().state).toBe("idle");
  } finally {
    poll.dispose();
  }
});

test("pairing guards invalid and duplicate submits and allows a corrected code", async () => {
  let calls = 0;
  const service = registerRequest(async () => {
    calls++;
    return encodeValue(
      calls === 1 ? { status: "pairing", error: "Wrong", left: 1 } : { status: "paired", device: "secret" },
    );
  });
  const actor = createActor(pairingMachine, { input: { service: service.id } });
  try {
    expect(actor.send({ type: "submit" }).ok).toBe(false);
    actor.send({ type: "edit", code: "123456" });
    expect(actor.send({ type: "submit" }).ok).toBe(true);
    expect(actor.send({ type: "submit" }).ok).toBe(false);
    await settle();
    expect(actor.snapshot().state).toBe("editing");
    actor.send({ type: "edit", code: "654321" });
    actor.send({ type: "submit" });
    await settle();
    expect(actor.snapshot()).toMatchObject({ state: "paired", data: { device: "secret" } });
    expect(calls).toBe(2);
  } finally {
    actor.stop();
    service.dispose();
  }
});

test("stream failure backs off, recovers, and close aborts the active read", async () => {
  let opens = 0;
  let reading: AbortSignal | undefined;
  const service = registerRequest(async (kind, signal) => {
    if (kind === "open") {
      if (++opens === 1) throw new Error("offline");
      return "ready";
    }
    reading = signal;
    return new Promise<string>((_, reject) =>
      signal.addEventListener("abort", () => reject(signal.reason), { once: true }),
    );
  });
  const actor = createActor(streamMachine, { input: { service: service.id } });
  try {
    await settle();
    expect(actor.snapshot()).toMatchObject({ state: "waiting", data: { delay: 500 } });
    await Bun.sleep(550);
    expect(actor.snapshot().state).toBe("streaming");
    expect(opens).toBe(2);
    actor.send({ type: "close" });
    expect(reading?.aborted).toBe(true);
    expect(actor.snapshot().state).toBe("closed");
  } finally {
    actor.stop();
    service.dispose();
  }
});

test("stream authorization refusal ends without scheduling a retry", async () => {
  const service = registerRequest(async () => "unauthorized");
  const actor = createActor(streamMachine, { input: { service: service.id } });
  try {
    await settle();
    expect(actor.snapshot().state).toBe("unauthorized");
  } finally {
    actor.stop();
    service.dispose();
  }
});

test("data boundary round trips SDK values and collections without reserved-key collisions", () => {
  const original = {
    missing: undefined,
    large: 12n,
    nested: { kind: "map", items: ["__proto__"] },
    map: new Map([["x", 3]]),
    set: new Set(["one"]),
  };
  expect(decodeValue(encodeValue(original))).toEqual(original);
  expect(() => encodeValue({ callback: () => {} })).toThrow("data only");
  expect(() => encodeValue(new Date())).toThrow("data only");
  expect(() => encodeValue({ value: Infinity })).toThrow("finite");
  const [read, write] = valueState({ value: 1 });
  write((old) => ({ value: old.value + 1 }));
  expect(read()).toEqual({ value: 2 });
});

test("read projections reject mutation, including nested collections", () => {
  const [read, write] = valueState({ nested: { count: 1 }, map: new Map([["a", 1]]), set: new Set([1]) });
  expect(() => {
    read().nested.count = 7;
  }).toThrow();
  expect(() => read().map.set("b", 2)).toThrow("actor event");
  expect(() => read().set.clear()).toThrow("actor event");
  write((old) => ({ ...old, nested: { count: 2 }, map: new Map([...old.map, ["b", 2]]) }));
  expect(read().nested.count).toBe(2);
  expect(read().map.size).toBe(2);
});
