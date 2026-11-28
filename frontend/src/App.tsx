import { useCallback, useEffect, useRef, useState } from "react";
import type { BoardItem } from "./types";
import { fetchBoard, seen, subscribeItems, takePrefetchedBoard } from "./api";
import Card from "./Card";
import { useHeldAnswer } from "./useHeldAnswer";
import { pruneCardStates, syncSignature, updateCardState } from "./cardState";
import { createBoardLoader } from "./boardLoader";
import type { BoardLoader } from "./boardLoader";
import { answerFailureMessage, isAnswered, signatureOf } from "./itemView";

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

// The filter switch: the track carries the state in colour, the knob in position.
const SWITCH = (
  <svg className="switch" viewBox="0 0 44 24" aria-hidden="true" focusable="false">
    <rect className="switch-track" x="1" y="1" width="42" height="22" rx="11" />
    <circle className="switch-knob" cx="12" cy="12" r="8" />
  </svg>
);

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

  // The id of the answer the board is holding back, so the card-state prune
  // never drops the state of a card the operator just decided.
  const heldIDRef = useRef<string | null>(null);

  // One loader per board: at most one read in flight plus one trailing re-read,
  // and a response older than the last applied one is dropped (boardLoader.ts).
  const loaderRef = useRef<BoardLoader | null>(null);
  if (loaderRef.current === null) {
    loaderRef.current = createBoardLoader<BoardItem[]>({
      // The first read consumes the fetch main.tsx started before React mounted.
      read: () => takePrefetchedBoard() ?? fetchBoard(),
      apply: (next) => {
        const ids = new Set<string>();
        for (const item of next) {
          ids.add(item.item_id);
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
        if (heldIDRef.current !== null) ids.add(heldIDRef.current);
        pruneCardStates(ids);
        loadedRef.current = true;
        setItems(next);
        setError(null);
        setRefreshError(null);
      },
      fail: (err) => {
        // A failed re-read (an unreadable frame, a transient 500) leaves the last
        // rendered board in place untouched; only a board that never loaded
        // shows the error.
        if (!loadedRef.current) setError(err.message);
        else setRefreshError(err.message);
      },
    });
  }
  const load = useCallback(() => loaderRef.current?.request(), []);

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
  heldIDRef.current = held ? held.itemID : null;

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

  // The undo bar names what is held: the held card's ask, single-lined.
  const heldItem = held ? items?.find((item) => item.item_id === held.itemID) : undefined;
  const heldTitle = held ? (heldItem?.payload || held.itemID).replace(/\s+/g, " ").trim() : "";

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
      <h1>Attention</h1>
      <div className="board-controls">
        <a className="board-filter" href={kindHref} data-board-kind data-all-kinds={kindAll}>
          {SWITCH}
          <span className="switch-label">{kindAll ? "Managers only" : "Show all kinds"}</span>
        </a>
        {kindHidden > 0 ? (
          <span className="kind-hidden" data-kind-hidden>
            {kindHidden} hidden
          </span>
        ) : null}
        <button
          type="button"
          className="board-filter"
          role="switch"
          data-board-filter
          aria-checked={filters.hideOn}
          onClick={toggleHide}
        >
          {SWITCH}
          <span className="switch-label">Hide answered</span>
        </button>
        <button
          type="button"
          className="board-filter flag-filter"
          role="switch"
          data-flag-filter
          aria-checked={filters.flagsOn}
          onClick={toggleFlags}
        >
          {SWITCH}
          <span className="switch-label">Hide flagged</span>
        </button>
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
        <span className="undo-verb">{held ? (held.decision === "allow" ? "Approved" : "Denied") : ""}</span>
        <span className="undo-title">{heldTitle}</span>
        <button type="button" className="undo-button" data-undo onClick={undo}>
          Undo · U
        </button>
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
              key={item.item_id}
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
