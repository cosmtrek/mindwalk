import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, test, type Page, type Route } from "playwright/test";

const fixtures = JSON.parse(
  readFileSync(
    fileURLToPath(new URL("../../testdata/agent-lens/browser-fixtures.json", import.meta.url)),
    "utf8"
  )
);

type TraceRoute = (agentID: string, requestCount: number, route: Route) => Promise<void>;

interface MockAppRoutes {
  sessions?: (fresh: boolean, route: Route) => Promise<void>;
  snapshot?: (route: Route) => Promise<void>;
  graph?: (route: Route) => Promise<void>;
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((release) => {
    resolve = release;
  });
  return { promise, resolve };
}

async function installHeldRecorder(page: Page) {
  await page.addInitScript(() => {
    const recording = window as Window & {
      __finishFakeRecording?: () => void;
      __fakeRecordingStopped?: boolean;
    };

    class FakeMediaRecorder {
      static isTypeSupported() {
        return true;
      }

      state = "inactive";
      mimeType: string;
      ondataavailable: ((event: { data: Blob }) => void) | null = null;
      onerror: (() => void) | null = null;
      onstop: (() => void) | null = null;

      constructor(_stream: unknown, options?: { mimeType?: string }) {
        this.mimeType = options?.mimeType ?? "video/webm";
      }

      start() {
        this.state = "recording";
      }

      stop() {
        this.state = "inactive";
        this.ondataavailable?.({ data: new Blob(["fictional-video"], { type: this.mimeType }) });
        recording.__fakeRecordingStopped = true;
        recording.__finishFakeRecording = () => this.onstop?.();
      }
    }

    Object.defineProperty(window, "MediaRecorder", { configurable: true, value: FakeMediaRecorder });
    Object.defineProperty(HTMLCanvasElement.prototype, "captureStream", {
      configurable: true,
      value: () => ({ getTracks: () => [{ stop() {} }] })
    });
    HTMLAnchorElement.prototype.click = function click() {};
  });
}

async function beginHeldExport(page: Page) {
  await page.getByRole("button", { name: "More playback controls" }).click();
  await page.getByRole("button", { name: "Export video" }).click();
  await page.waitForFunction(() => {
    return (window as Window & { __fakeRecordingStopped?: boolean }).__fakeRecordingStopped === true;
  });
}

async function mockApp(page: Page, traceRoute?: TraceRoute, routes: MockAppRoutes = {}) {
  const requests = new Map<string, number>();

  await page.route("**/api/sessions**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;

    if (path === "/api/sessions") {
      if (routes.sessions) {
        await routes.sessions(url.searchParams.get("fresh") === "1", route);
        return;
      }
      await route.fulfill({ json: [fixtures.session] });
      return;
    }
    if (path === "/api/sessions/synthetic-root/snapshot") {
      if (routes.snapshot) {
        await routes.snapshot(route);
        return;
      }
      await route.fulfill({ json: { trace: fixtures.traces.root, city: fixtures.city } });
      return;
    }
    if (path === "/api/sessions/synthetic-root/agents") {
      if (routes.graph) {
        await routes.graph(route);
        return;
      }
      await route.fulfill({ json: fixtures.graph });
      return;
    }
    if (path === "/api/sessions/synthetic-root/report") {
      await route.fulfill({ json: fixtures.reportStatus });
      return;
    }

    const match = path.match(/^\/api\/sessions\/synthetic-root\/agents\/([^/]+)\/trace$/);
    if (match) {
      const agentID = decodeURIComponent(match[1]);
      const count = (requests.get(agentID) ?? 0) + 1;
      requests.set(agentID, count);
      if (traceRoute) {
        await traceRoute(agentID, count, route);
      } else {
        await fulfillAgentTrace(agentID, route);
      }
      return;
    }

    await route.fulfill({ status: 404, body: "fictional route not found" });
  });
}

async function fulfillAgentTrace(agentID: string, route: Route) {
  const trace = fixtures.traces[agentID];
  if (trace) {
    await route.fulfill({ json: trace });
  } else {
    await route.fulfill({ status: 409, body: "fictional trace unavailable" });
  }
}

async function openFixture(page: Page) {
  await page.goto("/?session=synthetic-root");
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  await expect(page.locator(".deck-pos-count")).toHaveText("4 / 4");
}

async function openAgents(page: Page) {
  await page.getByRole("button", { name: /Agent lenses/ }).click();
  return page.getByLabel("Agent lenses", { exact: true });
}

function row(panel: ReturnType<Page["getByLabel"]>, label: string) {
  return panel.getByRole("button").filter({ hasText: label });
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.clear();
  });
});

