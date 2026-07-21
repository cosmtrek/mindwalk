# Trace Health V1 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为每个 Mindwalk session 提供确定性、本地化的 Trace Health，统一通过 Dock、HTTP API、`mindwalk health` 文本输出和 `--json` 输出解释文件读取、错误、验证、子代理四类证据的可信边界。

**Architecture:** Adapter 在内存 Trace 上保留最小的验证结果证据；新的 `internal/health` 纯函数层组合 Trace、可选 AgentGraph 与图加载错误，生成版本化 `model.SessionHealth`。Server 与 CLI 复用同一 builder，React 只负责把固定状态和原因码翻译成产品文案，不重新计算可信度。

**Tech Stack:** Go 1.25、标准库 `flag`/`encoding/json`/`net/http`、`jsonschema/v6`、React 19、TypeScript 5.9、Vite 7、Playwright 1.61、现有 CSS 与 Lucide 图标。

## Global Constraints

- 不生成总分、百分比或排行榜。
- 不调用 LLM，不改变 Judge prompt、finding 或 verdict 聚合规则。
- 不持久化 SessionHealth，不增加独立磁盘缓存，不联网。
- 不升级或扩展导出的 Trace JSON v1；验证证据必须使用 `json:"-"` 保持在内存中。
- `unavailable` 表示日志证据不足；`failed` 表示 Mindwalk 计算失败；两者的结构和文案不得混用。
- `isError: false` 不能单独证明命令成功。
- 不输出原始任务文字、工具输入、工具输出或源日志行。
- V1 只在打开 session 和显式 Rescan 后刷新，不轮询，不实现 Live Follow。
- 修改 schema 时同步 Go/TypeScript 类型与 schema 测试；前端变化通过 `make embed-static` 生成 `internal/server/static`，不得手改静态产物。
- 每个任务严格执行 RED → GREEN → 重构/检查 → 提交；不夹带无关重构。

---

## 文件结构与职责

**新增文件：**

- `internal/model/health.go`：SessionHealth 合同、状态常量、原因码、内存验证证据类型。
- `schema/session-health.schema.json`：严格镜像 SessionHealth v1 JSON。
- `internal/model/health_schema_test.go`：代表性 payload 的 schema 校验。
- `internal/health/health.go`：四类信号的纯计算与 Dock badge 汇总。
- `internal/health/health_test.go`：表格驱动分类测试与输入不变性测试。
- `internal/health/format.go`：CLI 人类可读文本格式化。
- `internal/health/format_test.go`：稳定文本与隐私边界测试。
- `web/src/ui/HealthPanel.tsx`：Dock pop 的普通说明、技术详情、失败重试。
- `web/e2e/trace-health.spec.ts`：Trace Health 浏览器契约测试。

**修改文件：**

- `internal/model/model.go`：Trace 增加 `json:"-"` 的 `HealthEvidence`。
- `internal/adapter/adapter.go`：ToolResult 增加结果是否明确；BuildEvent 聚合验证证据。
- `internal/adapter/claudecode/adapter.go`、`adapter_test.go`：用 `*bool` 区分明确成功与缺失结果。
- `internal/adapter/codex/adapter.go`、`adapter_test.go`：把输出失败探测升级为 `(failed, known)`。
- `internal/server/server.go`、`server_test.go`：新增 health endpoint，隔离 AgentGraph 失败。
- `cmd/mindwalk/main.go`、`main_test.go`：新增 `health` 命令、`--json` 与相邻 session 发现。
- `web/src/types.ts`：SessionHealth TypeScript 合同。
- `web/src/api/client.ts`：`getSessionHealth`。
- `web/src/ui/Dock.tsx`：新增 `estimated`/`limited` badge 类型。
- `web/src/App.tsx`：加载、失效、重试并注册 Health pop。
- `web/src/styles.css`：Health pop、状态与 badge 的克制样式。
- `testdata/agent-lens/browser-fixtures.json`：补充浏览器测试使用的 health fixtures。
- `README.md`、`.claude/skills/verify/SKILL.md`：命令与 UI 验证说明。
- `internal/server/static/**`：仅由 `make embed-static` 生成。

---

### Task 1: 定义 SessionHealth 合同与非序列化验证证据

**Files:**
- Create: `internal/model/health.go`
- Create: `schema/session-health.schema.json`
- Create: `internal/model/health_schema_test.go`
- Modify: `internal/model/model.go`

**Interfaces:**
- Consumes: 现有 `model.Trace`、`model.AgentGraph`、`ObservabilityExact|Estimated|Unavailable`。
- Produces: `model.SessionHealthVersion`、`model.SessionHealth`、`model.HealthSignals`、四个信号结构、`model.TraceHealthEvidence`、`model.VerificationEvidence` 以及固定状态/原因码常量。

