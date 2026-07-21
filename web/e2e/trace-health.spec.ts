import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import {
  expect,
  test as base,
  type ConsoleMessage,
  type Locator,
  type Page,
  type Route
} from "playwright/test";

const fixtures = JSON.parse(
  readFileSync(
    fileURLToPath(new URL("../../testdata/agent-lens/browser-fixtures.json", import.meta.url)),
    "utf8"
  )
);

const test = base.extend<{ runtimeErrors: string[] }>({
  runtimeErrors: async ({ page }, use) => {
    const runtimeErrors: string[] = [];
    const onConsole = (message: ConsoleMessage) => {
      if (message.type() === "error") runtimeErrors.push(message.text());
    };
    const onPageError = (error: Error) => runtimeErrors.push(error.message);
    page.on("console", onConsole);
    page.on("pageerror", onPageError);
    await use(runtimeErrors);
    page.off("console", onConsole);
    page.off("pageerror", onPageError);
  }
});

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

type RecordedRequest = { method: string; pathname: string };

interface MockRequestRecorder {
  log: RecordedRequest[];
  unmatched: RecordedRequest[];
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
): Promise<MockRequestRecorder> {
  const counts = new Map<string, number>();
  const recorder: MockRequestRecorder = { log: [], unmatched: [] };

  page.on("request", (request) => {
    recorder.log.push({ method: request.method(), pathname: new URL(request.url()).pathname });
  });

  await page.route("**/api/sessions**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const requestCount = (counts.get(path) ?? 0) + 1;
    counts.set(path, requestCount);

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
      recorder.unmatched.push({ method: route.request().method(), pathname: path });
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
          await route.fulfill({ json: healthFixture("exact", key) });
        }
        return;
    }
  });

  return recorder;
}

function snapshotFor(key: string) {
  const trace = structuredClone(fixtures.traces.root);
  trace.session.id = key;
  trace.session.title = key === nextSession.key ? nextSession.title : fixtures.session.title;
  return { trace, city: fixtures.city };
}

function healthFixture(name: "exact" | "estimated" | "unavailable" | "agentFailed", key: string) {
  return { ...structuredClone(fixtures.health[name]), sessionKey: key };
}

function unavailableWithAgentFailure(key: string) {
  const health = healthFixture("unavailable", key);
  health.signals.subagents = structuredClone(fixtures.health.agentFailed.signals.subagents);
  return health;
}

function estimatedBadgeWithUnavailableSignals(key: string) {
  return { ...healthFixture("unavailable", key), badge: "estimated" };
}

