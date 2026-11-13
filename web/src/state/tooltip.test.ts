import { expect, test } from "bun:test";

import { createActor } from "@reactre/state";

import { tooltipMachine } from "./tooltip.machine.ts";

test("tooltip hides before its delay without later showing or warming", async () => {
  const actor = createActor(tooltipMachine);
  try {
    actor.send({ type: "arm", now: 100, delay: 20 });
    expect(actor.snapshot()).toMatchObject({ state: "waiting", data: { delay: 20 } });
    actor.send({ type: "hide", now: 110 });
    await Bun.sleep(40);
    expect(actor.snapshot()).toMatchObject({ state: "idle", data: { warmUntil: 0 } });
  } finally {
    actor.stop();
  }
});

test("rearming a pending tooltip replaces its timer", async () => {
  const actor = createActor(tooltipMachine);
  try {
    actor.send({ type: "arm", now: 100, delay: 20 });
    actor.send({ type: "arm", now: 110, delay: 10_000 });
    await Bun.sleep(40);
    expect(actor.snapshot()).toMatchObject({ state: "waiting", data: { delay: 10_000 } });
  } finally {
    actor.stop();
  }
});

test("only a shown tooltip starts the bounded warm reuse window", async () => {
  const actor = createActor(tooltipMachine);
  try {
    actor.send({ type: "arm", now: 100, delay: 0 });
    await Bun.sleep(20);
    expect(actor.snapshot().state).toBe("placing");
    actor.send({ type: "placed" });
    expect(actor.snapshot().state).toBe("showing");
    actor.send({ type: "hide", now: 200 });
    actor.send({ type: "arm", now: 799, delay: 450 });
    expect(actor.snapshot()).toMatchObject({ state: "waiting", data: { delay: 0 } });
    actor.send({ type: "hide", now: 799 });
    actor.send({ type: "arm", now: 800, delay: 450 });
    expect(actor.snapshot()).toMatchObject({ state: "waiting", data: { delay: 450 } });
  } finally {
    actor.stop();
  }
});

test("a missing DOM target cancels placement without warming or accepting a late placement", async () => {
  const actor = createActor(tooltipMachine);
  try {
    actor.send({ type: "arm", now: 100, delay: 0 });
    await Bun.sleep(20);
    expect(actor.snapshot().state).toBe("placing");
    actor.send({ type: "hide", now: 200 });
    expect(actor.snapshot()).toMatchObject({ state: "idle", data: { warmUntil: 0 } });
    expect(actor.send({ type: "placed" }).ok).toBe(false);
  } finally {
    actor.stop();
  }
});

test("closing a pending tooltip prevents later display and rearming", async () => {
  const actor = createActor(tooltipMachine);
  actor.send({ type: "arm", now: 100, delay: 20 });
  actor.send({ type: "close" });
  await Bun.sleep(40);
  expect(actor.snapshot().state).toBe("closed");
  expect(actor.send({ type: "arm", now: 200, delay: 0 }).ok).toBe(false);
  actor.stop();
});
