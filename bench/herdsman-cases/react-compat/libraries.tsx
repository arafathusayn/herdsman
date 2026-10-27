// Scenarios with the five React libraries npm downloads most (weekly
// downloads, 2026-10-27): @radix-ui/react-dialog, zustand,
// @tanstack/react-query, react-router and framer-motion. They run like the
// scenarios in ./cases.tsx: the same code on React 19 and on each runtime,
// with every log and step compared. The libraries come from ./package.json
// and import `react` like any app code, so they get the runtime under test.

import * as Dialog from "@radix-ui/react-dialog";
import { QueryClient, QueryClientProvider, useMutation, useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { AnimatePresence, MotionConfig, motion } from "framer-motion";
import { Suspense, useState } from "react";
import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
import {
  createMemoryRouter,
  Link,
  MemoryRouter,
  Outlet,
  Route,
  RouterProvider,
  Routes,
  useLoaderData,
  useNavigate,
  useParams,
} from "react-router";
import { create } from "zustand";
import type { T } from "./cases.tsx";

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

function mount(t: T, el: React.ReactNode): void {
  const r = createRoot(t.root);
  flushSync(() => r.render(el));
}

const key = (el: Element, k: string) => el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));
const active = () => {
  const a = document.activeElement as HTMLElement | null;
  return a === null || a === document.body ? "body" : a.nodeName.toLowerCase() + ":" + (a.textContent ?? "");
};

