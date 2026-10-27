// Scenarios for the React compatibility check (../react-compat.ts). Each one
// renders with the React API, acts on the page like a user, and records what
// it sees: `t.log` lines and, at every `t.step`, the container's HTML. The
// same file is built against React 19 and against herdsman-react, and
// the two transcripts must match line for line.

import {
  Activity,
  Children,
  Component,
  createContext,
  createElement,
  createRef,
  cloneElement,
  forwardRef,
  Fragment,
  isValidElement,
  lazy,
  memo,
  PureComponent,
  type ReactNode,
  StrictMode,
  Suspense,
  use,
  useActionState,
  useCallback,
  useContext,
  useDeferredValue,
  useEffect,
  useId,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useOptimistic,
  useReducer,
  useRef,
  useState,
  useSyncExternalStore,
  useTransition,
} from "react";
import { createPortal, flushSync, useFormStatus } from "react-dom";
import { createRoot, hydrateRoot, type Root } from "react-dom/client";

export interface T {
  /** The container, in the document. */
  readonly root: HTMLElement;
  /** A second container, for portals and second roots. */
  readonly other: HTMLElement;
  log(...parts: unknown[]): void;
  /** Wait for every update and effect to settle, then record the HTML of both containers. */
  step(label: string): Promise<void>;
  /** The element matching `sel` in the document. */
  $(sel: string): HTMLElement;
  /** Server HTML of this scenario, rendered by React's server renderer at build time. */
  readonly ssr: Record<string, string>;
}

type Case = (t: T) => Promise<void>;

/** Rendered on the server (React's renderToString) and then hydrated. */
export function HydrateApp() {
  const [n, setN] = useState(0);
  return (
    <main>
      <h1>Title {n}</h1>
      <button onClick={() => setN(n + 1)}>
        {"a"}
        {"b"} {n}
      </button>
      <ul>
        {["x", "y"].map((k) => (
          <li key={k}>{k}</li>
        ))}
      </ul>
    </main>
  );
}

function mount(t: T, el: ReactNode): Root {
  const r = createRoot(t.root);
  flushSync(() => r.render(el));
  return r;
}

const click = (el: HTMLElement): void => el.click();

