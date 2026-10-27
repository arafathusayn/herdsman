// The Appearance dialog: its sections, one checked choice per group, and
// picking a choice (or a theme in the theme switch) marks it, keeps the
// choice buttons' nodes and leaves the dialog open.
import { openAppearance } from "../../src/views/appearance.ts";
import { check, equal, q, run } from "./lib.ts";

(window as unknown as { requestAnimationFrame: unknown }).requestAnimationFrame ??= () => 0;

const dialog = (): HTMLDialogElement => q<HTMLDialogElement>(document, "dialog.appearance");
const grids = (): HTMLElement[] => [...dialog().querySelectorAll<HTMLElement>(".choice-grid")];
const checked = (group: Element): number[] =>
  [...group.children].flatMap((b, i) => (b.getAttribute("aria-checked") === "true" ? [i] : []));

/** Each group's one checked choice is marked active and carries the check icon; the others are plain. */
function marked(group: Element): boolean {
  return [...group.children].every((b) => {
    const on = b.getAttribute("aria-checked") === "true";
    return b.className === (on ? "choice active" : "choice") && !!b.querySelector(".choice-label svg") === on;
  });
}

await run(async () => {
  openAppearance();
  const dlg = dialog();
  check("opens as a modal dialog", dlg.open && dlg.matches(":modal"));
  equal("heading", q(dlg, "h2").textContent, "Appearance");
  equal(
    "sections",
    [...dlg.querySelectorAll(".appearance-section > h3")].map((h) => h.textContent),
    ["Theme", "Color themes", "Terminal colors", "Code highlighting"],
  );
  const sw = q(dlg, ".theme-switch");
  equal(
    "the theme switch's buttons are radios of type button",
    [...sw.children].map((b) => [b.getAttribute("type"), b.getAttribute("role")]),
    [
      ["button", "radio"],
      ["button", "radio"],
      ["button", "radio"],
    ],
  );
  equal("three choice groups", grids().length, 3);
  // The system theme is a choice of the switch, not of the color themes.
  equal("the switch checks System", checked(sw), [0]);
  for (const [i, g] of grids().entries()) {
    equal(`group ${i}: checked choices`, checked(g).length, i === 0 ? 0 : 1);
    check(`group ${i}: only the checked choice is active`, marked(g));
    check(
      `group ${i}: every choice is a radio of type button with a preview`,
      [...g.children].every(
        (b) =>
          b.getAttribute("type") === "button" &&
          b.getAttribute("role") === "radio" &&
          b.firstElementChild?.matches(".term-preview, .code-preview, .app-preview") === true,
      ),
    );
  }

  // Each group: a click checks that choice alone, keeps the nodes, and the dialog stays open.
  for (const [group, at] of [
    [1, 3],
    [2, 2],
    [0, 2],
  ] as const) {
    const g = grids()[group]!;
    const before = [...g.children];
    (before[at] as HTMLElement).click();
    const after = grids()[group]!;
    equal(`group ${group}: the clicked choice is the checked one`, checked(after), [at]);
    check(`group ${group}: marks follow the choice`, marked(after));
    check(
      `group ${group}: the choice buttons are the same nodes`,
      [...after.children].every((b, i) => b === before[i]),
    );
    check(`group ${group}: the dialog stays open`, dialog().open);
  }
  equal("a color theme leaves the switch unchecked", checked(q(dialog(), ".theme-switch")), []);

  // The theme switch inside the dialog's form picks the theme and does not submit the form.
  const dark = sw.children[2] as HTMLElement;
  dark.click();
  await new Promise((r) => setTimeout(r, 50));
  check("the theme switch keeps the dialog open", dialog().open);
  equal("the theme switch sets the theme", document.documentElement.dataset["theme"], "dark");
  equal("the theme switch marks its choice", checked(q(dialog(), ".theme-switch")), [2]);
  check("the theme switch keeps its node", q(dialog(), ".theme-switch") === sw);

  q(dialog(), ".dialog-actions .btn").click();
  await new Promise((r) => setTimeout(r, 50));
  check("Done closes and removes the dialog", document.querySelector("dialog.appearance") === null);
});
