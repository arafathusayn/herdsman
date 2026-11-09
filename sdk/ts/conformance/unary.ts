// Run by sdk/conformance/unary: bun run unary.ts <socket> <status table as JSON>.
// Exits non-zero and names each scenario that went wrong.
import { createClient, ErrorCodes, HerdsmanError, type ServiceTable } from "../src/client";
import { MessageService } from "../src/generated/herdsman/daemon/v1/message";

const [socket, table] = process.argv.slice(2);
if (!socket || !table) throw new Error("usage: unary.ts <socket> <status table>");
const statuses = JSON.parse(table) as Record<string, number>;

const options = { baseUrl: "http://localhost", init: { unix: socket } as RequestInit };
const client = createClient(MessageService, options);

const wrong: string[] = [];
function check(scenario: string, ok: boolean, seen: unknown) {
  if (!ok) wrong.push(`${scenario}: ${JSON.stringify(seen)}`);
}

async function failure(call: Promise<unknown>): Promise<HerdsmanError | undefined> {
  try {
    await call;
  } catch (error) {
    if (error instanceof HerdsmanError) return error;
    throw error;
  }
  return undefined;
}

const reply = await client.status({ route: { value: "alpha" } });
check("success", reply.route?.value === "alpha" && reply.lastSeq === "7", reply);

const served = Object.keys(statuses).sort();
check("code tables", served.join() === [...ErrorCodes].sort().join(), served);
for (const code of served) {
  const error = await failure(client.status({ route: { value: `fail:${code}` } }));
  const detail = error?.details[0];
  const carried = Buffer.from(detail?.value ?? "", "base64").toString().includes("daemon_code");
  check(
    code,
    error?.code === code &&
      error.message === "the reason" &&
      error.status === statuses[code] &&
      error.details.length === 1 &&
      detail?.type === "herdsman.core.v1.Error" &&
      carried,
    error && { code: error.code, message: error.message, status: error.status, details: error.details },
  );
}

const announced = await client.status({ route: { value: "deadline" } }, { timeoutMs: 60_000 });
check("timeout reaches the service", announced.lastSeq === "1", announced);

const late = await failure(client.status({ route: { value: "wait" } }, { timeoutMs: 20 }));
check("deadline passes as its code", late?.code === "deadline_exceeded", late?.code);

const NoSuch = {
  typeName: "no.Such",
  methods: { thing: { path: "/rpc/no.Such/Thing", signature: undefined as unknown as (request: object) => object } },
} as const satisfies ServiceTable;
const unknown = await failure(createClient(NoSuch, options).thing({}));
check("unknown procedure", unknown?.code === "unimplemented", unknown?.code);

if (wrong.length > 0) {
  console.error(wrong.join("\n"));
  process.exit(1);
}