function estimatedWithMissingSubagents(key: string) {
  const health = healthFixture("estimated", key);
  health.signals.subagents = {
    availability: "ready",
    quality: "estimated",
    reason: "mixed-agent-link-quality",
    affects: ["agent-lens"],
    exactCount: 1,
    derivedCount: 1,
    missingTraceCount: 2,
    unavailableTraceCount: 1
  };
  return health;
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

test.afterEach(async ({ runtimeErrors }) => {
  expect(runtimeErrors).toEqual([]);
});

test("shows failed and unavailable as distinct evidence states at compact width", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 760 });
  await mockApp(page, {
    health: async (key, _requestCount, route) => route.fulfill({ json: unavailableWithAgentFailure(key) })
  });
  await page.goto("/?session=synthetic-root");

  const trigger = healthTrigger(page);
  await expect(trigger).toHaveAttribute("aria-label", "Trace health: some evidence is limited");
  await expect(trigger.locator(".dock-dot-limited")).toHaveCount(1);

  const panel = await openHealth(page);
  await expect(panel.locator(".health-row-title")).toHaveText([
    "File reads",
    "Subagents",
    "Errors",
    "Verification"
  ]);

  const unavailable = healthRow(panel, "File reads");
  const failed = healthRow(panel, "Subagents");
  await expect(unavailable.locator(".health-row-state")).toHaveText("Cannot determine from this log");
  await expect(unavailable.locator(".health-signal-unavailable")).toHaveCount(1);
  await expect(unavailable).not.toHaveClass(/\berror\b/);
  await expect(unavailable.locator(".error")).toHaveCount(0);
  await expect(unavailable).not.toContainText("No reads occurred");
  const unavailableColors = await unavailable.evaluate((row) => {
    const marker = row.querySelector<HTMLElement>(".health-signal-unavailable");
    const probe = document.createElement("span");
    probe.style.color = "var(--alarm)";
    document.body.append(probe);
    const colors = {
      danger: getComputedStyle(probe).color,
      row: getComputedStyle(row).color,
      markerBorder: marker ? getComputedStyle(marker).borderColor : "",
      markerBackground: marker ? getComputedStyle(marker).backgroundColor : ""
    };
    probe.remove();
    return colors;
  });
  expect(unavailableColors.row).not.toBe(unavailableColors.danger);
  expect(unavailableColors.markerBorder).not.toBe(unavailableColors.danger);
  expect(unavailableColors.markerBackground).not.toBe(unavailableColors.danger);
  await expect(failed.locator(".health-row-state")).toHaveText("Failed");
  await expect(failed.locator(".health-signal-failed")).toHaveCount(1);
  await expect(page.locator(".city-scene canvas")).toBeVisible();
  await expect(page.locator(".deck-pos-count")).toHaveText("4 / 4");

  expect(
    await panel
      .locator(".health-row-toggle")
      .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("aria-expanded")))
  ).toEqual(["false", "false", "false", "false"]);
  const explanationControls = await panel
    .locator(".health-row-toggle")
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("aria-controls")));
  expect(explanationControls.every(Boolean)).toBe(true);
  expect(new Set(explanationControls).size).toBe(4);
  for (const explanationID of explanationControls) {
    await expect(panel.locator(`.health-explanation[id="${explanationID}"]`)).toHaveCount(1);
  }
  expect(await panel.locator(".health-technical").evaluateAll((nodes) => nodes.map((node) => node.hasAttribute("open")))).toEqual([
    false,
    false,
    false,
    false
  ]);

  await failed.locator(".health-row-toggle").click();
  await expect(failed.locator(".health-explanation")).toContainText("could not load subagent evidence");
  await expect(failed.locator(".health-explanation")).toContainText("Retry trace health");
  await failed.locator("summary").click();
  await expect(failed.locator(".health-technical")).toHaveAttribute("open", "");

  const box = await page.locator(".dock-pop").boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(320);
});

test("retries a failed signal through a health-only control", async ({ page }) => {
  const requests = await mockApp(page, {
    health: async (key, requestCount, route) => {
      await route.fulfill({ json: healthFixture(requestCount === 1 ? "agentFailed" : "exact", key) });
    }
  });
  await page.goto("/?session=synthetic-root");

  const panel = await openHealth(page);
  const failed = healthRow(panel, "Subagents");
  await expect(failed.locator(".health-row-state")).toHaveText("Failed");
  const retry = failed.getByRole("button", { name: "Retry trace health" });
  await expect(retry).toBeVisible();
  const before = requests.log.length;

  await retry.click();
  await expect(healthRow(panel, "Subagents").locator(".health-row-state")).toHaveText("Recorded directly");
  await expect(page.locator(".city-scene canvas")).toBeVisible();
  await expect(page.locator(".deck-pos-count")).toHaveText("4 / 4");
  expect(requests.log.slice(before)).toEqual([{
    method: "GET",
    pathname: `/api/sessions/${encodeURIComponent(fixtures.session.key)}/health`
  }]);
  expect(requests.unmatched).toEqual([]);
});

