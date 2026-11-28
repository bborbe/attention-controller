import type { FormEvent, KeyboardEvent, MouseEvent } from "react";
import type { BoardItem, Item, Question } from "./types";
import { answer, boardClient, close, flag, jump, rate, speak, cancel, unflag } from "./api";
import { updateCardState, useCardState } from "./cardState";
import type { CardState, Note } from "./cardState";
import {
  answerFailureMessage,
  boardViewOf,
  isDimmed,
  isStoreHeld,
  recordAnswer,
  recordClient,
  signatureOf,
} from "./itemView";

interface Props {
  item: BoardItem;
  onChanged: () => void;
  onDecide: (itemID: string, decision: "allow" | "deny") => void;
  // A close note outlives its card (the row leaves the board on the stream
  // echo), so it is shown at the board level.
  onClosed: (text: string, isError: boolean) => void;
}

interface Unit {
  tab: string;
  payload: string;
  cardinality: string;
  options: { label: string; description?: string; recommended?: boolean; exclusive?: boolean }[];
}

// The jump glyph, shared by the live and the disabled control (the
// server-rendered board's terminal-prompt icon).
const JUMP_ICON = (
  <svg className="jump-icon" viewBox="0 0 16 16" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
    <path d="M3.75 3.25h8.5a1.5 1.5 0 0 1 1.5 1.5v6.5a1.5 1.5 0 0 1-1.5 1.5h-8.5a1.5 1.5 0 0 1-1.5-1.5v-6.5a1.5 1.5 0 0 1 1.5-1.5z" />
    <path d="M5.75 6.5 7.5 8.25 5.75 10" />
    <path d="M9 10h1.75" />
  </svg>
);

// A stale-card failure line is read before the board re-reads the store.
const STALE_RELOAD_MS = 1500;

function unitsOf(item: Item): Unit[] {
  if (item.questions && item.questions.length > 0) {
    return item.questions.map((q: Question) => ({
      tab: q.tab,
      payload: q.payload,
      cardinality: q.cardinality || "single",
      options: q.options || [],
    }));
  }
  return [
    {
      tab: "",
      payload: item.payload,
      cardinality: item.answer_cardinality || "single",
      options: item.options || [],
    },
  ];
}

