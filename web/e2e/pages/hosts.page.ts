// The Hosts dialog (list, rename, add, forget) and the full-page connect and
// confirm screens, against hosts kept in localStorage.
import { confirmHost, openHosts, showConnect } from "../../src/views/hosts.ts";
import { check, equal, flush, q, run, typeIn } from "./lib.ts";

const hosts = {
  hosts: [
    { id: "a", url: "http://127.0.0.1:7878", name: "laptop", device: "s" },
    { id: "b", url: "https://box.example.com", name: "box" },
    { id: "c", url: "http://192.168.1.20:7878", name: "lan" },
  ],
  current: "a",
};
const reset = (v: unknown = hosts): void => localStorage.setItem("herdsman.hosts", JSON.stringify(v));
const stored = (): typeof hosts => JSON.parse(localStorage.getItem("herdsman.hosts")!) as typeof hosts;

/** Opens a fresh Hosts dialog over the given hosts. */
function dialog(v: unknown = hosts): HTMLDialogElement {
  for (const d of document.querySelectorAll("dialog")) d.remove();
  reset(v);
  openHosts();
  return q<HTMLDialogElement>(document, "dialog.hosts");
}
const rows = (d: Element): HTMLElement[] => [...d.querySelectorAll<HTMLElement>(".hosts-list .host")];
const describe = (r: HTMLElement) => ({
  cls: r.className,
  name: r.querySelector(".host-name")?.textContent ?? null,
  current: r.querySelector(".host-chip")?.textContent ?? null,
  url: r.querySelector(".host-url")?.textContent,
  state: r.querySelector(".host-main > span")?.textContent,
  connect: !!r.querySelector(".host-actions .btn"),
});
const tool = (r: HTMLElement, tip: "Rename" | "Forget"): HTMLElement => q(r, `[data-tip="${tip}"]`);
const key = (el: Element, k: string) => el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));

