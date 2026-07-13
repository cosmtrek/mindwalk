import { chromium } from "playwright";
import { appendFile, mkdtemp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const repoRoot = resolve(import.meta.dirname, "../..");
const binary = join(repoRoot, "bin", "mindwalk");
const fixtureRoot = await mkdtemp(join(tmpdir(), "mindwalk-live-e2e-"));
const observedRepo = join(fixtureRoot, "repo");
const claudeDir = join(fixtureRoot, "claude");
const codexDir = join(fixtureRoot, "codex");
const dataDir = join(fixtureRoot, "data");
const registryPath = join(fixtureRoot, "repos.json");
const claudePath = join(claudeDir, "claude-live.jsonl");
const codexPath = join(codexDir, "codex-live.jsonl");
let server;
let browser;

try {
  await Promise.all([mkdir(observedRepo), mkdir(claudeDir), mkdir(codexDir), mkdir(dataDir)]);
  await writeFile(join(observedRepo, "README.md"), "# live fixture\n");
  await writeFile(claudePath, claudeFixture());
  await writeFile(codexPath, codexFixture());

  const added = spawnSync(binary, ["repos", "add", "-config", registryPath, observedRepo], { encoding: "utf8" });
  if (added.status !== 0) throw new Error(`registry setup failed: ${added.stderr || added.stdout}`);

  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  server = await startServer(port);
  browser = await chromium.launch({ executablePath: "/usr/bin/chromium", headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
	await page.emulateMedia({ reducedMotion: "reduce" });
  const consoleErrors = [];
  page.on("console", (message) => { if (message.type() === "error") consoleErrors.push(message.text()); });
  page.on("pageerror", (error) => consoleErrors.push(error.message));
  // SSE stays open intentionally, so network-idle is not a readiness signal.
  await page.goto(`${baseURL}/?file=README.md`, { waitUntil: "domcontentloaded" });

  const root = page.getByTestId("observatory-root");
  await root.waitFor();
  await waitForTrace(page, 7);
	await page.getByRole("complementary", { name: "File README.md" }).waitFor();
  await page.getByText("claude-live.jsonl", { exact: true }).waitFor();
  await page.getByText("codex-live.jsonl", { exact: true }).waitFor();
  await page.getByText(/1 error/).waitFor();
  await page.getByText(/edit after last verify/).waitFor();
	await page.getByText("2 processes · display only", { exact: true }).waitFor();
	const memoryCreated = await fetch(`${baseURL}/api/memories`, {
		method: "POST",
		headers: { "Content-Type": "application/json", "X-Mindwalk-CSRF": "1" },
		body: JSON.stringify({ namespace: "e2e", title: "Durable local memory", body: "SQLite keyword fixture" })
	});
	if (memoryCreated.status !== 201) throw new Error(`memory create failed: ${await memoryCreated.text()}`);
	await page.getByRole("button", { name: "Memory" }).click();
	await page.getByLabel("Search local memories").fill("SQLite");
	await page.getByLabel("Search local memories").press("Enter");
	await page.getByText("Durable local memory", { exact: true }).waitFor();
	await page.getByText("Local FTS retrieval only — this is not model training.", { exact: true }).waitFor();
	await page.getByLabel("Close memory search").click();
	await page.getByRole("button", { name: "Review" }).click();
	await page.getByText("1 errors", { exact: true }).waitFor();
	const [comparisonResponse] = await Promise.all([
		page.waitForResponse((response) => response.url().includes("/api/compare?")),
		page.getByLabel("Compare with").selectOption({ label: "codex-live.jsonl" })
	]);
	if (!comparisonResponse.ok()) throw new Error(`comparison failed: ${comparisonResponse.status()} ${await comparisonResponse.text()}`);
	const comparisonResult = page.getByTestId("comparison-result");
	await comparisonResult.waitFor();
	const comparisonText = await comparisonResult.innerText();
	if (!comparisonText.includes("UNAVAILABLE")) throw new Error(`comparison omitted unavailable state: ${comparisonText}`);
	await page.getByLabel("Close review center").click();
  const liveButton = page.locator("button.live-state");
  await liveButton.filter({ hasText: "following" }).waitFor();

	const beforeLive = await healthSequence(baseURL);
  await appendClaudeTool("live-1", "2026-07-13T15:01:00Z");
	await waitFor(async () => (await healthSequence(baseURL)) === beforeLive + 1);
  await waitForTrace(page, 8);
	await page.getByRole("button", { name: "List" }).click();
	await page.getByRole("region", { name: "Accessible repository file list" }).waitFor();
	if (await page.locator("canvas").count() !== 0) throw new Error("LOW list view left a WebGL canvas mounted");
	await page.getByRole("button", { name: "Close inspector" }).click();
	await page.getByRole("button", { name: /README\.md/ }).click();
	const inspectorAnimation = await page.locator(".inspector").evaluate((element) => getComputedStyle(element).animationDuration);
	if (Number.parseFloat(inspectorAnimation) > 0.00001) throw new Error(`reduced motion not honored: ${inspectorAnimation}`);
	await page.getByRole("button", { name: "Memory" }).focus();
	await page.keyboard.press("Enter");
	await page.getByRole("region", { name: "Local memory search" }).waitFor();
	await page.getByLabel("Close memory search").click();
  await liveButton.click();
  await liveButton.filter({ hasText: "paused" }).waitFor();
  const pausedSequence = await healthSequence(baseURL);
  await appendClaudeTool("paused-1", "2026-07-13T15:01:03Z");
  await waitFor(async () => (await healthSequence(baseURL)) > pausedSequence);
  if (await root.getAttribute("data-trace-events") !== "8") throw new Error("paused live-follow changed the trace");

  await liveButton.click();
	const beforeResume = await healthSequence(baseURL);
  await appendClaudeTool("resume-1", "2026-07-13T15:01:06Z");
	await waitFor(async () => (await healthSequence(baseURL)) === beforeResume + 1);
  await waitForTrace(page, 10);

  // A half record remains invisible until both halves and its result arrive.
  const partialCall = claudeCall("2026-07-13T15:01:09Z", "partial-1", "Edit", editInput());
  const split = Math.floor(partialCall.length / 2);
  const beforePartial = await healthSequence(baseURL);
  await appendFile(claudePath, partialCall.slice(0, split));
  await new Promise((resolveWait) => setTimeout(resolveWait, 1200));
  const partialSequence = await healthSequence(baseURL);
	const partialTrace = await root.getAttribute("data-trace-events");
  if (partialSequence !== beforePartial || partialTrace !== "10") {
		const unexpected = await (await fetch(`${baseURL}/api/sessions/claude-live/events?after=${beforePartial}`)).text();
		throw new Error(`incomplete source record was accepted: sequence ${beforePartial}->${partialSequence}, trace=${partialTrace}, events=${unexpected}`);
  }
  await appendFile(claudePath, partialCall.slice(split) + "\n" + claudeResult("2026-07-13T15:01:10Z", "partial-1", "fixture") + "\n");
  await waitForTrace(page, 11);
  if (await healthSequence(baseURL) !== beforePartial + 1) throw new Error("completed source record was not accepted exactly once");

  const provenance = page.getByTestId("provenance-inspector");
  await provenance.waitFor({ timeout: 5000 });
  if (!(await provenance.textContent())?.includes("derived")) throw new Error("provenance quality not rendered");

  // Restart on the same origin. EventSource reconnects with its last event ID;
  // the durable ledger cursor must continue rather than reset.
  const beforeRestart = await healthSequence(baseURL);
  await stopServer(server);
  server = await startServer(port);
  await appendClaudeTool("restart-1", "2026-07-13T15:01:12Z");
  await waitFor(async () => (await healthSequence(baseURL)) === beforeRestart + 1);
  await waitForTrace(page, 12, 15000);

  // Switch to the Codex fixture and prove its stream advances as well.
  await page.getByText("codex-live.jsonl", { exact: true }).click();
  await waitForTrace(page, 7);
  await appendFile(codexPath, codexCall("2026-07-13T14:01:00Z", "live-verify", "exec_command", { cmd: "go test ./...", workdir: observedRepo }) + "\n");
  await appendFile(codexPath, codexOutput("2026-07-13T14:01:01Z", "live-verify", "Process exited with code 0\nPASS") + "\n");
  await waitForTrace(page, 8);

  const health = await (await fetch(`${baseURL}/api/ingestion/health`)).json();
  const sessions = await (await fetch(`${baseURL}/api/ingestion/sessions`)).json();
  if (health.status !== "live" || health.durableSequence <= beforeRestart) throw new Error(`unexpected health: ${JSON.stringify(health)}`);
  if (!sessions.some((session) => session.id === "claude-live" && session.association === "EXACT") ||
      !sessions.some((session) => session.id === "codex-live" && session.association === "EXACT")) {
    throw new Error(`unexpected ingestion sessions: ${JSON.stringify(sessions)}`);
  }
  if (await readFile(join(observedRepo, "README.md"), "utf8") !== "# live fixture\n" ||
      (await readdir(observedRepo)).join(",") !== "README.md") {
    throw new Error("the monitored repository was modified");
  }
	const navigationMs = await page.evaluate(() => performance.getEntriesByType("navigation")[0]?.domContentLoadedEventEnd ?? 0);
	const processStatus = await readFile(`/proc/${server.pid}/status`, "utf8");
	const rssKiB = Number(processStatus.match(/^VmRSS:\s+(\d+)\s+kB$/m)?.[1] || 0);
	const unexpectedConsoleErrors = consoleErrors.filter((message) => !message.includes("ERR_INCOMPLETE_CHUNKED_ENCODING"));
  if (unexpectedConsoleErrors.length) throw new Error(`browser console errors: ${unexpectedConsoleErrors.join(" | ")}`);
  process.stdout.write(`live Claude/Codex restart browser proof passed at durable sequence ${health.durableSequence}\n`);
	process.stdout.write(`synthetic LOW/browser measurement: DOMContentLoaded ${navigationMs.toFixed(1)} ms; server VmRSS ${rssKiB} KiB\n`);
} finally {
  if (browser) await browser.close();
  if (server) await stopServer(server);
  await rm(fixtureRoot, { recursive: true, force: true });
}

function claudeFixture() {
  const pairs = [
    claudePair("2026-07-13T15:00:01Z", "search", "Glob", { pattern: "**/*.md", path: observedRepo }, "README.md"),
    claudePair("2026-07-13T15:00:03Z", "read", "Read", { file_path: join(observedRepo, "README.md") }, "fixture"),
    claudePair("2026-07-13T15:00:05Z", "edit", "Edit", editInput(), "updated"),
    claudePair("2026-07-13T15:00:07Z", "verify", "Bash", { command: "go test ./..." }, "PASS"),
    claudePair("2026-07-13T15:00:09Z", "failed", "Bash", { command: "false" }, "exit status 1", true),
    claudePair("2026-07-13T15:00:11Z", "after-verify", "Edit", editInput(), "updated"),
    claudePair("2026-07-13T15:00:13Z", "agent", "Task", { description: "synthetic child" }, "complete")
  ];
  return [claudeUser("2026-07-13T15:00:00Z", "inspect"), ...pairs.flat()].join("\n") + "\n";
}

function codexFixture() {
  const values = [
    JSON.stringify({ timestamp: "2026-07-13T14:00:00Z", type: "session_meta", payload: { id: "codex-live", session_id: "codex-live", cwd: observedRepo } }),
    JSON.stringify({ timestamp: "2026-07-13T14:00:00.5Z", type: "response_item", payload: { type: "message", role: "user", content: [{ type: "input_text", text: "inspect" }] } }),
    codexCall("2026-07-13T14:00:01Z", "search", "exec_command", { cmd: "rg README", workdir: observedRepo }),
    codexOutput("2026-07-13T14:00:02Z", "search", "README.md:1:# live fixture"),
    codexCall("2026-07-13T14:00:03Z", "read", "exec_command", { cmd: "sed -n '1p' README.md", workdir: observedRepo }),
    codexOutput("2026-07-13T14:00:04Z", "read", "# live fixture"),
    codexCall("2026-07-13T14:00:05Z", "edit", "apply_patch", { _raw: "*** Begin Patch\n*** Update File: README.md\n@@\n-# live fixture\n+# live fixture\n*** End Patch" }),
    codexOutput("2026-07-13T14:00:06Z", "edit", "Success. Updated README.md"),
    codexCall("2026-07-13T14:00:07Z", "command", "exec_command", { cmd: "printf fixture", workdir: observedRepo }),
    codexOutput("2026-07-13T14:00:08Z", "command", "Process exited with code 0\nfixture"),
    codexCall("2026-07-13T14:00:09Z", "verify", "exec_command", { cmd: "go test ./...", workdir: observedRepo }),
    codexOutput("2026-07-13T14:00:10Z", "verify", "Process exited with code 0\nPASS"),
    codexCall("2026-07-13T14:00:11Z", "failed", "exec_command", { cmd: "go test ./...", workdir: observedRepo }),
    codexOutput("2026-07-13T14:00:12Z", "failed", "Process exited with code 1\nFAIL"),
    codexCall("2026-07-13T14:00:13Z", "after-verify", "apply_patch", { _raw: "*** Begin Patch\n*** Update File: README.md\n@@\n-# live fixture\n+# live fixture\n*** End Patch" }),
    codexOutput("2026-07-13T14:00:14Z", "after-verify", "Success. Updated README.md")
  ];
  return values.join("\n") + "\n";
}

async function appendClaudeTool(id, timestamp) {
  const base = new Date(timestamp).getTime();
  await appendFile(claudePath, claudeCall(new Date(base).toISOString(), id, "Edit", editInput()) + "\n");
  await appendFile(claudePath, claudeResult(new Date(base + 1000).toISOString(), id, "fixture") + "\n");
}

function editInput() {
  return { file_path: join(observedRepo, "README.md"), old_string: "fixture", new_string: "fixture" };
}

function claudePair(timestamp, id, name, input, result, failed = false) {
  const time = new Date(timestamp).getTime();
  return [claudeCall(new Date(time).toISOString(), id, name, input), claudeResult(new Date(time + 1000).toISOString(), id, result, failed)];
}

function claudeUser(timestamp, content) {
  return JSON.stringify({ type: "user", timestamp, cwd: observedRepo, sessionId: "claude-live", message: { role: "user", content } });
}

function claudeCall(timestamp, id, name, input) {
  return JSON.stringify({ type: "assistant", timestamp, cwd: observedRepo, sessionId: "claude-live", message: { role: "assistant", content: [{ type: "tool_use", id, name, input }] } });
}

function claudeResult(timestamp, id, content, failed = false) {
  return JSON.stringify({ type: "user", timestamp, cwd: observedRepo, sessionId: "claude-live", message: { role: "user", content: [{ type: "tool_result", tool_use_id: id, content, is_error: failed }] } });
}

function codexCall(timestamp, callId, name, args) {
  return JSON.stringify({ timestamp, type: "response_item", payload: { type: "function_call", id: `fc-${callId}`, call_id: callId, name, arguments: JSON.stringify(args) } });
}

function codexOutput(timestamp, callId, output) {
  return JSON.stringify({ timestamp, type: "response_item", payload: { type: "function_call_output", call_id: callId, output } });
}

async function startServer(port) {
  const child = spawn(binary, ["serve", "--no-open", "--port", String(port), "--dev", "--config", registryPath, "--data-dir", dataDir, "--claude-dir", claudeDir, "--codex-dir", codexDir], {
    cwd: repoRoot,
    stdio: ["ignore", "pipe", "pipe"]
  });
  await serverURL(child);
  return child;
}

async function stopServer(child) {
  if (!child || child.exitCode !== null) return;
  child.kill("SIGTERM");
  await Promise.race([once(child, "exit"), new Promise((resolveWait) => setTimeout(resolveWait, 2000))]);
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
      listener.close((err) => err ? reject(err) : resolvePort(address.port));
    });
  });
}

async function healthSequence(baseURL) {
  return Number((await (await fetch(`${baseURL}/api/ingestion/health`)).json()).durableSequence);
}

async function waitForTrace(page, count, timeout = 10000) {
  await page.waitForFunction(
    (wanted) => Number(document.querySelector('[data-testid="observatory-root"]')?.getAttribute("data-trace-events")) === wanted,
    count,
    { timeout }
  );
}

async function waitFor(predicate, timeout = 10000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      if (await predicate()) return;
    } catch {
      // A restart briefly refuses connections; retry until the deadline.
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error("condition timed out");
}
