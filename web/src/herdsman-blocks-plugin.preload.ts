// Runs tests with views compiled: bun test --preload ./src/herdsman-blocks-plugin.preload.ts <files>
// Template mistakes are left to run time, so tests that expect them to throw still can.
import { plugin } from "bun";

import { herdsmanBlocks } from "./herdsman-blocks-plugin.ts";

plugin(herdsmanBlocks({ mistakes: "runtime", quiet: true }));
