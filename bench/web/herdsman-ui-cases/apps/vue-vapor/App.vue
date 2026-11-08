<script setup vapor lang="ts">
// The four herdsman route tabs in Vapor mode, compiled by @vue/compiler-sfc's
// Vapor compiler: no virtual DOM, each binding is its own render effect that
// writes the DOM only when its value changed. Each click replaces `s` with the
// fresh snapshot; v-for keys come from the contract. Vapor has no v-memo (the
// compiler ignores it): per-binding effects already skip unchanged values.
import { shallowRef } from "vue";
import { checkKey, eventKey, lanes, lineKey, type State, statusLabel } from "../../shared/state.ts";

const props = defineProps<{ onSelect: (pane: string) => void }>();
const s = shallowRef<State | null>(null);

defineExpose({
  render(next: State) {
    s.value = next;
  },
});
</script>

<template>
  <template v-if="s">
    <div v-if="s.tab === 'team'" class="team"><div class="kanban">
      <section v-for="lane in lanes(s.members)" :key="lane.status" :class="'lane s-' + lane.status" :aria-label="lane.title">
        <h2><span class="lane-dot" aria-hidden="true"></span>{{ lane.title }}<span class="count">{{ lane.members.length }}</span></h2>
        <template v-if="lane.members.length">
          <button
            v-for="m in lane.members"
            :key="m.pane"
            :class="'card r-' + m.role + ' s-' + m.status + (s.selected === m.pane ? ' selected' : '')"
            :title="'Open ' + m.title + ' (' + m.pane + ')'"
            :data-pane="m.pane"
            @click="props.onSelect(m.pane)"
          >
            <div class="card-top"><span class="card-title">{{ m.title }}</span><span :class="'pill s-' + m.status"><span class="dot" aria-hidden="true"></span>{{ statusLabel[m.status] }}</span></div>
            <div v-if="m.task !== null" class="card-task">{{ m.task }}</div>
            <div class="card-sub">{{ m.sub }}</div>
            <div class="card-foot"><code>{{ m.pane }}</code><span class="muted">{{ m.tab }}</span><span class="muted clip" :title="m.term">{{ m.term }}</span></div>
          </button>
        </template>
        <p v-else class="lane-empty">None</p>
      </section>
    </div></div>
    <ol v-else-if="s.tab === 'activity'" class="timeline activity">
      <li
        v-for="e in s.events"
        :key="eventKey(e)"
        :class="e.source"
      >
        <time :datetime="e.at">{{ e.clock }}</time>
        <p v-if="e.source === 'web'" class="event"><span class="chip accent">web</span><span>{{ e.text ?? "" }}</span></p>
        <p v-else-if="e.status" class="event"><strong v-if="e.who !== null">{{ e.who }}</strong><span :class="'pill s-' + e.status"><span class="dot" aria-hidden="true"></span>{{ statusLabel[e.status] }}</span></p>
        <p v-else class="event"><span class="chip">{{ e.label }}</span><strong v-if="e.who !== null">{{ e.who }}</strong></p>
      </li>
    </ol>
    <ul v-else-if="s.tab === 'checklist'" class="checklist">
      <li v-for="c in s.checklist" :key="checkKey(c)" :class="c.done ? 'done' : c.skipped ? 'skipped' : ''">
        <span class="box" :aria-label="c.done ? 'Done' : c.skipped ? 'Not applicable' : 'Open'">{{ c.done ? "✓" : c.skipped ? "–" : "" }}</span>
        <code v-if="c.id">{{ c.id }}</code>
        <span>{{ c.text }}</span>
      </li>
    </ul>
    <ol v-else class="timeline">
      <li v-for="l in s.log" :key="lineKey(l)" :class="l.text.startsWith('web:') ? 'web' : ''">
        <time>{{ l.time }}</time>
        <p>{{ l.text }}</p>
      </li>
    </ol>
  </template>
</template>
