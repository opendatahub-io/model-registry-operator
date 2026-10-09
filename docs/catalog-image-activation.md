# Catalog image activation handoff

Activation-dependent readiness is deferred until RHOAIENG-97414 supplies genuine
runtime outcomes and backend serving enforcement. This PR resolves independent
catalog/benchmark selections, watches ImageStreams, reports selection/import
failures, requests deployment scale-down as temporary containment, and propagates
current Catalog readiness through AIHub to DSC.

Current `Ready` and `Available` require successful image resolution and workload
health. Empty selections retain product release defaults, including upstream
development tags. No runtime activation success is fabricated, and no missing
activation reporter prevents the existing release-default path from becoming ready.
The code does not implement `status.imageUpdate`, persisted activation attempts,
runtime outcome transport, atomic activation, or a backend availability gate.

The contract below describes future combined integration requirements, not APIs
already implemented by this PR. Activation-based readiness must land together with
its runtime producer and gate; do not enable it before that integration exists.

## Candidate and outcome contract

Resolve both selections into immutable references before producing an activation
candidate. Different repositories, digests, and selection modes are supported.
Release tags need trustworthy resolution evidence. Never substitute historical
imports for a failed current stable import; unrelated imports cannot invalidate pins.

Persist Catalog UID/generation, an opaque attempt ID, requested selections, both
immutable image references, and release or ImageStream UID/tag/import evidence.
Publish current intent before applying the candidate workload and correlate the
workload and runtime report with that attempt and both references.

Meaningful selection, resolved-image, and relevant import-outcome changes create
new attempts. Failure followed by success creates a new attempt even for the same
digest. Ordinary resyncs, resource versions, timestamps, unrelated tags, and
successful unchanged imports must not create attempts. Non-image Catalog edits
must preserve valid evidence for unchanged image intent.

Use one typed outcome with Catalog UID, attempt ID, both references when resolved,
state (`Pending`, `Succeeded`, `Failed`), stage, stable reason, and actionable
message. The operator produces Selection/Import outcomes; a trusted runtime adapter
produces Pull/Initialization/Loading/Activation outcomes. Accept activation success
only at Activation after both datasets are active.

Select authenticated runtime transport during integration; do not assume the runtime
has Catalog status write access. Keep records separate and patch only owned fields
with optimistic concurrency. Recheck intent after conflicts. Late reports for a
superseded attempt, another Catalog UID, or a different image pair cannot affect
readiness, serving, or last-success data.

## Shared readiness and serving gate

Extend the existing single Catalog reducer to require current selection success,
matching current-attempt activation success, and workload health. The Catalog watch
must continue propagating confirmed failures through AIHub to DSC without clearing
unrelated component failures.

Resolution permits activation but does not clear a confirmed failure. Missing,
pending, stale, or mismatched evidence cannot establish recovery. Update last-success
metadata only after successful atomic activation; it is recovery material, never
serving permission. Define timeouts and terminal outcomes for pending work.

The runtime must enforce authorization for the current attempt and image pair.
Close the gate during substantive candidate transitions and confirmed failures.
Return HTTP 503 with a stable `CatalogUnavailable` code and current stage/reason
for data requests, while retaining health/status endpoints.

Default closed after restart or loss of valid current authorization. Existing
replicas, direct clients, and cached responses must not bypass the gate.
Deployment scale-down alone is insufficient. Preserve user-managed source
configuration. Stage and validate all required catalog and benchmark content
before atomically activating the complete pair and reporting success.

## Joint validation before enabling activation readiness

Activate pair A, request B, and observe progressing/unready status. Fail B's import
and verify Catalog/AIHub/DSC failure plus structured backend unavailability. Resolve
B but fail benchmark validation; deployment health and a late A success cannot
restore serving. Explicitly pin A and require activation for the new rollback
attempt before recovery. A later successful scheduled import must also recover
automatically after successful activation, without another Catalog edit.

Repeat across controller/runtime restarts, independent image pairs, same-digest
failure/recovery, cleared fields, and another component failure. Check atomicity,
retained recovery material, unchanged observations, and preserved administrator
sources. Operator tests alone cannot prove runtime availability or atomic activation.
