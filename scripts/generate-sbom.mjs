import { readFile, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { delimiter } from "node:path";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const output = resolve(root, process.argv[2] || "docs/sbom.cdx.json");
const lock = JSON.parse(await readFile(resolve(root, "web/package-lock.json"), "utf8"));
const components = [];

for (const [path, pkg] of Object.entries(lock.packages || {})) {
  if (!path || !pkg.version || !path.includes("node_modules/")) continue;
  const name = pkg.name || path.slice(path.lastIndexOf("node_modules/") + "node_modules/".length);
  const purlName = name.startsWith("@") ? name.replace("/", "%2F") : name;
  components.push({
    type: "library",
    name,
    version: pkg.version,
    "bom-ref": `pkg:npm/${purlName}@${pkg.version}`,
    purl: `pkg:npm/${purlName}@${pkg.version}`,
    ...(pkg.license ? { licenses: [{ license: { id: pkg.license } }] } : {})
  });
}

const goPath = [resolve(process.env.HOME || "", ".local/go/bin"), process.env.PATH || ""].join(delimiter);
const go = spawnSync("go", ["list", "-m", "-f", "{{.Path}} {{.Version}}", "all"], {
  cwd: root,
  encoding: "utf8",
  env: { ...process.env, PATH: goPath }
});
if (go.error || go.status !== 0) throw new Error(go.error?.message || go.stderr || "go list failed");
for (const line of go.stdout.trim().split("\n").slice(1)) {
  const [name, version] = line.trim().split(/\s+/);
  if (!name || !version) continue;
  components.push({ type: "library", name, version, "bom-ref": `pkg:golang/${name}@${version}`, purl: `pkg:golang/${name}@${version}` });
}

const uniqueComponents = [...new Map(components.map((component) => [component["bom-ref"], component])).values()];
uniqueComponents.sort((a, b) => a["bom-ref"].localeCompare(b["bom-ref"]));
const sbom = {
  bomFormat: "CycloneDX",
  specVersion: "1.5",
  version: 1,
  metadata: {
    timestamp: new Date().toISOString(),
    tools: { components: [{ type: "application", name: "mindwalk-local-sbom-generator", version: "1" }] },
    component: { type: "application", name: "mindwalk-observatory", version: "0.1.0-observatory" }
  },
  components: uniqueComponents
};
await writeFile(output, JSON.stringify(sbom, null, 2) + "\n", { mode: 0o644 });
process.stdout.write(`wrote ${uniqueComponents.length} components to ${output}\n`);
