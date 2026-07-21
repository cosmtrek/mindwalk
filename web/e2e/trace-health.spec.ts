import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, test, type Locator, type Page, type Route } from "playwright/test";

const fixtures = JSON.parse(
  readFileSync(
    fileURLToPath(new URL("../../testdata/agent-lens/browser-fixtures.json", import.meta.url)),
    "utf8"
  )
);

const nextSession = {
  ...fixtures.session,
  key: "synthetic-next",
  id: "synthetic-next",
  title: "Next fictional trace"
};

interface AppHandlers {
  sessions?: (fresh: boolean, route: Route) => Promise<void>;
  snapshot?: (key: string, requestCount: number, route: Route) => Promise<void>;
  health?: (key: string, requestCount: number, route: Route) => Promise<void>;
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((release) => {
    resolve = release;
  });
  return { promise, resolve };
}

async function mockApp(
  page: Page,
  handlers: AppHandlers = {},
  sessions = [fixtures.session]
): Promise<Map<string, number>> {
  const requests = new Map<string, number>();

  await page.route("**/api/sessions**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const requestCount = (requests.get(path) ?? 0) + 1;
    requests.set(path, requestCount);

    if (path === "/api/sessions") {
      if (handlers.sessions) {
        await handlers.sessions(url.searchParams.get("fresh") === "1", route);
      } else {
        await route.fulfill({ json: sessions });
      }
      return;
    }

    const match = path.match(/^\/api\/sessions\/([^/]+)\/(snapshot|agents|health|report)$/);
    if (!match) {
      await route.fulfill({ status: 404, body: "fictional route not found" });
      return;
    }

    const key = decodeURIComponent(match[1]);
    switch (match[2]) {
      case "snapshot":
        if (handlers.snapshot) {
          await handlers.snapshot(key, requestCount, route);
        } else {
          await route.fulfill({ json: snapshotFor(key) });
        }
        return;
      case "agents":
        await route.fulfill({ json: { ...fixtures.graph, rootSessionKey: key } });
        return;
      case "report":
        await route.fulfill({ json: fixtures.reportStatus });
        return;
      case "health":
        if (handlers.health) {
          await handlers.health(key, requestCount, route);
        } else {
          await route.fulfill({ json: exactHealth(key) });
        }
        return;
    }
  });

  return requests;
}

function snapshotFor(key: string) {
  const trace = structuredClone(fixtures.traces.root);
  trace.session.id = key;
  trace.session.title = key === nextSession.key ? nextSession.title : fixtures.session.title;
  return { trace, city: fixtures.city };
}

function exactHealth(key: string) {
  return {
    version: 1,
    sessionKey: key,
    signals: {
      reads: {
        availability: "ready",
        quality: "exact",
        reason: "structured-read-targets",
        affects: ["map"],
        directCount: 3,
        inferredCount: 0
      },
      errors: {
        availability: "ready",
        quality: "exact",
        reason: "structured-error-status",
        affects: ["error-rate"],
        recognizedCount: 0
      },
      verification: {
        availability: "ready",
        quality: "exact",
        reason: "structured-verification-results",
        affects: ["judge-verification"],
        recognizedCount: 1,
        knownResultCount: 1,
        unknownResultCount: 0,
        editsAfterLastVerify: 0
      },
      subagents: {
        availability: "not-applicable",
        reason: "no-subagents",
        affects: ["agent-lens"],
        exactCount: 0,
        derivedCount: 0,
        missingTraceCount: 0,
        unavailableTraceCount: 0
      }
    }
  };
}