test("explains missing and unavailable traces in estimated subagent copy", async ({ page }) => {
  await mockApp(page, {
    health: async (key, _requestCount, route) => route.fulfill({ json: estimatedWithMissingSubagents(key) })
  });
  await page.goto("/?session=synthetic-root");

  const panel = await openHealth(page);
  const subagents = healthRow(panel, "Subagents");
  await expect(subagents.locator(".health-row-state")).toHaveText("Partly inferred");
  await subagents.locator(".health-row-toggle").click();
  await expect(subagents.locator(".health-explanation")).toContainText("2 traces are missing");
  await expect(subagents.locator(".health-explanation")).toContainText("1 trace is unavailable");
});

test("keeps trace health inside the intermediate viewport", async ({ page }) => {
  await page.setViewportSize({ width: 768, height: 820 });
  await mockApp(page);
  await page.goto("/?session=synthetic-root");

  await openHealth(page);
  const bounds = await page.locator(".dock-pop-health").evaluate((element) => {
    const rect = element.getBoundingClientRect();
    const dockRect = element.parentElement?.getBoundingClientRect();
    const deckRect = document.querySelector(".deck")?.getBoundingClientRect();
    const stripRect = document.querySelector(".dock-strip")?.getBoundingClientRect();
    return {
      left: rect.left,
      right: rect.right,
      top: rect.top,
      bottom: rect.bottom,
      dockBottom: dockRect?.bottom ?? 0,
      deckTop: deckRect?.top ?? 0,
      stripLeft: stripRect?.left ?? 0,
      viewportHeight: window.innerHeight
    };
  });

  expect(bounds.left).toBeGreaterThanOrEqual(0);
  expect(bounds.stripLeft - bounds.right).toBe(10);
  expect(bounds.top).toBeGreaterThanOrEqual(0);
  expect(bounds.bottom).toBeLessThanOrEqual(bounds.viewportHeight);
  expect(bounds.bottom).toBeLessThanOrEqual(bounds.deckTop);
  expect(bounds.dockBottom).toBeLessThanOrEqual(bounds.deckTop);
});

test("uses the server badge without deriving a replacement from signals", async ({ page }) => {
  await mockApp(page, {
    health: async (key, _requestCount, route) => {
      await route.fulfill({ json: estimatedBadgeWithUnavailableSignals(key) });
    }
  });
  await page.goto("/?session=synthetic-root");

  const trigger = healthTrigger(page);
  await expect(trigger).toHaveAttribute("aria-label", "Trace health: some evidence is estimated");
  await expect(trigger.locator(".dock-dot-estimated")).toHaveCount(1);
  await expect(trigger.locator(".dock-dot-limited")).toHaveCount(0);

  const panel = await openHealth(page);
  const reads = healthRow(panel, "File reads");
  await expect(reads.locator(".health-row-state")).toHaveText("Cannot determine from this log");
  await expect(reads.locator(".health-technical")).not.toHaveAttribute("open", "");
});

