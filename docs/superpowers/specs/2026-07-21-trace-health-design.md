# Trace Health V1 Design

## Status

Approved product design for a first implementation plan. This document defines the user experience, data ownership, contracts, failure behavior, validation, and explicit non-goals. It does not authorize implementation by itself.

## Summary

Trace Health tells users how much confidence Mindwalk can place in the signals extracted from one coding-agent session. It answers a question that must come before judging agent behavior:

> What did Mindwalk observe directly, what did it infer, and what can it not determine from this log?

V1 covers four signals:

1. file reads;
2. errors;
3. verification commands and their outcomes;
4. subagent correlation and trace availability.

The feature is deterministic and fully local. It does not call an LLM, assign a score, modify judge verdicts, or persist a new report. One shared `internal/health` builder serves the web UI, HTTP API, human-readable CLI output, and JSON CLI output.

## Product principles

### Report evidence limits, not agent quality

Trace Health evaluates Mindwalk's evidence, not the agent. Missing data must never be presented as agent failure. In particular:

- "no recognized error" is not "no error occurred";
- "no recognized verification" is not "the agent did not verify" when the source signal is incomplete;
- a missing child trace is not a failed child launch unless the source contains explicit failure evidence;
- `isError: false` is not proof of success when the source does not provide a structured result status.

### No aggregate score

V1 must not emit a percentage or a single health grade. Such a score would hide which conclusions are supported and create false precision. The UI reports each signal separately.

### One fact, one rule

Status and counts are computed once in Go. The CLI and web client render the same `SessionHealth` result and do not recreate classification rules.

### Quiet by default

The Dock entry always exists, but it shows a badge only when at least one signal is inferred, unavailable, or failed to compute. A lack of evidence is not styled as an agent error and does not use the existing red error treatment.

## User experience

### Dock entry

Add a session-scoped Trace Health entry to the existing registry-driven Dock. It is a compact pop, not a full-height sheet.

Badge behavior:

- all applicable signals are exact: no badge;
- at least one signal is estimated and none is unavailable or failed: quiet filled indicator;
- at least one signal is unavailable or failed: quiet hollow indicator;
- a non-applicable signal does not affect the badge.

The badge does not appear while health is loading, preventing a temporary warning flash.

### Summary view

The pop lists unavailable or failed signals first, then estimated signals, exact signals, and non-applicable signals. The four rows use plain product language:

```text
Trace health

File reads       Partly inferred
Errors           Recorded directly
Verification     Cannot fully determine
Subagents        3 exact, 1 derived, 1 missing
```

Each row expands in place. The first layer explains the result in user language. A secondary "Technical details" disclosure contains stable reason codes, counts, and affected features for adapter developers.

### Signal language

The internal quality values map to UI language as follows:

| Internal value | User-facing meaning |
| --- | --- |
| `exact` | Recorded directly |
| `estimated` | Partly inferred |
| `unavailable` | Cannot determine from this log |

Computation availability is separate from signal quality:

| Availability | Meaning |
| --- | --- |
| `ready` | The signal was classified |
| `not-applicable` | The session has no such subject, for example no recorded subagent launch |
| `failed` | Mindwalk could not compute this signal; the user may retry |

`unavailable` means the log lacks usable evidence. `failed` means Mindwalk encountered an operational error. They must never share the same message.

## Signal definitions

### File reads

Inputs:

- read targets in the normalized trace;
- `Target.Weak`, which already marks paths inferred from command text;
- `trace.stats.observability.reads`.

Outputs:

- quality and reason code;
- direct read count;
- inferred read count;
- affected features: map, reread rate, judge exploration, and judge wandering.

Classification:

- `exact`: the trace grades reads exact and has no weak read targets;
- `estimated`: the trace grades reads estimated or contains weak read targets;
- `unavailable`: the trace grades reads unavailable.

When unavailable, the copy must say that Mindwalk cannot distinguish "no reads occurred" from "the log did not expose readable file-access signals."

### Errors

Inputs:

- error events in the normalized trace;
- `trace.stats.observability.errors`.

Outputs:

- quality and reason code;
- recognized error count;
- affected features: error rate, timeline error marks, and judge verification.

Classification follows the existing observability grade. When quality is not exact, zero recognized errors must render as "No errors were recognized," never "No errors occurred."

### Verification

Inputs:

- events classified as `verify`;
- edit/verify ordering;
- adapter-owned evidence about whether each recognized verification result was structurally known or inferred/unknown.

