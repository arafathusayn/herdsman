// The paired user's own clicks and keys tell the server the user is here
// (A33), at most once per interval: herdsman afk then does not decide a
// question for someone who is looking at the app. Timers never call it.

/** A function to call on each user event; it sends at most once per `every` ms. */
export function activityPinger(send: () => void, every: number, now: () => number = Date.now): () => void {
  let last = -Infinity;
  return () => {
    const t = now();
    if (t - last < every) return;
    last = t;
    send();
  };
}