- [ ] **Step 1: 写 schema 合同失败测试**

在 `internal/model/health_schema_test.go` 写两个测试：一个完整 mixed payload 应通过；一个 `availability: "ready"` 但缺少 `quality` 的 payload 应失败。测试核心代码如下：

```go
func compileHealthSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile("../../schema/session-health.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestSessionHealthSchemaAcceptsRepresentativeHealth(t *testing.T) {
	health := SessionHealth{
		Version:    SessionHealthVersion,
		SessionKey: "codex-root",
		Signals: HealthSignals{
			Reads: ReadHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonReadsInferred}, DirectCount: 18, InferredCount: 12},
			Errors: ErrorHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityExact, Reason: HealthReasonStructuredErrors}},
			Verification: VerificationHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonVerificationUnknown}, RecognizedCount: 4, KnownResultCount: 3, UnknownResultCount: 1},
			Subagents: SubagentHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonMixedAgentLinks}, ExactCount: 3, DerivedCount: 1, MissingTraceCount: 1},
		},
	}
	assertSchemaAccepts(t, compileHealthSchema(t), health)
}
```

非法 payload 测试使用原始 `map[string]any` 删除 `signals.reads.quality`，断言 `schema.Validate` 返回非 nil。

- [ ] **Step 2: 运行测试确认 RED**

Run: `go test ./internal/model -run SessionHealthSchema -count=1`

Expected: FAIL，错误包含 `undefined: SessionHealth` 或 `session-health.schema.json: no such file`。

- [ ] **Step 3: 实现 Go 合同与 Trace 内存证据**

在 `internal/model/health.go` 定义固定结构，禁止 `map[string]any`：

```go
package model

const SessionHealthVersion = 1

const (
	HealthReady         = "ready"
	HealthNotApplicable = "not-applicable"
	HealthFailed        = "failed"

	HealthReasonStructuredReads       = "structured-read-targets"
	HealthReasonReadsInferred         = "some-reads-inferred-from-shell"
	HealthReasonReadsUnavailable      = "read-signal-unavailable"
	HealthReasonStructuredErrors      = "structured-error-status"
	HealthReasonErrorsInferred        = "errors-inferred-from-output"
	HealthReasonErrorsUnavailable     = "error-signal-unavailable"
	HealthReasonStructuredVerify      = "structured-verification-results"
	HealthReasonVerificationInferred  = "verification-command-recognition-inferred"
	HealthReasonVerificationUnknown   = "some-verification-results-unknown"
	HealthReasonVerificationUnavailable = "verification-signal-unavailable"
	HealthReasonNoSubagents           = "no-subagents"
	HealthReasonExactAgentLinks       = "exact-agent-links"
	HealthReasonMixedAgentLinks       = "mixed-agent-link-quality"
	HealthReasonAgentContextMissing   = "agent-graph-context-unavailable"
	HealthReasonAgentGraphFailed      = "agent-graph-load-failed"
)

type SessionHealth struct {
	Version    int           `json:"version"`
	SessionKey string        `json:"sessionKey"`
	Badge      string        `json:"badge,omitempty"`
	Signals    HealthSignals `json:"signals"`
}

const (
	HealthBadgeEstimated = "estimated"
	HealthBadgeLimited   = "limited"
)

type HealthSignals struct {
	Reads        ReadHealth         `json:"reads"`
	Errors       ErrorHealth        `json:"errors"`
	Verification VerificationHealth `json:"verification"`
	Subagents    SubagentHealth     `json:"subagents"`
}

type HealthSignal struct {
	Availability string   `json:"availability"`
	Quality      string   `json:"quality,omitempty"`
	Reason       string   `json:"reason"`
	Affects      []string `json:"affects"`
}

type ReadHealth struct {
	HealthSignal
	DirectCount   int `json:"directCount"`
	InferredCount int `json:"inferredCount"`
}

type ErrorHealth struct {
	HealthSignal
	RecognizedCount int `json:"recognizedCount"`
}

type VerificationHealth struct {
	HealthSignal
	RecognizedCount      int `json:"recognizedCount"`
	KnownResultCount     int `json:"knownResultCount"`
	UnknownResultCount   int `json:"unknownResultCount"`
	EditsAfterLastVerify int `json:"editsAfterLastVerify"`
}

type SubagentHealth struct {
	HealthSignal
	ExactCount            int `json:"exactCount"`
	DerivedCount          int `json:"derivedCount"`
	MissingTraceCount     int `json:"missingTraceCount"`
	UnavailableTraceCount int `json:"unavailableTraceCount"`
}

type TraceHealthEvidence struct {
	Verification VerificationEvidence `json:"-"`
}

type VerificationEvidence struct {
	Quality            string
	RecognizedCount    int
	KnownResultCount   int
	UnknownResultCount int
}
```