Outputs:

- quality and reason code;
- recognized verification count;
- known result count;
- unknown result count;
- whether edits occurred after the last recognized verification;
- affected feature: judge verification.

The current exported event contract stores `isError` as a Boolean and cannot distinguish a known success from an unknown outcome that did not look like an error. V1 must not infer that distinction from `isError: false`.

Adapters therefore attach non-serialized verification evidence to the in-memory trace. This evidence is used only by `internal/health` and is excluded from exported Trace JSON. It records aggregate known/unknown outcome counts and the verification signal grade. The existing Trace schema remains version 1.

Classification:

- `exact`: recognized verification commands have structurally known outcomes;
- `estimated`: command recognition or at least one outcome depends on inference;
- `unavailable`: the source carries no usable verification signal.

### Subagents

Inputs:

- the root-scoped `AgentGraph`;
- graph load failure, if any.

Outputs:

- exact-link count;
- derived-link count;
- missing-trace count;
- unavailable-trace count;
- affected feature: Agent Lens.

Classification:

- `not-applicable`: no subagent launch is recorded;
- `exact`: every child has an exact link and an available trace;
- `estimated`: at least one child is derived, missing, or unavailable, while the graph remains usable;
- `failed`: the graph could not be built.

A mixed graph stays usable and reports its counts. One missing child must not make the entire signal `unavailable`.

## Architecture

### Data flow

```text
Raw Claude/Codex session logs
  -> adapters
       -> Trace + non-serialized verification evidence
       -> AgentGraph
  -> internal/health.Build(trace, graph, graphError)
       -> SessionHealth
            -> GET /api/sessions/{key}/health
            -> mindwalk health <session>
            -> mindwalk health <session> --json
```

### Ownership

Create `internal/health` as a pure derived layer. It may read a Trace, an optional AgentGraph, and an AgentGraph error. It must not:

- read raw session files;
- scan directories;
- mutate Trace or AgentGraph;
- call an LLM;
- write a cache or report;
- depend on web presentation code.

Adapters remain responsible for source-specific interpretation. `internal/health` owns only cross-source classification and aggregation.

### Why SessionHealth is separate from Trace

Subagent quality belongs to AgentGraph, not an actor-scoped Trace. Adding all health data to Trace would blur the existing artifact boundary and make child traces ambiguous. SessionHealth is a deterministic view over existing artifacts, not a fourth persisted artifact.

### Why classification is not computed in React

Keeping rules in Go gives the CLI, API, tests, and web client one answer. React maps stable statuses and reason codes to product language; it does not decide quality.

## Contract

Add a versioned `model.SessionHealth` contract and a mirrored `schema/session-health.schema.json`.

The builder also emits an optional top-level `badge` (`estimated` or `limited`). The web client renders this value directly; it does not repeat the aggregation rule. The field is absent when every applicable signal is exact.

The JSON shape is fixed by signal rather than using an untyped map:

```json
{
  "version": 1,
  "sessionKey": "codex-abc",
  "badge": "estimated",
  "signals": {
    "reads": {
      "availability": "ready",
      "quality": "estimated",
      "reason": "some-reads-inferred-from-shell",
      "directCount": 18,
      "inferredCount": 12,
      "affects": ["map", "reread-rate", "judge-exploration", "judge-wandering"]
    },
    "errors": {
      "availability": "ready",
      "quality": "exact",
      "reason": "structured-error-status",
      "recognizedCount": 0,
      "affects": ["error-rate", "timeline-errors", "judge-verification"]
    },
    "verification": {
      "availability": "ready",
      "quality": "estimated",
      "reason": "some-verification-results-unknown",
      "recognizedCount": 4,
      "knownResultCount": 3,
      "unknownResultCount": 1,
      "editsAfterLastVerify": 0,
      "affects": ["judge-verification"]
    },
    "subagents": {
      "availability": "ready",
      "quality": "estimated",
      "reason": "mixed-agent-link-quality",
      "exactCount": 3,
      "derivedCount": 1,
      "missingTraceCount": 1,
      "unavailableTraceCount": 0,
      "affects": ["agent-lens"]
    }
  }
}
```

Reason codes are stable machine values. User-facing prose lives in the web client and CLI formatter. V1 reason codes cover only the classification cases defined in this document; adapters must not return arbitrary prose.

## HTTP API

Add:

```text
GET /api/sessions/{sessionKey}/health
```

Behavior:

