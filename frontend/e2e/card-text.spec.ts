// Card body text: a markdown link in the payload renders as an anchor.
//
// The retired server-rendered board turned `[label](url)` in a card body into an
// anchor (pkg/handler/card-text.go, now ported to src/cardText.tsx). These specs
// assert the rendered anchor through the real binary, so a broken port or a
// bundle that fell back to plain text fails here.

import { expect, openBoard, pushMessage, row, test } from "./support";

test("renders a markdown link in the payload as an anchor", async ({ page, request, st }) => {
  const href = "http://127.0.0.1:1337/jump?pane=87&t=x";
  const itemID = await pushMessage(request, st, {
    payload: `jump to [pane 87](${href})`,
  });

  await openBoard(page, st, "?producer_kind=all");

  const anchor = row(page, itemID).locator(".card-title a");
  await expect(anchor).toHaveCount(1);
  await expect(anchor).toHaveAttribute("href", href);
  await expect(anchor).toHaveText("pane 87");
  // A new tab must not get a live window.opener.
  await expect(anchor).toHaveAttribute("target", "_blank");
  await expect(anchor).toHaveAttribute("rel", "noopener noreferrer");
});

test("leaves a refused scheme as text and links a bare url", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, {
    payload: "see [x](javascript:alert(1)) and https://example.com/x",
  });

  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  // Only the bare https URL becomes an anchor; the javascript: candidate stays
  // literal text so the operator can still read what the card tried to do.
  await expect(card.locator(".card-title a")).toHaveCount(1);
  await expect(card.locator(".card-title a")).toHaveAttribute("href", "https://example.com/x");
  await expect(card.locator(".card-title")).toContainText("[x](javascript:alert(1))");
});
