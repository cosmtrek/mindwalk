import { chromium } from "playwright";
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

/*
 * Contract exercised by this proof:
 *
 *   GET  /api/repository-discovery/config
 *   GET  /api/repository-discovery/status
 *   GET  /api/repository-discovery/results?showHidden=1
 *   POST /api/repository-discovery/start
 *
 * Mutations made by the browser carry the application's CSRF header. The
 * proof intentionally drives them through the UI, except for read-only API
 * assertions that distinguish discovery from registration.
 *
 * The accessible labels below are also the user-facing contract. A failure is
 * preferable to silently weakening the confirmation or selection workflow.
 */

const repoRoot = resolve(import.meta.dirname, "../..");
const binary = join(repoRoot, "bin", "mindwalk");
const fixtureRoot = await mkdtemp(join(tmpdir(), "mindwalk-discovery-e2e-"));
const syntheticHome = join(fixtureRoot, "home");
const scanRoot = join(syntheticHome, "workspace");
const emptyRoot = join(syntheticHome, "empty-workspace");
const alphaRepo = join(scanRoot, "alpha-repository");
const betaRepo = join(scanRoot, "beta-repository");
const slowTree = join(scanRoot, "slow-cancellable-tree");
const manualRepo = join(fixtureRoot, "manual-repository");
const configRoot = join(fixtureRoot, "config");
const dataRoot = join(fixtureRoot, "data");
const registryPath = join(configRoot, "repos.json");
const claudeDir = join(fixtureRoot, "claude-empty");
const codexDir = join(fixtureRoot, "codex-empty");
const approvedName = "Alpha Discovery Approved";
const manualName = "Manual Fixture";
let server;
let browser;