- 200 with `SessionHealth` when the trace loads;
- 404 for an unknown session;
- a graph failure still returns 200, with only the subagent signal marked `failed`;
- health calculation does not start a judge, write a report, or block the normal session snapshot endpoint;
- a fresh Rescan invalidates/reloads Trace and AgentGraph through their existing paths, then recomputes health;
- V1 does not poll or live-refresh health.

The endpoint computes from the stable Trace and AgentGraph snapshots available for the selected session. SessionHealth has no independent long-lived or disk cache.

## CLI

Add:

```text
mindwalk health <session>
mindwalk health <session> --json
```

Default output is stable, human-readable text. `--json` emits only the API-compatible JSON object on stdout. Progress and diagnostic messages, if any, go to stderr.

The CLI attempts source-appropriate adjacent-session discovery for AgentGraph. If the input is an isolated session file and the required catalog cannot be established, the subagent signal reports that the inspection context is unavailable; it must not report child traces as missing.

The command is local-only and never calls the judge or any network service.

## Refresh and failure isolation

- Initial health loading does not block the map or timeline.
- Health remains stable until the next explicit Rescan in V1.
- Rescan reloads the underlying Trace and AgentGraph before recomputing health.
- A failed AgentGraph load affects only the subagent row.
- A failed health request does not clear or replace the active session.
- The Dock offers a health-only retry after request failure.
- A single signal computation failure is represented as `availability: failed`; other signals remain usable.

`health.Build` must be safe for concurrent calls and must not mutate shared cached inputs.

## Judge relationship

V1 explains evidence quality but does not change judge prompts, findings, or verdict rollup. Existing judge behavior remains the source of truth for evaluation.

Future work may allow the judge rollup to consume richer verification health, but that requires a separate design because it changes evaluation semantics and cached report validity.

## Validation

### Core health tests

Use table-driven unit tests covering all three qualities plus `not-applicable` and `failed` availability. Required cases include:

- direct reads only;
- mixed direct and weak reads;
- unavailable read signal;
- exact, estimated, and unavailable error signals;
- zero recognized errors under an estimated signal;
- all verification outcomes known;
- mixed known and unknown verification outcomes;
- edits after the last verification;
- no usable verification signal;
- no subagent launches;
- all exact child links;
- mixed exact, derived, missing, and unavailable children;
- AgentGraph failure;
- proof that Build does not mutate either input.

### Adapter tests

For Claude and Codex, add fixtures that prove verification result evidence is classified from source semantics rather than `isError: false`. Include a known success, explicit failure, and unknown outcome where the source format permits it.

### Contract tests

Validate representative exact, estimated, unavailable, not-applicable, and failed SessionHealth payloads against `schema/session-health.schema.json`.

### Server tests

Cover success, unknown session, graph failure isolation, Rescan freshness, concurrent requests, and proof that the endpoint neither starts a judge nor writes a report.

### CLI tests

Cover stable human output, JSON-only stdout, schema-valid JSON, isolated input handling, unknown session errors, and absence of raw task text or tool output in the result.

### Browser tests

Add deterministic Playwright cases for:

- exact health with no badge;
- estimated health with a quiet badge and expanded counts;
- unavailable health sorted first without error styling;
- technical details collapsed by default;
- subagent-only computation failure with retry while map and timeline remain usable;
- Rescan refresh;
- zero console errors.

### Full verification

Before completion, run targeted unit tests, adapter and server race tests, `go test ./...`, the frontend build, Playwright, `make test`, a production-binary CLI smoke test, and the embedded-asset drift check.

## Security and privacy

SessionHealth contains counts, enums, stable reason codes, and affected feature identifiers. It must not contain:

- raw task wording;
- raw tool input or output;
- source log lines;
- arbitrary error text containing session contents;
- new file paths beyond identifiers already required by the surrounding session request.

No Health operation may send data off machine.

## V1 non-goals

- no health percentage or leaderboard;
- no LLM call;
- no judge behavior change;
- no persisted Health report or independent cache;
- no polling or live follow;
- no raw-log line coverage or unknown-record inventory;
- no automatic environment repair;
- no batch session scanning;
- no broad adapter refactor;
- no new localization system;
- no claim that missing evidence proves missing agent behavior.

## Follow-up phases

After V1 is validated in real Claude and Codex sessions, a separate proposal may add raw parser coverage: total records, recognized records, malformed records, and unsupported tool/event shapes. A later adapter compatibility suite may use those diagnostics across harness versions. Neither is required for V1.
