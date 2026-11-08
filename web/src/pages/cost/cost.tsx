import { NotServed, PageFrame } from "../load.tsx";

export function CostPage() {
  return (
    <PageFrame title="Cost" lead="Measured tokens and cost for each task.">
      <NotServed what="Cost reporting" terminal="herdsman cost" />
    </PageFrame>
  );
}