try {
  await Promise.all([
    mkdir(scanRoot, { recursive: true }),
    mkdir(emptyRoot, { recursive: true }),
    mkdir(configRoot, { recursive: true }),
    mkdir(dataRoot, { recursive: true }),
    mkdir(claudeDir, { recursive: true }),
    mkdir(codexDir, { recursive: true })
  ]);
  await Promise.all([
    createGitRepository(alphaRepo, "# alpha discovery fixture\n"),
    createGitRepository(betaRepo, "# beta discovery fixture\n"),
    createGitRepository(manualRepo, "# manual registration fixture\n"),
    createDirectoryTree(slowTree, 12, 30)
  ]);

  const alphaBefore = await repositorySnapshot(alphaRepo);
  const betaBefore = await repositorySnapshot(betaRepo);
  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  server = await startServer(port);
  browser = await chromium.launch({ executablePath: "/usr/bin/chromium", headless: true });

  const first = await newProofPage(browser, baseURL);
  const { page, consoleErrors } = first;
  let startRequests = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/repository-discovery/start")) startRequests++;
  });

  await page.goto(`${baseURL}/?profile=low`, { waitUntil: "domcontentloaded" });
  await page.getByTestId("observatory-root").waitFor();
  try {
    await page.getByText("First run", { exact: true }).waitFor();
  } catch (error) {
    const body = await page.locator("body").innerText().catch(() => "<body unavailable>");
    throw new Error(`first-run onboarding did not appear: ${error.message}\nBODY:\n${body}\nCONSOLE:\n${consoleErrors.join("\n")}`);
  }
  await page.getByRole("heading", { name: /Add your first repository/i }).waitFor();
  if (await page.locator("canvas").count() !== 0) throw new Error("LOW first-run view mounted a WebGL canvas");

  await new Promise((resolveWait) => setTimeout(resolveWait, 500));
  if (startRequests !== 0) throw new Error("repository discovery started before explicit owner action");
  await assertRegisteredPaths(baseURL, []);

  // Home discovery is an explicit choice and still stops at an exact-plan
  // confirmation. This HOME is synthetic; the proof never scans the owner's
  // real home directory.
  await page.getByRole("button", { name: "Scan my home folder", exact: true }).click();
  const homeDiscovery = page.getByRole("dialog", { name: "Find repositories" });
  try {
    await homeDiscovery.locator(".discovery-root-list").getByText(syntheticHome, { exact: true }).waitFor();
  } catch (error) {
    const dialogText = await homeDiscovery.innerText().catch(() => "<dialog unavailable>");
    const config = await discoveryConfig(baseURL).catch((cause) => ({ error: cause.message }));
    throw new Error(`synthetic home was not prepared for confirmation: ${error.message}\nDIALOG:\n${dialogText}\nCONFIG:\n${JSON.stringify(config)}\nCONSOLE:\n${consoleErrors.join("\n")}`);
  }
  await homeDiscovery.getByRole("button", { name: "Review scan", exact: true }).click();
  await homeDiscovery.getByRole("heading", { name: "Confirm repository scan" }).waitFor();
  if (startRequests !== 0) throw new Error("home-folder choice started scanning before final approval");
  await homeDiscovery.getByRole("button", { name: "Cancel", exact: true }).click();
  await homeDiscovery.waitFor({ state: "detached" });

  await page.getByRole("button", { name: "Choose folders", exact: true }).click();
  const discovery = page.getByRole("dialog", { name: "Find repositories" });
  await discovery.waitFor();
  await discovery.getByRole("heading", { name: "Choose folders to scan" }).waitFor();
  const persistedHomeRow = discovery.locator(".discovery-root-list > div").filter({ hasText: syntheticHome });
  if (await persistedHomeRow.count()) await persistedHomeRow.getByRole("button", { name: "Remove", exact: true }).click();
  await discovery.getByLabel("Scan root path", { exact: true }).fill(scanRoot);
  await discovery.getByRole("button", { name: "Add scan root", exact: true }).click();
  await discovery.getByText(scanRoot, { exact: true }).waitFor();
  await discovery.getByLabel("Scan root path", { exact: true }).fill(emptyRoot);
  await discovery.getByRole("button", { name: "Add scan root", exact: true }).click();
  await discovery.getByText(emptyRoot, { exact: true }).waitFor();
  await discovery.getByRole("button", { name: "Review scan", exact: true }).click();
  await discovery.getByRole("heading", { name: "Confirm repository scan" }).waitFor();
  await discovery.getByText(scanRoot, { exact: true }).waitFor();
  await discovery.getByText(emptyRoot, { exact: true }).waitFor();
  if (startRequests !== 0) throw new Error("reviewing scan roots bypassed the final start confirmation");
  await assertRegisteredPaths(baseURL, []);

  await discovery.getByRole("button", { name: "Start scan", exact: true }).click();
  const progress = discovery.getByRole("region", { name: "Repository scan progress" });
  await progress.waitFor();
  const reducedMotionAnimation = await progress.locator(".spin").evaluate((element) => getComputedStyle(element).animationName);
  if (reducedMotionAnimation !== "none") {
    throw new Error(`reduced-motion scan indicator still animates: ${reducedMotionAnimation}`);
  }
  await waitFor(async () => Number((await discoveryStatus(baseURL)).directoriesExamined) > 0, 15000);
  await waitForDiscoveryTerminal(baseURL, 30000);
  await discovery.getByRole("heading", { name: "Discovered repositories" }).waitFor();
  const results = await discoveryResults(baseURL, true);
  const alphaResult = resultForPath(results, alphaRepo);
  const betaResult = resultForPath(results, betaRepo);
  if (!alphaResult || !betaResult) {
    throw new Error(`ordinary repositories missing from discovery results: ${JSON.stringify(results)}`);
  }
  const finished = await discoveryStatus(baseURL);
  if (Number(finished.directoriesExamined) <= 0 || Number(finished.repositoriesFound) < 2) {
    throw new Error(`scan progress was not real: ${JSON.stringify(finished)}`);
  }
  await assertRegisteredPaths(baseURL, []);

  const resultSearch = discovery.getByRole("searchbox", { name: "Search discovered repositories" });
  await resultSearch.fill(alphaResult.name);
  await discovery.getByRole("button", { name: "Select all visible", exact: true }).click();
  const alphaCheckbox = discovery.getByRole("checkbox", { name: `Select ${alphaResult.name} at ${alphaRepo}`, exact: true });
  const betaCheckboxBeforeFilterReset = discovery.getByRole("checkbox", { name: `Select ${betaResult.name} at ${betaRepo}`, exact: true });
  if (!(await alphaCheckbox.isChecked())) {
    throw new Error("Select all visible did not select the visible repository");
  }
  await resultSearch.fill("");
  await betaCheckboxBeforeFilterReset.waitFor();
  if (await betaCheckboxBeforeFilterReset.isChecked()) {
    throw new Error("Select all visible selected a repository outside the active search filter");
  }
  await discovery.getByRole("button", { name: "Clear selection", exact: true }).click();
  await alphaCheckbox.check();
  await discovery.getByRole("button", { name: "Add selected", exact: true }).click();
  await discovery.getByRole("heading", { name: "Confirm repositories to add" }).waitFor();
  await discovery.getByText(alphaRepo, { exact: true }).waitFor();
  if (await discovery.getByText(betaRepo, { exact: true }).count() !== 0) {
    throw new Error("unselected repository appeared in the exact add confirmation");
  }
  await discovery.getByLabel(`${alphaResult.name} display name`, { exact: true }).fill(approvedName);
  await discovery.getByLabel(`${alphaResult.name} group or project`, { exact: true }).fill("synthetic-e2e");
  await discovery.getByLabel(`${alphaResult.name} tags`, { exact: true }).fill("discovery, selected");
  const color = discovery.getByLabel(`${alphaResult.name} color`, { exact: true });
  await color.fill("#43d9ff");
  const enabled = discovery.getByLabel(`${alphaResult.name} enabled`, { exact: true });
  if (!(await enabled.isChecked())) await enabled.check();
  await discovery.getByRole("button", { name: "Confirm and add selected", exact: true }).click();

  await waitFor(async () => (await registeredRepositories(baseURL)).length === 1);
  const registeredAfterDiscovery = await registeredRepositories(baseURL);
  const registeredAlpha = registeredAfterDiscovery.find((entry) => entry.repo.path === alphaRepo);
  if (!registeredAlpha || registeredAlpha.repo.name !== approvedName) {
    throw new Error(`selected repository metadata was not registered: ${JSON.stringify(registeredAfterDiscovery)}`);
  }
  if (registeredAfterDiscovery.some((entry) => entry.repo.path === betaRepo)) {
    throw new Error("unselected discovery result was registered");
  }

  await closeDiscoveryIfOpen(page);
  await page.getByLabel("Repository", { exact: true }).selectOption({ label: approvedName });
  const list = page.getByRole("region", { name: "Accessible repository file list" });
  await list.waitFor();
  await list.getByRole("button", { name: /README\.md/ }).waitFor();
  if (await page.locator("canvas").count() !== 0) throw new Error("LOW registered-repository map mounted a WebGL canvas");

  const manualForm = page.locator("form.rail-open");
  await manualForm.getByLabel("Add a repository", { exact: true }).fill(manualRepo);
  await manualForm.getByPlaceholder("Display name (optional)").fill(manualName);
  await manualForm.getByRole("button", { name: "Add", exact: true }).click();
  await waitFor(async () => (await registeredRepositories(baseURL)).length === 2);
  await assertRegisteredPaths(baseURL, [alphaRepo, manualRepo]);

  await openDiscovery(page);
  const reopened = page.getByRole("dialog", { name: "Find repositories" });
  await reopened.getByRole("heading", { name: "Discovered repositories" }).waitFor();
  const betaCheckbox = reopened.getByRole("checkbox", { name: `Select ${betaResult.name} at ${betaRepo}`, exact: true });
  await betaCheckbox.check();
  await reopened.getByRole("button", { name: "Hide selected", exact: true }).click();
  await betaCheckbox.waitFor({ state: "detached" });
  await reopened.getByRole("button", { name: "Show hidden discoveries", exact: true }).click();
  const hiddenBeta = reopened.getByRole("checkbox", { name: `Select ${betaResult.name} at ${betaRepo}`, exact: true });
  await hiddenBeta.waitFor();
  await hiddenBeta.check();
  await reopened.getByRole("button", { name: "Unhide selected", exact: true }).click();
  await assertRegisteredPaths(baseURL, [alphaRepo, manualRepo]);

  // Add enough directory metadata after the successful scan that cancellation
  // is observable without slowing the primary selection proof.
  await createDirectoryTree(slowTree, 120, 100);
  const startsBeforeCancel = startRequests;
  await reopened.getByText("Rescan one root", { exact: true }).click();
  await reopened.getByRole("button", { name: `Rescan root: ${emptyRoot}`, exact: true }).waitFor();
  await reopened.getByRole("button", { name: `Rescan root: ${scanRoot}`, exact: true }).click();
  await reopened.getByRole("region", { name: "Repository scan progress" }).waitFor();
  await reopened.getByRole("button", { name: "Cancel scan", exact: true }).click();
  await waitFor(async () => isCancelled(await discoveryStatus(baseURL)), 15000);
  if (startRequests !== startsBeforeCancel + 1) throw new Error("Rescan did not start exactly one explicit scan");
  await assertRegisteredPaths(baseURL, [alphaRepo, manualRepo]);

  const configBeforeRestart = await discoveryConfig(baseURL);
  const rootsBeforeRestart = approvedRootPaths(configBeforeRestart);
  if (!rootsBeforeRestart.includes(scanRoot) || !rootsBeforeRestart.includes(emptyRoot)) {
    throw new Error(`approved synthetic root was not persisted: ${JSON.stringify(configBeforeRestart)}`);
  }
  await assertNoConsoleErrors(consoleErrors);

  await page.close();
  await stopServer(server);
  server = await startServer(port);

  const second = await newProofPage(browser, baseURL);
  let restartStarts = 0;
  second.page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/repository-discovery/start")) restartStarts++;
  });
  await second.page.goto(`${baseURL}/?profile=low`, { waitUntil: "domcontentloaded" });
  await second.page.getByTestId("observatory-root").waitFor();
  await new Promise((resolveWait) => setTimeout(resolveWait, 1000));
  if (restartStarts !== 0) throw new Error("server/browser restart automatically started repository discovery");
  const configAfterRestart = await discoveryConfig(baseURL);
  if (!approvedRootPaths(configAfterRestart).includes(scanRoot) || !approvedRootPaths(configAfterRestart).includes(emptyRoot)) {
    throw new Error(`restart lost approved scan preferences: ${JSON.stringify(configAfterRestart)}`);
  }
  const restartStatus = await discoveryStatus(baseURL);
  if (isRunning(restartStatus)) throw new Error(`restart resumed a scan automatically: ${JSON.stringify(restartStatus)}`);
  await assertRegisteredPaths(baseURL, [alphaRepo, manualRepo]);
  if (await second.page.locator("canvas").count() !== 0) throw new Error("LOW restart view mounted a WebGL canvas");
  await assertNoConsoleErrors(second.consoleErrors);

  if (JSON.stringify(await repositorySnapshot(alphaRepo)) !== JSON.stringify(alphaBefore) ||
      JSON.stringify(await repositorySnapshot(betaRepo)) !== JSON.stringify(betaBefore)) {
    throw new Error("repository discovery modified a discovered repository");
  }

  process.stdout.write(
    `repository discovery browser proof passed: examined ${finished.directoriesExamined}, found ${finished.repositoriesFound}, registered 1 selected + 1 manual, cancellation and restart safe\n`
  );
} finally {
  if (browser) await browser.close();
  if (server) await stopServer(server);
  await rm(fixtureRoot, { recursive: true, force: true });
}

