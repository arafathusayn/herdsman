// herdsman-react with class components: the same exports as
// herdsman-react.ts, plus the class component runtime (lifecycles,
// setState, error boundaries). Alias `react` and `react-dom` to this module
// instead of herdsman-react.ts when the app or a library it uses
// renders class components; the default module leaves them out and throws a
// clear error when one renders.

import { __installClasses, type ClassCore, type ClassSupport, type Props, shallowEqual } from "./herdsman-react.ts";

export * from "./herdsman-react.ts";
export { default } from "./herdsman-react.ts";

// biome-ignore lint: fibers and instances are opaque here.
type Any = any;

const INSTANCE = Symbol("fiber"); // class instance -> its fiber
const isThenable = (v: Any): boolean => v !== null && typeof v === "object" && typeof v.then === "function";

function support(core: ClassCore): ClassSupport {
  const { DIRTY, FORCE, RERENDER, DEAD, NO_CONTEXT } = core;

  function nextState(f: Any, inst: Any, state: Any, props: Props): Any {
    const q: Any[] | null = f.q;
    if (q === null) return state;
    f.q = null;
    for (const u of q) {
      const part = typeof u === "function" ? u.call(inst, state, props) : u;
      if (part != null) state = { ...state, ...part };
    }
    return state;
  }

  // Class props leave out `ref`: the ref gets the instance.
  function classProps(p: Props): Props {
    if (!("ref" in p)) return p;
    const out = { ...p };
    delete out["ref"];
    return out;
  }

  function boundary(f: Any): boolean {
    return (
      typeof f.t.getDerivedStateFromError === "function" ||
      (f.s !== null && typeof f.s.componentDidCatch === "function")
    );
  }

  function render(f: Any, inst: Any): void {
    const out = core.rendering(f, () => {
      try {
        f.f &= ~RERENDER;
        return inst.render();
      } catch (e) {
        f.f |= DIRTY;
        throw e;
      }
    });
    core.reconcile(f, out);
  }

  function classRef(f: Any, old: Props | null, inst: Any): void {
    const ref = f.p.ref,
      oref = old === null ? undefined : old["ref"];
    if (ref === oref) return;
    if (oref != null) core.detachRef(f, oref);
    if (ref != null) core.later(f, () => core.attachRef(f, ref, inst));
  }

  // setState callbacks, with layout effects.
  function callbacks(f: Any): void {
    const cbs: (() => void)[] | null = f.rx;
    if (cbs === null) return;
    f.rx = null;
    for (const cb of cbs) core.later(f, () => cb.call(f.s));
  }

  function run(f: Any, old: Props | null, mount: boolean): void {
    const C = f.t;
    const props = classProps(f.p);
    const ctxType = C.contextType;
    const context = ctxType != null ? core.readContext(f, ctxType) : NO_CONTEXT;
    let inst = f.s;
    let thrown = f.x;
    f.x = null;
    if (thrown === null) {
      let prevProps: Any = null,
        prevState: Any = null,
        snapshot: Any;
      if (mount) {
        inst = f.s = core.rendering(f, () => new C(props, context));
        inst.props = props;
        inst.context = context;
        if (inst.state === undefined) inst.state = null;
        inst[INSTANCE] = f;
        if (typeof C.getDerivedStateFromProps === "function") {
          const d = C.getDerivedStateFromProps(props, inst.state);
          if (d != null) inst.state = { ...inst.state, ...d };
        } else if (typeof C.getSnapshotBeforeUpdate !== "function") {
          const will = inst.UNSAFE_componentWillMount ?? inst.componentWillMount;
          if (typeof will === "function") {
            will.call(inst);
            inst.state = nextState(f, inst, inst.state, props);
          }
        }
      } else {
        prevProps = inst.props;
        prevState = inst.state;
        const legacy =
          typeof C.getDerivedStateFromProps !== "function" && typeof inst.getSnapshotBeforeUpdate !== "function";
        if (legacy && old !== f.p) {
          const wrp = inst.UNSAFE_componentWillReceiveProps ?? inst.componentWillReceiveProps;
          if (typeof wrp === "function") wrp.call(inst, props, context);
        }
        let state = nextState(f, inst, prevState, props);
        if (typeof C.getDerivedStateFromProps === "function") {
          const d = C.getDerivedStateFromProps(props, state);
          if (d != null) state = { ...state, ...d };
        }
        let go = (f.f & FORCE) !== 0 || inst.context !== context;
        if (!go) {
          if (typeof inst.shouldComponentUpdate === "function")
            go = !!inst.shouldComponentUpdate(props, state, context);
          else if (C.prototype.isPureReactComponent)
            go = !shallowEqual(prevProps, props) || !shallowEqual(prevState, state);
          else go = true;
        }
        f.f &= ~FORCE;
        if (!go) {
          inst.props = props;
          inst.state = state;
          inst.context = context;
          callbacks(f);
          return;
        }
        if (legacy) {
          const wu = inst.UNSAFE_componentWillUpdate ?? inst.componentWillUpdate;
          if (typeof wu === "function") wu.call(inst, props, state, context);
        }
        inst.props = props;
        inst.state = state;
        inst.context = context;
        if (typeof inst.getSnapshotBeforeUpdate === "function")
          snapshot = inst.getSnapshotBeforeUpdate(prevProps, prevState);
      }
      try {
        render(f, inst);
        if (mount) {
          if (typeof inst.componentDidMount === "function") core.later(f, () => inst.componentDidMount());
        } else if (typeof inst.componentDidUpdate === "function") {
          core.later(f, () => inst.componentDidUpdate(prevProps, prevState, snapshot));
        }
        classRef(f, old, inst);
        callbacks(f);
        return;
      } catch (e) {
        if (isThenable(e) || !boundary(f)) throw e;
        thrown = e;
      }
    }
    // Error boundary: drop the children, take the error's state, render again.
    core.removeAll(f);
    f.f &= ~DIRTY;
    const gdsfe = C.getDerivedStateFromError;
    if (typeof gdsfe === "function") {
      const d = gdsfe(thrown);
      if (d != null) inst.state = { ...inst.state, ...d };
    }
    const caught = thrown;
    core.later(f, () => {
      if (typeof inst.componentDidCatch === "function") inst.componentDidCatch(caught, { componentStack: "" });
      const report = f.o.opts.onCaughtError;
      if (report) report(caught, { componentStack: "" });
      else console.error(caught);
    });
    if (typeof gdsfe === "function") render(f, inst);
    classRef(f, old, inst);
  }

  return {
    run,
    boundary,
    enqueue(inst, partial, cb, force) {
      const f = inst[INSTANCE];
      if (f === undefined || f.f & DEAD) return;
      if (partial !== null) (f.q ??= []).push(partial);
      if (force) f.f |= FORCE;
      if (typeof cb === "function") (f.rx ??= []).push(cb);
      if (core.isRendering(f)) f.f |= RERENDER;
      else core.schedule(f);
    },
    unmount(f) {
      const inst = f.s;
      if (inst !== null && typeof inst.componentWillUnmount === "function") inst.componentWillUnmount();
      if (f.p.ref != null) core.detachRef(f, f.p.ref);
    },
  };
}

__installClasses(support);
