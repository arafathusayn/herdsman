<script setup lang="ts">
// The four herdsman route tabs, compiled by @vue/compiler-sfc (static hoisting,
// patch flags). Each click replaces `s` with the fresh snapshot; v-for keys come
// from the contract and v-memo lists every field an item renders, so an item
// whose values did not change skips its re-render, as the official keyed entry does.
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
    <div v-if="s.tab === 'team'" key="team" class="team"><div class="kanban">
      <section v-for="lane in lanes(s.members)" :key="lane.status" :class="'lane s-' + lane.status" :aria-label="lane.title">
        <h2><span class="lane-dot" aria-hidden="true"></span>{{ lane.title }}<span class="count">{{ lane.members.length }}</span></h2>
        <template v-if="lane.members.length">
          <button
            v-for="m in lane.members"
            :key="m.pane"
            v-memo="[m.title, m.role, m.status, m.task, m.sub, m.tab, m.term, s.selected === m.pane]"
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
    <ol v-else-if="s.tab === 'activity'" key="activity" class="timeline activity">
      <li
        v-for="e in s.events"
        :key="eventKey(e)"
        v-memo="[e.clock, e.source, e.label, e.who, e.status, e.text]"
        :class="e.source"
      >
        <time :datetime="e.at">{{ e.clock }}</time>
        <p v-if="e.source === 'web'" class="event"><span class="chip accent">web</span><span>{{ e.text ?? "" }}</span></p>
        <p v-else-if="e.status" class="event"><strong v-if="e.who !== null">{{ e.who }}</strong><span :class="'pill s-' + e.status"><span class="dot" aria-hidden="true"></span>{{ statusLabel[e.status] }}</span></p>
        <p v-else class="event"><span class="chip">{{ e.label }}</span><strong v-if="e.who !== null">{{ e.who }}</strong></p>
      </li>
    </ol>
    <ul v-else-if="s.tab === 'checklist'" key="checklist" class="checklist">
      <li v-for="c in s.checklist" :key="checkKey(c)" v-memo="[c.id, c.text, c.done, c.skipped]" :class="c.done ? 'done' : c.skipped ? 'skipped' : ''">
        <span class="box" :aria-label="c.done ? 'Done' : c.skipped ? 'Not applicable' : 'Open'">{{ c.done ? "✓" : c.skipped ? "–" : "" }}</span>
        <code v-if="c.id">{{ c.id }}</code>
        <span>{{ c.text }}</span>
      </li>
    </ul>
    <ol v-else key="log" class="timeline">
      <li v-for="l in s.log" :key="lineKey(l)" v-memo="[l.time, l.text]" :class="l.text.startsWith('web:') ? 'web' : ''">
        <time>{{ l.time }}</time>
        <p>{{ l.text }}</p>
      </li>
    </ol>
  </template>
</template>
