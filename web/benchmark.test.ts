import { beforeEach, expect, test } from "bun:test";

import { createRoot } from "@reactre/core/features/owner";
import { useActor } from "@reactre/state/signals";

import { createApp as createSignalApp } from "../bench/web/herdsman-ui-cases/apps/reactre-signals/app.tsx";
import { createApp as createActorApp } from "../bench/web/herdsman-ui-cases/apps/reactre-state/app.tsx";
import { viewMachine } from "../bench/web/herdsman-ui-cases/apps/reactre-state/view.machine.ts";
import { rows } from "../bench/web/herdsman-ui-cases/shared/reactre/rows.ts";
import { eventKey, World } from "../bench/web/herdsman-ui-cases/shared/state.ts";
import { valueState } from "./src/state/value.ts";
import { all, type FakeElement, installDom } from "./src/testdom.ts";

beforeEach(installDom);

test("benchmark row equality distinguishes absent fields from explicit undefined", () => {
  createRoot((dispose) => {
    const [read, write] = valueState<{ pane: string; a?: string | undefined; b?: string | undefined }[]>([
      { pane: "p", a: undefined },
    ]);
    const projected = rows(read, (row) => row.pane);
    const first = projected()[0];
    write([{ pane: "p", b: undefined }]);
    expect(projected()[0]).not.toBe(first);
    expect(Object.hasOwn(projected()[0]!, "b")).toBe(true);
    expect(Object.hasOwn(projected()[0]!, "a")).toBe(false);
    const second = projected()[0];
    write([{ pane: "p" }]);
    expect(projected()[0]).not.toBe(second);
    expect(Object.hasOwn(projected()[0]!, "b")).toBe(false);
    dispose();
  });
});

for (const [name, createApp] of [
  ["actor", createActorApp],
  ["signal", createSignalApp],
] as const) {
  test(`benchmark cards update non-key fields, optional bodies and selection through ${name}`, () => {
    const root = document.createElement("main");
    const elements = () => all(root as unknown as FakeElement);
    let chosen = "";
    const app = createApp(root, (pane) => {
      chosen = pane;
    });
    const world = new World();
    try {
      app.render(world.snapshot());
      const member = world.s.members[0]!;
      member.title = "Changed title";
      member.task = "A newly present task";
      app.render(world.snapshot());
      const card = () => elements().find((el) => el.getAttribute("data-pane") === member.pane)!;
      expect(card().textContent).toContain("Changed title");
      expect(card().textContent).toContain("A newly present task");
      member.task = null;
      world.s.selected = member.pane;
      app.render(world.snapshot());
      expect(card().textContent).not.toContain("A newly present task");
      expect(card().className).toContain("selected");
      (card() as FakeElement).dispatch("click");
      expect(chosen).toBe(member.pane);
    } finally {
      app.dispose();
    }
    expect(root.textContent).toBe("");
    app.render(world.snapshot());
    expect(root.textContent).toBe("");
  });

  test(`benchmark ${name} event bodies update every shape and changed fields with the same event key`, () => {
    const root = document.createElement("main");
    const app = createApp(root, () => {});
    const world = new World();
    world.s.tab = "activity";
    const event = { ...world.s.events[0]!, source: "herdr" as const, status: null, who: "Before", label: "Label" };
    world.s.events = [event];
    try {
      app.render(world.snapshot());
      const key = eventKey(event);
      event.who = "<after>";
      event.label = "Changed label";
      event.clock = "Changed clock";
      app.render(world.snapshot());
      expect(eventKey(event)).toBe(key);
      expect(root.textContent).toContain("Changed clockChanged label<after>");
      expect(root.textContent).not.toContain("Before");

      world.s.events = [{ ...event, status: "working", who: "Worker" }];
      app.render(world.snapshot());
      expect(root.textContent).toContain("WorkerWorking");
      world.s.events = [{ ...event, status: "blocked", who: null }];
      app.render(world.snapshot());
      expect(root.textContent).toContain("Needs input");
      expect(root.textContent).not.toContain("Worker");
      world.s.events = [{ ...event, who: null }];
      app.render(world.snapshot());
      expect(root.textContent).toContain("Changed label");
      expect(root.textContent).not.toContain("<after>");
      world.s.events = [{ ...event, source: "web", who: null, text: "Web message" }];
      app.render(world.snapshot());
      expect(root.textContent).toContain("webWeb message");
      expect(root.textContent).not.toContain("Changed label");
    } finally {
      app.dispose();
    }
  });
}

test("benchmark projections reconcile every keyed row from the state actor", () => {
  createRoot((dispose) => {
    const [snapshot, actor] = useActor(viewMachine);
    const members = rows(
      () => {
        const s = snapshot();
        return s.state === "showing" ? s.data.value.members : [];
      },
      (m) => m.pane,
    );
    const events = rows(() => {
      const s = snapshot();
      return s.state === "showing" ? s.data.value.events : [];
    }, eventKey);
    const world = new World();
    actor.send({ type: "render", next: world.snapshot() });
    const original = members();
    actor.send({ type: "render", next: world.snapshot() });
    expect(members()).toBe(original);

    world.s.members[1]!.title = "Changed field with the same key";
    actor.send({ type: "render", next: world.snapshot() });
    expect(members()[0]).toBe(original[0]);
    expect(members()[1]).not.toBe(original[1]);
    expect(members()[1]!.title).toBe(world.s.members[1]!.title);

    const event = events()[0]!;
    world.s.events[0]!.who = "Renamed without changing the event key";
    actor.send({ type: "render", next: world.snapshot() });
    expect(eventKey(events()[0]!)).toBe(eventKey(event));
    expect(events()[0]).not.toBe(event);
    expect(events()[0]!.who).toBe(world.s.events[0]!.who);

    const removed = members()[0]!;
    const member = world.s.members.shift()!;
    actor.send({ type: "render", next: world.snapshot() });
    expect(members().some((m) => m.pane === removed.pane)).toBe(false);
    world.s.members.push(member);
    actor.send({ type: "render", next: world.snapshot() });
    expect(members().at(-1)).not.toBe(removed);
    expect(members().at(-1)).toEqual(removed);
    dispose();
  });
});