function type(el: HTMLElement, value: string): void {
  const input = el as HTMLInputElement;
  // The native setter, as a user's keystroke would leave it.
  const proto = el.nodeName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

export const cases: Record<string, Case> = {
  async "host props and updates"(t) {
    let set!: (n: number) => void;
    function App() {
      const [n, setN] = useState(0);
      set = setN;
      return (
        <div
          id="a"
          className={n % 2 ? "odd" : "even"}
          style={n === 2 ? undefined : { color: "red", width: n * 10, opacity: 0.5, "--x": n, zIndex: n } as object}
          title={n === 1 ? undefined : "t" + n}
          data-n={n}
          aria-hidden={n === 1}
          hidden={n === 3}
          tabIndex={n}
          draggable={n > 0}
          spellCheck={n > 1}
          onClick={undefined}
        >
          <label htmlFor="x">l</label>
          <input id="x" readOnly maxLength={3} disabled={n === 1} value={"v" + n} autoComplete="off" />
          <meta httpEquiv="refresh" />
          {n === 0 ? "zero" : n}
          <span dangerouslySetInnerHTML={{ __html: "<b>" + n + "</b>" }} />
          <img src={n ? "a.png" : undefined} alt="" />
        </div>
      );
    }
    mount(t, <App />);
    await t.step("mount");
    for (let i = 1; i <= 4; i++) {
      set(i);
      await t.step("n=" + i);
    }
  },

  async "svg and namespaces"(t) {
    let set!: (n: number) => void;
    function App() {
      const [n, setN] = useState(1);
      set = setN;
      return (
        <svg viewBox="0 0 10 10" width={10} className="ic">
          <defs>
            <linearGradient id="g" gradientUnits="userSpaceOnUse" />
          </defs>
          <path d="M0 0" strokeWidth={n} strokeLinecap="round" fillOpacity={0.5} />
          <use xlinkHref="#g" />
          <foreignObject>
            <div className="in">html</div>
          </foreignObject>
          {n > 1 && <circle r={n} />}
        </svg>
      );
    }
    mount(t, <App />);
    await t.step("mount");
    t.log(t.$("svg path").namespaceURI, t.$("svg .in").namespaceURI, t.$("svg use").getAttributeNS("http://www.w3.org/1999/xlink", "href"));
    set(2);
    await t.step("update");
    t.log(t.$("circle").namespaceURI);
  },

  async "keyed lists"(t) {
    let set!: (k: string[]) => void;
    function Row({ k }: { k: string }) {
      return <li>{k}</li>;
    }
    function App() {
      const [keys, setKeys] = useState(["a", "b", "c", "d", "e"]);
      set = setKeys;
      return (
        <ul>
          {keys.map((k) => (
            <Row key={k} k={k} />
          ))}
        </ul>
      );
    }
    mount(t, <App />);
    await t.step("mount");
    const ids = new Map<string, Node>();
    for (const li of t.root.querySelectorAll("li")) ids.set(li.textContent!, li);
    const same = () => [...t.root.querySelectorAll("li")].map((li) => (ids.get(li.textContent!) === li ? "=" : "+")).join("");
    for (const next of [
      ["e", "d", "c", "b", "a"],
      ["a", "b", "c", "d", "e"],
      ["b", "a", "c", "d", "e"],
      ["a", "x", "b", "c", "y", "d", "e"],
      ["e", "c", "a"],
      [],
      ["c", "a", "e"],
      ["a", "c", "e", "b", "d"],
    ]) {
      set(next);
      await t.step(next.join(","));
      t.log(same());
    }
  },

  async "fragments, arrays and conditionals keep sibling state"(t) {
    let toggle!: () => void;
    function Counter({ name }: { name: string }) {
      const [n, setN] = useState(0);
      return (
        <button id={name} onClick={() => setN(n + 1)}>
          {name}:{n}
        </button>
      );
    }
    function App() {
      const [on, setOn] = useState(false);
      toggle = () => setOn((v) => !v);
      return (
        <>
          {on && <b>first</b>}
          <Counter name="c1" />
          {on ? null : <i>maybe</i>}
          <>
            <Counter name="c2" />
            {[<span key="x">x</span>, [<em key="y">y</em>, "text", 3]]}
          </>
          {on ? <Fragment key="f1">k1</Fragment> : <Fragment key="f2">k2</Fragment>}
          {false}
          {null}
          {undefined}
          {0}
          {""}
        </>
      );
    }
    mount(t, <App />);
    click(t.$("#c1"));
    click(t.$("#c2"));
    click(t.$("#c2"));
    await t.step("clicked");
    toggle();
    await t.step("toggled on");
    toggle();
    await t.step("toggled off");
  },

  async "batching and render counts"(t) {
    let renders = 0;
    function App() {
      const [a, setA] = useState(0);
      const [b, setB] = useState(0);
      renders++;
      return (
        <button
          onClick={() => {
            setA((x) => x + 1);
            setA((x) => x + 1);
            setB(b + 1);
            t.log("in handler", a, b);
          }}
        >
          {a},{b}
        </button>
      );
    }
    mount(t, <App />);
    click(t.$("button"));
    await t.step("click");
    t.log("renders", renders);
    setTimeout(() => click(t.$("button")), 0);
    await t.step("click from timeout");
    t.log("renders", renders);
  },

  async "reducer and setState during render"(t) {
    function reducer(s: { n: number }, a: string) {
      return a === "inc" ? { n: s.n + 1 } : a === "same" ? s : { n: 0 };
    }
    let renders = 0;
    function App({ v }: { v: number }) {
      const [s, dispatch] = useReducer(reducer, 5, (x: number) => ({ n: x }));
      const [prev, setPrev] = useState(v);
      const [derived, setDerived] = useState(0);
      if (prev !== v) {
        setPrev(v);
        setDerived(v * 2);
      }
      renders++;
      return (
        <div>
          <button id="inc" onClick={() => dispatch("inc")} />
          <button id="same" onClick={() => dispatch("same")} />
          {s.n}/{derived}
        </div>
      );
    }
    const r = mount(t, <App v={1} />);
    click(t.$("#inc"));
    await t.step("inc");
    const before = renders;
    click(t.$("#same"));
    await t.step("same");
    t.log("rendered again", renders - before > 1);
    flushSync(() => r.render(<App v={2} />));
    await t.step("v=2");
  },

  async "effect order"(t) {
    function Child({ name, n }: { name: string; n: number }) {
      useLayoutEffect(() => {
        t.log("layout", name, n);
        return () => t.log("layout cleanup", name, n);
      }, [n]);
      useEffect(() => {
        t.log("effect", name, n);
        return () => t.log("effect cleanup", name, n);
      }, [n]);
      useEffect(() => {
        t.log("every render", name);
      });
      useEffect(() => {
        t.log("once", name);
        return () => t.log("once cleanup", name);
      }, []);
      return <i>{name}</i>;
    }
    function App({ n, show }: { n: number; show: boolean }) {
      useLayoutEffect(() => {
        t.log("parent layout", n);
        return () => t.log("parent layout cleanup", n);
      });
      useEffect(() => {
        t.log("parent effect", n);
        return () => t.log("parent effect cleanup", n);
      });
      return (
        <div>
          <Child name="a" n={n} />
          {show && <Child name="b" n={n} />}
        </div>
      );
    }
    const r = mount(t, <App n={1} show />);
    await t.step("mount");
    flushSync(() => r.render(<App n={2} show />));
    await t.step("update");
    flushSync(() => r.render(<App n={2} show={false} />));
    await t.step("hide b");
    r.unmount();
    await t.step("unmount");
  },

  async "layout effect updates render before paint"(t) {
    function App() {
      const [w, setW] = useState(0);
      const ref = useRef<HTMLDivElement>(null);
      useLayoutEffect(() => {
        if (w === 0) setW(ref.current!.childNodes.length + 10);
      }, [w]);
      useEffect(() => {
        t.log("effect sees", w);
      });
      return <div ref={ref}>w={w}</div>;
    }
    mount(t, <App />);
    t.log("after flushSync", t.root.textContent);
    await t.step("settled");
  },

  async "memo, useMemo, useCallback"(t) {
    const renders: string[] = [];
    const Leaf = memo(function Leaf({ v, on }: { v: number; on: () => void }) {
      renders.push("leaf" + v);
      return <span onClick={on}>{v}</span>;
    });
    const Custom = memo(
      function Custom({ v }: { v: { id: number; x: number } }) {
        renders.push("custom" + v.x);
        return <b>{v.x}</b>;
      },
      (a, b) => a.v.id === b.v.id,
    );
    let bump!: () => void;
    function App() {
      const [n, setN] = useState(0);
      bump = () => setN((x) => x + 1);
      const half = useMemo(() => {
        renders.push("memo");
        return Math.floor(n / 2);
      }, [n >> 1]);
      const on = useCallback(() => {}, []);
      return (
        <div>
          <Leaf v={half} on={on} />
          <Custom v={{ id: half, x: n }} />
          {n}
        </div>
      );
    }
    mount(t, <App />);
    for (let i = 0; i < 4; i++) {
      bump();
      await t.step("bump");
      t.log(renders.splice(0).join(" "));
    }
  },

  async "context through memo, defaults and consumers"(t) {
    const Theme = createContext("light");
    const Count = createContext({ n: 0 });
    let renders = 0;
    const Frozen = memo(function Frozen() {
      renders++;
      return <Reader />;
    });
    function Reader() {
      const theme = useContext(Theme);
      const c = use(Count);
      return (
        <p>
          {theme}:{c.n}
        </p>
      );
    }
    class ClassReader extends Component {
      static contextType = Theme;
      override render() {
        return <em>{String(this.context)}</em>;
      }
    }
    let set!: (s: string) => void;
    function App() {
      const [theme, setTheme] = useState("dark");
      set = setTheme;
      return (
        <div>
          <Reader />
          <Theme value={theme}>
            <Frozen />
            <ClassReader />
            <Theme.Consumer>{(v) => <s>{v}</s>}</Theme.Consumer>
            <Theme.Provider value={theme + "!"}>
              <Count value={{ n: 7 }}>
                <Reader />
              </Count>
            </Theme.Provider>
          </Theme>
        </div>
      );
    }
    mount(t, <App />);
    await t.step("mount");
    set("blue");
    await t.step("changed");
    t.log("frozen renders", renders);
  },

  async "class lifecycle"(t) {
    class Child extends Component<{ n: number }, { s: number }> {
      override state = { s: 0 };
      static getDerivedStateFromProps(p: { n: number }, s: { s: number }) {
        return p.n > 5 ? { s: s.s + 100 } : null;
      }
      override componentDidMount() {
        t.log("did mount", this.props.n);
      }
      override shouldComponentUpdate(next: { n: number }) {
        t.log("should update", this.props.n, next.n);
        return next.n !== 3;
      }
      override getSnapshotBeforeUpdate(prev: { n: number }) {
        return "snap" + prev.n;
      }
      override componentDidUpdate(prev: { n: number }, _ps: unknown, snap: unknown) {
        t.log("did update", prev.n, this.props.n, snap);
      }
      override componentWillUnmount() {
        t.log("will unmount");
      }
      override render() {
        return (
          <button onClick={() => this.setState((s) => ({ s: s.s + 1 }), () => t.log("callback", this.state.s))}>
            {this.props.n}/{this.state.s}
          </button>
        );
      }
    }
    class Pure extends PureComponent<{ v: number }> {
      override render() {
        t.log("pure render", this.props.v);
        return <i>{this.props.v}</i>;
      }
    }
    const r = mount(
      t,
      <>
        <Child n={1} />
        <Pure v={1} />
      </>,
    );
    click(t.$("button"));
    await t.step("clicked");
    for (const n of [2, 3, 6]) {
      flushSync(() =>
        r.render(
          <>
            <Child n={n} />
            <Pure v={n > 2 ? 2 : 1} />
          </>,
        ),
      );
      await t.step("n=" + n);
    }
    flushSync(() => r.render(null));
    await t.step("unmounted");
  },

  async "error boundaries"(t) {
    class Boundary extends Component<{ children: ReactNode }, { error: string | null }> {
      override state = { error: null as string | null };
      static getDerivedStateFromError(e: Error) {
        return { error: e.message };
      }
      override componentDidCatch(e: Error) {
        t.log("caught", e.message);
      }
      override render() {
        return this.state.error ? (
          <p onClick={() => this.setState({ error: null })}>error: {this.state.error}</p>
        ) : (
          this.props.children
        );
      }
    }
    let boom!: (b: boolean) => void;
    function Bomb() {
      const [b, setB] = useState(false);
      boom = setB;
      useEffect(() => () => t.log("bomb cleanup"), []);
      if (b) throw new Error("kaboom");
      return <span>ok</span>;
    }
    function Sibling() {
      useEffect(() => () => t.log("sibling cleanup"), []);
      return <i>sib</i>;
    }
    const prev = console.error;
    console.error = () => {};
    try {
      mount(
        t,
        <div>
          <Boundary>
            <Sibling />
            <Bomb />
          </Boundary>
        </div>,
      );
      await t.step("mount");
      boom(true);
      await t.step("thrown");
    } finally {
      console.error = prev;
    }
  },

  async "suspense, lazy and use(promise)"(t) {
    let resolve!: (v: string) => void;
    const p = new Promise<string>((r) => (resolve = r));
    function Data() {
      const v = use(p);
      return <b>{v}</b>;
    }
    let resolveLazy!: (m: { default: () => ReactNode }) => void;
    const Lazy = lazy(() => new Promise<{ default: () => ReactNode }>((r) => (resolveLazy = r)));
    function Effects({ name }: { name: string }) {
      useEffect(() => {
        t.log("effect", name);
      }, [name]);
      return <i>{name}</i>;
    }
    mount(
      t,
      <div>
        <Suspense fallback={<span>loading</span>}>
          <Effects name="inside" />
          <Data />
        </Suspense>
        <Suspense fallback="lazy...">
          <Lazy />
        </Suspense>
      </div>,
    );
    await t.step("fallbacks");
        // React holds revealed content back for up to 300 ms after a fallback shows.
    const reveal = () => new Promise((r) => setTimeout(r, 400));
    resolve("data");
    await p;
    await reveal();
    await t.step("data");
    resolveLazy({ default: () => <u>lazy</u> });
    await reveal();
    await t.step("lazy");
  },

  async "portals bubble through the React tree"(t) {
    function App() {
      const [n, setN] = useState(0);
      return (
        <div onClick={() => (t.log("parent saw click"), setN(n + 1))}>
          parent {n}
          {createPortal(<button id="in-portal">portal {n}</button>, t.other)}
        </div>
      );
    }
    mount(t, <App />);
    await t.step("mount");
    click(t.$("#in-portal"));
    await t.step("clicked");
  },

  async "events: bubbling, capture, stopPropagation, synthetic fields"(t) {
    function App() {
      return (
        <div
          id="outer"
          onClickCapture={(e) => t.log("outer capture", e.currentTarget.id, (e.target as HTMLElement).id, e.eventPhase)}
          onClick={(e) => t.log("outer bubble", e.currentTarget.id, e.type, typeof e.nativeEvent, e.isPropagationStopped())}
        >
          <div id="mid" onClick={(e) => t.log("mid", e.currentTarget.id, e.button, e.shiftKey)}>
            <button id="inner" onClick={(e) => t.log("inner", e.currentTarget.id, e.isDefaultPrevented())}>
              go
            </button>
            <button
              id="stop"
              onClick={(e) => {
                e.stopPropagation();
                e.preventDefault();
                t.log("stop", e.isPropagationStopped(), e.isDefaultPrevented(), e.defaultPrevented);
              }}
            >
              stop
            </button>
          </div>
                    <span id="dbl" onDoubleClick={(e) => t.log("dbl", e.type)} />
          <p id="hover" onMouseEnter={(e) => t.log("enter p", e.type, (e.target as HTMLElement).id)} onMouseLeave={(e) => t.log("leave p", (e.relatedTarget as HTMLElement | null)?.id)}>
            <b id="hb" onMouseEnter={() => t.log("enter b")} onMouseLeave={() => t.log("leave b")}>
              b
            </b>
          </p>
          <input id="f" onFocus={(e) => t.log("focus", e.type)} onBlur={(e) => t.log("blur", e.type)} />
        </div>
      );
    }
    mount(t, <App />);
    click(t.$("#inner"));
    click(t.$("#stop"));
    t.$("#dbl").dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
        const move = (from: Element, to: Element) => {
      from.dispatchEvent(new MouseEvent("mouseout", { bubbles: true, relatedTarget: to }));
      to.dispatchEvent(new MouseEvent("mouseover", { bubbles: true, relatedTarget: from }));
    };
    move(document.body, t.$("#hb"));
    move(t.$("#hb"), t.$("#hover"));
    move(t.$("#hover"), t.$("#dbl"));
    t.$("#dbl").dispatchEvent(new MouseEvent("mouseenter", { bubbles: false }));
    t.$("#f").focus();
    t.$("#f").blur();
    await t.step("events");
  },

  async "controlled and uncontrolled form fields"(t) {
    function App() {
      const [text, setText] = useState("ab");
      const [upper, setUpper] = useState("X");
      const [checked, setChecked] = useState(false);
      const [pick, setPick] = useState("b");
      const [area, setArea] = useState("hello");
      return (
        <form>
          <input id="text" value={text} onChange={(e) => setText(e.target.value.slice(0, 4))} />
          <input id="upper" value={upper} onChange={(e) => setUpper(e.target.value.toUpperCase())} />
          <input id="locked" value="fixed" onChange={() => {}} />
          <input id="free" defaultValue="free" />
          <input id="cb" type="checkbox" checked={checked} onChange={(e) => setChecked(e.target.checked)} />
          <input id="cb2" type="checkbox" defaultChecked />
          <select id="sel" value={pick} onChange={(e) => setPick(e.target.value)}>
            <option value="a">A</option>
            <option value="b">B</option>
            <option value="c">C</option>
          </select>
          <select id="multi" multiple value={["a", "c"]} onChange={() => {}}>
            <option value="a">A</option>
            <option value="b">B</option>
            <option value="c">C</option>
          </select>
          <textarea id="area" value={area} onChange={(e) => setArea(e.target.value)} />
          <p>
            {text}|{upper}|{String(checked)}|{pick}|{area}
          </p>
        </form>
      );
    }
    mount(t, <App />);
    await t.step("mount");
    const val = (id: string) => (t.$(id) as HTMLInputElement).value;
    type(t.$("#text"), "abcdefg");
    type(t.$("#upper"), "xyz");
    type(t.$("#locked"), "changed");
    type(t.$("#area"), "bye");
    click(t.$("#cb"));
    const sel = t.$("#sel") as HTMLSelectElement;
    sel.value = "c";
    sel.dispatchEvent(new Event("change", { bubbles: true }));
    await t.step("typed");
    t.log(val("#text"), val("#upper"), val("#locked"), val("#free"), val("#area"), (t.$("#cb") as HTMLInputElement).checked);
    t.log(
      [...(t.$("#multi") as HTMLSelectElement).options].map((o) => o.selected).join(),
      (t.$("#sel") as HTMLSelectElement).value,
      (t.$("#cb2") as HTMLInputElement).checked,
    );
  },

  async "refs: object, callback with cleanup, forwardRef, imperative handle, ref as prop"(t) {
    const obj = createRef<HTMLDivElement>();
    const Fancy = forwardRef<HTMLButtonElement, { label: string }>(function Fancy({ label }, ref) {
      return <button ref={ref}>{label}</button>;
    });
    function AsProp({ ref }: { ref?: React.Ref<HTMLElement> }) {
      return <section ref={ref}>prop</section>;
    }
    const Handle = forwardRef<{ hello(): string }>(function Handle(_props, ref) {
      useImperativeHandle(ref, () => ({ hello: () => "hi" }), []);
      return null;
    });
    function App({ show }: { show: boolean }) {
      const fancy = useRef<HTMLButtonElement>(null);
      const handle = useRef<{ hello(): string }>(null);
      const prop = useRef<HTMLElement>(null);
      useLayoutEffect(() => {
        t.log("layout sees", obj.current?.id, fancy.current?.textContent, handle.current?.hello(), prop.current?.nodeName);
      });
      return (
        <div id="obj" ref={obj}>
          {show && (
            <i
              ref={(el) => {
                t.log("callback ref", el?.nodeName);
                return () => t.log("callback cleanup");
              }}
            />
          )}
          <Fancy label="fancy" ref={fancy} />
          <Handle ref={handle} />
          <AsProp ref={prop} />
        </div>
      );
    }
    const r = mount(t, <App show />);
    await t.step("mount");
    flushSync(() => r.render(<App show={false} />));
    await t.step("hidden");
    flushSync(() => r.render(null));
    t.log("after unmount", obj.current);
  },

  async "external stores"(t) {
    let value = 1;
    const subs = new Set<() => void>();
    const store = {
      subscribe(cb: () => void) {
        subs.add(cb);
        return () => subs.delete(cb);
      },
      get: () => value,
      set(v: number) {
        value = v;
        for (const s of subs) s();
      },
    };
    function A() {
      return <b>{useSyncExternalStore(store.subscribe, store.get)}</b>;
    }
    function B() {
      const v = useSyncExternalStore(store.subscribe, () => value % 2);
      return <i>{v}</i>;
    }
    const r = mount(
      t,
      <p>
        <A />
        <B />
      </p>,
    );
    await t.step("mount");
    store.set(2);
    await t.step("set 2");
    store.set(4);
    await t.step("set 4");
    r.unmount();
    t.log("subs after unmount", subs.size);
  },

  async "transitions, deferred values and optimistic state"(t) {
    let start!: () => void;
    function App() {
      const [n, setN] = useState(0);
      const [pending, startTransition] = useTransition();
      const deferred = useDeferredValue(n);
      const [optimistic, addOptimistic] = useOptimistic(n, (s, add: number) => s + add);
      start = () =>
        startTransition(async () => {
          addOptimistic(100);
          await new Promise((r) => setTimeout(r, 5));
          startTransition(() => setN((x) => x + 1));
        });
      return (
        <p>
          {n}/{deferred}/{optimistic}/{String(pending)}
        </p>
      );
    }
    mount(t, <App />);
    await t.step("mount");
    flushSync(() => start());
    t.log("optimistic now", t.root.textContent);
    await new Promise((r) => setTimeout(r, 30));
    await t.step("done");
  },

  async "actions: useActionState, form actions, useFormStatus"(t) {
    function Status() {
      const s = useFormStatus();
      return <i>{s.pending ? "sending " + String(s.data?.get("name")) : "idle"}</i>;
    }
    let release!: () => void;
    function App() {
      const [state, act, pending] = useActionState(async (prev: string, data: FormData) => {
        await new Promise<void>((r) => (release = r));
        return prev + "+" + String(data.get("name"));
      }, "start");
      return (
        <form action={act}>
          <input name="name" defaultValue="ann" />
          <button id="send">send</button>
          <Status />
          <p>
            {state}/{String(pending)}
          </p>
        </form>
      );
    }
    mount(t, <App />);
    await t.step("mount");
    (t.$("form input") as HTMLInputElement).value = "bob";
    click(t.$("#send"));
    await t.step("pending");
    release();
    await new Promise((r) => setTimeout(r, 10));
    await t.step("done");
    t.log("field after action", (t.$("form input") as HTMLInputElement).value);
  },

  async "children utilities and elements"(t) {
    function List({ children }: { children: ReactNode }) {
      t.log("count", Children.count(children));
      t.log(
        "keys",
        Children.toArray(children)
          .map((c) => (isValidElement(c) ? c.key : String(c)))
          .join(" "),
      );
      return <ul>{Children.map(children, (c, i) => (isValidElement(c) ? cloneElement(c as React.ReactElement<{ title?: string }>, { title: "i" + i }) : <li>{c}</li>))}</ul>;
    }
    mount(
      t,
      <List>
        <li key="a">a</li>
        {[<li key="b">b</li>, <li key="c">c</li>]}
        text
        {null}
        {[["deep"]]}
        <li>no key</li>
      </List>,
    );
    await t.step("mount");
    t.log(isValidElement(<b />), isValidElement({}), Children.only(<b />).type);
    const el = createElement("a", { href: "x", key: "k" }, "one", "two");
    t.log(el.key, JSON.stringify(el.props));
  },

  async "activity hides and keeps state"(t) {
    let set!: (m: "visible" | "hidden") => void;
    function Counter() {
      const [n, setN] = useState(0);
      useLayoutEffect(() => {
        t.log("layout on");
        return () => t.log("layout off");
      }, []);
      return <button onClick={() => setN(n + 1)}>{n}</button>;
    }
    function App() {
      const [mode, setMode] = useState<"visible" | "hidden">("visible");
      set = setMode;
      return (
        <div>
          <Activity mode={mode}>
            <Counter />
            text
          </Activity>
        </div>
      );
    }
    mount(t, <App />);
    click(t.$("button"));
    await t.step("clicked");
    set("hidden");
    await t.step("hidden");
    set("visible");
    await t.step("visible");
  },

  async "hydration adopts server HTML"(t) {
    t.root.innerHTML = t.ssr["hydrate"]!;
    const before = t.root.querySelector("button");
    flushSync(() => hydrateRoot(t.root, <HydrateApp />));
    await t.step("hydrated");
    t.log("same button", t.root.querySelector("button") === before);
    click(t.$("button"));
    await t.step("clicked");
  },

  async "roots: render values, replace content, unmount"(t) {
    t.root.innerHTML = "<p>old</p>";
    const r = createRoot(t.root);
    for (const v of ["str", 42, null, [1, "two"], <b key="b">el</b>, false]) {
      flushSync(() => r.render(v as ReactNode));
      await t.step(JSON.stringify(v));
    }
    const second = createRoot(t.other);
    flushSync(() =>
      second.render(
        <StrictMode>
          <i>second</i>
        </StrictMode>,
      ),
    );
    await t.step("second root");
    second.unmount();
    r.unmount();
    await t.step("unmounted");
  },

  async "useId is stable and unique"(t) {
    function Field() {
      const id = useId();
      return (
        <>
          <label htmlFor={id}>l</label>
          <input id={id} />
        </>
      );
    }
    mount(
      t,
      <div>
        <Field />
        <Field />
      </div>,
    );
    const ids = [...t.root.querySelectorAll("input")].map((i) => i.id);
    t.log(ids[0] !== ids[1], ids.every((id) => t.root.querySelector(`label[for="${CSS.escape(id)}"]`) !== null));
  },
};
