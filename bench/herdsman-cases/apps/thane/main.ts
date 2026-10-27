// Entry: mounts the thane component into the route body and writes each
// snapshot into its store signals in one batch.

import { batch, mount } from 'thane';
import { start } from '../../shared/driver.ts';
import { checklist, events, HerdsmanCases, log, members, selected, tab } from './app.ts';

start((root, onSelect) => {
  mount({ component: HerdsmanCases, target: root, props: { onSelect } });
  return {
    render(s) {
      batch(() => {
        members(s.members);
        selected(s.selected);
        events(s.events);
        checklist(s.checklist);
        log(s.log);
        tab(s.tab);
      });
    },
  };
});
