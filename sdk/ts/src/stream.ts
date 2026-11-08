// An async iterator over herdsman's WebSocket stream, as JSON text frames.

import { HerdsmanError } from "./client";

export const JSON_SUBPROTOCOL = "herdsman.json";

const STREAM_PATH = "/rpc/herdsman.daemon.v1.EventService/Stream";
const DEFAULT_MAX_QUEUED = 1024;
const CLOSE_NORMAL = 1000;
const CLOSE_INVALID_DATA = 1007;
const CLOSE_POLICY = 1008;

/** The part of the standard WebSocket this module uses. */
export interface WebSocketLike {
  readonly protocol: string;
  onopen: ((event: never) => void) | null;
  onmessage: ((event: { data: unknown }) => void) | null;
  onerror: ((event: never) => void) | null;
  onclose: ((event: { code: number; reason: string }) => void) | null;
  send(data: string): void;
  close(code?: number): void;
}

export interface StreamOptions {
  /** The server's origin and any path before `/rpc`; http maps to ws. */
  baseUrl: string;
  /** Replaces the global WebSocket. */
  webSocket?: (url: string, protocols: string[]) => WebSocketLike;
  signal?: AbortSignal;
  /** Unread frames to hold before the stream fails; default 1024. */
  maxQueued?: number;
}

/** One connection: send frames, and `for await` the ones that arrive. */
export interface FrameStream<Out, In> extends AsyncIterableIterator<In> {
  send(frame: Out): void;
  close(): void;
}

type Pending<In> = { resolve: (result: IteratorResult<In>) => void; reject: (error: unknown) => void };

const finished = { done: true, value: undefined } as const;

/** Opens the stream. The first frames may be sent before the socket opens. */
export function openStream<Out, In>(options: StreamOptions): FrameStream<Out, In> {
  const url = options.baseUrl.replace(/\/+$/, "").replace(/^http/i, "ws") + STREAM_PATH;
  const maxQueued = options.maxQueued ?? DEFAULT_MAX_QUEUED;
  const dial = options.webSocket ?? ((target, protocols) => new WebSocket(target, protocols) as unknown as WebSocketLike);

  const frames: In[] = [];
  const unsent: string[] = [];
  let reader: Pending<In> | undefined;
  let opened = false;
  let ended = false;
  let failure: HerdsmanError | undefined;

  const socket = dial(url, [JSON_SUBPROTOCOL]);

  // After this, no event changes the stream. Queued frames stay readable.
  const finish = (error?: HerdsmanError, closeCode?: number) => {
    if (ended) return;
    ended = true;
    failure = error;
    options.signal?.removeEventListener("abort", onAbort);
    if (closeCode !== undefined) socket.close(closeCode);
    if (reader !== undefined) {
      const waiting = reader;
      reader = undefined;
      if (error === undefined) waiting.resolve(finished);
      else waiting.reject(error);
    }
  };

  const onAbort = () => finish(new HerdsmanError("canceled", "the stream was canceled"), CLOSE_NORMAL);

  socket.onopen = () => {
    if (ended) return;
    if (socket.protocol !== JSON_SUBPROTOCOL) {
      finish(new HerdsmanError("unavailable", `the server chose subprotocol "${socket.protocol}"`), CLOSE_POLICY);
      return;
    }
    opened = true;
    for (const data of unsent.splice(0)) socket.send(data);
  };

  socket.onmessage = ({ data }) => {
    if (ended) return;
    let frame: In;
    try {
      if (typeof data !== "string") throw new TypeError("binary frame");
      frame = JSON.parse(data) as In;
    } catch {
      finish(new HerdsmanError("internal", "the server sent a frame that is not JSON text"), CLOSE_INVALID_DATA);
      return;
    }
    if (reader !== undefined) {
      const waiting = reader;
      reader = undefined;
      waiting.resolve({ done: false, value: frame });
      return;
    }
    if (frames.length >= maxQueued) {
      finish(new HerdsmanError("resource_exhausted", `more than ${maxQueued} frames unread`), CLOSE_POLICY);
      return;
    }
    frames.push(frame);
  };

  socket.onerror = () => finish(new HerdsmanError("unavailable", "the connection failed"));

  socket.onclose = ({ code, reason }) => {
    finish(code === CLOSE_NORMAL ? undefined : new HerdsmanError("unavailable", `the connection closed: ${code} ${reason}`));
  };

  if (options.signal?.aborted) onAbort();
  else options.signal?.addEventListener("abort", onAbort, { once: true });

  const close = () => finish(undefined, CLOSE_NORMAL);

  return {
    send(frame) {
      if (ended) throw failure ?? new HerdsmanError("failed_precondition", "the stream is closed");
      const data = JSON.stringify(frame);
      if (opened) socket.send(data);
      else unsent.push(data);
    },
    close,
    next() {
      const frame = frames.shift();
      if (frame !== undefined) return Promise.resolve({ done: false, value: frame });
      if (ended) return failure === undefined ? Promise.resolve(finished) : Promise.reject(failure);
      return new Promise((resolve, reject) => {
        reader = { resolve, reject };
      });
    },
    return() {
      close();
      frames.length = 0;
      return Promise.resolve(finished);
    },
    [Symbol.asyncIterator]() {
      return this;
    },
  };
}
