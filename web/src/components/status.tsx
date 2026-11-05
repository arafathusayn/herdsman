import { type Member, statusLabel } from "../model.ts";

export function Pill(props: { status: Member["status"] }) {
  return (
    <span class={`pill s-${props.status}`}>
      <span class="dot" aria-hidden="true" />
      {statusLabel[props.status]}
    </span>
  );
}