export const libraryCases: Record<string, (t: T) => Promise<void>> = {
  async "@radix-ui/react-dialog: open, focus, escape, controlled"(t) {
    function App() {
      const [open, setOpen] = useState(false);
      return (
        <div>
          <Dialog.Root>
            <Dialog.Trigger id="trigger">Open</Dialog.Trigger>
            <Dialog.Portal container={t.other}>
              <Dialog.Overlay className="overlay" />
              <Dialog.Content className="content">
                <Dialog.Title>Title</Dialog.Title>
                <Dialog.Description>Description</Dialog.Description>
                <input id="field" />
                <Dialog.Close id="close">Close</Dialog.Close>
              </Dialog.Content>
            </Dialog.Portal>
          </Dialog.Root>
          <Dialog.Root open={open} onOpenChange={(o) => (t.log("controlled change", o), setOpen(o))}>
            <Dialog.Trigger id="trigger2">Controlled {String(open)}</Dialog.Trigger>
            <Dialog.Portal container={t.other}>
              <Dialog.Content aria-describedby={undefined}>
                <Dialog.Title>Second</Dialog.Title>
                <Dialog.Close id="close2">x</Dialog.Close>
              </Dialog.Content>
            </Dialog.Portal>
          </Dialog.Root>
        </div>
      );
    }
    mount(t, <App />);
    await t.step("closed");
    t.$("#trigger").click();
    await wait(50);
    await t.step("open");
    t.log("focus", active());
    key(t.$(".content"), "Escape");
    await wait(50);
    await t.step("escaped");
    t.log("focus", active());
    t.$("#trigger").click();
    await wait(50);
    t.$("#close").click();
    await wait(50);
    await t.step("closed by button");
    t.$("#trigger2").click();
    await wait(50);
    await t.step("controlled open");
    t.$("#close2").click();
    await wait(50);
    await t.step("controlled closed");
  },

  async "zustand: slices, selectors, updates from outside React"(t) {
    const useStore = create<{ count: number; name: string; inc(): void; rename(n: string): void }>((set) => ({
      count: 0,
      name: "ann",
      inc: () => set((s) => ({ count: s.count + 1 })),
      rename: (name) => set({ name }),
    }));
    const renders: string[] = [];
    function Count() {
      const count = useStore((s) => s.count);
      const inc = useStore((s) => s.inc);
      renders.push("count");
      return (
        <button id="inc" onClick={inc}>
          {count}
        </button>
      );
    }
    function Name() {
      const name = useStore((s) => s.name);
      renders.push("name");
      return <b>{name}</b>;
    }
    mount(
      t,
      <p>
        <Count />
        <Name />
      </p>,
    );
    t.$("#inc").click();
    t.$("#inc").click();
    await t.step("clicked twice");
    t.log(renders.splice(0).join(" "));
    useStore.getState().rename("bob");
    await t.step("renamed outside");
    t.log(renders.splice(0).join(" "));
    useStore.setState({ count: 10, name: "cy" });
    await t.step("set both");
    t.log(renders.splice(0).join(" "));
  },

  async "@tanstack/react-query: queries, refetch, mutations, suspense"(t) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } });
    let n = 0;
    const releases: (() => void)[] = [];
    const fetchTodo = (id: number) =>
      new Promise<string>((resolve) => releases.push(() => resolve(`todo ${id} v${++n}`)));
    const release = async () => {
      releases.shift()?.();
      await wait(20);
    };
    function Todo({ id }: { id: number }) {
      const q = useQuery({ queryKey: ["todo", id], queryFn: () => fetchTodo(id) });
      return (
        <li>
          {q.status}/{q.fetchStatus}: {q.data ?? "-"}
        </li>
      );
    }
    function Save() {
      const m = useMutation({
        mutationFn: async (v: string) => {
          await wait(5);
          return v.toUpperCase();
        },
        onSuccess: () => client.invalidateQueries({ queryKey: ["todo", 1] }),
      });
      return (
        <button id="save" onClick={() => m.mutate("saved")}>
          {m.status}:{m.data ?? ""}
        </button>
      );
    }
    function Sus() {
      const q = useSuspenseQuery({ queryKey: ["sus"], queryFn: () => fetchTodo(9) });
      return <em>{q.data}</em>;
    }
    mount(
      t,
      <QueryClientProvider client={client}>
        <ul>
          <Todo id={1} />
          <Todo id={1} />
          <Todo id={2} />
        </ul>
        <Save />
        <Suspense fallback={<i>suspended</i>}>
          <Sus />
        </Suspense>
      </QueryClientProvider>,
    );
    await t.step("pending");
        // The suspense query fetches during render, first. React reveals
    // suspended content up to 300 ms late, so steps after it wait that out.
    await release();
    await wait(400);
    await t.step("suspense loaded");
    await release();
    await release();
    await t.step("todos loaded");
    t.$("#save").click();
    await wait(30);
    await t.step("mutated, refetching");
    await release();
    await t.step("refetched");
  },

  async "react-router: data router with loaders, links, params, navigation"(t) {
    function Layout() {
      return (
        <div>
          <nav>
            <Link to="/">Home</Link>
            <Link to="/users/42" id="to-user">
              User
            </Link>
          </nav>
          <Outlet />
        </div>
      );
    }
    function User() {
      const { id } = useParams();
      const data = useLoaderData() as { name: string };
      const navigate = useNavigate();
      return (
        <section>
          user {id}: {data.name}
          <button id="back" onClick={() => navigate("/")}>
            back
          </button>
        </section>
      );
    }
    const router = createMemoryRouter(
      [
        {
          path: "/",
          element: <Layout />,
          children: [
            { index: true, element: <h1>home</h1> },
            { path: "users/:id", element: <User />, loader: async ({ params }) => ({ name: "user-" + params["id"] }) },
          ],
        },
      ],
      { initialEntries: ["/"] },
    );
    mount(t, <RouterProvider router={router} />);
    await wait(20);
    await t.step("home");
    t.$("#to-user").click();
    await wait(50);
    await t.step("user");
    t.log("location", router.state.location.pathname);
    t.$("#back").click();
    await wait(50);
    await t.step("back home");
  },

  async "react-router: declarative routes"(t) {
    function Page({ name }: { name: string }) {
      const navigate = useNavigate();
      return (
        <button id={"go-" + name} onClick={() => navigate(name === "a" ? "/b/7" : "/a")}>
          {name}
        </button>
      );
    }
    function B() {
      return <i>b {useParams()["n"]}</i>;
    }
    mount(
      t,
      <MemoryRouter initialEntries={["/a"]}>
        <Routes>
          <Route path="/a" element={<Page name="a" />} />
          <Route
            path="/b/:n"
            element={
              <>
                <B />
                <Page name="b" />
              </>
            }
          />
        </Routes>
      </MemoryRouter>,
    );
    await t.step("a");
    t.$("#go-a").click();
    await wait(20);
    await t.step("b");
    t.$("#go-b").click();
    await wait(20);
    await t.step("a again");
  },

  async "framer-motion: animate, presence, variants"(t) {
    function App() {
      const [items, setItems] = useState(["x", "y", "z"]);
      const [on, setOn] = useState(false);
      return (
        <MotionConfig transition={{ duration: 0 }}>
          <motion.div id="box" initial={{ opacity: 0, x: 0 }} animate={{ opacity: on ? 0.5 : 1, x: on ? 100 : 0 }} />
          <button id="toggle" onClick={() => setOn(!on)}>
            toggle
          </button>
          <button id="drop" onClick={() => setItems(items.slice(1))}>
            drop
          </button>
          <ul>
            <AnimatePresence onExitComplete={() => t.log("exit complete")}>
              {items.map((i) => (
                <motion.li key={i} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
                  {i}
                </motion.li>
              ))}
            </AnimatePresence>
          </ul>
        </MotionConfig>
      );
    }
    mount(t, <App />);
    await wait(100);
    await t.step("mounted");
    t.$("#toggle").click();
    await wait(100);
    await t.step("toggled");
    t.$("#drop").click();
    await wait(200);
    await t.step("dropped");
  },
};
