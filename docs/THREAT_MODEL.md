# Threat model

Mindwalk Observatory is a single-owner, loopback-only local application. Its
inputs are untrusted agent logs, explicit repository paths, explicit memory
text, URL parameters, and browser API requests. Monitored repositories and
source logs are read-only inputs.

## Trust boundaries and controls

| Boundary | Primary threats | Enforced controls |
|---|---|---|
| Browser -> local server | cross-site mutation, oversized bodies, arbitrary paths | `127.0.0.1` bind, same-origin/CSRF header, method checks, bounded JSON, registered IDs |
| Scan root -> discovery preview | implicit surveillance, source/credential reads, symlink or mount escape, repository code execution, resource exhaustion, automatic registration | explicit persisted root approval plus explicit start; protected roots and locked/additive exclusions; rooted directory-metadata walk with per-component no-symlink and same-file checks; Linux mount-ID/device and Windows volume boundaries; bounded no-config direct reads of `.git` indirection, `HEAD`, refs, packed refs, and `commondir`; no Git process; depth/directory/result/time limits; cooperative yielding; cancellation; stable preview IDs; shared CLI/server owner lock; separate final confirmation |
| Session root -> ingestion | symlink escape, malformed/oversized records, secrets, command injection | canonical configured roots, incremental size/shape validation, metadata-only quarantine, redaction, adapters never execute recorded commands |
| Canonical event -> ledger | tamper, duplicate, torn write, content capture | deterministic verification, ID dedupe, append+fsync, torn-tail quarantine, metadata-only attrs |
| Memory input -> brain | hidden writes, secret persistence, SQL/FTS injection, silent deletion | explicit owner mutation only, redaction, parameterized SQL, quoted FTS terms, corrections and tombstones in append-only JSONL |
| Optional integrations | unintended network/action, credential exposure | all disabled; dry-run/read-only contracts; no send/execute/shell implementation |
| API data -> UI/export | XSS, fabricated state, secret display | React escaping, strict CSP, redacted normalized models, provenance/quality or `UNAVAILABLE` |

## Residual risks

- A malicious local process running as the same OS user can read owner data or
  alter local files; Observatory is not a sandbox against the owning account.
- Hashes provide integrity/deduplication, not authentication against an
  attacker who can rewrite the whole local data directory.
- Very large numbers of individually valid small source records can consume
  CPU during adapter replay. Line/poll/response bounds and LOW list mode limit
  per-operation impact; long-session indexing remains measurable work.
- Directory metadata and approved canonical repository paths are themselves
  sensitive. Discovery state is private and local, but another process running
  as the same OS user can read it. Forget scan history removes the latest
  repository-only result snapshot, not the approved-root preferences.
- An approved tree can change after scanning. Final registration therefore
  repeats rooted no-symlink metadata and approved-root validation, then stores
  that exact canonical path without resolving it again; a changed or missing
  repository fails its own registration. A later same-user filesystem swap
  can make the stored path invalid, but cannot substitute an outside path in
  the registry record. This is not a sandbox against a malicious same-user
  process.
- Permission and metadata errors can make a repository inaccessible or leave
  branch, HEAD, worktree, or cleanliness as `unknown`. The UI reports these
  states and warnings rather than inferring success.
- SQLite FTS shadow tables retain indexed text by design. The database lives
  under a private `0700` root; tombstone rebuild removes active search entries,
  while the append-only JSONL/Markdown history intentionally preserves the
  redacted tombstone chain.

No claim is made about hidden model reasoning, private chain-of-thought,
external runtime control, or memory retrieval being model training.
