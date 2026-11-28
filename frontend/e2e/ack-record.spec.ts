// parity.md § Ack + record cards — ack cards and dimmed records close with
// POST /api/1.0/attention/<id>/close. The React card routes its corner X
// through the answer path instead, so the two corner-X rows are failing gap
// tests rather than weakened ones.

import {
  answerViaAPI,
  captureItemRequests,
  expect,
  openBoard,
  pushAck,
  pushMessage,
  requestsFor,
  row,
  test,
  waitForRequestTo,
} from "./support";

test("Acknowledge posts /close and the card leaves", async ({ page, request, st }) => {
  const itemID = await pushAck(request, st, "e2e: acknowledge me");
  await openBoard(page, st, "?producer_kind=all");
  await expect(row(page, itemID)).toBeVisible();

  const pending = waitForRequestTo(page, itemID, "close");
  await row(page, itemID).getByRole("button", { name: "Acknowledge" }).click();

  const { body } = await pending;
  // The close body carries the board's own client declaration and nothing else
  // answer-shaped.
  expect(body).toMatchObject({ answered_by: "attention-board" });
  expect((body as Record<string, unknown>).answer).toBeUndefined();
  await expect(row(page, itemID)).toHaveCount(0);
});

test("GAP: Acknowledge shows the 'Acknowledged.' note", async ({ page, request, st }) => {
  const itemID = await pushAck(request, st, "e2e: acknowledge note");
  await openBoard(page, st, "?producer_kind=all");

  const pending = waitForRequestTo(page, itemID, "close");
  await row(page, itemID).getByRole("button", { name: "Acknowledge" }).click();
  await pending;

  // parity.md: "Acknowledge: posts /close; the card leaves; note
  // 'Acknowledged.'". The React handler reloads the board and sets no note, so
  // the card leaves with no confirmation.
  await expect(row(page, itemID)).toHaveCount(0);
  await expect(page.locator(".note")).toContainText("Acknowledged.");
});

test("GAP: the corner X on an ack card posts /close", async ({ page, request, st }) => {
  const itemID = await pushAck(request, st, "e2e: ack corner X");
  await openBoard(page, st, "?producer_kind=all");

  const seen = captureItemRequests(page);
  await row(page, itemID).locator("[data-corner-x]").click();

  // parity.md: "Corner X on an ack card: same close." Wait for whichever
  // item-scoped request the click produced, then assert its verb.
  await expect
    .poll(() => requestsFor(seen, itemID).length, { timeout: 8000 })
    .toBeGreaterThan(0);
  expect(requestsFor(seen, itemID).map((c) => c.path)).toEqual([
    `/api/1.0/attention/${itemID}/close`,
  ]);
});

test("GAP: a dimmed record's corner X posts /close and answers nothing", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: dimmed record" });
  await answerViaAPI(request, st, itemID);

  await openBoard(page, st, "?producer_kind=all&hide=none");
  const card = row(page, itemID);
  await expect(card).toHaveClass(/dimmed/);
  // A dimmed record renders no answer form.
  await expect(card.locator("[data-other]")).toHaveCount(0);

  const seen = captureItemRequests(page);
  await card.locator("[data-corner-x]").click();

  await expect
    .poll(() => requestsFor(seen, itemID).length, { timeout: 8000 })
    .toBeGreaterThan(0);
  // parity.md: "Dimmed record: answered cards render dimmed; the corner X
  // closes them (posts /close, answers nothing)."
  expect(requestsFor(seen, itemID).map((c) => c.path)).toEqual([
    `/api/1.0/attention/${itemID}/close`,
  ]);
});
