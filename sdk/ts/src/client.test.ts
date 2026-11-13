import { afterEach, describe, expect, test } from "bun:test";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { MessageService } from "./generated/herdsman/daemon/v1/message";
import { createClient, ErrorCodes, HerdsmanError, type FetchLike } from "./client";

// The table a daemon serves failures with. Every code must survive the trip.
const statusOf = {
  canceled: 499,
  unknown: 500,
  invalid_argument: 400,
  deadline_exceeded: 504,
  not_found: 404,
  already_exists: 409,
  permission_denied: 403,
  resource_exhausted: 429,
  failed_precondition: 400,
  aborted: 409,
  out_of_range: 400,
  unimplemented: 501,
  internal: 500,
  unavailable: 503,
  data_loss: 500,
  unauthenticated: 401,
} as const;

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

interface Seen {
  input: string;
  init: RequestInit;
}

function fake(respond: (seen: Seen) => Response | Promise<Response>): { fetch: FetchLike; calls: Seen[] } {
  const calls: Seen[] = [];
  return {
    calls,
    fetch: async (input, init) => {
      const seen = { input, init };
      calls.push(seen);
      return respond(seen);
    },
  };
}

async function failure(call: Promise<unknown>): Promise<HerdsmanError> {
  try {
    await call;
  } catch (error) {
    expect(error).toBeInstanceOf(HerdsmanError);
    return error as HerdsmanError;
  }
  throw new Error("the call did not fail");
}

describe("createClient", () => {
  test("posts the request as JSON to the method's path with the protocol header", async () => {
    const { fetch, calls } = fake(() => json({ inbox: "/x", lastSeq: "9" }));
    const client = createClient(MessageService, { baseUrl: "http://daemon/", fetch });
    const reply = await client.status({ route: { value: "r1" } });
    expect(reply.inbox).toBe("/x");
    expect(reply.lastSeq).toBe("9");
    const [call] = calls;
    expect(call?.input).toBe("http://daemon/rpc/herdsman.daemon.v1.MessageService/Status");
    expect(call?.init.method).toBe("POST");
    expect(call?.init.body).toBe('{"route":{"value":"r1"}}');
    const headers = new Headers(call?.init.headers);
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(headers.get("Connect-Protocol-Version")).toBe("1");
    expect(headers.has("Connect-Timeout-Ms")).toBe(false);
  });

  test("sends an empty message when no request is given", async () => {
    const { fetch, calls } = fake(() => json({}));
    await createClient(MessageService, { baseUrl: "http://daemon", fetch }).status();
    expect(calls[0]?.init.body).toBe("{}");
  });

  test("merges client and call headers and passes extra init through", async () => {
    const { fetch, calls } = fake(() => json({}));
    const client = createClient(MessageService, {
      baseUrl: "http://daemon",
      fetch,
      headers: { "X-A": "client", "X-B": "client" },
      init: { credentials: "include" },
    });
    await client.status({}, { headers: { "X-B": "call" } });
    const headers = new Headers(calls[0]?.init.headers);
    expect(headers.get("X-A")).toBe("client");
    expect(headers.get("X-B")).toBe("call");
    expect(calls[0]?.init.credentials).toBe("include");
  });

  test("announces the call's timeout over the client's, rounded up", async () => {
    const { fetch, calls } = fake(() => json({}));
    const client = createClient(MessageService, { baseUrl: "http://daemon", fetch, timeoutMs: 5000 });
    await client.status();
    await client.status({}, { timeoutMs: 12.1 });
    expect(new Headers(calls[0]?.init.headers).get("Connect-Timeout-Ms")).toBe("5000");
    expect(new Headers(calls[1]?.init.headers).get("Connect-Timeout-Ms")).toBe("13");
  });

  test("refuses a timeout that is not positive without calling", async () => {
    const { fetch, calls } = fake(() => json({}));
    const client = createClient(MessageService, { baseUrl: "http://daemon", fetch });
    for (const timeoutMs of [0, -1, Number.NaN]) {
      expect((await failure(client.status({}, { timeoutMs }))).code).toBe("invalid_argument");
    }
    expect(calls.length).toBe(0);
  });

  for (const code of ErrorCodes) {
    test(`keeps the code, message and details of ${code}`, async () => {
      const status = statusOf[code];
      const { fetch } = fake(() =>
        json({ code, message: "why", details: [{ type: "demo.Detail", value: "AQ" }, { type: 7 }, "x"] }, status),
      );
      const error = await failure(createClient(MessageService, { baseUrl: "http://d", fetch }).status());
      expect(error.code).toBe(code);
      expect(error.message).toBe("why");
      expect(error.status).toBe(status);
      expect(error.details).toEqual([{ type: "demo.Detail", value: "AQ" }]);
    });
  }

  const fromStatus: [number, string][] = [
    [400, "internal"],
    [401, "unauthenticated"],
    [403, "permission_denied"],
    [404, "unimplemented"],
    [429, "unavailable"],
    [502, "unavailable"],
    [503, "unavailable"],
    [504, "unavailable"],
    [418, "unknown"],
  ];
  for (const [status, code] of fromStatus) {
    test(`describes a bare HTTP ${status} as ${code}`, async () => {
      const { fetch } = fake(() => new Response("<html>", { status }));
      const error = await failure(createClient(MessageService, { baseUrl: "http://d", fetch }).status());
      expect(error.code as string).toBe(code);
      expect(error.message).toBe(`HTTP ${status}`);
    });
  }

  test("a body with an invalid code is described by its status", async () => {
    const { fetch } = fake(() => json({ code: "nope", message: "x" }, 403));
    expect((await failure(createClient(MessageService, { baseUrl: "http://d", fetch }).status())).code).toBe(
      "permission_denied",
    );
  });

  test("a success that is not a JSON object is internal", async () => {
    for (const response of [
      new Response("{", { headers: { "Content-Type": "application/json" } }),
      new Response("[1]", { headers: { "Content-Type": "application/json" } }),
      new Response("{}", { headers: { "Content-Type": "text/html" } }),
    ]) {
      const { fetch } = fake(() => response);
      expect((await failure(createClient(MessageService, { baseUrl: "http://d", fetch }).status())).code).toBe(
        "internal",
      );
    }
  });

  test("a transport failure is unavailable", async () => {
    const fetch: FetchLike = async () => {
      throw new TypeError("refused");
    };
    const error = await failure(createClient(MessageService, { baseUrl: "http://d", fetch }).status());
    expect(error.code).toBe("unavailable");
    expect(error.message).toContain("refused");
  });

  // Never answers until the call's signal aborts.
  const hanging: FetchLike = (_input, init) =>
    new Promise((_resolve, reject) => {
      init.signal?.addEventListener("abort", () => reject(init.signal?.reason));
    });

  test("the caller's abort is canceled", async () => {
    const controller = new AbortController();
    const call = createClient(MessageService, { baseUrl: "http://d", fetch: hanging }).status(
      {},
      { signal: controller.signal },
    );
    controller.abort();
    expect((await failure(call)).code).toBe("canceled");
  });

  test("a passed timeout is deadline_exceeded", async () => {
    const call = createClient(MessageService, { baseUrl: "http://d", fetch: hanging }).status({}, { timeoutMs: 5 });
    expect((await failure(call)).code).toBe("deadline_exceeded");
  });
});

