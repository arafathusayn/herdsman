"use pure";

import { type ErrorCode, type ErrorDetail, HerdsmanError } from "@herdsman/sdk";

// A tagged representation, rather than a magic property in user data, keeps
// arbitrary server fields collision-free. No DOM node, callback or resource
// can be accidentally retained in application state.
import { AuthError } from "./errors.ts";

type Wire = null | boolean | number | string | { kind: string; items: Wire[] };

function pack(value: unknown): Wire {
  if (value === null || typeof value === "boolean" || typeof value === "string") return value;
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw new TypeError("UI state numbers must be finite");
    return value;
  }
  if (value === undefined) return { kind: "undefined", items: [] };
  if (typeof value === "bigint") return { kind: "bigint", items: [String(value)] };
  if (value instanceof HerdsmanError)
    return {
      kind: "rpc-error",
      items: [value.code, value.message, pack(value.details), pack(value.status)],
    };
  if (value instanceof AuthError) return { kind: "auth-error", items: [value.message] };
  if (value instanceof Error) return { kind: "error", items: [value.name, value.message] };
  if (Array.isArray(value)) return { kind: "array", items: value.map(pack) };
  if (value instanceof Map)
    return {
      kind: "map",
      items: [...value].flatMap(([key, item]) => [pack(key), pack(item)]),
    };
  if (value instanceof Set) return { kind: "set", items: [...value].map(pack) };
  if (typeof value === "object" && Object.getPrototypeOf(value) === Object.prototype)
    return {
      kind: "object",
      items: Object.entries(value).flatMap(([key, item]) => [key, pack(item)]),
    };
  throw new TypeError("UI state holds data only; keep callbacks and DOM resources in the view");
}

function unpack(wire: Wire): unknown {
  if (wire === null || typeof wire !== "object") return wire;
  const items = wire.items.map(unpack);
  switch (wire.kind) {
    case "undefined":
      return undefined;
    case "bigint":
      return BigInt(String(items[0]));
    case "auth-error":
      return new AuthError(String(items[0]));
    case "error":
      return Object.assign(new Error(String(items[1])), {
        name: String(items[0]),
      });
    case "rpc-error":
      return new HerdsmanError(items[0] as ErrorCode, String(items[1]), {
        details: items[2] as ErrorDetail[],
        ...(items[3] === undefined ? {} : { status: items[3] as number }),
      });
    case "array":
      return items;
    case "set":
      return new Set(items);
    case "map": {
      const map = new Map<unknown, unknown>();
      for (let i = 0; i < items.length; i += 2) map.set(items[i], items[i + 1]);
      return map;
    }
    case "object": {
      const entries: [string, unknown][] = [];
      for (let i = 0; i < items.length; i += 2) entries.push([String(items[i]), items[i + 1]]);
      return Object.fromEntries(entries);
    }
    default:
      throw new TypeError(`Unknown UI state encoding: ${wire.kind}`);
  }
}

export function encodeValue(value: unknown): string {
  return JSON.stringify(pack(value));
}
/** Only decode snapshots created by encodeValue, never untrusted persisted input. */
export function decodeValue<T>(json: string): T {
  return unpack(JSON.parse(json) as Wire) as T;
}

export function decodeUnknown(json: string): unknown {
  return unpack(JSON.parse(json) as Wire);
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