function mixedHealth(key: string, badge: "estimated" | "limited" = "limited") {
  return {
    version: 1,
    sessionKey: key,
    badge,
    signals: {
      reads: {
        availability: "ready",
        quality: "unavailable",
        reason: "read-signal-unavailable",
        affects: ["map", "judge-exploration"],
        directCount: 0,
        inferredCount: 0
      },
      errors: {
        availability: "ready",
        quality: "exact",
        reason: "structured-error-status",
        affects: ["error-rate"],
        recognizedCount: 0
      },
      verification: {
        availability: "ready",
        quality: "estimated",
        reason: "some-verification-results-unknown",
        affects: ["judge-verification"],
        recognizedCount: 2,
        knownResultCount: 1,
        unknownResultCount: 1,
        editsAfterLastVerify: 1
      },
      subagents: {
        availability: "failed",
        reason: "agent-graph-load-failed",
        affects: ["agent-lens"],
        exactCount: 0,
        derivedCount: 0,
        missingTraceCount: 0,
        unavailableTraceCount: 0
      }
    }
  };
}

function healthTrigger(page: Page): Locator {
  return page.locator(".dock-strip").getByRole("button", { name: /Trace health/ });
}

async function openHealth(page: Page): Promise<Locator> {
  await healthTrigger(page).click();
  const panel = page.getByRole("region", { name: "Trace health" });
  await panel.waitFor();
  return panel;
}

function healthRow(panel: Locator, title: string): Locator {
  return panel.locator(".health-row").filter({ hasText: title });
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.clear());
});

test("shows failed and unavailable as distinct evidence states at compact width", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 760 });
  await mockApp(page, {
    health: async (key, _requestCount, route) => route.fulfill({ json: mixedHealth(key) })
  });
  await page.goto("/?session=synthetic-root");

  const trigger = healthTrigger(page);
  await expect(trigger).toHaveAttribute("aria-label", "Trace health: some evidence is limited");
  await expect(trigger.locator(".dock-dot-limited")).toHaveCount(1);

  const panel = await openHealth(page);
  await expect(panel.locator(".health-row-title")).toHaveText([
    "File reads",
    "Subagents",
    "Verification",
    "Errors"
  ]);

  const unavailable = healthRow(panel, "File reads");
  const failed = healthRow(panel, "Subagents");
  await expect(unavailable.locator(".health-row-state")).toHaveText("Unavailable");
  await expect(unavailable.locator(".health-signal-unavailable")).toHaveCount(1);
  await expect(failed.locator(".health-row-state")).toHaveText("Failed");
  await expect(failed.locator(".health-signal-failed")).toHaveCount(1);

  expect(
    await panel
      .locator(".health-row-toggle")
      .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("aria-expanded")))
  ).toEqual(["false", "false", "false", "false"]);
  expect(await panel.locator(".health-technical").evaluateAll((nodes) => nodes.map((node) => node.hasAttribute("open")))).toEqual([
    false,
    false,
    false,
    false
  ]);

  await failed.locator(".health-row-toggle").click();
  await expect(failed.locator(".health-explanation")).toContainText("could not load subagent evidence");
  await failed.locator("summary").click();
  await expect(failed.locator(".health-technical")).toHaveAttribute("open", "");

  const box = await page.locator(".dock-pop").boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(320);
});

test("uses the server badge without deriving a replacement from signals", async ({ page }) => {
  await mockApp(page, {
    health: async (key, _requestCount, route) => route.fulfill({ json: mixedHealth(key, "estimated") })
  });
  await page.goto("/?session=synthetic-root");

  const trigger = healthTrigger(page);
  await expect(trigger).toHaveAttribute("aria-label", "Trace health: some evidence is estimated");
  await expect(trigger.locator(".dock-dot-estimated")).toHaveCount(1);
  await expect(trigger.locator(".dock-dot-limited")).toHaveCount(0);
});

test("relates the pop trigger to its panel and restores focus on both close paths", async ({ page }) => {
  await mockApp(page);
  await page.goto("/?session=synthetic-root");

  const trigger = healthTrigger(page);
  await expect(trigger).toHaveAttribute("aria-expanded", "false");
  const controls = await trigger.getAttribute("aria-controls");
  expect(controls).toBeTruthy();

  await trigger.focus();
  await page.keyboard.press("Enter");
  await expect(trigger).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator(`#${controls}`)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(trigger).toHaveAttribute("aria-expanded", "false");
  await expect(trigger).toBeFocused();

  await page.keyboard.press("Enter");
  await page.getByRole("button", { name: "Close trace health" }).click();
  await expect(trigger).toHaveAttribute("aria-expanded", "false");
  await expect(trigger).toBeFocused();
});