test("Agents shows truthful rows and switches lenses without letting missing rows move the scene", async ({
  page
}) => {
  await mockApp(page);
  await openFixture(page);
  const panel = await openAgents(page);

  const main = row(panel, "Main");
  const atlas = row(panel, "Atlas");
  const missing = row(panel, "Comet");
  const failed = row(panel, "Drift");

  await expect(main).toHaveAttribute("aria-pressed", "true");
  await expect(atlas).toContainText("2 events");
  await expect(missing).toContainText("Trace missing");
  await expect(missing).toBeDisabled();
  await expect(failed).toContainText("Launch failed · no trace");
  await expect(failed).toBeDisabled();

  await atlas.click();
  await expect(page.locator(".hud-lens")).toHaveText("LensAtlas");
  await expect(page.locator(".deck-pos-count")).toHaveText("2 / 2");
  await expect(page.getByRole("slider", { name: "Playback position" })).toHaveValue("1");

  await page.getByRole("slider", { name: "Playback position" }).fill("0");
  await expect(page.locator(".deck-pos-count")).toHaveText("1 / 2");
  await main.click();
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  await expect(page.locator(".deck-pos-count")).toHaveText("4 / 4");

  await atlas.click();
  await expect(page.locator(".deck-pos-count")).toHaveText("1 / 2");
  await expect(missing).toBeDisabled();
  await expect(page.locator(".hud-lens")).toHaveText("LensAtlas");
});

test("an available zero-event child opens an empty lens and returns to Main", async ({ page }) => {
  await mockApp(page);
  await openFixture(page);
  const panel = await openAgents(page);
  const nova = row(panel, "Nova");

  await expect(nova).toContainText("0 events");
  await expect(nova).toBeEnabled();
  await nova.click();
  await expect(page.locator(".hud-lens")).toHaveText("LensNova");
  await expect(page.locator(".deck-pos-count")).toHaveText("0 / 0");
  await expect(page.locator(".readout-summary")).toHaveText("Select a session to start the walk.");

  await row(panel, "Main").click();
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  await expect(page.locator(".deck-pos-count")).toHaveText("4 / 4");
});

test("subagent marks open Agents only and report evidence restores Main", async ({ page }) => {
  await mockApp(page);
  await openFixture(page);

  await page.getByRole("button", { name: /Jump to fictional helper launch/ }).click();
  const agents = page.getByLabel("Agent lenses", { exact: true });
  await expect(agents).toBeVisible();
  await expect(row(agents, "Main")).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  await expect(page.locator(".deck-pos-count")).toHaveText("2 / 4");

  await row(agents, "Borealis").click();
  await expect(page.locator(".hud-lens")).toHaveText("LensBorealis");
  await expect(page.locator(".deck-pos-count")).toHaveText("3 / 3");

  await page.getByRole("button", { name: "Evaluation ready", exact: true }).click();
  await page.getByRole("button", { name: "The fictional garden was verified." }).click();
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  await expect(page.locator(".deck-pos-count")).toHaveText("3 / 4");
});

test("export locks evaluation navigation without changing the child lens or playhead", async ({ page }) => {
  await installHeldRecorder(page);

  await mockApp(page);
  await openFixture(page);
  const panel = await openAgents(page);
  await row(panel, "Borealis").click();
  await page.getByRole("slider", { name: "Playback position" }).fill("1");
  await page.getByRole("button", { name: "Evaluation ready", exact: true }).click();

  await beginHeldExport(page);

  const evidence = page.getByRole("button", { name: "The fictional garden was verified." });
  const moment = page.getByRole("button", { name: /A fictional checkpoint was recorded/ });
  await expect(evidence).toBeDisabled();
  await expect(moment).toBeDisabled();

  for (const control of [evidence, moment]) {
    await control.evaluate((button) => {
      button.removeAttribute("disabled");
      button.click();
    });
    await expect(page.locator(".hud-lens")).toHaveText("LensBorealis");
    await expect(page.locator(".deck-pos-count")).toHaveText("3 / 3");
  }

  await page.evaluate(() => {
    (window as Window & { __finishFakeRecording?: () => void }).__finishFakeRecording?.();
  });
  await expect(page.getByRole("button", { name: "More playback controls" })).toBeEnabled();
  await expect(page.locator(".hud-lens")).toHaveText("LensBorealis");
  await expect(page.locator(".deck-pos-count")).toHaveText("2 / 3");
});