async function newProofPage(browserInstance, baseURL) {
  const page = await browserInstance.newPage({ viewport: { width: 1440, height: 1000 } });
  await page.emulateMedia({ reducedMotion: "reduce" });
  const consoleErrors = [];
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });
  page.on("pageerror", (error) => consoleErrors.push(error.message));
  page.on("requestfailed", (request) => {
    const failure = request.failure()?.errorText ?? "request failed";
    if (!request.url().startsWith(baseURL)) consoleErrors.push(`${failure}: ${request.url()}`);
  });
  return { page, consoleErrors };
}

async function openDiscovery(page) {
  const existing = page.getByRole("dialog", { name: "Find repositories" });
  if (await existing.count()) return;
  await page.getByRole("button", { name: "Find repositories", exact: true }).click();
  await existing.waitFor();
}

async function closeDiscoveryIfOpen(page) {
  const discovery = page.getByRole("dialog", { name: "Find repositories" });
  if (!(await discovery.count())) return;
  await discovery.getByRole("button", { name: "Close repository discovery", exact: true }).click();
  await discovery.waitFor({ state: "detached" });
}

async function createGitRepository(path, contents) {
  await mkdir(path, { recursive: true });
  await writeFile(join(path, "README.md"), contents);
  git(path, "init", "-q");
  git(path, "checkout", "-q", "-b", "main");
  git(path, "add", "README.md");
  git(path, "commit", "-q", "-m", "synthetic fixture");
}