test("rejects a delayed health response after switching sessions", async ({ page }) => {
  const oldHealth = deferred();
  const oldHealthStarted = deferred();
  await mockApp(
    page,
    {
      health: async (key, _requestCount, route) => {
        if (key === fixtures.session.key) {
          oldHealthStarted.resolve();
          await oldHealth.promise;
          await route.fulfill({ json: mixedHealth(key, "limited") });
          return;
        }
        await route.fulfill({ json: mixedHealth(key, "estimated") });
      }
    },
    [fixtures.session, nextSession]
  );
  await page.goto("/?session=synthetic-root");
  await oldHealthStarted.promise;

  await page.getByRole("button", { name: /Next fictional trace/ }).click();
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: some evidence is estimated"
  );
  oldHealth.resolve();
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: some evidence is estimated"
  );
  await expect(healthTrigger(page).locator(".dock-dot-estimated")).toHaveCount(1);
});

test("keeps health failure local and retries only the health request", async ({ page }) => {
  const requests = await mockApp(page, {
    health: async (key, requestCount, route) => {
      if (requestCount === 1) {
        await route.fulfill({ status: 503, body: "health temporarily unavailable" });
      } else {
        await route.fulfill({ json: exactHealth(key) });
      }
    }
  });
  await page.goto("/?session=synthetic-root");

  const panel = await openHealth(page);
  await expect(panel.getByRole("alert")).toContainText("health temporarily unavailable");
  await expect(page.locator(".toast.error")).toHaveCount(0);
  const before = new Map(requests);

  await panel.getByRole("button", { name: "Retry trace health" }).click();
  await expect(panel.locator(".health-row")).toHaveCount(4);
  expect(requests.get("/api/sessions/synthetic-root/health")).toBe(2);
  expect(requests.get("/api/sessions")).toBe(before.get("/api/sessions"));
  expect(requests.get("/api/sessions/synthetic-root/snapshot")).toBe(
    before.get("/api/sessions/synthetic-root/snapshot")
  );
  expect(requests.get("/api/sessions/synthetic-root/agents")).toBe(
    before.get("/api/sessions/synthetic-root/agents")
  );
  expect(requests.get("/api/sessions/synthetic-root/report")).toBe(
    before.get("/api/sessions/synthetic-root/report")
  );
});

test("loads refreshed health only after a successful fresh scan and snapshot", async ({ page }) => {
  const freshScan = deferred();
  const order: string[] = [];
  let healthRequests = 0;
  await mockApp(page, {
    sessions: async (fresh, route) => {
      if (fresh) {
        order.push("fresh-scan");
        await freshScan.promise;
      }
      await route.fulfill({ json: [fixtures.session] });
    },
    snapshot: async (_key, requestCount, route) => {
      if (requestCount === 2) order.push("fresh-snapshot");
      await route.fulfill({ json: snapshotFor(fixtures.session.key) });
    },
    health: async (key, _requestCount, route) => {
      healthRequests++;
      order.push(`health-${healthRequests}`);
      const response = healthRequests === 1 ? exactHealth(key) : mixedHealth(key, "estimated");
      await route.fulfill({ json: response });
    }
  });
  await page.goto("/?session=synthetic-root");
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: evidence recorded directly where applicable"
  );

  await page.getByRole("button", { name: "Rescan sessions" }).click();
  await expect.poll(() => order.includes("fresh-scan")).toBe(true);
  expect(healthRequests).toBe(1);
  freshScan.resolve();

  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: some evidence is estimated"
  );
  expect(order.indexOf("fresh-snapshot")).toBeGreaterThan(order.indexOf("fresh-scan"));
  expect(order.indexOf("health-2")).toBeGreaterThan(order.indexOf("fresh-snapshot"));
});
