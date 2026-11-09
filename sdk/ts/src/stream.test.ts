import { afterEach, describe, expect, test } from "bun:test";
import { HerdsmanError } from "./client";
import { JSON_SUBPROTOCOL, openStream, type WebSocketLike } from "./stream";

// A socket the test drives by hand.
class FakeSocket implements WebSocketLike {
  protocol = JSON_SUBPROTOCOL;
  sent: string[] = [];
  closed: number[] = [];
  onopen: ((event: unknown) => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  url = "";
  protocols: string[] = [];
  send(data: string) {
    this.sent.push(data);
  }
  close(code?: number) {
    this.closed.push(code ?? 1000);
  }
  open() {
    this.onopen?.({});
  }
  frame(data: unknown) {
    this.onmessage?.({ data });
  }
  end(code = 1000, reason = "") {
    this.onclose?.({ code, reason });
  }
}

function start(options: { maxQueued?: number; signal?: AbortSignal; session?: string } = {}) {
  const socket = new FakeSocket();
  const stream = openStream<{ out: number }, { n: number }>({
    baseUrl: "https://daemon/base/",
    webSocket: (url, protocols) => {
      socket.url = url;
      socket.protocols = protocols;
      return socket;
    },
    ...options,
  });
  return { socket, stream };
}

async function failure(next: Promise<unknown>): Promise<HerdsmanError> {
  try {
    await next;
  } catch (error) {
    expect(error).toBeInstanceOf(HerdsmanError);
    return error as HerdsmanError;
  }
  throw new Error("no failure");
}

describe("openStream", () => {
  test("dials the stream path over ws with the JSON subprotocol", () => {
    const { socket } = start();
    expect(socket.url).toBe("wss://daemon/base/rpc/herdsman.daemon.v1.EventService/Stream");
    expect(socket.protocols).toEqual(["herdsman.json"]);
  });

  test("offers the session as a second subprotocol, after the JSON one", () => {
    const { socket } = start({ session: "device-1" });
    expect(socket.protocols).toEqual(["herdsman.json", "herdsman.session.device-1"]);
  });

  test("offers no session entry for an empty session", () => {
    const { socket } = start({ session: "" });
    expect(socket.protocols).toEqual(["herdsman.json"]);
  });

  test("refuses a server that selects the session entry", async () => {
    const { socket, stream } = start({ session: "device-1" });
    socket.protocol = "herdsman.session.device-1";
    socket.open();
    expect((await failure(stream.next())).code).toBe("unavailable");
    expect(socket.closed).toEqual([1008]);
  });

  test("holds frames until the socket opens, then keeps their order", () => {
    const { socket, stream } = start();
    stream.send({ out: 1 });
    stream.send({ out: 2 });
    expect(socket.sent).toEqual([]);
    socket.open();
    stream.send({ out: 3 });
    expect(socket.sent).toEqual(['{"out":1}', '{"out":2}', '{"out":3}']);
  });

  test("yields frames in order, both queued and awaited", async () => {
    const { socket, stream } = start();
    socket.open();
    socket.frame('{"n":1}');
    socket.frame('{"n":2}');
    expect((await stream.next()).value).toEqual({ n: 1 });
    expect((await stream.next()).value).toEqual({ n: 2 });
    const pending = stream.next();
    socket.frame('{"n":3}');
    expect((await pending).value).toEqual({ n: 3 });
  });

  test("a clean close ends the iteration after the queued frames", async () => {
    const { socket, stream } = start();
    socket.open();
    socket.frame('{"n":1}');
    socket.end();
    const seen: unknown[] = [];
    for await (const frame of stream) seen.push(frame);
    expect(seen).toEqual([{ n: 1 }]);
  });

  test("a clean close releases a waiting reader", async () => {
    const { socket, stream } = start();
    socket.open();
    const pending = stream.next();
    socket.end();
    expect((await pending).done).toBe(true);
  });

  test("an abnormal close fails the reader as unavailable", async () => {
    const { socket, stream } = start();
    socket.open();
    const pending = stream.next();
    socket.end(1006, "gone");
    const error = await failure(pending);
    expect(error.code).toBe("unavailable");
    expect(error.message).toContain("1006");
  });

  test("a server that declines the subprotocol is refused", async () => {
    const { socket, stream } = start();
    socket.protocol = "";
    socket.open();
    expect((await failure(stream.next())).code).toBe("unavailable");
    expect(socket.closed.length).toBe(1);
  });

  test("a frame that is not JSON text is internal", async () => {
    for (const bad of ["{", new Uint8Array([1])]) {
      const { socket, stream } = start();
      socket.open();
      socket.frame(bad);
      expect((await failure(stream.next())).code).toBe("internal");
      expect(socket.closed).toEqual([1007]);
    }
  });

  test("a reader that falls behind the bound fails instead of growing", async () => {
    const { socket, stream } = start({ maxQueued: 2 });
    socket.open();
    socket.frame('{"n":1}');
    socket.frame('{"n":2}');
    socket.frame('{"n":3}');
    // The frames before the gap stay readable; the failure marks the gap.
    expect((await stream.next()).value).toEqual({ n: 1 });
    expect((await stream.next()).value).toEqual({ n: 2 });
    expect((await failure(stream.next())).code).toBe("resource_exhausted");
    expect(socket.closed).toEqual([1008]);
  });

  test("overlapping reads are answered in the order they were made", async () => {
    const { socket, stream } = start();
    socket.open();
    const first = stream.next();
    const second = stream.next();
    socket.frame('{"n":1}');
    socket.frame('{"n":2}');
    expect((await first).value).toEqual({ n: 1 });
    expect((await second).value).toEqual({ n: 2 });
  });

  test("the end of the stream releases every waiting reader", async () => {
    const closed = start();
    closed.socket.open();
    const reads = [closed.stream.next(), closed.stream.next()];
    closed.stream.close();
    for (const read of reads) expect((await read).done).toBe(true);

    const failed = start();
    failed.socket.open();
    const failing = [failed.stream.next(), failed.stream.next()];
    failed.socket.end(1006, "gone");
    for (const read of failing) expect((await failure(read)).code).toBe("unavailable");
  });

  test("close ends a waiting reader and sends nothing after it", async () => {
    const { socket, stream } = start();
    socket.open();
    const pending = stream.next();
    stream.close();
    expect((await pending).done).toBe(true);
    expect(socket.closed).toEqual([1000]);
    expect(() => stream.send({ out: 1 })).toThrow(HerdsmanError);
    expect((await stream.next()).done).toBe(true);
  });

  test("leaving a for-await loop closes the socket", async () => {
    const { socket, stream } = start();
    socket.open();
    socket.frame('{"n":1}');
    for await (const _frame of stream) break;
    expect(socket.closed).toEqual([1000]);
  });

  test("the caller's abort is canceled, also when already aborted", async () => {
    const controller = new AbortController();
    const { socket, stream } = start({ signal: controller.signal });
    socket.open();
    const pending = stream.next();
    controller.abort();
    expect((await failure(pending)).code).toBe("canceled");
    expect(socket.closed).toEqual([1000]);
    const late = start({ signal: AbortSignal.abort() });
    expect((await failure(late.stream.next())).code).toBe("canceled");
  });

  test("a socket error before any close fails as unavailable", async () => {
    const { socket, stream } = start();
    socket.onerror?.({});
    expect((await failure(stream.next())).code).toBe("unavailable");
  });
});

describe("against a real WebSocket server", () => {
  let stop = () => {};
  afterEach(() => stop());

  test("carries frames both ways under the JSON subprotocol", async () => {
    const server = Bun.serve({
      port: 0,
      fetch(request, srv) {
        if (srv.upgrade(request, { headers: { "Sec-WebSocket-Protocol": JSON_SUBPROTOCOL } })) return;
        return new Response("no", { status: 400 });
      },
      websocket: {
        message(ws, data) {
          const { out } = JSON.parse(String(data)) as { out: number };
          ws.send(JSON.stringify({ n: out + 1 }));
          ws.close(1000, "done");
        },
      },
    });
    stop = () => void server.stop(true);
    const stream = openStream<{ out: number }, { n: number }>({ baseUrl: `http://127.0.0.1:${server.port}` });
    stream.send({ out: 41 });
    const seen: { n: number }[] = [];
    for await (const frame of stream) seen.push(frame);
    expect(seen).toEqual([{ n: 42 }]);
  });
});
