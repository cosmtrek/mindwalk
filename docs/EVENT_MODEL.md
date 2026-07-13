# EVENT MODEL

Status: `internal/event` and its P3 ingestion path are IMPLEMENTED + TESTED,
including the append-only ledger, deterministic projector replay,
root-confined application opener `OpenLogAt`, Claude/Codex normalization,
durable SSE cursor, and provenance API/UI.

## EventEnvelope v1

Defined in `internal/event` and mirrored by `schema/event.schema.json` +
`schema/provenance.schema.json`. Wire format follows the repo's existing
camelCase convention (the master prompt names the same fields in snake_case;
the mapping is 1:1, e.g. `source_type` → `sourceType`, `raw_event_hash` →
`rawEventHash`, `occurred_at` → `occurredAt`).

| Field | Notes |
|---|---|
| `schemaVersion` | const `1` |
| `eventId` | `ev1_` + first 32 hex chars of `normalizedEventHash`; derived, never an input |
| `eventType` | one of 51 canonical types (see `AllTypes` in `event.go` / the schema enum) |
| `occurredAt`, `observedAt` | RFC3339, canonical UTC (RFC3339Nano spelling) |
| `seq` | non-negative source-stream sequence |
| `repoId`, `sessionId`, `agentId`, `parentAgentId`, `parentEventId` | optional pointers — never-recorded (absent) is distinguishable from recorded-empty (`""`) |
| `attrs` | normalized **metadata only** (tool name, path token, exit status, counts) — never message or file content; content capture stays off by default |
| `redactedFields` | names of fields withheld by redaction policy |
| `provenance` | see below |
| `normalizedEventHash` | SHA-256 hex over the canonical serialization |

The SSE/API transport uses the envelope's immutable one-based ledger position
as `sequence`. That durable global cursor is intentionally separate from
`EventEnvelope.seq`, which retains source semantics. Session IDs in canonical
events use the adapter's stable path-derived session key, avoiding collisions
when multiple Codex rollout files share one display ID.

### Provenance

`sourceType` (required), `sourceName`, `sourceEventId`, `rawEventHash`
(SHA-256 of the raw source record — the raw content itself is **never stored**
in an envelope), `quality` (required), `fieldQuality` (per-field overrides),
`confidence` ([0,1]), `explanation` (**required** when quality is `derived`).

Rejected or torn ledger lines are not copied into the dead-letter file.
Quarantine stores the observed time, reason, byte count, and SHA-256 of the
rejected bytes, preserving evidence without durably copying possible secrets.

### Quality vocabulary

`exact | estimated | derived | unavailable | redacted` — the first three
values extend the vocabulary `internal/model` already uses for
`Stats.Observability`.

## Identity and canonical serialization

- **Canonical form:** compact `encoding/json` output of the envelope with
  `eventId` and `normalizedEventHash` cleared. Struct fields serialize in
  declaration order; Go sorts map keys, so attr/fieldQuality insertion order
  can never change identity. Timestamps are normalized to UTC RFC3339Nano
  before hashing, so timezone spelling can't either.
- **Hash:** SHA-256 over the canonical bytes. **ID:** `ev1_` + first 128 bits
  of the hash, hex.
- `Finalize` normalizes, validates, and computes identity; it is idempotent.
  `Verify` recomputes identity and fails with `ErrIdentity` on tamper.
- A golden test (`TestGoldenIdentity`) pins the serialization contract; if it
  breaks, stored event identity breaks, so treat a golden change as a schema
  migration, not a test fix.

## Validation

`Validate` fails closed with `ErrInvalid` on: wrong schema version; unknown
event type or quality; missing/garbage/non-UTC timestamps; negative sequence;
malformed `eventId` / hashes; empty attr keys or redacted-field entries;
missing `sourceType`; out-of-range confidence; `derived` without explanation.

## JSON Schema note

The Go validator is the enforced contract; the JSON Schemas mirror it for
external consumers, matching how upstream treats `trace`/`citymap` schemas.
The schemas describe **finalized** envelopes (identity fields required);
`Validate` also accepts pre-finalize envelopes, which is internal state only.
No JSON-Schema validation library is added. The separate P6 brain index adds
the pinned `modernc.org/sqlite` driver; event validation remains standard
library code and does not depend on SQLite.
