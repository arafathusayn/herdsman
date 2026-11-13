import { expect, test } from "bun:test";

import { createActor } from "@reactre/state";

import { executionMachine } from "./execution.machine.ts";
import { registerRequest } from "./request-service.ts";
import { decodeValue } from "./wire.ts";

test("execution submits require confirmation and a line; pending commands reject duplicates", async () => {
  const calls: unknown[] = [];
  let finish!: (message: string) => void;
  const service = registerRequest((json) => {
    calls.push(JSON.parse(json));
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  const actor = createActor(executionMachine, { input: { service: service.id } });
  try {
    actor.send({ type: "edit", line: " go on " });
    expect(actor.send({ type: "submit" }).ok).toBe(false);
    actor.send({ type: "ask" });
    actor.send({ type: "edit", line: "  " });
    expect(actor.send({ type: "submit" }).ok).toBe(false);
    expect(calls).toEqual([]);
    actor.send({ type: "edit", line: " go on " });
    actor.send({ type: "submit" });
    expect(actor.snapshot().state).toBe("submitting");
    expect(actor.send({ type: "submit" }).ok).toBe(false);
    expect(actor.send({ type: "show" }).ok).toBe(false);
    await Bun.sleep(0);
    expect(calls).toEqual([{ kind: "submit", line: "go on" }]);
    finish("Typed submit s1: typed");
    await Bun.sleep(0);
    expect(actor.snapshot().state).toBe("idle");
    expect(actor.snapshot().data.line).toBe("");
    expect(actor.snapshot().data.done).toBe("Typed submit s1: typed");
  } finally {
    actor.stop();
    service.dispose();
  }
});

test("execution failure retains the confirmed draft for an explicit retry", async () => {
  let attempts = 0;
  const service = registerRequest(async () => {
    if (++attempts === 1) throw new Error("not ready");
    return "accepted";
  });
  const actor = createActor(executionMachine, { input: { service: service.id } });
  try {
    actor.send({ type: "ask" });
    actor.send({ type: "edit", line: "draft" });
    actor.send({ type: "submit" });
    await Bun.sleep(0);
    expect(actor.snapshot().state).toBe("confirming");
    expect(actor.snapshot().data.line).toBe("draft");
    expect(decodeValue<Error>(actor.snapshot().data.error).message).toBe("not ready");
    actor.send({ type: "submit" });
    await Bun.sleep(0);
    expect(attempts).toBe(2);
    expect(actor.snapshot().state).toBe("idle");
    expect(actor.snapshot().data.error).toBe("");
  } finally {
    actor.stop();
    service.dispose();
  }
});

for (const confirming of [false, true]) {
  test(`showing an execution returns to its prior form state (${confirming})`, async () => {
    const service = registerRequest(async () => "shown");
    const actor = createActor(executionMachine, { input: { service: service.id } });
    try {
      actor.send({ type: "edit", line: "draft" });
      if (confirming) actor.send({ type: "ask" });
      actor.send({ type: "show" });
      await Bun.sleep(0);
      expect(actor.snapshot().state).toBe(confirming ? "confirming" : "idle");
      expect(actor.snapshot().data.line).toBe("draft");
      expect(actor.snapshot().data.done).toBe("shown");
      if (confirming) {
        actor.send({ type: "cancel" });
        expect(actor.snapshot().state).toBe("idle");
        expect(actor.snapshot().data.line).toBe("draft");
      }
    } finally {
      actor.stop();
      service.dispose();
    }
  });
}

for (const command of ["show", "submit"] as const) {
  test(`stop waiting cancels ${command}, keeps the draft and ignores late completion`, async () => {
    let signal!: AbortSignal;
    let finish!: (message: string) => void;
    const service = registerRequest((_, current) => {
      signal = current;
      return new Promise((resolve) => {
        finish = resolve;
      });
    });
    const actor = createActor(executionMachine, { input: { service: service.id } });
    try {
      actor.send({ type: "ask" });
      actor.send({ type: "edit", line: "draft" });
      actor.send({ type: command });
      await Bun.sleep(0);
      actor.send({ type: "cancel" });
      expect(signal.aborted).toBe(true);
      expect(actor.snapshot().state).toBe("confirming");
      expect(actor.snapshot().data.line).toBe("draft");
      const cancelled = actor.snapshot();
      finish("late response");
      await Bun.sleep(0);
      expect(actor.snapshot()).toEqual(cancelled);
      expect(actor.snapshot().data.done).toContain("Stopped waiting");
    } finally {
      actor.stop();
      service.dispose();
    }
  });

  test(`disposing an execution aborts ${command} and drops a late result`, async () => {
    let signal!: AbortSignal;
    let finish!: (message: string) => void;
    const service = registerRequest((_, current) => {
      signal = current;
      return new Promise((resolve) => {
        finish = resolve;
      });
    });
    const actor = createActor(executionMachine, { input: { service: service.id } });
    try {
      actor.send({ type: "ask" });
      actor.send({ type: "edit", line: "draft" });
      actor.send({ type: command });
      await Bun.sleep(0);
      const before = actor.snapshot();
      actor.stop();
      expect(signal.aborted).toBe(true);
      finish("late response");
      await Bun.sleep(0);
      expect(actor.snapshot()).toEqual(before);
      expect(actor.send({ type: command }).ok).toBe(false);
    } finally {
      actor.stop();
      service.dispose();
    }
  });
}
