// The js-framework-benchmark app in plain React: one reducer holds the rows
// and the selection, rows are memoized components keyed by id, and handlers
// dispatch actions. Nothing here is specific to a runtime: jfb/build.ts
// bundles this file once with React 19 and once with herdsman-react.

import { memo, useReducer } from "react";
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

const initial = { rows: [], selected: 0 };

function reducer(state, action) {
  switch (action.type) {
    case "run":
      return { rows: build(1000), selected: 0 };
    case "runLots":
      return { rows: build(10000), selected: 0 };
    case "add":
      return { rows: state.rows.concat(build(1000)), selected: state.selected };
    case "update": {
      const rows = state.rows.slice();
      for (let i = 0; i < rows.length; i += 10) rows[i] = { id: rows[i].id, label: rows[i].label + " !!!" };
      return { rows, selected: state.selected };
    }
    case "clear":
      return initial;
    case "swapRows": {
      if (state.rows.length <= 998) return state;
      const rows = state.rows.slice();
      const a = rows[1];
      rows[1] = rows[998];
      rows[998] = a;
      return { rows, selected: state.selected };
    }
    case "remove":
      return { rows: state.rows.filter((r) => r.id !== action.id), selected: state.selected };
    case "select":
      return { rows: state.rows, selected: action.id };
  }
  return state;
}

const Row = memo(function Row({ row, selected, dispatch }) {
  return (
    <tr className={selected ? "danger" : ""}>
      <td className="col-md-1">{row.id}</td>
      <td className="col-md-4">
        <a onClick={() => dispatch({ type: "select", id: row.id })}>{row.label}</a>
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
            <Button id="run" title="Create 1,000 rows" onClick={() => dispatch({ type: "run" })} />
            <Button id="runlots" title="Create 10,000 rows" onClick={() => dispatch({ type: "runLots" })} />
            <Button id="add" title="Append 1,000 rows" onClick={() => dispatch({ type: "add" })} />
            <Button id="update" title="Update every 10th row" onClick={() => dispatch({ type: "update" })} />
            <Button id="clear" title="Clear" onClick={() => dispatch({ type: "clear" })} />
            <Button id="swaprows" title="Swap Rows" onClick={() => dispatch({ type: "swapRows" })} />
          </div>
        </div>
      </div>
    </div>
  );
});

function Main() {
  const [{ rows, selected }, dispatch] = useReducer(reducer, initial);
  return (
    <div className="container">
      <Jumbotron dispatch={dispatch} />
      <table className="table table-hover table-striped test-data">
        <tbody>
          {rows.map((row) => (
            <Row key={row.id} row={row} selected={selected === row.id} dispatch={dispatch} />
          ))}
        </tbody>
      </table>
      <span className="preloadicon glyphicon glyphicon-remove" aria-hidden="true" />
    </div>
  );
}

createRoot(document.getElementById("main")).render(<Main />);
