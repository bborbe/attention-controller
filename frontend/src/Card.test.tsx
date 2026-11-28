// Card unit specs for the server-derived render data the board JSON carries.
import { render } from "@testing-library/react";
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