test("relates the pop trigger to its panel and restores focus on both close paths", async ({ page }) => {
  await mockApp(page);
  await page.goto("/?session=synthetic-root");

  const trigger = healthTrigger(page);
  await expect(trigger).toHaveAttribute(
    "aria-label",
    "Trace health: evidence recorded directly where applicable"
  );
  await expect(trigger.locator(".dock-dot")).toHaveCount(0);
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
  const oldHealthDelivered = deferred();
  await mockApp(
    page,
    {
      health: async (key, _requestCount, route) => {
        if (key === fixtures.session.key) {
          oldHealthStarted.resolve();
          await oldHealth.promise;
          await route.fulfill({ json: healthFixture("agentFailed", key) });
          oldHealthDelivered.resolve();
          return;
        }
        await route.fulfill({ json: healthFixture("estimated", key) });
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
  const panel = await openHealth(page);
  await expect(healthRow(panel, "File reads").locator(".health-row-state")).toHaveText("Partly inferred");
  await expect(healthRow(panel, "Subagents").locator(".health-row-state")).toHaveText("Recorded directly");
  oldHealth.resolve();
  await oldHealthDelivered.promise;
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
  );
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: some evidence is estimated"
  );
  await expect(healthTrigger(page).locator(".dock-dot-estimated")).toHaveCount(1);
  await expect(healthRow(panel, "File reads").locator(".health-row-state")).toHaveText("Partly inferred");
  await expect(healthRow(panel, "Subagents").locator(".health-row-state")).toHaveText("Recorded directly");
});

test("keeps health failure local and retries only the health request", async ({ page }) => {
  const requests = await mockApp(page, {
    health: async (key, requestCount, route) => {
      if (requestCount === 1) {
        await route.fulfill({ status: 503, body: "health temporarily unavailable" });
      } else {
        await route.fulfill({ json: healthFixture("exact", key) });
      }
    }
  });
  await page.goto("/?session=synthetic-root");

  const panel = await openHealth(page);
  await expect(panel.getByRole("alert")).toContainText("health temporarily unavailable");
  await expect(page.locator(".toast.error")).toHaveCount(0);
  const before = requests.log.length;

  await panel.getByRole("button", { name: "Retry trace health" }).click();
  await expect(panel.locator(".health-row")).toHaveCount(4);
  expect(requests.log.slice(before)).toEqual([{
    method: "GET",
    pathname: `/api/sessions/${encodeURIComponent(fixtures.session.key)}/health`
  }]);
  expect(requests.unmatched).toEqual([]);
});

test("loads refreshed health only after a successful fresh scan and snapshot", async ({ page }) => {
  const freshSnapshot = deferred();
  const freshSnapshotStarted = deferred();
  const order: string[] = [];
  let healthRequests = 0;
  await mockApp(page, {
    sessions: async (fresh, route) => {
      if (fresh) order.push("fresh-scan");
      await route.fulfill({ json: [fixtures.session] });
    },
    snapshot: async (_key, requestCount, route) => {
      if (requestCount === 2) {
        order.push("fresh-snapshot-started");
        freshSnapshotStarted.resolve();
        await freshSnapshot.promise;
      }
      await route.fulfill({ json: snapshotFor(fixtures.session.key) });
      if (requestCount === 2) order.push("fresh-snapshot-delivered");
    },
    health: async (key, _requestCount, route) => {
      healthRequests++;
      order.push(`health-${healthRequests}`);
      const response = healthRequests === 1
        ? healthFixture("estimated", key)
        : healthFixture("exact", key);
      await route.fulfill({ json: response });
    }
  });
  await page.goto("/?session=synthetic-root");
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: some evidence is estimated"
  );
  const initialPanel = await openHealth(page);
  const estimatedReads = healthRow(initialPanel, "File reads");
  await expect(estimatedReads.locator(".health-row-state")).toHaveText("Partly inferred");
  await expect(estimatedReads.locator(".health-technical")).not.toHaveAttribute("open", "");
  await estimatedReads.locator(".health-row-toggle").click();
  await expect(estimatedReads.locator(".health-explanation")).toContainText("2 reads");
  await expect(estimatedReads.locator(".health-explanation")).toContainText("1 read");
  await estimatedReads.locator("summary").click();
  await expect(estimatedReads.locator("dl")).toContainText("Direct2");
  await expect(estimatedReads.locator("dl")).toContainText("Inferred1");

  await page.getByRole("button", { name: "Rescan sessions" }).click();
  await freshSnapshotStarted.promise;
  expect(healthRequests).toBe(1);
  expect(order).toEqual(["health-1", "fresh-scan", "fresh-snapshot-started"]);
  freshSnapshot.resolve();

  await expect.poll(() => healthRequests).toBe(2);
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: evidence recorded directly where applicable"
  );
  const refreshedPanel = await openHealth(page);
  await expect(healthRow(refreshedPanel, "File reads").locator(".health-row-state")).toHaveText("Recorded directly");
  expect(order.indexOf("fresh-snapshot-delivered")).toBeGreaterThan(
    order.indexOf("fresh-snapshot-started")
  );
  expect(order.indexOf("health-2")).toBeGreaterThan(order.indexOf("fresh-snapshot-delivered"));
});

