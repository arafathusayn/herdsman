// Preloaded before every test file (bunfig.toml): compiled TSX components
// need a document before app imports. Tests install a fresh one as needed.
import { registerReactreTests } from "../reactre.ts";
import { installFakeDocument } from "./test-dom-core.ts";

installFakeDocument();
registerReactreTests();