test("export locks Inspector history and guards forced history jumps", async ({ page }) => {
  await installHeldRecorder(page);
  await mockApp(page);
  await openFixture(page);

  await page.getByRole("button", { name: "Evaluation ready", exact: true }).click();
  await page.getByRole("button", { name: "The fictional garden was verified." }).click();
  await page.getByRole("slider", { name: "Playback position" }).fill("3");
  await page.getByRole("button", { name: "Inspect the selected file" }).click();
  const visit = page.locator(".history-row").filter({ hasText: "#3" });

  await beginHeldExport(page);
  await expect(visit).toBeDisabled();

  await visit.evaluate((button) => {
    button.removeAttribute("disabled");
    button.click();
  });
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  await expect(page.locator(".deck-pos-count")).toHaveText("4 / 4");
});

test("export locks Rescan and guards forced refresh requests", async ({ page }) => {
  await installHeldRecorder(page);
  const sessionRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/sessions" || url.pathname.endsWith("/snapshot")) {
      sessionRequests.push(`${url.pathname}${url.search}`);
    }
  });
  await mockApp(page);
  await openFixture(page);
  const panel = await openAgents(page);
  await row(panel, "Borealis").click();
  await page.getByRole("slider", { name: "Playback position" }).fill("1");

  await beginHeldExport(page);
  const rescan = page.getByRole("button", { name: "Rescan sessions" });
  await expect(rescan).toBeDisabled();
  const before = sessionRequests.length;

  await rescan.evaluate((button) => {
    button.removeAttribute("disabled");
    button.click();
  });
  await page.evaluate(
    () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
  );

  expect(sessionRequests).toHaveLength(before);
  await expect(page.locator(".hud-lens")).toHaveText("LensBorealis");
  await expect(page.locator(".deck-pos-count")).toHaveText("3 / 3");
});

test("rescan invalidates child projections and preserves the actor playhead", async ({ page }) => {
  let refreshed = false;
  const refreshedGraph = structuredClone(fixtures.graph);
  refreshedGraph.agents.find((agent: { id: string }) => agent.id === "child-a").traceEventCount = 3;

  const refreshedCity = structuredClone(fixtures.city);
  refreshedCity.files.push({
    id: 3,
    path: "src/orbit.ts",
    dir: "src",
    lines: 10,
    bytes: 200,
    lang: "TypeScript",
    rect: { x: 4, z: 0, w: 1, d: 1 },
    ghost: false
  });

  const refreshedChild = structuredClone(fixtures.traces["child-a"]);
  refreshedChild.session.eventCount = 3;
  refreshedChild.events.push({
    seq: 2,
    tool: "Read",
    action: "read",
    targets: [{ path: "src/orbit.ts", fileId: 3, touch: "read" }],
    resultBytes: 7,
    isError: false,
    summary: "Read the refreshed fictional orbit module"
  });
  refreshedChild.stats.filesInRepo = 3;
  refreshedChild.stats.fovea = 2;
  refreshedChild.stats.actions.read += 1;

  await mockApp(
    page,
    async (agentID, requestCount, route) => {
      if (agentID === "child-a") {
        await route.fulfill({ json: requestCount === 1 ? fixtures.traces["child-a"] : refreshedChild });
        return;
      }
      await fulfillAgentTrace(agentID, route);
    },
    {
      sessions: async (fresh, route) => {
        if (fresh) refreshed = true;
        await route.fulfill({ json: [fixtures.session] });
      },
      snapshot: async (route) => {
        await route.fulfill({
          json: { trace: fixtures.traces.root, city: refreshed ? refreshedCity : fixtures.city }
        });
      },
      graph: async (route) => {
        await route.fulfill({ json: refreshed ? refreshedGraph : fixtures.graph });
      }
    }
  );

  await openFixture(page);
  const panel = await openAgents(page);
  await row(panel, "Atlas").click();
  await expect(page.locator(".deck-pos-count")).toHaveText("2 / 2");

  await page.getByRole("button", { name: "Rescan sessions" }).click();
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  await expect(row(panel, "Atlas")).toContainText("3 events");

  await row(panel, "Atlas").click();
  await expect(page.locator(".hud-lens")).toHaveText("LensAtlas");
  await expect(page.locator(".deck-pos-count")).toHaveText("2 / 3");
  await expect(page.locator(".hud-commit")).toContainText("3 files");
});