test("does not refresh health after a failed fresh scan", async ({ page }) => {
  let healthRequests = 0;
  await mockApp(page, {
    sessions: async (fresh, route) => {
      if (fresh) {
        await route.fulfill({ status: 503, body: "fresh scan unavailable" });
      } else {
        await route.fulfill({ json: [fixtures.session] });
      }
    },
    health: async (key, _requestCount, route) => {
      healthRequests++;
      await route.fulfill({ json: healthFixture("exact", key) });
    }
  });
  await page.goto("/?session=synthetic-root");
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: evidence recorded directly where applicable"
  );

  await page.getByRole("button", { name: "Rescan sessions" }).click();
  await expect(page.locator(".toast.error")).toContainText("fresh scan unavailable");
  expect(healthRequests).toBe(1);
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: evidence recorded directly where applicable"
  );
});

test("does not refresh health after a successful scan whose snapshot fails", async ({ page }) => {
  let healthRequests = 0;
  await mockApp(page, {
    sessions: async (_fresh, route) => route.fulfill({ json: [fixtures.session] }),
    snapshot: async (key, requestCount, route) => {
      if (requestCount === 2) {
        await route.fulfill({ status: 503, body: "fresh snapshot unavailable" });
      } else {
        await route.fulfill({ json: snapshotFor(key) });
      }
    },
    health: async (key, _requestCount, route) => {
      healthRequests++;
      await route.fulfill({ json: healthFixture("exact", key) });
    }
  });
  await page.goto("/?session=synthetic-root");
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: evidence recorded directly where applicable"
  );

  await page.getByRole("button", { name: "Rescan sessions" }).click();
  await expect(page.locator(".toast.error")).toContainText("fresh snapshot unavailable");
  expect(healthRequests).toBe(1);
  await expect(page.locator(".deck-pos-count")).toHaveText("4 / 4");
});

test("loads fallback-session health only after its fresh snapshot applies", async ({ page }) => {
  const fallbackSnapshot = deferred();
  const fallbackSnapshotStarted = deferred();
  const order: string[] = [];
  let fallbackHealthRequests = 0;
  await mockApp(page, {
    sessions: async (fresh, route) => {
      await route.fulfill({ json: fresh ? [nextSession] : [fixtures.session] });
    },
    snapshot: async (key, _requestCount, route) => {
      if (key === nextSession.key) {
        order.push("fallback-snapshot-started");
        fallbackSnapshotStarted.resolve();
        await fallbackSnapshot.promise;
        order.push("fallback-snapshot-delivered");
      }
      await route.fulfill({ json: snapshotFor(key) });
    },
    health: async (key, _requestCount, route) => {
      if (key === nextSession.key) {
        fallbackHealthRequests++;
        order.push("fallback-health");
      }
      await route.fulfill({ json: healthFixture("exact", key) });
    }
  });
  await page.goto("/?session=synthetic-root");
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: evidence recorded directly where applicable"
  );

  await page.getByRole("button", { name: "Rescan sessions" }).click();
  await fallbackSnapshotStarted.promise;
  expect(fallbackHealthRequests).toBe(0);
  expect(order).toEqual(["fallback-snapshot-started"]);
  fallbackSnapshot.resolve();

  await expect.poll(() => fallbackHealthRequests).toBe(1);
  expect(order).toEqual(["fallback-snapshot-started", "fallback-snapshot-delivered", "fallback-health"]);
  await expect(page.locator(".session-row.active")).toContainText(nextSession.title);
  await expect(healthTrigger(page)).toHaveAttribute(
    "aria-label",
    "Trace health: evidence recorded directly where applicable"
  );
});