在 `internal/model/model.go` 的 `Trace` 增加：

```go
HealthEvidence TraceHealthEvidence `json:"-"`
```

- [ ] **Step 4: 写严格 JSON Schema**

创建 `schema/session-health.schema.json`，要求顶层 `version/sessionKey/signals` 和四个固定 signal；可选 `badge` 只允许 `estimated|limited`；`additionalProperties` 全部为 `false`。每个 signal 使用条件约束：`availability == ready` 时必须有 `quality` 且值为 `exact|estimated|unavailable`；`not-applicable|failed` 时禁止 `quality`。所有计数为非负整数，`affects` 为唯一字符串数组。

- [ ] **Step 5: 运行合同测试确认 GREEN**

Run: `gofmt -w internal/model/health.go internal/model/health_schema_test.go && go test ./internal/model -run SessionHealthSchema -count=1`

Expected: PASS。

- [ ] **Step 6: 确认 Trace JSON 未变化**

在现有 Trace schema 测试或新测试中为 `Trace.HealthEvidence.Verification` 填入非零值，marshal 后断言输出不包含 `HealthEvidence`、`verification` 或新增字段。

Run: `go test ./internal/model -count=1`

Expected: PASS，Trace v1 schema 仍接受输出。

- [ ] **Step 7: 提交合同**

```bash
git add internal/model/model.go internal/model/health.go internal/model/health_schema_test.go schema/session-health.schema.json
git commit -m "feat(model): define session health contract"
```

---

### Task 2: 让 Claude 与 Codex adapter 保留验证结果证据

**Files:**
- Modify: `internal/adapter/adapter.go`
- Modify: `internal/adapter/adapter_test.go`
- Modify: `internal/adapter/claudecode/adapter.go`
- Modify: `internal/adapter/claudecode/adapter_test.go`
- Modify: `internal/adapter/codex/adapter.go`
- Modify: `internal/adapter/codex/adapter_test.go`

**Interfaces:**
- Consumes: `model.Trace.HealthEvidence.Verification`、现有 `actionFor`、Claude `tool_result.is_error`、Codex output envelope。
- Produces: `adapter.ToolResult.OutcomeKnown bool`；`BuildEvent` 对 `verify` 事件递增 recognized/known/unknown，并把当前基于 shell command / static exec 文本的识别质量记为 `estimated`；Claude 用字段是否存在判定 known；Codex `commandOutputStatus(string) (failed bool, known bool)`。

- [ ] **Step 1: 写共享 BuildEvent RED 测试**

在 `internal/adapter/adapter_test.go` 新增三次 verify call：known success、known failure、unknown。断言：

```go
got := trace.HealthEvidence.Verification
want := model.VerificationEvidence{Quality: model.ObservabilityEstimated, RecognizedCount: 3, KnownResultCount: 2, UnknownResultCount: 1}
if got != want {
	t.Fatalf("verification evidence = %#v, want %#v", got, want)
}
```

- [ ] **Step 2: 运行共享测试确认 RED**

Run: `go test ./internal/adapter -run VerificationEvidence -count=1`

Expected: FAIL，`ToolResult` 没有 `OutcomeKnown` 或 evidence 仍为零。

- [ ] **Step 3: 最小实现共享 evidence 聚合**

修改 `adapter.ToolResult`：

```go
type ToolResult struct {
	Content      string
	IsError      bool
	OutcomeKnown bool
}
```

在 `BuildEvent` 先构造 event，再按 action 记录：

```go
if event.Action == "verify" {
	evidence := &trace.HealthEvidence.Verification
	evidence.Quality = model.ObservabilityEstimated
	evidence.RecognizedCount++
	if result.OutcomeKnown {
		evidence.KnownResultCount++
	} else {
		evidence.UnknownResultCount++
	}
}
```

- [ ] **Step 4: 运行共享测试确认 GREEN**

Run: `gofmt -w internal/adapter/adapter.go internal/adapter/adapter_test.go && go test ./internal/adapter -run VerificationEvidence -count=1`

Expected: PASS。

- [ ] **Step 5: 写 Claude 字段存在性 RED 测试**

在 Claude fixture 中放入三个 `Bash` verify：`"is_error": false`、`"is_error": true`、完全缺失 `is_error`。断言三个事件 error 值保持正确，evidence 为 `recognized=3 known=2 unknown=1`。

