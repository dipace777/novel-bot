import { chromium, expect, test } from "@playwright/test";
import type { Browser } from "@playwright/test";

// The API owns Chromium. This test connects to it without launching a local browser.
test.skip(
  ({ browserName }) => browserName !== "chromium",
  "Novel Bot exposes Chromium over CDP.",
);
test.use({ trace: "off", screenshot: "off", video: "off" });

test("creates a Novel Bot session and visits Example Domain", async ({
  request,
}) => {
  test.setTimeout(90_000);

  const apiKey = process.env.NOVEL_BOT_API_KEY?.trim();
  if (!apiKey) {
    throw new Error(
      "Set NOVEL_BOT_API_KEY to an application API key generated in the Novel Bot UI.",
    );
  }

  const apiURL = process.env.NOVEL_BOT_API_URL ?? "http://127.0.0.1:8080";
  const sessionsURL = new URL("/sessions", apiURL).toString();
  const headers = { Authorization: `Bearer ${apiKey}` };
  let sessionID: string | undefined;
  let browser: Browser | undefined;

  try {
    // Send the empty JSON object required by POST /sessions.
    const created = await request.post(sessionsURL, {
      headers,
      data: {},
      timeout: 20_000,
    });
    expect(
      created.status(),
      "Session creation failed. Check your API key, running API/worker, and available capacity.",
    ).toBe(201);

    const session = await created.json();
    expect(typeof session.id, "The API must return a session ID.").toBe(
      "string",
    );
    sessionID = session.id;
    expect(sessionID).toMatch(/^[a-f0-9]{32}$/);
    expect(
      typeof session.cdp_url,
      "The API must return a CDP WebSocket URL.",
    ).toBe("string");
    expect(new URL(session.cdp_url).protocol).toMatch(/^wss?:$/);

    // Authenticate the CDP WebSocket handshake with the same tenant's API key.
    browser = await chromium.connectOverCDP(session.cdp_url, {
      headers,
      timeout: 20_000,
    });

    const context = browser.contexts()[0];
    if (!context)
      throw new Error("The remote browser did not expose its default context.");

    const page = await context.newPage();
    const response = await page.goto("https://example.com", {
      waitUntil: "domcontentloaded",
      timeout: 25_000,
    });
    expect(
      response?.ok(),
      "The destination must return a successful HTTP response.",
    ).toBe(true);
    await expect(page).toHaveTitle("Example Domain");
    await expect(page.locator("body")).toContainText("documentation examples");
    // Keep the page visible briefly during local testing before deleting the session.
    await page.waitForTimeout(5_000);
  } finally {
    try {
      // Disconnecting Playwright alone does not release Novel Bot's session quota.
      // Delete even if CDP connection, navigation, or an assertion failed.
      if (sessionID) {
        const deleted = await request.delete(
          `${sessionsURL}/${encodeURIComponent(sessionID)}`,
          {
            headers,
            timeout: 10_000,
          },
        );
        expect(
          [204, 404],
          `Session cleanup failed with HTTP ${deleted.status()}; the worker may still hold capacity.`,
        ).toContain(deleted.status());
      }
    } finally {
      await browser?.close();
    }
  }
});
