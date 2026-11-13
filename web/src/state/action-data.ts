"use pure";

/** Narrow the service result before it enters a machine event. */
export function actionReply(json: string): { type: "completed"; status: string; message: string } {
  const value: unknown = JSON.parse(json);
  if (
    typeof value !== "object" ||
    value === null ||
    !("status" in value) ||
    !("message" in value) ||
    typeof value.status !== "string" ||
    typeof value.message !== "string"
  )
    throw new Error("Invalid action reply");
  return { type: "completed", status: value.status, message: value.message };
}
export function confirmReply(value: string): { type: "confirmed" } | { type: "declined" } {
  return value === "true" ? { type: "confirmed" } : { type: "declined" };
}
