// thane, #2 on the js-framework-benchmark keyed table, as an expert would write
// herdsman's route tabs with it: one component whose template the thane
// compiler turns into cloned static markup plus direct signal subscriptions,
// whenElse() for the tab switch and the event body shapes, when() for optional
// elements, and repeat() with a key for every list.
//
// The store is module-level signals (thane's pattern for shared state). Each
// snapshot is written into them as is: repeat() matches rows by key and updates
// a kept row in place from the new item, its compiled guards skipping the
// writes whose values did not change.

import { computed, defineComponent, html, repeat, signal, when, whenElse } from 'thane';
import {
  type Check,
  checkKey,
  type Event,
  eventKey,
  lanes,
  type Line,
  lineKey,
  type Member,
  type Status,
  statusLabel,
  type Tab,
} from '../../shared/state.ts';

export const tab = signal<Tab>('team');
export const selected = signal<string | null>(null);
export const members = signal<Member[]>([]);
export const events = signal<Event[]>([]);
export const checklist = signal<Check[]>([]);
export const log = signal<Line[]>([]);

type Props = { onSelect: (pane: string) => void };

const label = (s: Status) => statusLabel[s];
const checkClass = (c: Check) => (c.done ? 'done' : c.skipped ? 'skipped' : '');
const checkLabel = (c: Check) => (c.done ? 'Done' : c.skipped ? 'Not applicable' : 'Open');
const checkBox = (c: Check) => (c.done ? '✓' : c.skipped ? '–' : '');
const lineClass = (l: Line) => (l.text.startsWith('web:') ? 'web' : '');

export const HerdsmanCases = defineComponent<Props>('herdsman-cases', ({ props }) => {
  const laneList = computed(() => lanes(members()));
  const select = (pane: string) => props.onSelect(pane);

  return {
    template: html`
      ${whenElse(
        tab() === 'team',
        html`<div class="team"><div class="kanban">${repeat(
          laneList(),
          (lane) => html`<section class="lane s-${lane.status}" aria-label=${lane.title}><h2><span class="lane-dot" aria-hidden="true"></span>${lane.title}<span class="count">${lane.members.length}</span></h2>${repeat(
            lane.members,
            (m) => html`<button class="card r-${m.role} s-${m.status}${selected() === m.pane ? ' selected' : ''}" title="Open ${m.title} (${m.pane})" data-pane=${m.pane} @click=${() => select(m.pane)}><div class="card-top"><span class="card-title">${m.title}</span><span class="pill s-${m.status}"><span class="dot" aria-hidden="true"></span>${label(m.status)}</span></div><div class="card-task" ${when(m.task !== null)}>${m.task}</div><div class="card-sub">${m.sub}</div><div class="card-foot"><code>${m.pane}</code><span class="muted">${m.tab}</span><span class="muted clip" title=${m.term}>${m.term}</span></div></button>`,
            html`<p class="lane-empty">None</p>`,
            (m) => m.pane,
          )}</section>`,
          null,
          (lane) => lane.status,
        )}</div></div>`,
        html`${whenElse(
          tab() === 'activity',
          html`<ol class="timeline activity">${repeat(
            events(),
            (e) => html`<li class=${e.source}><time datetime=${e.at}>${e.clock}</time><p class="event">${whenElse(
              e.source === 'web',
              html`<span class="chip accent">web</span><span>${e.text ?? ''}</span>`,
              html`${whenElse(
                e.status !== null,
                html`<strong ${when(e.who !== null)}>${e.who}</strong><span class="pill s-${e.status}"><span class="dot" aria-hidden="true"></span>${e.status === null ? '' : label(e.status)}</span>`,
                html`<span class="chip">${e.label}</span><strong ${when(e.who !== null)}>${e.who}</strong>`,
              )}`,
            )}</p></li>`,
            null,
            (e) => eventKey(e),
          )}</ol>`,
          html`${whenElse(
            tab() === 'checklist',
            html`<ul class="checklist">${repeat(
              checklist(),
              (c) => html`<li class=${checkClass(c)}><span class="box" aria-label=${checkLabel(c)}>${checkBox(c)}</span><code ${when(c.id !== '')}>${c.id}</code><span>${c.text}</span></li>`,
              null,
              (c) => checkKey(c),
            )}</ul>`,
            html`<ol class="timeline">${repeat(
              log(),
              (l) => html`<li class=${lineClass(l)}><time>${l.time}</time><p>${l.text}</p></li>`,
              null,
              (l) => lineKey(l),
            )}</ol>`,
          )}`,
        )}`,
      )}
    `,
  };
});