Run: `go test ./internal/adapter/claudecode -run VerificationOutcomeEvidence -count=1`

Expected: FAIL，因为当前 `bool` 无法区分 false 与字段缺失。

- [ ] **Step 6: 用指针解析 Claude `is_error`**

把 `contentItem.IsError` 改为：

```go
IsError *bool `json:"is_error"`
```

`buildEvent` 改为：

```go
isError := result.IsError != nil && *result.IsError
return adapter.BuildEvent(trace, call, adapter.ToolResult{
	Content:      adapter.ContentToString(result.Content),
	IsError:      isError,
	OutcomeKnown: result.IsError != nil,
})
```

更新直接构造 `contentItem` 的测试为 `boolPtr(false/true)` 辅助函数。

- [ ] **Step 7: 运行 Claude 测试确认 GREEN**

Run: `gofmt -w internal/adapter/claudecode/adapter.go internal/adapter/claudecode/adapter_test.go && go test ./internal/adapter/claudecode -count=1`

Expected: PASS。

- [ ] **Step 8: 写 Codex output certainty RED 测试**

扩展 `commandOutputFailed` 表格为 `commandOutputStatus`：JSON `exit_code`、`Process exited with code`、`Script completed`、`Script failed`、`Aborted by user` 都是 known；普通自由文本是 unknown；`Script running` 不是最终结果，必须 unknown。断言 `(failed, known)` 二元组。

Run: `go test ./internal/adapter/codex -run CommandOutputStatus -count=1`

Expected: FAIL，函数不存在。

- [ ] **Step 9: 实现 Codex `(failed, known)` 并接入 decodeOutput**

把现有探测主体迁移为：

```go
func commandOutputStatus(output string) (failed bool, known bool) {
	// JSON exit_code / metadata.exit_code are terminal. Without an exit code,
	// timed_out:true is a known failure; bare timed_out:false remains unknown.
	// Explicit patch failure, Script completed/failed, exit-code lines, and user abort return known=true.
	// Script running and unstructured text return known=false.
}

func commandOutputFailed(output string) bool {
	failed, _ := commandOutputStatus(output)
	return failed
}
```

`decodeOutput` 使用：

```go
failed, known := commandOutputStatus(output)
return payload.CallID, adapter.ToolResult{Content: output, IsError: failed, OutcomeKnown: known}, true
```

当 `patch_apply_end.success` 覆盖 result 时，同时设置 `result.OutcomeKnown = true`。

- [ ] **Step 10: 运行 adapter 全量测试**

Run: `gofmt -w internal/adapter/codex/adapter.go internal/adapter/codex/adapter_test.go && go test ./internal/adapter/... -count=1`

Expected: PASS。

- [ ] **Step 11: 提交 adapter evidence**

```bash
git add internal/adapter/adapter.go internal/adapter/adapter_test.go internal/adapter/claudecode/adapter.go internal/adapter/claudecode/adapter_test.go internal/adapter/codex/adapter.go internal/adapter/codex/adapter_test.go
git commit -m "feat(adapter): preserve verification evidence"
```

---

### Task 3: 实现纯函数 SessionHealth builder 与文本格式化

**Files:**
- Create: `internal/health/health.go`
- Create: `internal/health/health_test.go`
- Create: `internal/health/format.go`
- Create: `internal/health/format_test.go`

**Interfaces:**
- Consumes: `health.Build(sessionKey string, trace *model.Trace, graph *model.AgentGraph, graphErr error)` 的四个输入。
- Produces: 含顶层 `Badge` 的 `model.SessionHealth`；`health.WriteText(w io.Writer, summary model.SessionHealth) error`。

- [ ] **Step 1: 写四信号表格 RED 测试**

在 `health_test.go` 为 reads/errors/verification/subagents 分组写表格测试，至少包含设计文档列出的 exact、estimated、unavailable、not-applicable、failed。Verification 必须分别覆盖 exact recognition + all-known outcomes、estimated recognition + all-known outcomes、任一 unknown outcome、无可用 signal。使用固定 helper 构造 Trace/Graph，并断言完整 signal 结构，而不是只断言 quality。

必须单独覆盖：

```go
func TestBuildDoesNotMutateInputs(t *testing.T) {
	beforeTrace, _ := json.Marshal(trace)
	beforeGraph, _ := json.Marshal(graph)
	_ = Build("root", trace, graph, nil)
	afterTrace, _ := json.Marshal(trace)
	afterGraph, _ := json.Marshal(graph)
	if !bytes.Equal(beforeTrace, afterTrace) || !bytes.Equal(beforeGraph, afterGraph) {
		t.Fatal("Build mutated its inputs")
	}
}
```