test("a rapid delayed Atlas to Borealis switch cannot let Atlas overwrite Borealis", async ({ page }) => {
  const atlasRelease = deferred();
  const atlasFulfilled = deferred();
  await mockApp(page, async (agentID, _count, route) => {
    if (agentID === "child-a") await atlasRelease.promise;
    await fulfillAgentTrace(agentID, route);
    if (agentID === "child-a") atlasFulfilled.resolve();
  });
  await openFixture(page);
  const panel = await openAgents(page);

  await row(panel, "Atlas").click();
  await expect(panel).toContainText("Loading trace…");
  await row(panel, "Borealis").click();
  await expect(page.locator(".hud-lens")).toHaveText("LensBorealis");
  await expect(page.locator(".deck-pos-count")).toHaveText("3 / 3");

  atlasRelease.resolve();
  await atlasFulfilled.promise;
  await expect(page.locator(".hud-lens")).toHaveText("LensBorealis");
  await expect(row(panel, "Borealis")).toHaveAttribute("aria-pressed", "true");
});

test("a failed child load keeps Main visible and Retry starts a fresh request", async ({ page }) => {
  await mockApp(page, async (agentID, count, route) => {
    if (agentID === "child-a" && count === 1) {
      await route.fulfill({ status: 503, body: "fictional child timeout" });
      return;
    }
    await fulfillAgentTrace(agentID, route);
  });
  await openFixture(page);
  const panel = await openAgents(page);

  await row(panel, "Atlas").click();
  await expect(panel.getByRole("alert")).toContainText("fictional child timeout");
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  await expect(page.locator(".deck-pos-count")).toHaveText("4 / 4");

  await panel.getByRole("button", { name: "Retry" }).click();
  await expect(page.locator(".hud-lens")).toHaveText("LensAtlas");
  await expect(page.locator(".deck-pos-count")).toHaveText("2 / 2");
});

test("export locks lens controls, discards delayed child data, and allows a fresh request afterward", async ({
  page
}) => {
  const borealisRelease = deferred();
  const borealisFulfilled = deferred();
  await page.addInitScript(() => {
    class FakeMediaRecorder {
      static isTypeSupported() {
        return true;
      }

      state = "inactive";
      mimeType: string;
      ondataavailable: ((event: { data: Blob }) => void) | null = null;
      onerror: (() => void) | null = null;
      onstop: (() => void) | null = null;

      constructor(_stream: unknown, options?: { mimeType?: string }) {
        this.mimeType = options?.mimeType ?? "video/webm";
      }

      start() {
        this.state = "recording";
      }

      stop() {
        this.state = "inactive";
        this.ondataavailable?.({ data: new Blob(["fictional-video"], { type: this.mimeType }) });
        setTimeout(() => this.onstop?.(), 0);
      }
    }

    Object.defineProperty(window, "MediaRecorder", { configurable: true, value: FakeMediaRecorder });
    Object.defineProperty(HTMLCanvasElement.prototype, "captureStream", {
      configurable: true,
      value: () => ({ getTracks: () => [{ stop() {} }] })
    });
    HTMLAnchorElement.prototype.click = function click() {};
  });

  await mockApp(page, async (agentID, count, route) => {
    if (agentID === "child-a" && count === 1) {
      await route.fulfill({ status: 503, body: "fictional retry setup" });
      return;
    }
    if (agentID === "child-b" && count === 1) {
      await borealisRelease.promise;
    }
    await fulfillAgentTrace(agentID, route);
    if (agentID === "child-b" && count === 1) borealisFulfilled.resolve();
  });
  await openFixture(page);
  const panel = await openAgents(page);

  await row(panel, "Atlas").click();
  const retry = panel.getByRole("button", { name: "Retry" });
  await expect(retry).toBeVisible();

  await page.getByRole("button", { name: "More playback controls" }).click();
  await page.getByRole("button", { name: "Export video" }).click();
  await expect(retry).toBeDisabled();
  await expect(row(panel, "Atlas")).toBeDisabled();
  await expect(page.getByRole("button", { name: /Jump to fictional helper launch/ })).toBeDisabled();
  await expect(page.getByRole("slider", { name: "Playback position" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "More playback controls" })).toBeEnabled({ timeout: 3_000 });

  await retry.click();
  await expect(page.locator(".hud-lens")).toHaveText("LensAtlas");
  await row(panel, "Main").click();
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");

  await row(panel, "Borealis").click();
  await expect(panel).toContainText("Loading trace…");
  await page.getByRole("button", { name: "More playback controls" }).click();
  await page.getByRole("button", { name: "Export video" }).click();
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  borealisRelease.resolve();
  await borealisFulfilled.promise;
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");
  await expect(page.getByRole("button", { name: "More playback controls" })).toBeEnabled({ timeout: 3_000 });
  await expect(page.locator(".hud-lens")).toHaveText("LensMain");

  await row(panel, "Borealis").click();
  await expect(page.locator(".hud-lens")).toHaveText("LensBorealis");
  await expect(page.locator(".deck-pos-count")).toHaveText("3 / 3");
});
