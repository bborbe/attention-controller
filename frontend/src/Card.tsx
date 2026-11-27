import { useState } from "react";
import type { Item, Question } from "./types";
import { answer, boardClient, close, flag, rate, speak, cancel, unflag } from "./api";

interface Props {
  item: Item;
  onChanged: () => void;
}

interface Unit {
  tab: string;
  payload: string;
  cardinality: string;
  options: { label: string; description?: string; recommended?: boolean; exclusive?: boolean }[];
}

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

export default function Card({ item, onChanged }: Props) {
  const dimmed = item.state === "answered";
  const units = unitsOf(item);
  const multi = units.length > 1;

  const [activeTab, setActiveTab] = useState(units[0].tab);
  const [selections, setSelections] = useState<Record<string, string[]>>({});
  const [otherText, setOtherText] = useState<Record<string, string>>({});
  const [note, setNote] = useState<{ text: string; error: boolean } | null>(null);
  const [speaking, setSpeaking] = useState(false);
  const [infoOpen, setInfoOpen] = useState(false);
  const [flagReason, setFlagReason] = useState("");
  const [showFlag, setShowFlag] = useState(false);

  const itemID = item.item_id;

  function pick(unit: Unit, label: string) {
    const current = selections[unit.tab] || [];
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
    setSelections({ ...selections, [unit.tab]: next });
    setOtherText({ ...otherText, [unit.tab]: "" });
  }

  function typeOther(unit: Unit, value: string) {
    setOtherText({ ...otherText, [unit.tab]: value });
    if (value.trim()) setSelections({ ...selections, [unit.tab]: [] });
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
    const res = await answer(itemID, request);
    if (res.ok) {
      setNote({ text: "Answer sent.", error: false });
      onChanged();
    } else {
      setNote({ text: `Answer failed - HTTP ${res.status}`, error: true });
    }
  }

  async function decide(decision: "allow" | "deny") {
    const res = await answer(itemID, { ...boardClient(), decision });
    if (!res.ok) setNote({ text: `${decision} failed - HTTP ${res.status}`, error: true });
    else onChanged();
  }

  async function acknowledge() {
    const res = await close(itemID);
    if (!res.ok) setNote({ text: `Acknowledge failed - HTTP ${res.status}`, error: true });
    else onChanged();
  }

  async function toggleRating(value: "up" | "down") {
    const next = item.rating === value ? "" : value;
    const res = await rate(itemID, next);
    if (!res.ok) setNote({ text: `Rate failed - HTTP ${res.status}`, error: true });
    else onChanged();
  }

  async function submitFlag() {
    if (!flagReason.trim()) {
      setNote({ text: "Say why this card should not have reached you.", error: true });
      return;
    }
    const res = await flag(itemID, flagReason.trim());
    if (!res.ok) setNote({ text: `Flag failed - HTTP ${res.status}`, error: true });
    else {
      setFlagReason("");
      setShowFlag(false);
      onChanged();
    }
  }

  async function doUnflag() {
    const res = await unflag(itemID);
    if (!res.ok) setNote({ text: `Unflag failed - HTTP ${res.status}`, error: true });
    else onChanged();
  }

  async function toggleSpeak() {
    if (speaking) {
      await cancel(itemID, "");
      setSpeaking(false);
      return;
    }
    const res = await speak(itemID);
    if (res.ok) setSpeaking(true);
    else setNote({ text: `Read aloud failed - HTTP ${res.status}`, error: true });
  }

  const activeUnit = units.find((u) => u.tab === activeTab) || units[0];

  return (
    <li className={`item${dimmed ? " dimmed" : ""}`} data-item-id={itemID} data-producer-kind={item.producer_kind}>
      <div className="card-head">
        <span className="card-title">{item.payload}</span>
        <span className="card-controls">
          <button type="button" onClick={toggleSpeak} aria-pressed={speaking} aria-label={speaking ? "Stop reading" : "Read aloud"}>
            {speaking ? "■" : "🔊"}
          </button>
          <button type="button" onClick={() => setInfoOpen(!infoOpen)} aria-expanded={infoOpen} aria-label="Card identity">
            i
          </button>
          <button type="button" onClick={() => submitAnswer("hidden")} aria-label="Hide this card">
            ✕
          </button>
        </span>
      </div>

      {item.context ? <p className="subtitle">{item.context}</p> : null}

      {infoOpen ? (
        <pre className="info-panel">
          item {itemID}
          {"\n"}producer {item.producer_id} ({item.producer_kind})
          {"\n"}state {item.state}
          {item.risk ? `\nrisk ${item.risk}` : ""}
        </pre>
      ) : null}

      {!dimmed && item.answer_mechanism === "message" ? (
        <div className="answer">
          {multi ? (
            <div className="tabs">
              {units.map((u) => (
                <button
                  type="button"
                  key={u.tab}
                  data-tab={u.tab}
                  className={u.tab === activeTab ? "active" : ""}
                  onClick={() => setActiveTab(u.tab)}
                >
                  {u.tab}
                </button>
              ))}
            </div>
          ) : null}
          <div className="panel" data-question={activeUnit.tab}>
            {multi ? <p className="question">{activeUnit.payload}</p> : null}
            {activeUnit.options.map((o) => (
              <label key={o.label} className="option">
                <input
                  type={activeUnit.cardinality === "multiple" ? "checkbox" : "radio"}
                  name={`q-${activeUnit.tab}`}
                  checked={(selections[activeUnit.tab] || []).includes(o.label)}
                  onChange={() => pick(activeUnit, o.label)}
                />
                <span>
                  {o.label}
                  {o.recommended ? " (Recommended)" : ""}
                  {o.description ? <span className="subtitle"> — {o.description}</span> : null}
                </span>
              </label>
            ))}
            <label className="option-other">
              <input
                type="text"
                className="other"
                placeholder="Other…"
                value={otherText[activeUnit.tab] || ""}
                onChange={(e) => typeOther(activeUnit, e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                    e.preventDefault();
                    void submitAnswer("send");
                  }
                }}
              />
            </label>
          </div>
          <div className="actions">
            <button type="button" className="next" onClick={() => submitAnswer("send")}>
              ✓ Submit answer
            </button>
            <button type="button" value="skip" onClick={() => submitAnswer("skip")}>
              ✕ Dismiss
            </button>
          </div>
        </div>
      ) : null}

      {!dimmed && item.answer_mechanism === "permission" ? (
        <div className="actions">
          <button type="button" onClick={() => decide("allow")}>
            Allow
          </button>
          <button type="button" onClick={() => decide("deny")}>
            Deny
          </button>
        </div>
      ) : null}

      {!dimmed && item.answer_mechanism === "ack" ? (
        <div className="actions">
          <button type="button" onClick={acknowledge}>
            Acknowledge
          </button>
        </div>
      ) : null}

      <div className="actions">
        <span className="rating" data-rating={item.rating || ""}>
          <button type="button" data-rate="up" className={item.rating === "up" ? "rated" : ""} aria-pressed={item.rating === "up"} onClick={() => toggleRating("up")}>
            👍
          </button>
          <button type="button" data-rate="down" className={item.rating === "down" ? "rated" : ""} aria-pressed={item.rating === "down"} onClick={() => toggleRating("down")}>
            👎
          </button>
        </span>
        {item.flagged_at ? (
          <button type="button" onClick={doUnflag}>
            Unflag
          </button>
        ) : (
          <button type="button" onClick={() => setShowFlag(!showFlag)}>
            ⚑ Flag as misrouted
          </button>
        )}
      </div>

      {showFlag ? (
        <div className="flag-form">
          <input type="text" name="reason" value={flagReason} onChange={(e) => setFlagReason(e.target.value)} placeholder="Why is this misrouted?" />
          <button type="button" onClick={submitFlag}>
            Submit flag
          </button>
        </div>
      ) : null}

      {note ? <div className={`note${note.error ? " failed" : ""}`}>{note.text}</div> : null}
    </li>
  );
}
