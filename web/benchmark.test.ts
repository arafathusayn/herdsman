import { expect, test } from "bun:test";

import { createRoot } from "@reactre/core/features/owner";
import { useActor } from "@reactre/state/signals";

import { rows } from "../bench/web/herdsman-ui-cases/apps/reactre-state/rows.ts";
import { viewMachine } from "../bench/web/herdsman-ui-cases/apps/reactre-state/view.machine.ts";
import { eventKey, World } from "../bench/web/herdsman-ui-cases/shared/state.ts";

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