function git(path, ...args) {
  const result = spawnSync(
    "git",
    ["--no-optional-locks", "-c", "core.hooksPath=/dev/null", "-c", "user.email=e2e@example.invalid", "-c", "user.name=Mindwalk E2E", "-C", path, ...args],
    {
      encoding: "utf8",
      env: {
        ...process.env,
        GIT_CONFIG_NOSYSTEM: "1",
        GIT_CONFIG_GLOBAL: "/dev/null",
        GIT_TERMINAL_PROMPT: "0"
      }
    }
  );
  if (result.status !== 0) throw new Error(`git ${args.join(" ")} failed: ${result.stderr || result.stdout}`);
  return result.stdout.trim();
}

async function repositorySnapshot(path) {
  return {
    readme: await readFile(join(path, "README.md"), "utf8"),
    entries: (await readdir(path)).sort(),
    head: git(path, "rev-parse", "HEAD"),
    status: git(path, "status", "--porcelain=v1")
  };
}

async function createDirectoryTree(root, groups, leavesPerGroup) {
  await mkdir(root, { recursive: true });
  for (let group = 0; group < groups; group++) {
    const paths = [];
    for (let leaf = 0; leaf < leavesPerGroup; leaf++) {
      paths.push(mkdir(join(root, `group-${String(group).padStart(3, "0")}`, `leaf-${String(leaf).padStart(3, "0")}`), { recursive: true }));
    }
    await Promise.all(paths);
  }
}

