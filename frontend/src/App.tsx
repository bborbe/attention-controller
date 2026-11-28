import { useCallback, useEffect, useRef, useState } from "react";
import type { BoardItem } from "./types";
import { fetchBoard, seen, subscribeItems, takePrefetchedBoard } from "./api";
import Card from "./Card";
import { useHeldAnswer } from "./useHeldAnswer";
import { syncSignature, updateCardState } from "./cardState";
import { answerFailureMessage, isAnswered, renderKeyOf, signatureOf } from "./itemView";

const HIDE_PARAM = "hide";
const HIDE_NONE = "none";
const FLAGS_PARAM = "flags";
const FLAGS_FLAGGED = "flagged";
const FLAGS_NONE = "none";
const KIND_PARAM = "producer_kind";
const KIND_ALL = "all";
const KIND_DEFAULT = "manager";
// How long a stale-card failure line is shown before the board re-reads the
// store, so the outcome is read before the card turns into its record.
const STALE_RELOAD_MS = 1500;
// How long a board-level close note ("Acknowledged.") stays up after its card left.
const BOARD_NOTE_MS = 8000;

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
  const [items, setItems] = useState<BoardItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // A failed re-read after a good load: the last board stays rendered and the
  // failure is named inline.
  const [refreshError, setRefreshError] = useState<string | null>(null);
  const [filters, setFilters] = useState<Filters>(readFilters);
  const [tracking, setTracking] = useState(true);
  const [boardNote, setBoardNote] = useState<{ text: string; error: boolean } | null>(null);
  const seenRef = useRef<Set<string>>(new Set());
  const loadedRef = useRef(false);
  const noteTimerRef = useRef<number | null>(null);
  const kindRef = useRef(filters.kind);
  kindRef.current = filters.kind;

  const load = useCallback(() => {
    (takePrefetchedBoard() ?? fetchBoard())
      .then((next) => {
        for (const item of next) {
          syncSignature(item.item_id, signatureOf(item));
          // The board records that it painted a card, once: before any
          // answered/flag filter parks it, but only for the kind view's rows —
          // a card the kind view withholds was never painted.
          const kind = kindRef.current;
          const painted = kind === KIND_ALL || item.producer_kind === kind || !!item.board?.uncovered;
          if (painted && !item.displayed_at && !seenRef.current.has(item.item_id)) {
            seenRef.current.add(item.item_id);
            seen(item.item_id);
          }
        }
        loadedRef.current = true;
        setItems(next);
        setError(null);
        setRefreshError(null);
      })
      .catch((err: Error) => {
        // A failed re-read (an unreadable frame, a transient 500) leaves the last
        // rendered board in place untouched; only a board that never loaded
        // shows the error.
        if (!loadedRef.current) setError(err.message);
        else setRefreshError(err.message);
      });
  }, []);

  const onHeldFailed = useCallback(
    (itemID: string, status: number, body: string) => {
      const failure =
        status === 0
          ? { text: `Answer failed - ${body}`, stale: false }
          : answerFailureMessage(status, body);
      updateCardState(itemID, { note: { text: failure.text, error: true } });
      if (failure.stale) window.setTimeout(load, STALE_RELOAD_MS);
    },
    [load],
  );

  const { held, decide, undo, cancelIfAnswered } = useHeldAnswer(load, onHeldFailed);

  useEffect(() => {
    load();
    return subscribeItems(load, setTracking);
  }, [load]);

  useEffect(() => {
    if (items) {
      cancelIfAnswered(items);
    }
  }, [items, cancelIfAnswered]);

  const showBoardNote = useCallback((text: string, isError: boolean) => {
    if (noteTimerRef.current !== null) clearTimeout(noteTimerRef.current);
    setBoardNote({ text, error: isError });
    noteTimerRef.current = window.setTimeout(() => setBoardNote(null), BOARD_NOTE_MS);
  }, []);

  if (error) return <p className="err">Could not read the board: {error}</p>;
  if (!items) return <p className="subtitle">Loading…</p>;

  const kindAll = filters.kind === KIND_ALL;
  // A card no live manager can relay is shown whatever its kind (the retired
  // server-rendered board's filterByProducerKind rule): the managers-only view
  // rests on a manager relaying the worker's question, and an uncovered card has
  // no manager to relay it.
  const kindMatches = (item: BoardItem) =>
    kindAll || item.producer_kind === filters.kind || !!item.board?.uncovered;

  // The rows the kind filter withholds that the operator would otherwise see:
  // under the answered filter an answered record is already parked by that
  // filter, so only the open ones (store-held included) count.
  const kindHidden = kindAll
    ? 0
    : items.filter((item) => !kindMatches(item) && (!filters.hideOn || !isAnswered(item))).length;

  const visible = items.filter((item) => {
    const flagged = !!item.flagged_at;
    if (held && item.item_id === held.itemID) return false;
    if (filters.hideOn && isAnswered(item)) return false;
    if (filters.flagsOn && flagged) return false;
    if (!kindMatches(item)) return false;
    return true;
  });

  function toggleHide() {
    const next = !filters.hideOn;
    writeFilter(HIDE_PARAM, next ? null : HIDE_NONE);
    setFilters({ ...filters, hideOn: next });
  }

  function toggleFlags() {
    const next = !filters.flagsOn;
    writeFilter(FLAGS_PARAM, next ? FLAGS_FLAGGED : FLAGS_NONE);
    setFilters({ ...filters, flagsOn: next });
  }

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
        {kindHidden > 0 ? (
          <span className="kind-hidden" data-kind-hidden>
            {kindHidden} hidden
          </span>
        ) : null}
        <span className="stream-stale" data-stream-stale hidden={tracking}>
          Not tracking the store - showing the last known state.
        </span>
        {/* In the control row, not above the list, so showing it never moves
            the first card. */}
        {refreshError ? (
          <span className="err refresh-error" data-refresh-error>
            Could not refresh the board: {refreshError}
          </span>
        ) : null}
        {boardNote ? (
          <span className={`note board-note${boardNote.error ? " failed" : ""}`}>{boardNote.text}</span>
        ) : null}
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
            <Card
              key={renderKeyOf(item)}
              item={item}
              onChanged={load}
              onDecide={decide}
              onClosed={showBoardNote}
            />
          ))}
        </ul>
      )}
    </div>
  );
}
