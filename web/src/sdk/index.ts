// The app's one binding to @herdsman/sdk: every page's clients for the current
// host, carrying that browser's device secret.
import { type Client, createClient, type FetchLike, HerdsmanError, type ServiceTable } from "@herdsman/sdk";
import { LedgerService } from "@herdsman/sdk/herdsman/daemon/v1/ledger";
import { ProjectService } from "@herdsman/sdk/herdsman/daemon/v1/project";
import { RouteService } from "@herdsman/sdk/herdsman/daemon/v1/route";
import { RuntimeService } from "@herdsman/sdk/herdsman/daemon/v1/runtime";
import { TaskService } from "@herdsman/sdk/herdsman/daemon/v1/task";
import { SuggestService } from "@herdsman/sdk/herdsman/suggest/v1/suggest";

export interface SdkHost {
  url: string;
  device?: string;
}

export interface Services {
  projects: Client<typeof ProjectService>;
  tasks: Client<typeof TaskService>;
  routes: Client<typeof RouteService>;
  runtime: Client<typeof RuntimeService>;
  ledger: Client<typeof LedgerService>;
  suggest: Client<typeof SuggestService>;
}

let current: Services | null = null;

/** Points every page at a host. `send` replaces the browser's fetch in tests. */
export function bindHost(host: SdkHost, send?: FetchLike): void {
  const client = <S extends ServiceTable>(service: S) =>
    createClient(service, {
      baseUrl: host.url,
      init: { credentials: "omit", cache: "no-store" },
      ...(host.device ? { headers: { Authorization: `Bearer ${host.device}` } } : {}),
      ...(send ? { fetch: send } : {}),
    });
  current = {
    projects: client(ProjectService),
    tasks: client(TaskService),
    routes: client(RouteService),
    runtime: client(RuntimeService),
    ledger: client(LedgerService),
    suggest: client(SuggestService),
  };
}

/** The clients of the bound host. */
export function services(): Services {
  if (!current) throw new HerdsmanError("failed_precondition", "no host is selected");
  return current;
}

/** Whether the daemon behind this host has no such method yet. */
export function notServed(error: unknown): boolean {
  return error instanceof HerdsmanError && error.code === "unimplemented";
}

/** One sentence for a failed call. */
export function explain(error: unknown): string {
  if (!(error instanceof HerdsmanError)) return error instanceof Error ? error.message : String(error);
  switch (error.code) {
    case "unauthenticated":
      return "This browser is not paired with the host. Open the link that herdsman web printed.";
    case "permission_denied":
      return error.message || "The host refused this browser.";
    case "unavailable":
      return `The daemon cannot be reached: ${error.message}`;
    default:
      return error.message || `The call failed: ${error.code}`;
  }
}
