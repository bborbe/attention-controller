import { useCallback, useEffect, useState } from "react";
import type { Item } from "./types";
import { fetchBoard, subscribeItems } from "./api";
import Card from "./Card";
import { useHeldAnswer } from "./useHeldAnswer";

const HIDE_PARAM = "hide";
const HIDE_NONE = "none";
const FLAGS_PARAM = "flags";
const FLAGS_NONE = "none";
const KIND_PARAM = "producer_kind";
const KIND_ALL = "all";
const KIND_DEFAULT = "manager";

interface Filters {
  hideOn: boolean;
  flagsOn: boolean;
  kind: string;
}

function readFilters(): Filters {
  const p = new URLSearchParams(window.location.search);
  return {
    hideOn: p.get(HIDE_PARAM) !== HIDE_NONE,
    flagsOn: p.get(FLAGS_PARAM) !== FLAGS_NONE,
    kind: p.get(KIND_PARAM) || KIND_DEFAULT,
  };
}

function writeFilter(name: string, value: string | null) {
  const url = new URL(window.location.href);
  if (value === null) url.searchParams.delete(name);
  else url.searchParams.set(name, value);
  window.history.replaceState(null, "", url);
}

export default function App() {
  const [items, setItems] = useState<Item[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filters, setFilters] = useState<Filters>(readFilters);

  const load = useCallback(() => {
    fetchBoard()
      .then((next) => {
        setItems(next);
        setError(null);
      })
      .catch((err: Error) => setError(err.message));
  }, []);

  const { held, decide, undo, cancelIfAnswered } = useHeldAnswer(load);

  useEffect(() => {
    load();
    return subscribeItems(load);
  }, [load]);

  // A held answer is cancelled when the store answers that item from another arm
  // (a stream frame carrying it answered), so an Undo can never restore a card the
  // store no longer has open.
  useEffect(() => {
    if (items) {
      cancelIfAnswered(items);
    }
  }, [items, cancelIfAnswered]);

  if (error) return <p className="err">Could not read the board: {error}</p>;
  if (!items) return <p className="subtitle">Loading…</p>;

  const visible = items.filter((item) => {
    const answered = item.state === "answered";
    const flagged = !!item.flagged_at;
    if (held && item.item_id === held.itemID) return false;
    if (filters.hideOn && answered) return false;
    if (filters.flagsOn && flagged) return false;
    if (filters.kind !== KIND_ALL && item.producer_kind !== filters.kind) return false;
    return true;
  });

  function toggleHide() {
    const next = !filters.hideOn;
    writeFilter(HIDE_PARAM, next ? null : HIDE_NONE);
    setFilters({ ...filters, hideOn: next });
  }

  function toggleFlags() {
    const next = !filters.flagsOn;
    writeFilter(FLAGS_PARAM, next ? null : FLAGS_NONE);
    setFilters({ ...filters, flagsOn: next });
  }

  const kindAll = filters.kind === KIND_ALL;
  const kindHref = (() => {
    const url = new URL(window.location.href);
    url.searchParams.set(KIND_PARAM, kindAll ? KIND_DEFAULT : KIND_ALL);
    return url.pathname + url.search;
  })();

  return (
    <div>
      <div className="board-controls">
        <button
          type="button"
          role="switch"
          data-board-filter
          aria-checked={filters.hideOn}
          onClick={toggleHide}
        >
          Hide answered
        </button>
        <button
          type="button"
          role="switch"
          data-flag-filter
          aria-checked={filters.flagsOn}
          onClick={toggleFlags}
        >
          Hide flagged
        </button>
        <a href={kindHref} data-board-kind data-all-kinds={kindAll}>
          {kindAll ? "Show manager cards" : "Show all kinds"}
        </a>
      </div>
      <div data-undo-bar className={`undo-bar${held ? " is-active" : ""}`}>
        <span>
          Answer held for a moment —{" "}
          <button type="button" data-undo onClick={undo}>
            Undo
          </button>
        </span>
      </div>
      {visible.length === 0 ? (
        <p className="empty">
          {kindAll
            ? "Nothing needs attention."
            : "No cards of this kind need attention. The others are hidden by the kind filter above."}
        </p>
      ) : (
        <ul className="items">
          {visible.map((item) => (
            <Card key={item.item_id} item={item} onChanged={load} onDecide={decide} />
          ))}
        </ul>
      )}
    </div>
  );
}
