// parity.md § Live channel (GET /api/1.0/attention/stream, SSE). The React page
// subscribes with EventSource and reloads the board read on every message, so
// the observable is a row appearing/disappearing with no page reload.
//
// The stale indicator and the uncovered markers are known gaps (no element in
// the React page); the stale row is a failing test, the rest are marked in
// their own specs.

import {
  answerViaAPI,
  expect,
  openBoard,
  pushMessage,
  row,
  test,
} from "./support";

test("a pushed row swaps in over the stream with no page reload", async ({
  page,
  request,
  st,
}) => {
  await openBoard(page, st, "?producer_kind=all");
  const before = await page.evaluate(() => performance.timeOrigin);

  const itemID = await pushMessage(request, st, { payload: "e2e: streamed in" });

  await expect(row(page, itemID)).toBeVisible();
  // Same document: no reload happened to get the row.
  expect(await page.evaluate(() => performance.timeOrigin)).toBe(before);
});

test("a removed row leaves over the stream", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: streamed out" });
  await openBoard(page, st, "?producer_kind=all");
  await expect(row(page, itemID)).toBeVisible();
  const before = await page.evaluate(() => performance.timeOrigin);

  await answerViaAPI(request, st, itemID);

  await expect(row(page, itemID)).toHaveCount(0);
  expect(await page.evaluate(() => performance.timeOrigin)).toBe(before);
});

test("an in-progress answer survives a stream re-render", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: carry my form" });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  await card.locator('[data-option="alpha"]').check();
  await card.locator("[data-other]").fill("half typed");
  await expect(card.locator('[data-option="alpha"]')).not.toBeChecked();

  // A second push makes the stream re-render the whole list.
  const other = await pushMessage(request, st, { payload: "e2e: carry trigger" });
  await expect(row(page, other)).toBeVisible();

  // Picks and typed text survive.
  await expect(row(page, itemID).locator("[data-other]")).toHaveValue("half typed");
});

test("a shown failure note survives a stream re-render", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: note survives" });
  await openBoard(page, st, "?producer_kind=all");

  await page.route("**/api/1.0/attention/*/speak", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: '{"error":{"code":"INTERNAL","message":"e2e: speak forced to fail"}}',
    }),
  );

  await row(page, itemID).locator("[data-speak]").click();
  await expect(row(page, itemID).locator(".note.failed")).toHaveText(
    "Read aloud failed - HTTP 500",
  );

  const other = await pushMessage(request, st, { payload: "e2e: note trigger" });
  await expect(row(page, other)).toBeVisible();

  await expect(row(page, itemID).locator(".note.failed")).toHaveText(
    "Read aloud failed - HTTP 500",
  );
});

test("GAP: the stream stale indicator appears when the stream errors", async ({
  page,
  st,
}) => {
  await page.route("**/api/1.0/attention/stream", (route) => route.abort());

  await openBoard(page, st, "?producer_kind=all");

  // parity.md: "Stream stale indicator: shown on stream error, hidden on the
  // next message." The React page renders no such element.
  await expect(page.locator("[data-stream-stale]")).toBeVisible();
});

test("a failed stream reconnects and picks up the next message", async ({
  page,
  request,
  st,
}) => {
  const streamPattern = "**/api/1.0/attention/stream";
  await page.route(streamPattern, (route) => route.abort());

  await openBoard(page, st, "?producer_kind=all");

  // Let the browser's own retry reach the real endpoint, then wait until it has
  // a live connection before pushing — a message pushed while the stream is
  // still down is never delivered.
  const retry = page.waitForRequest(
    (r) => r.url().endsWith("/api/1.0/attention/stream"),
    { timeout: 15_000 },
  );
  await page.unroute(streamPattern);
  await retry;
  await page.waitForTimeout(750);

  const itemID = await pushMessage(request, st, { payload: "e2e: after reconnect" });
  await expect(row(page, itemID)).toBeVisible({ timeout: 20_000 });
});
