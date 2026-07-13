# THIRD PARTY NOTICES

## Upstream foundation

This project is derived from **mindwalk** by Ricko Yu (cosmtrek):

- Repository: https://github.com/cosmtrek/mindwalk
- License: MIT — `Copyright (c) 2026 Ricko Yu` (preserved verbatim in
  [`LICENSE`](LICENSE))
- Upstream commit this work builds on:
  `97a543c2272b38cb5b8ea9b1b067b21e8ac039cb` (`upstream/master`,
  `git describe`: `v0.1.0-8-g97a543c`)
- Upstream tag `v0.1.0`: `d7a31c4109ab6e9beb193371a12d9d4c6d39ac03`

Eric Martin's Observatory extensions will be clearly marked in their own
packages/files and documented in `docs/PROVENANCE.md`. The upstream copyright
notice is never removed.

## Go dependencies

Observatory adds one direct Go dependency:

| Package | Version | License | Purpose |
|---|---|---|---|
| modernc.org/sqlite | 1.53.0 | BSD-3-Clause | CGO-free local SQLite/FTS5 index |

The dependency graph contains 24 additional pinned modules from
`github.com/dustin`, `github.com/google`, `github.com/hashicorp`,
`github.com/mattn`, `github.com/ncruces`, `github.com/remyoudompheng`,
`golang.org/x`, and `modernc.org`. Installed license files are permissive
BSD/MIT/Go-style licenses. Exact package URLs and versions are recorded in
`docs/sbom.cdx.json`; `go.sum` is the integrity lock.

## Frontend (npm) direct dependencies

Versions and licenses read from the installed packages on 2026-07-13
(lockfile-driven `npm ci`):

| Package | Version | License |
|---|---|---|
| react | 19.2.7 | MIT |
| react-dom | 19.2.7 | MIT |
| three | 0.182.0 | MIT |
| vite | 7.3.6 | MIT |
| @vitejs/plugin-react | 5.2.0 | MIT |
| zustand | 5.0.14 | MIT |
| lucide-react | 0.561.0 | ISC |
| @fontsource-variable/fraunces | 5.2.9 | OFL-1.1 |
| @fontsource-variable/schibsted-grotesk | 5.2.8 | OFL-1.1 |
| typescript (dev) | 5.9.3 | Apache-2.0 |
| playwright (dev) | 1.61.1 | Apache-2.0 |
| pngjs (dev) | 7.0.0 | MIT |
| @types/node (dev) | 24.13.3 | MIT |
| @types/pngjs (dev) | 6.0.5 | MIT |
| @types/react (dev) | 19.2.17 | MIT |
| @types/react-dom (dev) | 19.2.3 | MIT |
| @types/three (dev) | 0.181.0 | MIT |

All permissive (MIT / ISC / Apache-2.0 / SIL OFL 1.1 for font files). No
copyleft obligations. Transitive dependencies are pinned by
`web/package-lock.json`; `docs/sbom.cdx.json` is the combined CycloneDX 1.5
Go/npm inventory.
