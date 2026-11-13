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

test("benchmark rows reconcile after empty mounting, equal snapshots, reordering and removal", () => {
  createRoot((dispose) => {
    const [read, write] = valueState<{ id: string; label: string }[]>([]);
    const projected = rows(read, (row) => row.id);
    const empty = projected();
    write([]);
    expect(projected()).toBe(empty);
    write([
      { id: "a", label: "A" },
      { id: "b", label: "B" },
    ]);
    const first = projected();
    write(first.map((row) => ({ ...row })));
    expect(projected()).toBe(first);
    write([
      { id: "b", label: "Changed" },
      { id: "a", label: "A" },
    ]);
    expect(projected()[1]).toBe(first[0]);
    expect(projected()[0]).not.toBe(first[1]);
    expect(projected()[0]?.label).toBe("Changed");
    write([]);
    expect(projected()).toHaveLength(0);
    write([{ id: "a", label: "A" }]);
    expect(projected()[0]).not.toBe(first[0]);
    expect(projected()[0]).toEqual(first[0]);
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
      const retained = card();
      member.role = "reviewer";
      member.sub = "Updated subtitle";
      member.tab = "Updated terminal tab";
      member.term = "Updated terminal title";
      app.render(world.snapshot());
      expect(card()).toBe(retained);
      expect(card().className).toContain("r-reviewer");
      expect(card().textContent).toContain("Updated subtitle");
      expect(card().textContent).toContain("Updated terminal tab");
      expect(card().textContent).toContain("Updated terminal title");
      expect(elements().find((el) => el.getAttribute("title") === member.term)).toBeDefined();
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

  test(`benchmark ${name} updates empty lanes and disposes removed optional lanes`, () => {
    const root = document.createElement("main");
    const app = createApp(root, () => {});
    const world = new World();
    const member = { ...world.s.members[0]!, status: "gone" as const };
    const elements = () => all(root as unknown as FakeElement);
    const sections = () => elements().filter((el) => el.getAttribute("aria-label") === "Pane gone");
    try {
      world.s.members = [];
      app.render(world.snapshot());
      expect(elements().filter((el) => el.className === "lane-empty")).toHaveLength(4);
      world.s.members = [member];
      app.render(world.snapshot());
      expect(sections()).toHaveLength(1);
      const lane = sections()[0];
      member.title = "Changed in the same lane";
      app.render(world.snapshot());
      expect(sections()[0]).toBe(lane);
      expect(root.textContent).toContain(member.title);
      world.s.members = [];
      app.render(world.snapshot());
      expect(sections()).toHaveLength(0);
      expect(elements().filter((el) => el.className === "lane-empty")).toHaveLength(4);
    } finally {
      app.dispose();
    }
  });

  test(`benchmark ${name} keeps checklist fields live and reloads a disposed tab`, () => {
    const root = document.createElement("main");
    const app = createApp(root, () => {});
    const world = new World();
    world.s.tab = "checklist";
    world.s.checklist = [{ id: "C1", text: "Original", done: false, skipped: false }];
    const boxes = () => all(root as unknown as FakeElement).filter((el) => el.className === "box");
    try {
      app.render(world.snapshot());
      const box = boxes()[0];
      world.s.checklist[0]!.text = "Changed with the same key";
      world.s.checklist[0]!.skipped = true;
      app.render(world.snapshot());
      expect(boxes()[0]).toBe(box);
      expect(boxes()[0]?.getAttribute("aria-label")).toBe("Not applicable");
      expect(root.textContent).toContain("Changed with the same key");
      world.s.checklist[0]!.done = true;
      app.render(world.snapshot());
      expect(boxes()[0]?.getAttribute("aria-label")).toBe("Done");
      world.s.tab = "team";
      app.render(world.snapshot());
      world.s.checklist = [{ id: "", text: "Changed while hidden", done: false, skipped: false }];
      app.render(world.snapshot());
      world.s.tab = "checklist";
      app.render(world.snapshot());
      expect(boxes()[0]).not.toBe(box);
      expect(boxes()[0]?.getAttribute("aria-label")).toBe("Open");
      expect(root.textContent).toContain("Changed while hidden");
      expect(root.textContent).not.toContain("C1");
    } finally {
      app.dispose();
    }
  });

  test(`benchmark ${name} log prefix classes handle short strings and preserve keyed lines`, () => {
    const root = document.createElement("main");
    const app = createApp(root, () => {});
    const world = new World();
    world.s.tab = "log";
    const texts = ["", "w", "we", "web", "web:", "web: hello", "xweb:", "Web:", "webx", "🙂"];
    world.s.log = texts.map((text, i) => ({ time: String(i), text }));
    const items = () => all(root as unknown as FakeElement).filter((el) => el.tagName === "LI");
    try {
      app.render(world.snapshot());
      const original = items();
      expect(original.map((el) => el.className)).toEqual(texts.map((text) => (text.startsWith("web:") ? "web" : "")));
      world.s.log.reverse();
      app.render(world.snapshot());
      expect(items()).toEqual([...original].reverse());
    } finally {
      app.dispose();
    }
    app.render(world.snapshot());
    expect(root.textContent).toBe("");
  });

  test(`benchmark ${name} event bodies update every shape and changed fields with the same event key`, () => {
    const root = document.createElement("main");
    const app = createApp(root, () => {});
    const world = new World();
    world.s.tab = "activity";
    const event = {
      ...world.s.events[0]!,
      source: "herdr" as const,
      status: null,
      who: "Before",
      label: "Label",
    };
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