- [ ] **Step 2: 运行 builder 测试确认 RED**

Run: `go test ./internal/health -run 'Build|Badge' -count=1`

Expected: FAIL，package 或函数不存在。

- [ ] **Step 3: 实现 reads/errors/verification 分类**

`Build` 初始化固定 affects 列表；reads 通过 `target.Touch == "read"` 与 `target.Weak` 计数；errors 使用 `trace.Stats.Errors` 汇总；verification 使用 `trace.HealthEvidence.Verification` 和 `trace.Stats.EditsAfterLastVerify`。Verification 只有在 recognition quality 为 exact 且全部 recognized outcomes 都 known 时才是 exact；recognition 为 estimated 或任一 outcome unknown 时为 estimated；没有可用 recognition signal 时为 unavailable。

当 `trace.Stats.Actions.Verify > 0` 但内存 evidence 的 recognized 为 0（例如手工构造或旧路径）时，不得伪造 known；返回 `quality=unavailable`、reason `verification-signal-unavailable`，recognized 仍取 `Actions.Verify`。

- [ ] **Step 4: 实现 subagent 分类与图错误隔离**

规则顺序固定：

```go
switch {
case trace.Stats.Subagents == 0:
	availability = model.HealthNotApplicable
	reason = model.HealthReasonNoSubagents
case graphErr != nil:
	availability = model.HealthFailed
	reason = model.HealthReasonAgentGraphFailed
case graph == nil:
	availability = model.HealthReady
	quality = model.ObservabilityUnavailable
	reason = model.HealthReasonAgentContextMissing
default:
	// 跳过 main，分别统计 exact/derived、missing/unavailable。
}
```

混合图质量为 estimated；仅当所有 child 都 exact 且 available 时为 exact。

- [ ] **Step 5: 实现 Dock badge 汇总**

builder 在 `SessionHealth.Badge` 写入显示状态：任何 `failed` 或 quality `unavailable` → `limited`；否则任何 estimated → `estimated`；否则保持空字符串并在 JSON 中省略。`not-applicable` 被忽略。该字段不评价 agent 好坏。

- [ ] **Step 6: 运行 builder 测试确认 GREEN**

Run: `gofmt -w internal/health/health.go internal/health/health_test.go && go test ./internal/health -run 'Build|Badge' -count=1`

Expected: PASS。

- [ ] **Step 7: 写文本格式化 RED 测试**

测试固定顺序为 failed/unavailable → estimated → exact → not-applicable；断言 estimated errors 为零时使用 `No errors were recognized.`，输出不含 session task、event summary、path 或 tool output。

- [ ] **Step 8: 实现 `WriteText`**

文本标题固定为 `Session health`，四类 signal 使用 `File reads / Errors / Verification / Subagents`。技术原因码不在默认文本展开；`--json` 才提供完整原因码。不得使用 ANSI color，保证管道和快照稳定。

- [ ] **Step 9: 运行 health package 全量测试**

Run: `gofmt -w internal/health/format.go internal/health/format_test.go && go test ./internal/health -count=1`

Expected: PASS。

- [ ] **Step 10: 提交核心 builder**

```bash
git add internal/health internal/model/health.go
git commit -m "feat: derive trace health signals"
```

---

### Task 4: 提供 Health HTTP API 并隔离 AgentGraph 失败

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `health.Build`、`Server.findSession`、`traceAndMapMeta`、`agentGraph`。
- Produces: `GET /api/sessions/{selector}/health`；`Server.sessionHealth(selector string) (*model.SessionHealth, error)`。

- [ ] **Step 1: 写 endpoint RED 测试**

在 `server_test.go` 使用现有 `requestSessionResource`/`newAgentAPITestServer`，覆盖：正常 mixed response、未知 session 404、graph loader error 仍返回 200 且只有 subagents 为 failed。为测试 source 增加可选 `graphErr error`：

```go
func (s *agentAPISource) BuildAgentGraph(...) (*model.AgentGraph, error) {
	if s.graphErr != nil { return nil, s.graphErr }
	return s.graphs[root.Key], nil
}
```

- [ ] **Step 2: 运行 endpoint 测试确认 RED**

Run: `go test ./internal/server -run SessionHealth -count=1`

Expected: FAIL，health route 返回 404。

- [ ] **Step 3: 实现 server helper 与路由**

新增 import `internal/health`。在 `handleSessionResource` 的资源 switch 增加：

