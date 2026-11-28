// parity.md § Permission cards — the Allow/Deny pair is held client-side for
// HOLD_MS (4.5 s) and then posted as answer{decision}. Each test asserts the
// request that left the page; the store's own state does not settle in this
// harness (the answer-hold waits on delivery), so it is never the assertion.
//
// The corner X on a permission card dispatches Deny; the U handler ignores a
// keystroke typed into a focused text field.

import {
  expect,
  openBoard,
  pushMessage,
  pushPermission,
  row,
  test,
  waitForRequestTo,
} from "./support";

test("Allow is held, then posts answer{decision:allow}", async ({ page, request, st }) => {
  const itemID = await pushPermission(request, st, "e2e: allow me");
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  const clickedAt = Date.now();
  const pending = waitForRequestTo(page, itemID, "answer");

  await card.locator('[data-decision="allow"]').click();

  // Immediately after the click the bar is active and the card is still there.
  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);
  await expect(row(page, itemID)).toBeVisible();

  const { body } = await pending;
  expect(body).toMatchObject({ decision: "allow" });
  // The hold is load-bearing: the answer cannot have left immediately.
  expect(Date.now() - clickedAt).toBeGreaterThanOrEqual(3000);

  // Once delivered the bar goes quiet again.
  await expect(page.locator("[data-undo-bar]")).not.toHaveClass(/is-active/);
});

test("Deny posts answer{decision:deny}", async ({ page, request, st }) => {
  const itemID = await pushPermission(request, st, "e2e: deny me");
  await openBoard(page, st, "?producer_kind=all");

  const pending = waitForRequestTo(page, itemID, "answer");
  await row(page, itemID).locator('[data-decision="deny"]').click();

  const { body } = await pending;
  expect(body).toMatchObject({ decision: "deny" });
});

test("Undo cancels the hold, restores the card, and delivers nothing", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushPermission(request, st, "e2e: undo me");
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  await card.locator('[data-decision="allow"]').click();
  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);

  await page.locator("[data-undo]").click();
  await expect(page.locator("[data-undo-bar]")).not.toHaveClass(/is-active/);
  await expect(row(page, itemID)).toBeVisible();

  // Wait past the hold window; nothing may be delivered.
  await page.waitForTimeout(5500);
  const stored = await request.get(`${st.baseURL}/api/1.0/attention/${itemID}`);
  const item = (await stored.json()) as { answered_at?: string };
  expect(item.answered_at, "Undo must deliver nothing").toBeUndefined();
  await expect(row(page, itemID)).toBeVisible();
});

test("the U key cancels the hold like Undo", async ({ page, request, st }) => {
  const itemID = await pushPermission(request, st, "e2e: U undoes");
  await openBoard(page, st, "?producer_kind=all");

  await row(page, itemID).locator('[data-decision="allow"]').click();
  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);

  await page.keyboard.press("u");
  await expect(page.locator("[data-undo-bar]")).not.toHaveClass(/is-active/);

  await page.waitForTimeout(5500);
  const stored = await request.get(`${st.baseURL}/api/1.0/attention/${itemID}`);
  const item = (await stored.json()) as { answered_at?: string };
  expect(item.answered_at).toBeUndefined();
});

test("the U handler ignores a focused text field", async ({ page, request, st }) => {
  const itemID = await pushPermission(request, st, "e2e: U while typing");
  const messageID = await pushMessage(request, st, { payload: "e2e: U while typing anchor" });
  await openBoard(page, st, "?producer_kind=all");

  await row(page, itemID).locator('[data-decision="allow"]').click();
  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);

  // parity.md: "U key: same as Undo; ignored while typing in a field". A `u`
  // typed into a field must not cancel the hold.
  await row(page, messageID).locator("[data-other]").focus();
  await page.keyboard.type("u");

  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);
});

test("answering another card flushes the held answer immediately", async ({
  page,
  request,
  st,
}) => {
  const first = await pushPermission(request, st, "e2e: flush first");
  const second = await pushPermission(request, st, "e2e: flush second");
  await openBoard(page, st, "?producer_kind=all");

  await row(page, first).locator('[data-decision="allow"]').click();
  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);

  await page.waitForTimeout(600);
  const firstPending = waitForRequestTo(page, first, "answer", 4000);
  const decidedAt = Date.now();
  await row(page, second).locator('[data-decision="deny"]').click();

  const { body } = await firstPending;
  expect(body).toMatchObject({ decision: "allow" });
  // Flushed on the second decision, not after its own hold window.
  expect(Date.now() - decidedAt).toBeLessThan(3000);

  // Only one answer is ever held: the second card is the one now held.
  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);
});

test("pagehide delivers the held answer", async ({ page, request, st }) => {
  const itemID = await pushPermission(request, st, "e2e: pagehide delivers");
  await openBoard(page, st, "?producer_kind=all");

  await row(page, itemID).locator('[data-decision="allow"]').click();
  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);

  // Leaving the page inside the hold must still deliver: the answer is posted
  // with `keepalive`, so it outlives the document. The request itself cannot be
  // observed reliably across a navigation, so the store's answered_at is the
  // observable — a permission item records answered_at while staying open.
  await page.goto("about:blank");

  await expect
    .poll(
      async () => {
        const res = await request.get(`${st.baseURL}/api/1.0/attention/${itemID}`);
        const item = (await res.json()) as { answered_at?: string; decision?: string };
        return `${item.answered_at ?? ""}|${item.decision ?? ""}`;
      },
      { timeout: 8000 },
    )
    .toContain("allow");
});

test("the corner X on a permission card dispatches Deny", async ({ page, request, st }) => {
  const itemID = await pushPermission(request, st, "e2e: permission corner X");
  await openBoard(page, st, "?producer_kind=all");

  const pending = waitForRequestTo(page, itemID, "answer", 8000);
  await row(page, itemID).locator("[data-corner-x]").click();

  const { body } = await pending;
  // parity.md: "Corner X on a permission card: dispatches the Deny control."
  expect(body).toMatchObject({ decision: "deny" });
});
