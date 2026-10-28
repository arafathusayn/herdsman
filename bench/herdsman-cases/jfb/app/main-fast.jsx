// The js-framework-benchmark app in plain React, written the way a fast
// React app is: the rows live in a reducer, but the selection lives in a
// small external store that each row reads through useSyncExternalStore,
// keyed by row id. Selecting a row then re-renders the two rows whose
// "selected" changed, not the table. Nothing here is specific to a runtime:
// it runs on React 19 as it does on herdsman-react.

import { memo, useCallback, useReducer, useSyncExternalStore } from "react";
import { createRoot } from "react-dom/client";

const adjectives = ["pretty", "large", "big", "small", "tall", "short", "long", "handsome", "plain", "quaint", "clean", "elegant", "easy", "angry", "crazy", "helpful", "mushy", "odd", "unsightly", "adorable", "important", "inexpensive", "cheap", "expensive", "fancy"];
const colours = ["red", "yellow", "blue", "green", "pink", "brown", "purple", "brown", "white", "black", "orange"];
const nouns = ["table", "chair", "house", "bbq", "desk", "car", "pony", "cookie", "sandwich", "burger", "pizza", "mouse", "keyboard"];

const pick = (list) => list[Math.round(Math.random() * 1000) % list.length];
let nextId = 1;
function build(count) {
  const rows = new Array(count);
  for (let i = 0; i < count; i++) rows[i] = { id: nextId++, label: `${pick(adjectives)} ${pick(colours)} ${pick(nouns)}` };
  return rows;
}

// The selection: one id, and the one listener of each row, by id. Changing
// it tells the rows that were and are now selected, and no other.
let selected = 0;
const listeners = new Map();
function select(id) {
  const was = selected;
  if (was === id) return;
  selected = id;
  listeners.get(was)?.();
  listeners.get(id)?.();
}
function useSelected(id) {
  const subscribe = useCallback(
    (cb) => {
      listeners.set(id, cb);
      return () => {
        if (listeners.get(id) === cb) listeners.delete(id);
      };
    },
    [id],
  );
  return useSyncExternalStore(subscribe, () => selected === id);
}

function reducer(rows, action) {
  switch (action.type) {
    case "run":
      return build(1000);
    case "runLots":
      return build(10000);
    case "add":
      return rows.concat(build(1000));
    case "update": {
      const next = rows.slice();
      for (let i = 0; i < next.length; i += 10) next[i] = { id: next[i].id, label: next[i].label + " !!!" };
      return next;
    }
    case "clear":
      return [];
    case "swapRows": {
      if (rows.length <= 998) return rows;
      const next = rows.slice();
      const a = next[1];
      next[1] = next[998];
      next[998] = a;
      return next;
    }
    case "remove":
      return rows.filter((r) => r.id !== action.id);
  }
  return rows;
}

const Row = memo(function Row({ row, dispatch }) {
  const isSelected = useSelected(row.id);
  return (
    <tr className={isSelected ? "danger" : ""}>
      <td className="col-md-1">{row.id}</td>
      <td className="col-md-4">
        <a onClick={() => select(row.id)}>{row.label}</a>
      </td>
      <td className="col-md-1">
        <a onClick={() => dispatch({ type: "remove", id: row.id })}>
          <span className="glyphicon glyphicon-remove" aria-hidden="true" />
        </a>
      </td>
      <td className="col-md-6" />
    </tr>
  );
});

function Button({ id, title, onClick }) {
  return (
    <div className="col-sm-6 smallpad">
      <button type="button" className="btn btn-primary btn-block" id={id} onClick={onClick}>
        {title}
      </button>
    </div>
  );
}

const Jumbotron = memo(function Jumbotron({ dispatch }) {
  return (
    <div className="jumbotron">
      <div className="row">
        <div className="col-md-6">
          <h1>React keyed</h1>
        </div>
        <div className="col-md-6">
          <div className="row">
            <Button id="run" title="Create 1,000 rows" onClick={() => (select(0), dispatch({ type: "run" }))} />
            <Button id="runlots" title="Create 10,000 rows" onClick={() => (select(0), dispatch({ type: "runLots" }))} />
            <Button id="add" title="Append 1,000 rows" onClick={() => dispatch({ type: "add" })} />
            <Button id="update" title="Update every 10th row" onClick={() => dispatch({ type: "update" })} />
            <Button id="clear" title="Clear" onClick={() => (select(0), dispatch({ type: "clear" }))} />
            <Button id="swaprows" title="Swap Rows" onClick={() => dispatch({ type: "swapRows" })} />
          </div>
        </div>
      </div>
    </div>
  );
});

function Main() {
  const [rows, dispatch] = useReducer(reducer, []);
  return (
    <div className="container">
      <Jumbotron dispatch={dispatch} />
      <table className="table table-hover table-striped test-data">
        <tbody>
          {rows.map((row) => (
            <Row key={row.id} row={row} dispatch={dispatch} />
          ))}
        </tbody>
      </table>
      <span className="preloadicon glyphicon glyphicon-remove" aria-hidden="true" />
    </div>
  );
}

createRoot(document.getElementById("main")).render(<Main />);