```go
case "health":
	summary, err := s.sessionHealth(selector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, summary)
```

helper：

```go
func (s *Server) sessionHealth(selector string) (*model.SessionHealth, error) {
	root, err := s.findSession(selector)
	if err != nil { return nil, err }
	trace, _, err := s.traceAndMapMeta(root)
	if err != nil { return nil, err }
	graph, graphErr := s.agentGraph(root)
	key := root.Key
	if key == "" { key = adapter.SessionKey(root.Harness, root.Path) }
	summary := health.Build(key, trace, graph, graphErr)
	return &summary, nil
}
```

- [ ] **Step 4: 验证 Rescan 新鲜度与不写 report**

新增测试：先请求 health，再扩展 session 文件并触发 `fresh=1` list/scan 路径，再请求 health，recognized count 必须更新。对 judge runner/cache 使用现有 stub 或检查 report cache 目录保持空，证明 endpoint 不启动 analyze、不写 report。

- [ ] **Step 5: 运行 server 目标测试与 race**

Run: `go test ./internal/server -run SessionHealth -count=1 && go test -race ./internal/server -run SessionHealth -count=1`

Expected: PASS。

- [ ] **Step 6: 提交 API**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): expose session health"
```

---

### Task 5: 增加 `mindwalk health` 文本与 JSON 命令

**Files:**
- Modify: `cmd/mindwalk/main.go`
- Modify: `cmd/mindwalk/main_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `health.Build`、`health.WriteText`、`adapter.AgentGraphSource`、现有 Claude/Codex adapters。
- Produces: `healthCommand(args []string) error`；`loadHealthInput(path string) (adapter.Source, model.SessionMeta, *model.Trace, error)`；`mindwalk health <session> [--json]`。

- [ ] **Step 1: 写 CLI RED 测试**

在 `main_test.go` 使用临时 Claude/Codex fixture，覆盖：默认文本、`--json`、flag 位于 positional 后、孤立文件、缺失文件、JSON stdout 可 unmarshal。把输出逻辑放进可注入 writer 的 helper：

```go
func writeHealth(w io.Writer, summary model.SessionHealth, asJSON bool) error
```

测试 `--json` 缓冲区以 `{` 开头且不包含 `mindwalk:` 或进度文字。

- [ ] **Step 2: 运行 CLI 测试确认 RED**

Run: `go test ./cmd/mindwalk -run Health -count=1`

Expected: FAIL，unknown command `health` 或 helper 不存在。

- [ ] **Step 3: 实现 source-aware session 加载**

把现有 `parseTrace` 内部循环提取为：

```go
func loadSession(path string) (adapter.Source, model.SessionMeta, *model.Trace, error)
```

它按 Claude/Codex 顺序调用 `Parse`，成功后调用同一 source 的 `Summarize`；`parseTrace` 继续包装此函数并只返回 Trace，保持 `trace`/`analyze` 行为不变。

只有当输入绝对路径位于 `source.SessionDir()` 内时，CLI 才递归遍历该目录的 `.jsonl` 文件并对每个文件调用同一 source 的 `Summarize`，构造包含 auxiliary session 的完整 catalog，再调用 `BuildAgentGraph`。不能使用 `Source.ListSessions()`，因为现有 Codex/Claude 列表会隐藏 auxiliary children。若输入位于默认 session 目录之外，则传入 `graph=nil, graphErr=nil`，让 subagent signal 表示 inspection context unavailable，而不是 missing traces。

- [ ] **Step 4: 实现 health 参数解析与输出**

使用 `flag.NewFlagSet("health", flag.ContinueOnError)` 和与 `analyze` 相同的循环，允许 `--json` 位于 positional 前后。要求恰好一个 session 参数；usage 为：

```text
usage: mindwalk health <session.jsonl> [--json]
```

`writeHealth` 的 JSON 分支使用 `json.NewEncoder(w).Encode(summary)`；文本分支调用 `health.WriteText`。

- [ ] **Step 5: 更新总 usage 与 README**

在 `run` switch 增加 `case "health"`。README Quick start 命令表增加 health，并在 Reading the picture 增加一句：Trace Health 解释哪些数据直接记录、推测或不可判断；明确该功能完全本地、不调用 judge。

- [ ] **Step 6: 运行 CLI 与现有命令回归测试**

Run: `gofmt -w cmd/mindwalk/main.go cmd/mindwalk/main_test.go && go test ./cmd/mindwalk -count=1 && go run ./cmd/mindwalk help | rg 'health <session>'`

Expected: tests PASS，help 包含 health 命令。

- [ ] **Step 7: 生产 binary 冒烟**

Run:

