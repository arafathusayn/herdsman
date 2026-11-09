import { NotServed, PageFrame } from "../load.tsx";

export function TracesPage() {
  return (
    <PageFrame title="Traces" lead="Spans for each turn, command, check and review of a route.">
      <NotServed what="Trace reading" terminal="herdsman trace <route folder> --json" />
    </PageFrame>
  );
}
