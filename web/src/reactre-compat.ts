// reactre-compat: React's API for apps on reactre. Alias `react`,
// `react-dom` (and their subpaths, `react/jsx-runtime` and
// `react-dom/client` included) to this module and a React app runs on it
// unchanged: function components and hooks, context, memo, refs, portals,
// Suspense and lazy, transitions, synthetic events, controlled fields and
// hydration, with React 19's semantics (bench/herdsman-cases/react-compat.ts
// checks it against React itself, scenario by scenario).
//
// It is the React layer of the herdsman runtime (herdsman-react.ts): React's
// model of re-rendering components with their state, over compiled
// templates. Class components are in reactre-compat-classes.ts. Compile the
// app's JSX with herdsman-react-compiler for templates; reactre's own TSX
// (reactre.ts, reactre-compiler.ts) is the faster model, signals and
// fine-grained updates, for code written for it.

export * from "./herdsman-react.ts";
export { default } from "./herdsman-react.ts";
