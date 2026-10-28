// Standings from several run.ts logs pooled: each entry's samples from every
// log given for it, medians and the official weighted score. On a noisy
// machine one run swings the score by several hundredths; pooling steadies it.
//
//   bun jfb/pool.ts <logs for the other entries> <logs for herdsman-react>
//
// Each argument is a comma-separated list of saved run.ts outputs; the second
// lets ours come from runs of its current build only.
const W: Record<string, number> = { "01_run1k": 0.64280248137063, "02_replace1k": 0.5607178150466176, "03_update10th1k_x16": 0.5643800750716564, "04_select1k": 0.1925635870170522, "05_swap1k": 0.13200612879341714, "06_remove-one-1k": 0.5277091212292658, "07_create10k": 0.5644449600965534, "08_create1k-after1k_x2": 0.5508359820582848, "09_clear1k_x8": 0.4225836631419211 };
const others = process.argv[2]!.split(","),
  ours = (process.argv[3] ?? process.argv[2]!).split(",");
const s = new Map<string, number[]>();
for (const [files, onlyOurs] of [[others, false], [ours, true]] as const)
  for (const f of files)
    for (const line of (await Bun.file(f).text()).split("\n")) {
      const m = /^(\S+)\s+(\S+)\s+median\s+\S+\s+(.*)$/.exec(line);
      if (!m) continue;
      if ((m[2] === "herdsman-react") !== onlyOurs) continue;
      const k = m[2] + "|" + m[1];
      s.set(k, [...(s.get(k) ?? []), ...m[3]!.trim().split(/\s+/).map(Number)]);
    }
const med = (x: number[]) => { const a = [...x].sort((p, q) => p - q); return a.length % 2 ? a[(a.length - 1) / 2]! : (a[a.length / 2 - 1]! + a[a.length / 2]!) / 2; };
const entries = [...new Set([...s.keys()].map((k) => k.split("|")[0]!))];
const tests = Object.keys(W);
const best = Object.fromEntries(tests.map((t) => [t, Math.min(...entries.map((e) => med(s.get(e + "|" + t) ?? [Infinity])))]));
const score = (e: string) => Math.exp(tests.reduce((a, t) => a + W[t]! * Math.log(med(s.get(e + "|" + t)!) / best[t]!), 0) / tests.reduce((a, t) => a + W[t]!, 0));
for (const e of entries.sort((a, b) => score(a) - score(b))) console.log(e.padEnd(16), score(e).toFixed(3), tests.map((t) => `${t.slice(3, 10)} ${(med(s.get(e + "|" + t)!) / best[t]!).toFixed(2)} (n=${s.get(e + "|" + t)!.length})`).join("  "));