```bash
go build -o .tmp-mindwalk-health ./cmd/mindwalk
./.tmp-mindwalk-health health testdata/claude-session.jsonl --json | node -e 'let s=""; process.stdin.on("data", c => s += c); process.stdin.on("end", () => { const v = JSON.parse(s); if (v.version !== 1 || !v.signals?.reads) process.exit(1); });'
trash .tmp-mindwalk-health
```

Expected: exit 0，stdout 为单个合法 JSON 对象。

- [ ] **Step 8: 提交 CLI**

```bash
git add cmd/mindwalk/main.go cmd/mindwalk/main_test.go README.md
git commit -m "feat(cli): inspect session health"
```

---

### Task 6: 增加 Dock Trace Health pop 与刷新/失败状态

**Files:**
- Modify: `web/src/types.ts`
- Modify: `web/src/api/client.ts`
- Modify: `web/src/ui/Dock.tsx`
- Create: `web/src/ui/HealthPanel.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/styles.css`

**Interfaces:**
- Consumes: `GET /api/sessions/{key}/health`、`SessionHealth` JSON、`PanelDescriptor`。
- Produces: `getSessionHealth(key): Promise<SessionHealth>`；`HealthPanel`；Dock badge `estimated|limited`；session switch/Rescan/health-only retry 行为。

- [ ] **Step 1: 先写 TypeScript 合同与 API 调用**

在 `types.ts` 定义固定接口，字段名与 Go JSON 完全一致：

```ts
export type HealthAvailability = "ready" | "not-applicable" | "failed";
export type HealthQuality = "exact" | "estimated" | "unavailable";
export interface HealthSignalBase { availability: HealthAvailability; quality?: HealthQuality; reason: string; affects: string[]; }
export interface SessionHealth { version: 1; sessionKey: string; badge?: "estimated" | "limited"; signals: { reads: ReadHealth; errors: ErrorHealth; verification: VerificationHealth; subagents: SubagentHealth } }
```

`client.ts` 增加：

```ts
export function getSessionHealth(key: string): Promise<SessionHealth> {
  return getJSON(`/api/sessions/${encodeURIComponent(key)}/health`);
}
```

Run: `npm --prefix web run build`

Expected: PASS，证明类型与 import 完整。

- [ ] **Step 2: 写 `HealthPanel` 组件静态结构**

Props 固定为：

```ts
interface HealthPanelProps {
  health?: SessionHealth;
  loading: boolean;
  error?: string;
  onRetry: () => void;
  onClose: () => void;
}
```

四行按 `failed/unavailable → estimated → exact → not-applicable` 排序。每行使用 `<button aria-expanded>` 展开普通说明；技术详情放在嵌套之外的 sibling disclosure，避免嵌套交互控件。reason code、counts、affects 默认折叠。

- [ ] **Step 3: 扩展 Dock badge 类型与样式**

`PanelBadge` 增加 `estimated | limited`。CSS 新增两种非红色 dot：estimated 使用现有月光/低饱和色，limited 使用空心边框；保留现有 running/done/stale/failed 行为。

- [ ] **Step 4: 在 App 中增加 generation-guarded health 状态**

新增：

```ts
const healthRequest = useRef(0);
const [sessionHealth, setSessionHealth] = useState<SessionHealth>();
const [healthLoading, setHealthLoading] = useState(false);
const [healthError, setHealthError] = useState<string>();
```

`loadHealth(key)` 使用 request generation 和 `activeSessionKeyRef`，旧 session 的迟到响应不得覆盖当前状态。切换 session 时清空旧 health；初始选择后非阻塞加载。Health 失败不调用全局 `setError`，只写 `healthError`。

- [ ] **Step 5: 接入 Rescan 与 health-only retry**

`refresh` 必须在 `scan(true)` 完成后，若 active key 仍相同再调用 `loadHealth(key)`；不得在 scan 之前加载旧快照。Retry 只调用 `loadHealth(activeSessionKeyRef.current)`，不重载地图、Trace 或 AgentGraph。

- [ ] **Step 6: 注册 compact pop**

在 session panels 中、Agents 与 Evaluate 之间注册：

```ts
{
  id: "health",
  icon: ShieldCheck,
  hint: healthHint(sessionHealth, healthLoading, healthError),
  section: "session",
  presentation: "pop",
  badge: sessionHealth?.badge ?? null,
  render: () => <HealthPanel ... />
}
```

React 只读取 server 返回的顶层 `badge`，不得遍历 signals 重新推导。

- [ ] **Step 7: 构建前端确认 GREEN**

Run: `npm --prefix web run build`