export default function Card({ item, onChanged, onDecide, onClosed }: Props) {
  const itemID = item.item_id;
  const state = useCardState(itemID, signatureOf(item));
  const dimmed = isDimmed(item);
  const held = isStoreHeld(item);
  const units = unitsOf(item);
  const multi = units.length > 1;
  const message = item.answer_mechanism === "message";
  const permission = item.answer_mechanism === "permission";
  const ack = item.answer_mechanism === "ack";
  const view = boardViewOf(item);
  const prov = view.provenance;

  const set = (patch: Partial<CardState> | ((s: CardState) => Partial<CardState>)) =>
    updateCardState(itemID, patch);
  const setNote = (note: Note | null) => set({ note });

  const activeTab =
    state.activeTab !== null && units.some((u) => u.tab === state.activeTab)
      ? state.activeTab
      : units[0].tab;
  const selections = state.selections;
  const otherText = state.otherText;

  function pick(unit: Unit, label: string) {
    set((s) => {
      const current = s.selections[unit.tab] || [];
      const multiPick = unit.cardinality === "multiple";
      const opt = unit.options.find((o) => o.label === label);
      let next: string[];
      if (!multiPick) {
        next = [label];
      } else if (current.includes(label)) {
        next = current.filter((l) => l !== label);
      } else if (opt?.exclusive) {
        next = [label];
      } else {
        const keep = current.filter((l) => !unit.options.find((o) => o.label === l)?.exclusive);
        next = [...keep, label];
      }
      // Picking a named option takes the typed text with it: the text was the
      // Other control's answer, and collectAnswers would send it ahead of the pick.
      return {
        selections: { ...s.selections, [unit.tab]: next },
        otherText: { ...s.otherText, [unit.tab]: "" },
      };
    });
  }

  function typeOther(unit: Unit, value: string) {
    set((s) => ({
      otherText: { ...s.otherText, [unit.tab]: value },
      // Typing selects Other and clears every pick; emptying the field releases
      // Other rather than restoring an earlier pick.
      selections: value.trim() ? { ...s.selections, [unit.tab]: [] } : s.selections,
    }));
  }

  // The Other control's checked state is owned by the text field: a click (or a
  // keyboard move, which the browser dispatches as a click) on the option is
  // refused, and focus goes to the field instead. The field itself passes.
  function guardOther(event: MouseEvent<HTMLElement>, unit: Unit) {
    const target = event.target as HTMLElement;
    if (target.classList.contains("other")) return;
    event.preventDefault();
    const field = document.querySelector<HTMLInputElement>(
      `li.item[data-item-id="${CSS.escape(itemID)}"] .panel[data-question="${CSS.escape(unit.tab)}"] input.other`,
    );
    field?.focus();
  }

  function collectAnswers(): { question: string; kind: string; value?: string; values?: string[] }[] {
    const entries: { question: string; kind: string; value?: string; values?: string[] }[] = [];
    for (const unit of units) {
      const text = (otherText[unit.tab] || "").trim();
      if (text) {
        entries.push({ question: unit.tab, kind: "text", value: text });
        continue;
      }
      const picks = selections[unit.tab] || [];
      if (picks.length === 0) continue;
      if (unit.cardinality === "multiple") entries.push({ question: unit.tab, kind: "option", values: picks });
      else entries.push({ question: unit.tab, kind: "option", value: picks[0] });
    }
    return entries;
  }

  async function submitAnswer(kind: "send" | "skip" | "hidden") {
    const request: Record<string, unknown> = { ...boardClient() };
    if (kind === "skip" || kind === "hidden") {
      if (multi) request.answers = units.map((u) => ({ question: u.tab, kind }));
      else request.answer = { kind };
    } else {
      const entries = collectAnswers();
      if (entries.length === 0) {
        setNote({ text: "Pick an option or write an answer first.", error: true });
        return;
      }
      if (multi) request.answers = entries;
      else request.answer = entries[0].values
        ? { kind: "option", values: entries[0].values }
        : { kind: entries[0].kind, value: entries[0].value };
    }
    try {
      const res = await answer(itemID, request);
      if (res.ok) {
        setNote({ text: "Answer sent.", error: false });
        onChanged();
        return;
      }
      const failure = answerFailureMessage(res.status, await res.text());
      setNote({ text: failure.text, error: true });
      if (failure.stale) window.setTimeout(onChanged, STALE_RELOAD_MS);
    } catch (err) {
      setNote({ text: `Answer failed - ${String(err)}`, error: true });
    }
  }

  // closeCard is the one write behind Acknowledge and every corner X that
  // closes: open/answered -> closed, answering nothing.
  async function closeCard(acknowledged: boolean) {
    try {
      const res = await close(itemID);
      if (res.ok) {
        setNote(null);
        onClosed(acknowledged ? "Acknowledged." : "Cleared.", false);
        onChanged();
        return;
      }
      const body = await res.text();
      setNote({
        text: `${acknowledged ? "Acknowledge" : "Clear"} failed - HTTP ${res.status} - ${body}`,
        error: true,
      });
    } catch (err) {
      setNote({ text: `${acknowledged ? "Acknowledge" : "Clear"} failed - ${String(err)}`, error: true });
    }
  }

  // The corner X is the mechanism's own dominant act: on a record (answered or
  // store-held) it closes; on a permission card it is Deny (through the hold); on
  // an ack card it is Acknowledge; on a message card it is the hidden answer.
  function cornerX() {
    if (dimmed) {
      void closeCard(false);
      return;
    }
    if (permission) {
      onDecide(itemID, "deny");
      return;
    }
    if (ack) {
      void closeCard(true);
      return;
    }
    void submitAnswer("hidden");
  }

  // The optimistic rating shows only while the stored rating is still the one
  // the click was made against: once the store reports any other value (this
  // click's echo, or a later write from another arm) the stored value wins.
  const storedRating = item.rating || "";
  const shownRating =
    state.optimisticRating !== null && state.optimisticBase === storedRating
      ? state.optimisticRating
      : storedRating;

  async function toggleRating(value: "up" | "down") {
    const previous = shownRating;
    const next = previous === value ? "" : value;
    // Optimistic: the highlight flips now and reverts on failure.
    set({ optimisticRating: next, optimisticBase: storedRating });
    try {
      const res = await rate(itemID, next);
      if (!res.ok) throw new Error(`rate failed: ${res.status}`);
      set({ note: null });
      onChanged();
    } catch (err) {
      set({
        optimisticRating: null,
        optimisticBase: null,
        note: { text: `Could not rate this card: ${(err as Error).message}`, error: true },
      });
    }
  }

  async function submitFlag(event?: FormEvent) {
    event?.preventDefault();
    const reason = state.flagReason.trim();
    if (!reason) {
      setNote({ text: "Say why this card should not have reached you.", error: true });
      return;
    }
    // Cleared at submit, so the stream echo never carries a sent reason; put
    // back on failure.
    set({ flagReason: "" });
    try {
      const res = await flag(itemID, reason);
      if (!res.ok) throw new Error(`flag failed: ${res.status}`);
      setNote(null);
      onChanged();
    } catch (err) {
      set((s) => ({
        flagReason: s.flagReason || reason,
        note: { text: `Could not flag this card: ${(err as Error).message}`, error: true },
      }));
    }
  }

  async function doUnflag() {
    try {
      const res = await unflag(itemID);
      if (!res.ok) throw new Error(`unflag failed: ${res.status}`);
      setNote(null);
      onChanged();
    } catch (err) {
      setNote({ text: `Could not unflag this card: ${(err as Error).message}`, error: true });
    }
  }

  // The jump control calls the board's own endpoint, which performs the jump
  // server-side and answers 204, so the click switches WezTerm and leaves the
  // board where it was.
  async function doJump() {
    if (!view.jump_url) return;
    try {
      const res = await jump(view.jump_url);
      if (res.ok) {
        setNote({ text: "Jumped.", error: false });
        return;
      }
      setNote({ text: `Jump failed - HTTP ${res.status} - ${await res.text()}`, error: true });
    } catch (err) {
      setNote({ text: `Jump failed - ${String(err)}`, error: true });
    }
  }

  async function toggleSpeak() {
    if (state.speaking) {
      await cancel(itemID, "");
      set({ speaking: false });
      return;
    }
    const res = await speak(itemID);
    if (res.ok) set({ speaking: true, note: null });
    else setNote({ text: `Read aloud failed - HTTP ${res.status}`, error: true });
  }

  function onOtherKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.nativeEvent.isComposing || event.keyCode === 229) return;
    if (event.key === "Enter") {
      event.preventDefault();
      void submitAnswer("send");
    }
  }

  const client = dimmed ? recordClient(item) : "";

  function renderPanel(unit: Unit) {
    const multiPick = unit.cardinality === "multiple";
    const name = `q-${itemID}-${unit.tab}`;
    const text = otherText[unit.tab] || "";
    const picks = selections[unit.tab] || [];
    return (
      <div
        className="panel"
        key={unit.tab}
        data-question={unit.tab}
        data-multi-pick={multiPick}
      >
        {multi ? <div className="question">{unit.payload}</div> : null}
        {unit.options.length > 0 ? (
          <div className="options">
            {unit.options.map((o) => (
              <label key={o.label} className="option">
                <input
                  type={multiPick ? "checkbox" : "radio"}
                  data-option={o.label}
                  data-exclusive={o.exclusive ? "" : undefined}
                  name={name}
                  value={o.label}
                  checked={!text.trim() && picks.includes(o.label)}
                  onChange={() => pick(unit, o.label)}
                />
                <span className="option-body">
                  <span className="option-label">
                    {o.label}
                    {o.recommended ? <span className="recommended"> (Recommended)</span> : null}
                  </span>
                  {o.description ? <span className="option-desc"> — {o.description}</span> : null}
                </span>
              </label>
            ))}
            <label className="option option-other" onClick={(e) => guardOther(e, unit)}>
              <input
                type={multiPick ? "checkbox" : "radio"}
                name={name}
                value=""
                data-other
                checked={!!text.trim()}
                onChange={() => undefined}
              />
              <span className="option-body">
                <span className="option-label">Other…</span>
                <input
                  type="text"
                  className="other"
                  name="text"
                  placeholder="Type your answer"
                  value={text}
                  onChange={(e) => typeOther(unit, e.target.value)}
                  onKeyDown={onOtherKeyDown}
                />
              </span>
            </label>
          </div>
        ) : (
          <input
            type="text"
            className="other"
            name="text"
            placeholder="Other..."
            value={text}
            onChange={(e) => typeOther(unit, e.target.value)}
            onKeyDown={onOtherKeyDown}
          />
        )}
      </div>
    );
  }

  return (
    <li
      className={`item risk-${item.risk || "decision"}${dimmed ? " dimmed" : ""}`}
      data-item-id={itemID}
      data-held={held ? "true" : "false"}
      data-producer-kind={item.producer_kind}
      data-uncovered={view.uncovered ? "true" : undefined}
      data-flagged={item.flag_reason ? "true" : "false"}
    >
      <button
        type="button"
        className="corner-x"
        data-corner-x
        onClick={cornerX}
        aria-label={permission && !dimmed ? "Deny this request" : "Hide (handled elsewhere)"}
      >
        ✕
      </button>
      {/* No read-aloud control when the server has no tts server configured:
          its endpoint is unrouted, so the control could only fail. */}
      {!dimmed && view.speak_enabled ? (
        <button
          type="button"
          className="speak"
          data-speak
          onClick={toggleSpeak}
          aria-pressed={state.speaking}
          aria-label={state.speaking ? "Stop reading" : "Read aloud"}
          title={state.speaking ? "Stop reading" : "Read aloud"}
        >
          {state.speaking ? "■" : "🔊"}
        </button>
      ) : null}
      {/* The jump control: live when a pane validated (data-jump carries the
          board-relative /jump/{itemID} path), disabled with the same glyph when
          none did, so the corner reads the same on every card. */}
      {view.jump_url ? (
        <button
          type="button"
          className="jump-corner"
          data-jump={view.jump_url}
          aria-label="Jump to session"
          title="Jump to session"
          onClick={doJump}
        >
          {JUMP_ICON}
        </button>
      ) : view.no_jump ? (
        <button type="button" className="jump-corner" disabled aria-label="Jump to session" title="Jump to session">
          {JUMP_ICON}
        </button>
      ) : null}
      <button
        type="button"
        className="info-toggle"
        data-info-toggle
        onClick={() => set((s) => ({ infoOpen: !s.infoOpen }))}
        aria-expanded={state.infoOpen}
        aria-label="Card information"
      >
        i
      </button>

      {/* The navigation line leads the card: task, goal and topic links into
          the vault, then the session name. Machine identity lives in the info
          panel. */}
      {prov.task_href || prov.goal_href || prov.topic_href || prov.session_name ? (
        <div className="provenance">
          {prov.task_href ? (
            <span className="task">
              <a href={prov.task_href}>{prov.task_name}</a>
            </span>
          ) : null}
          {prov.goal_href ? (
            <span className="goal">
              <a href={prov.goal_href}>{prov.goal_name}</a>
            </span>
          ) : null}
          {prov.topic_href ? (
            <span className="topic">
              <a href={prov.topic_href}>{prov.topic_name}</a>
            </span>
          ) : null}
          {prov.session_name ? <span className="session-name">{prov.session_name}</span> : null}
        </div>
      ) : null}

      {view.uncovered ? (
        <div className="uncovered-reason" data-uncovered-reason>
          Shown because no live manager can relay it.
        </div>
      ) : null}

      {dimmed ? null : message ? (
        multi ? (
          <div className="card-title">{item.payload}</div>
        ) : (
          <div className="question card-title">{item.payload}</div>
        )
      ) : (
        <div className="payload card-title">{item.payload}</div>
      )}

      {item.context ? <div className="context">{item.context}</div> : null}

      {dimmed ? (
        <div className="record">
          <div className="record-question">{item.payload}</div>
          <div className="record-answer">answered: {recordAnswer(item)}</div>
          {client ? <div className="record-client">client: {client}</div> : null}
        </div>
      ) : null}

      {!dimmed && message ? (
        <form
          className="answer"
          data-multi={multi}
          onSubmit={(e) => {
            e.preventDefault();
            void submitAnswer("send");
          }}
        >
          {multi ? (
            <div className="tabs">
              {units.map((u) => (
                <button
                  type="button"
                  key={u.tab}
                  data-tab={u.tab}
                  className={`tab${u.tab === activeTab ? " active" : ""}`}
                  onClick={() => set({ activeTab: u.tab })}
                >
                  {u.tab}
                </button>
              ))}
            </div>
          ) : null}
          {/* Only the active tab's panel is rendered; every panel's picks and
              text live in the card state, so switching tabs loses nothing. */}
          {renderPanel(units.find((u) => u.tab === activeTab) || units[0])}
          <div className="actions">
            <button type="button" value="skip" className="dismiss" onClick={() => submitAnswer("skip")}>
              ✕ Dismiss
            </button>
            <button type="button" value="send" className="next" onClick={() => submitAnswer("send")}>
              ✓ Submit answer
            </button>
          </div>
        </form>
      ) : null}

      {!dimmed ? (
        <form className="flag" data-flag-control onSubmit={submitFlag}>
          <input
            className="flag-reason-input"
            type="text"
            name="reason"
            value={state.flagReason}
            onChange={(e) => set({ flagReason: e.target.value })}
            placeholder="Why should this not have reached you?"
            aria-label="Reason this card was misrouted"
          />
          <button
            type="button"
            className="flag-open"
            onClick={(e) => {
              const field = (e.currentTarget.form as HTMLFormElement | null)?.querySelector<HTMLInputElement>(
                "input[name=reason]",
              );
              field?.focus();
            }}
          >
            ⚑ Flag as misrouted
          </button>
          <button type="submit" className="flag-submit">
            Submit flag
          </button>
        </form>
      ) : null}

      {item.flag_reason ? (
        <div className="flag-reason-shown">
          flagged: {item.flag_reason}{" "}
          <button type="button" className="unflag" data-unflag onClick={doUnflag}>
            Unflag
          </button>
        </div>
      ) : null}

      <div className="rate" data-rate-control data-rating={shownRating}>
        <button
          type="button"
          data-rate="up"
          className={`rate-btn${shownRating === "up" ? " rated" : ""}`}
          aria-pressed={shownRating === "up"}
          aria-label="Good card"
          onClick={() => toggleRating("up")}
        >
          👍
        </button>
        <button
          type="button"
          data-rate="down"
          className={`rate-btn${shownRating === "down" ? " rated" : ""}`}
          aria-pressed={shownRating === "down"}
          aria-label="Should not have reached me"
          onClick={() => toggleRating("down")}
        >
          👎
        </button>
      </div>

      {view.undeliverable ? <div className="delivery-undeliverable">undeliverable</div> : null}

      {!dimmed && ack ? (
        <div className="actions">
          <button type="button" className="ack" data-ack onClick={() => closeCard(true)}>
            Acknowledge
          </button>
        </div>
      ) : null}

      {!dimmed && permission ? (
        <div className="actions">
          <button type="button" className="dismiss" data-decision="deny" onClick={() => onDecide(itemID, "deny")}>
            ✕ Deny
          </button>
          <button type="button" className="next" data-decision="allow" onClick={() => onDecide(itemID, "allow")}>
            ✓ Allow
          </button>
        </div>
      ) : null}

      {view.jump_command || view.jump_url ? (
        <div className="jump">
          {view.jump_command ? (
            <span>
              Approve in the session that asked: <code>{view.jump_command}</code>
            </span>
          ) : null}
        </div>
      ) : view.no_jump ? (
        <div className="jump-reason">
          <span className="no-jump">{view.no_jump}</span>
        </div>
      ) : null}

      <div className="info-panel" data-info-panel hidden={!state.infoOpen}>
        <div className="item-id">item {itemID}</div>
        <div className="producer">
          {item.producer_id} ({item.producer_kind})
        </div>
        <div className="meta">
          state {item.state}
          {item.risk ? ` · risk ${item.risk}` : ""}
        </div>
        {prov.host ? <span className="host">{prov.host}</span> : null}
        {prov.cwd ? <span className="cwd">{prov.cwd}</span> : null}
        {prov.tool ? <span className="tool">{prov.tool}</span> : null}
        {prov.pane ? <span className="pane">pane {prov.pane}</span> : null}
        {prov.pane && prov.pane_unvalidated ? <span className="unvalidated">unvalidated</span> : null}
        {!prov.pane && prov.unroutable ? <span className="unroutable">unroutable</span> : null}
        <div className="timeline" data-timeline>
          {view.timeline.map((line, i) => (
            <div className="timeline-line" key={i}>
              {line}
            </div>
          ))}
        </div>
        {view.delivery_outcome ? <div className="delivery-outcome">{view.delivery_outcome}</div> : null}
      </div>

      {state.note ? <div className={`note${state.note.error ? " failed" : ""}`}>{state.note.text}</div> : null}
    </li>
  );
}
