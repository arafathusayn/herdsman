"use pure";
export function startupReply(json: string): { type: "settled"; status: string; context: string } {
  const reply: unknown = JSON.parse(json);
  if (
    typeof reply !== "object" ||
    reply === null ||
    !("status" in reply) ||
    !("context" in reply) ||
    typeof reply.status !== "string" ||
    typeof reply.context !== "string"
  )
    throw new Error("Invalid startup result");
  return { type: "settled", status: reply.status, context: reply.context };
}