async function startServer(port) {
  const child = spawn(
    binary,
    [
      "serve", "--no-open", "--port", String(port), "--dev",
      "--config", registryPath,
      "--data-dir", dataRoot,
      "--claude-dir", claudeDir,
      "--codex-dir", codexDir
    ],
    {
      cwd: repoRoot,
      stdio: ["ignore", "pipe", "pipe"],
      env: {
        ...process.env,
        HOME: syntheticHome,
        XDG_CONFIG_HOME: configRoot,
        XDG_DATA_HOME: dataRoot
      }
    }
  );
  await serverURL(child);
  return child;
}

async function stopServer(child) {
  if (!child || child.exitCode !== null) return;
  child.kill("SIGTERM");
  await Promise.race([once(child, "exit"), new Promise((resolveWait) => setTimeout(resolveWait, 2000))]);
  if (child.exitCode === null) child.kill("SIGKILL");
}

function serverURL(child) {
  return new Promise((resolveURL, reject) => {
    let output = "";
    const timeout = setTimeout(() => reject(new Error(`server start timed out: ${output}`)), 10000);
    child.stdout.on("data", (chunk) => {
      output += chunk;
      const match = output.match(/mindwalk serving (http:\/\/127\.0\.0\.1:\d+)/);
      if (match) {
        clearTimeout(timeout);
        resolveURL(match[1]);
      }
    });
    child.stderr.on("data", (chunk) => { output += chunk; });
    child.once("exit", (code) => reject(new Error(`server exited ${code}: ${output}`)));
  });
}

