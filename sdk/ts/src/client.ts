// A typed client for herdsman's unary RPCs on plain fetch and JSON.

export * from "./stream";

export const ErrorCodes = [
  "canceled",
  "unknown",
  "invalid_argument",
  "deadline_exceeded",
  "not_found",
  "already_exists",
  "permission_denied",
  "resource_exhausted",
  "failed_precondition",
  "aborted",
  "out_of_range",
  "unimplemented",
  "internal",
  "unavailable",
  "data_loss",
  "unauthenticated",
] as const;

export type ErrorCode = (typeof ErrorCodes)[number];

/** A protobuf message carried with a failure, still encoded. */
export interface ErrorDetail {
  type: string;
  /** Base64 of the message's binary form. */
  value: string;
}

/** Every failure of a call. Branch on `code`; `message` is for people. */
export class HerdsmanError extends Error {
  override readonly name = "HerdsmanError";
  readonly code: ErrorCode;
  readonly details: readonly ErrorDetail[];
  /** The HTTP status, when a response arrived. */
  readonly status: number | undefined;

  constructor(code: ErrorCode, message: string, options: { details?: ErrorDetail[]; status?: number } = {}) {
    super(message);
    this.code = code;
    this.details = options.details ?? [];
    this.status = options.status;
  }
}

export type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

export interface ServiceMethod {
  readonly path: string;
  readonly signature: (request: never) => unknown;
}

/** The shape of a generated service table. */
export interface ServiceTable {
  readonly typeName: string;
  readonly methods: Readonly<Record<string, ServiceMethod>>;
}

export interface ClientOptions {
  /** The server's origin and any path before `/rpc`. */
  baseUrl: string;
  fetch?: FetchLike;
  headers?: HeadersInit;
  /** The default deadline of each call, in milliseconds. */
  timeoutMs?: number;
  /** Passed to every fetch; under Bun, `{ unix: path }` reaches a Unix socket. */
  init?: RequestInit;
}

export interface CallOptions {
  signal?: AbortSignal;
  /** This call's deadline in milliseconds, over the client's. */
  timeoutMs?: number;
  headers?: HeadersInit;
}

type MethodOf<M extends ServiceMethod> = M["signature"] extends (request: infer Request) => infer Response
  ? (request?: Request, options?: CallOptions) => Promise<Response>
  : never;

export type Client<S extends ServiceTable> = { readonly [K in keyof S["methods"]]: MethodOf<S["methods"][K]> };

/** Builds one async function per method of a generated service table. */
export function createClient<S extends ServiceTable>(service: S, options: ClientOptions): Client<S> {
  const client: Record<string, (request?: unknown, call?: CallOptions) => Promise<unknown>> = {};
  for (const [name, method] of Object.entries(service.methods)) {
    client[name] = (request, call) => invoke(method.path, request ?? {}, options, call ?? {});
  }
  return client as Client<S>;
}

function headersFor(options: ClientOptions, call: CallOptions, timeoutMs: number | undefined): Headers {
  const headers = new Headers(options.headers);
  new Headers(call.headers).forEach((value, key) => headers.set(key, value));
  headers.set("Content-Type", "application/json");
  headers.set("Connect-Protocol-Version", "1");
  if (timeoutMs !== undefined) headers.set("Connect-Timeout-Ms", String(Math.ceil(timeoutMs)));
  return headers;
}

async function invoke(path: string, request: unknown, options: ClientOptions, call: CallOptions): Promise<unknown> {
  const timeoutMs = call.timeoutMs ?? options.timeoutMs;
  if (timeoutMs !== undefined && !(timeoutMs > 0)) {
    throw new HerdsmanError("invalid_argument", `timeout must be positive: ${timeoutMs}`);
  }
  const deadline = timeoutMs === undefined ? undefined : AbortSignal.timeout(Math.ceil(timeoutMs));
  const signals = [call.signal, deadline].filter((signal) => signal !== undefined);
  const init: RequestInit = {
    ...options.init,
    method: "POST",
    headers: headersFor(options, call, timeoutMs),
    body: JSON.stringify(request),
  };
  if (signals.length > 0) init.signal = AbortSignal.any(signals);

  const send = options.fetch ?? ((input, sendInit) => fetch(input, sendInit));
  try {
    const response = await send(options.baseUrl.replace(/\/+$/, "") + path, init);
    return await decode(response);
  } catch (error) {
    if (error instanceof HerdsmanError) throw error;
    if (deadline?.aborted) throw new HerdsmanError("deadline_exceeded", "the call's deadline passed");
    if (call.signal?.aborted) throw new HerdsmanError("canceled", "the call was canceled");
    throw new HerdsmanError("unavailable", error instanceof Error ? error.message : String(error));
  }
}

async function decode(response: Response): Promise<unknown> {
  const text = await response.text();
  if (response.status !== 200) throw failureFrom(response.status, text);
  const type = response.headers.get("Content-Type") ?? "";
  if (!/^application\/json\s*(;|$)/i.test(type)) {
    throw new HerdsmanError("internal", `unexpected content type: ${type}`, { status: 200 });
  }
  const body = parseJSON(text);
  if (typeof body !== "object" || body === null || Array.isArray(body)) {
    throw new HerdsmanError("internal", "the reply is not a JSON object", { status: 200 });
  }
  return body;
}

function parseJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function isErrorCode(code: unknown): code is ErrorCode {
  return ErrorCodes.includes(code as ErrorCode);
}

function isDetail(detail: unknown): detail is ErrorDetail {
  const candidate = detail as Partial<ErrorDetail> | null;
  return typeof candidate?.type === "string" && typeof candidate.value === "string";
}

function failureFrom(status: number, text: string): HerdsmanError {
  const body = parseJSON(text) as { code?: unknown; message?: unknown; details?: unknown } | null | undefined;
  if (!isErrorCode(body?.code)) return new HerdsmanError(codeFromHTTP(status), `HTTP ${status}`, { status });
  const details = Array.isArray(body.details) ? body.details.filter(isDetail) : [];
  return new HerdsmanError(body.code, typeof body.message === "string" ? body.message : "", { details, status });
}

/** The code of a failure whose body names none, as when a proxy answered. */
function codeFromHTTP(status: number): ErrorCode {
  switch (status) {
    case 400:
      return "internal";
    case 401:
      return "unauthenticated";
    case 403:
      return "permission_denied";
    case 404:
      return "unimplemented";
    case 429:
    case 502:
    case 503:
    case 504:
      return "unavailable";
    default:
      return "unknown";
  }
}
