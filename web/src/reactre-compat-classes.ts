// reactre-compat with class components (lifecycles, setState, error
// boundaries): alias React to this module instead of reactre-compat.ts when
// the app or a library it uses renders classes.

export * from "./herdsman-react-classes.ts";
export { default } from "./herdsman-react-classes.ts";
