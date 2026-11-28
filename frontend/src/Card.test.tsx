// Card unit specs for the server-derived render data the board JSON carries.
import { fireEvent, render, waitFor } from "@testing-library/react";
import Card from "./Card";
import { boardViewOf } from "./itemView";
import type { BoardItem } from "./types";

function boardItem(itemID: string, speakEnabled: boolean): BoardItem {
  const base = { item_id: itemID } as BoardItem;
  return {
    ...base,
    producer_id: "e2e",
    producer_kind: "session",
    payload: "read me",
    state: "open",
    answer_mechanism: "message",
    created_at: "2026-11-28T10:00:00Z",
    board: { ...boardViewOf(base), speak_enabled: speakEnabled },
  } as BoardItem;
}

function renderCard(item: BoardItem) {
  return render(
    <ul>
      <Card item={item} onChanged={() => undefined} onDecide={() => undefined} onClosed={() => undefined} />
    </ul>,
  );
}

describe("Card read-aloud control", () => {
  it("renders no read-aloud control when the server runs without a tts URL", () => {
    const { container } = renderCard(boardItem("speak-off", false));
    expect(container.querySelector("li.item")).not.toBeNull();
    expect(container.querySelector("[data-speak]")).toBeNull();
  });

  it("renders the read-aloud control when a tts server is configured", () => {
    const { container } = renderCard(boardItem("speak-on", true));
    expect(container.querySelector("[data-speak]")).not.toBeNull();
  });
});

// stubFetch answers /speak with a message id and /cancel with cancelStatus,
// recording every request body by verb.
function stubFetch(cancelStatus: number) {
  const bodies: Record<string, unknown> = {};
  const mock = vi.fn(async (url: string, init?: RequestInit) => {
    const verb = url.split("/").pop() ?? "";
    if (init?.body) bodies[verb] = JSON.parse(String(init.body));
    if (verb === "speak") {
      return new Response('{"message_id":"tts-msg-1"}', { status: 200 });
    }
    if (verb === "cancel") {
      return new Response("{}", { status: cancelStatus });
    }
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", mock);
  return bodies;
}

describe("Card read-aloud stop", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("stops by the message id /speak returned", async () => {
    const bodies = stubFetch(200);
    const { container } = renderCard(boardItem("stop-ok", true));
    const button = container.querySelector("[data-speak]")!;

    fireEvent.click(button);
    await waitFor(() => expect(button.getAttribute("aria-pressed")).toBe("true"));

    fireEvent.click(button);
    await waitFor(() => expect(button.getAttribute("aria-pressed")).toBe("false"));
    expect(bodies.cancel).toEqual({ message_id: "tts-msg-1" });
  });

  it("keeps speaking and shows a failure when the stop is refused", async () => {
    const bodies = stubFetch(502);
    const { container } = renderCard(boardItem("stop-fail", true));
    const button = container.querySelector("[data-speak]")!;

    fireEvent.click(button);
    await waitFor(() => expect(button.getAttribute("aria-pressed")).toBe("true"));

    fireEvent.click(button);
    await waitFor(() =>
      expect(container.querySelector(".note.failed")?.textContent).toBe("Stop reading failed - HTTP 502"),
    );
    expect(button.getAttribute("aria-pressed")).toBe("true");
    expect(bodies.cancel).toEqual({ message_id: "tts-msg-1" });
  });
});

// A card whose declared risk and server-derived risk differ, plus an optional
// blast radius, so the styling tests can tell which field the card read.
function riskItem(
  itemID: string,
  declaredRisk: string | undefined,
  derivedRisk: string,
  blastRadius = "",
): BoardItem {
  const base = { item_id: itemID } as BoardItem;
  return {
    ...base,
    producer_id: "e2e",
    producer_kind: "session",
    payload: "move `vg0/data` from `hell` to `nuke-dev`",
    state: "open",
    answer_mechanism: "message",
    risk: declaredRisk,
    created_at: "2026-11-28T10:00:00Z",
    board: { ...boardViewOf(base), risk: derivedRisk, blast_radius: blastRadius },
  } as BoardItem;
}

describe("Card risk styling", () => {
  it("styles the risk band from the derived level, not the declaration", () => {
    // The declaration says routine; the board's derivation raised it. The band
    // must follow the derived level — the whole point of RiskOf.
    const { container } = renderCard(riskItem("derived", "routine", "risk"));
    const row = container.querySelector("li.item")!;
    expect(row.classList.contains("risk-risk")).toBe(true);
    expect(row.classList.contains("risk-routine")).toBe(false);
    expect(row.getAttribute("data-risk")).toBe("risk");
  });

  it("falls back to the declared level when the board carries none", () => {
    const { container } = renderCard(riskItem("declared", "risk", ""));
    const row = container.querySelector("li.item")!;
    expect(row.classList.contains("risk-risk")).toBe(true);
    expect(row.getAttribute("data-risk")).toBe("risk");
  });

  it("renders a blast radius from the board with its data attribute", () => {
    const blast = "Blast radius: irreversible infrastructure change.";
    const { container } = renderCard(riskItem("blast", "routine", "risk", blast));
    const row = container.querySelector("li.item")!;
    expect(row.getAttribute("data-blast-radius")).toBe(blast);
    const line = container.querySelector(".blast-radius[data-blast-radius-text]")!;
    expect(line).not.toBeNull();
    expect(line.textContent).toBe(blast);
  });

  it("renders no blast radius for a routine card", () => {
    const { container } = renderCard(riskItem("no-blast", "routine", "decision"));
    const row = container.querySelector("li.item")!;
    expect(row.hasAttribute("data-blast-radius")).toBe(false);
    expect(container.querySelector("[data-blast-radius-text]")).toBeNull();
  });
});

describe("Card body text", () => {
  it("renders a markdown link in the payload as an anchor", () => {
    const base = { item_id: "link" } as BoardItem;
    const item = {
      ...base,
      producer_id: "e2e",
      producer_kind: "session",
      payload: "jump to [pane 87](http://127.0.0.1:1337/jump?pane=87&t=x)",
      state: "open",
      answer_mechanism: "message",
      created_at: "2026-11-28T10:00:00Z",
      board: boardViewOf(base),
    } as BoardItem;
    const { container } = renderCard(item);
    const anchor = container.querySelector("li.item .card-title a")!;
    expect(anchor).not.toBeNull();
    expect(anchor.getAttribute("href")).toBe("http://127.0.0.1:1337/jump?pane=87&t=x");
    expect(anchor.textContent).toBe("pane 87");
    expect(anchor.getAttribute("target")).toBe("_blank");
    expect(anchor.getAttribute("rel")).toBe("noopener noreferrer");
  });
});