await run(async () => {
  let d = dialog();
  check("opens as a modal dialog", d.open && d.matches(":modal"));
  equal("heading", q(d, "h2").textContent, "Hosts");
  equal("rows", rows(d).map(describe), [
    {
      cls: "host current",
      name: "laptop",
      current: "Current",
      url: "http://127.0.0.1:7878",
      state: "Paired",
      connect: false,
    },
    {
      cls: "host",
      name: "box",
      current: null,
      url: "https://box.example.com",
      state: "Not paired",
      connect: true,
    },
    {
      cls: "host",
      name: "lan",
      current: null,
      url: "http://192.168.1.20:7878",
      state: "Not paired",
      connect: true,
    },
  ]);
  equal(
    "row tools are labelled buttons",
    rows(d).map((r) => [tool(r, "Rename").getAttribute("aria-label"), tool(r, "Forget").getAttribute("type")]),
    [
      ["Rename laptop", "button"],
      ["Rename box", "button"],
      ["Rename lan", "button"],
    ],
  );

  d = dialog({ hosts: [], current: "" });
  equal("no hosts", q(d, ".hosts-empty").textContent, "No hosts yet.");

  // Rename: the field replaces the name, focused with its text selected; Enter saves.
  d = dialog();
  const kept = rows(d);
  tool(rows(d)[1]!, "Rename").click();
  let field = q<HTMLInputElement>(d, ".host-rename");
  equal("rename field holds the name", field.value, "box");
  check("rename field is focused", document.activeElement === field);
  check("rename field text is selected", field.selectionStart === 0 && field.selectionEnd === field.value.length);
  check(
    "rename keeps the rows' nodes",
    rows(d).every((r, i) => r === kept[i]),
  );
  field.value = "renamed";
  key(field, "Enter");
  equal("Enter saves the name", rows(d)[1]!.querySelector(".host-name")?.textContent, "renamed");
  check("Enter ends the rename", d.querySelector(".host-rename") === null);
  equal("the new name is stored", stored().hosts[1]!.name, "renamed");

  // Escape cancels.
  d = dialog();
  tool(rows(d)[1]!, "Rename").click();
  field = q<HTMLInputElement>(d, ".host-rename");
  field.value = "nope";
  key(field, "Escape");
  equal("Escape keeps the name", rows(d)[1]!.querySelector(".host-name")?.textContent, "box");
  equal("Escape stores nothing", stored(), hosts);
  check("Escape leaves the dialog open", d.open);

  // Adding: a bad address is refused with a note; an address adds a host without switching.
  d = dialog();
  typeIn(q(d, 'input[name="host"]'), "ftp://x");
  q(d, '.hosts-add button[type="submit"]').click();
  const note = q(d, ".hosts-add [role=status]");
  equal(
    "a bad address is refused",
    [note.className, note.textContent],
    ["hosts-note error", "That is not a host address or a pairing link."],
  );
  equal("nothing is added", stored().hosts.length, 3);
  typeIn(q(d, 'input[name="host"]'), "10.0.0.9:7878");
  typeIn(q(d, 'input[name="name"]'), "nine");
  q(d, '.hosts-add button[type="submit"]').click();
  equal("the address is added", describe(rows(d)[3]!), {
    cls: "host",
    name: "nine",
    current: null,
    url: "http://10.0.0.9:7878",
    state: "Not paired",
    connect: true,
  });
  equal(
    "the note says what to do next",
    [note.className, note.textContent],
    ["hosts-note", "Added nine. Paste its pairing link to pair this browser with it."],
  );
  equal(
    "the fields are cleared",
    [...d.querySelectorAll<HTMLInputElement>(".hosts-add input")].map((i) => i.value),
    ["", ""],
  );
  equal("the current host stays", stored().current, "a");

  // Forget a host that is not the current one.
  d = dialog();
  tool(rows(d)[2]!, "Forget").click();
  equal(
    "forget removes the row",
    rows(d).map((r) => r.querySelector(".host-name")?.textContent),
    ["laptop", "box"],
  );
  equal(
    "forget removes the stored host",
    stored().hosts.map((h) => h.id),
    ["a", "b"],
  );
  d.close();
  await flush();
  check("closing removes the dialog", document.querySelector("dialog.hosts") === null);

  // The connect screen: the title, the text, the add form focused, the known hosts.
  const app = q(document, "#app");
  reset();
  let retried = 0;
  showConnect(app, "Connect to a host", "Paste the link.", () => retried++);
  equal(
    "connect title and text",
    [q(app, "main.pair h1").textContent, q(app, ".pair-lead").textContent],
    ["Connect to a host", "Paste the link."],
  );
  equal("connect focuses the address field", (document.activeElement as HTMLInputElement | null)?.name, "host");
  equal(
    "connect lists the hosts",
    rows(app).map((r) => r.querySelector(".host-name")?.textContent),
    ["laptop", "box", "lan"],
  );
  q(app, ".pair-retry").click();
  equal("Try again runs the retry", retried, 1);
  showConnect(app, "Pair", "No retry.");
  check("no retry, no Try again", app.querySelector(".pair-retry") === null);

  // The confirm screen answers with the button pressed.
  let answered: boolean | null = null;
  void confirmHost(app, "http://x:1").then((v) => (answered = v));
  equal("confirm names the host", q(app, ".host-url").textContent, "http://x:1");
  q(app, ".btn.primary").click();
  await flush();
  equal("Add and pair answers yes", answered, true);
  void confirmHost(app, "http://x:1").then((v) => (answered = v));
  q(app, ".dialog-actions .btn:not(.primary)").click();
  await flush();
  equal("Cancel answers no", answered, false);

  // The screens work again after something else replaced the root's content.
  app.replaceChildren(document.createElement("div"));
  showConnect(app, "Again", "Shown again.");
  equal("connect after a foreign replace", q(app, "h1").textContent, "Again");
});