function freePort() {
  return new Promise((resolvePort, reject) => {
    const listener = createServer();
    listener.once("error", reject);
    listener.listen(0, "127.0.0.1", () => {
      const address = listener.address();
      listener.close((error) => error ? reject(error) : resolvePort(address.port));
    });
  });
}

async function getJSON(url) {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`${response.status} ${response.statusText} from ${url}: ${await response.text()}`);
  return response.json();
}

function registeredRepositories(baseURL) {
  return getJSON(`${baseURL}/api/repositories`);
}

function discoveryConfig(baseURL) {
  return getJSON(`${baseURL}/api/repository-discovery/config`);
}

function discoveryStatus(baseURL) {
  return getJSON(`${baseURL}/api/repository-discovery/status`);
}

async function discoveryResults(baseURL, showHidden = false) {
  const value = await getJSON(`${baseURL}/api/repository-discovery/results${showHidden ? "?showHidden=1" : ""}`);
  return Array.isArray(value) ? value : value.results ?? [];
}

function resultForPath(results, path) {
  return results.find((result) => result.path === path || result.canonicalPath === path);
}

function approvedRootPaths(config) {
  return (config.approvedRoots ?? config.roots ?? []).map((root) => typeof root === "string" ? root : root.path);
}

function statusName(status) {
  return String(status.status ?? status.state ?? "").toLowerCase();
}

function isRunning(status) {
  return ["running", "scanning", "cancelling", "canceling"].includes(statusName(status));
}

function isCancelled(status) {
  return ["cancelled", "canceled"].includes(statusName(status));
}

async function waitForDiscoveryTerminal(baseURL, timeout) {
  await waitFor(async () => {
    const status = await discoveryStatus(baseURL);
    const state = statusName(status);
    if (["failed", "error", "timed_out", "timeout"].includes(state)) {
      throw new Error(`repository discovery failed: ${JSON.stringify(status)}`);
    }
    return ["complete", "completed", "finished", "directory_limit", "result_limit"].includes(state);
  }, timeout);
}

async function assertRegisteredPaths(baseURL, expected) {
  const actual = (await registeredRepositories(baseURL)).map((entry) => entry.repo.path).sort();
  const wanted = [...expected].sort();
  if (JSON.stringify(actual) !== JSON.stringify(wanted)) {
    throw new Error(`registered paths ${JSON.stringify(actual)}, want ${JSON.stringify(wanted)}`);
  }
}

async function assertNoConsoleErrors(messages) {
  const unexpected = messages.filter((message) => !message.includes("ERR_INCOMPLETE_CHUNKED_ENCODING"));
  if (unexpected.length) throw new Error(`browser console errors: ${unexpected.join(" | ")}`);
}

async function waitFor(predicate, timeout = 10000) {
  const deadline = Date.now() + timeout;
  let lastError;
  while (Date.now() < deadline) {
    try {
      if (await predicate()) return;
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error(`condition timed out${lastError ? `: ${lastError.message}` : ""}`);
}