describe("over a Unix socket", () => {
  let stop = () => {};
  afterEach(() => stop());

  test("reaches a real server with the socket given through init", async () => {
    const unix = join(tmpdir(), `herdsman-ts-${process.pid}-${Date.now()}.sock`);
    const server = Bun.serve({
      unix,
      fetch: async (request) => {
        const body = (await request.json()) as { route?: { value?: string } };
        return json({ inbox: body.route?.value, status: new URL(request.url).pathname });
      },
    });
    stop = () => void server.stop(true);
    const client = createClient(MessageService, { baseUrl: "http://localhost", init: { unix } as RequestInit });
    const reply = await client.status({ route: { value: "abc" } });
    expect(reply).toEqual({ inbox: "abc", status: "/rpc/herdsman.daemon.v1.MessageService/Status" });
  });
});

test("a preview is rejected by an older daemon before any mutation", async () => {
  let mutations = 0;
  const older = fake(({ init }) => {
    if (new Headers(init.headers).get("Content-Type") === "application/json") {
      mutations++;
      return json({});
    }
    return json({ code: "unimplemented", message: "unsupported content type" }, 415);
  });
  const client = createClient(MessageService, { baseUrl: "http://old", fetch: older.fetch });
  expect((await failure(client.send({ dryRun: true }))).code).toBe("unimplemented");
  expect(mutations).toBe(0);
  expect(new Headers(older.calls[0]?.init.headers).get("Content-Type")).toBe("application/vnd.herdsman.preview+json");
});
