# Local performance and LOW profile

The LOW profile is selected with `?profile=low` or the **List** view. It renders
an accessible 2D file list and does not mount a WebGL scene. All data remains
real; the profile changes rendering cost, not event or repository semantics.

Performance is measured, not promised. The P9 browser proof records Chromium
DOM-content-loaded time and the local Go server's Linux `VmRSS` for a tiny
synthetic two-session repository. Those fixture numbers are a smoke baseline,
not a capacity claim for large repositories or sessions. Re-run
`npm --prefix web run test:e2e:live` on this laptop after material renderer,
ingestion, or dependency changes and update the checkpoint evidence.

Reduced-motion media preference collapses animations to effectively zero;
keyboard activation, named regions, the List view, memory search, review
center, provenance inspector, and live status are covered by the browser flow.
