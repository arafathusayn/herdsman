// Preloaded before every test file (bunfig.toml): views compile their
// templates when their module loads, so a document must exist before any
// test imports app code. Test files install a fresh one as they need.
import { installFakeDocument } from "./herdsman-blocks.testdom.ts";

installFakeDocument();