Expected: TypeScript 和 Vite build PASS。

- [ ] **Step 8: 提交前端功能**

```bash
git add web/src/types.ts web/src/api/client.ts web/src/ui/Dock.tsx web/src/ui/HealthPanel.tsx web/src/App.tsx web/src/styles.css
git commit -m "feat(web): show trace health"
```

---

### Task 7: 浏览器验证、生产静态资源、文档与完整 QA

**Files:**
- Modify: `testdata/agent-lens/browser-fixtures.json`
- Create: `web/e2e/trace-health.spec.ts`
- Modify: `.claude/skills/verify/SKILL.md`
- Modify: `internal/server/static/index.html`
- Modify: `internal/server/static/assets/*` through generation only

**Interfaces:**
- Consumes: 完整 health API/UI/CLI。
- Produces: 精确、推测、不可判断、subagent failed、Rescan refresh 的浏览器证据；生产 binary/static parity；最终 QA 结论。

- [ ] **Step 1: 增加确定性 health fixture**

在 fixture JSON 增加 `health.exact`、`health.estimated`、`health.unavailable`、`health.agentFailed` 四个 schema-valid payload。不得包含真实路径、任务文字或工具输出。

- [ ] **Step 2: 写 Playwright RED 测试**

`trace-health.spec.ts` mock sessions/snapshot/agents/report/health 路由，覆盖：

1. exact：Dock 图标存在且无 badge；
2. estimated：quiet badge，展开显示 direct/inferred counts，技术详情默认关闭；
3. unavailable：排序最前，不使用 `.error`/红色样式，不出现 `No reads occurred`；
4. agentFailed：地图和 timeline 可用，subagent 行显示 retry；
5. Rescan：第一次 health 为 estimated，fresh scan 完成后第二次请求返回 exact，UI 更新；
6. 控制台零 error。

Run: `npm --prefix web run test:e2e -- trace-health.spec.ts`

Expected: FAIL，因为 fixture route 或 UI 尚未完全满足断言；若前面任务已全部实现，应先通过删掉一个 expected 文案确认测试能真实失败，再恢复断言运行 GREEN。

- [ ] **Step 3: 修正最小 UI/fixture 差异并跑 GREEN**

只修复 Trace Health 自身问题，不重构现有 Agent Lens 测试 helper。

Run: `npm --prefix web run test:e2e -- trace-health.spec.ts`

Expected: 全部 PASS。

- [ ] **Step 4: 更新 verify skill**

在 `.claude/skills/verify/SKILL.md` 增加手动验证：打开 exact 与 limited session、确认 badge 规则、展开技术详情、模拟 health 失败重试、Rescan 后刷新、确认 Evaluate 未被触发。

- [ ] **Step 5: 生成 embedded static 并检查漂移**

Run: `make embed-static && git status --short internal/server/static`

Expected: 只出现由当前 Vite build 生成的 hash 文件增删改；不得手工编辑产物。

- [ ] **Step 6: 运行完整自动验证**

Run:

```bash
go test ./... -count=1
go test -race -count=1 ./internal/adapter/... ./internal/health ./internal/server
npm --prefix web run build
npm --prefix web run test:e2e
make test
git diff --check
```

Expected: 全部 PASS；Playwright 包含现有 15 个 Agent Lens 用例和新增 Trace Health 用例。

- [ ] **Step 7: 生产 binary 双命令冒烟**

Run:

```bash
make build
./bin/mindwalk health testdata/claude-session.jsonl
./bin/mindwalk health testdata/claude-session.jsonl --json
```

Expected: 文本输出含四项；JSON 输出为单对象；两者都不出现 judge 启动文案或网络行为。

- [ ] **Step 8: 执行独立 QA gate**

按仓库 `qa-gate-review` 要求，由未参与实现的 reviewer 只读检查设计覆盖、diff、测试证据、Trace v1 不变性、Judge 不变性、隐私边界与生产 binary。结论必须为 PASS 才能声称完成；NEEDS WORK/FAIL 返回对应任务修复后重跑。

- [ ] **Step 9: 提交测试、文档与生成资源**

```bash
git add testdata/agent-lens/browser-fixtures.json web/e2e/trace-health.spec.ts .claude/skills/verify/SKILL.md internal/server/static README.md
git commit -m "test: verify trace health end to end"
```

- [ ] **Step 10: 最终提交审计**

Run:

```bash
git status --short
git log --oneline origin/master..HEAD
git diff --stat origin/master...HEAD
```

Expected: 除用户原有未跟踪 `.agents/` 外工作区干净；提交仅覆盖设计、计划与 Trace Health V1；无无关文件。
