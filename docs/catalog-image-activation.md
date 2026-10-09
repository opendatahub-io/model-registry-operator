# Catalog image activation handoff

RHOAIENG-97413 supplies selection/import resolution, persisted attempt identity,
the shared outcome contract, and the Catalog readiness reducer. RHOAIENG-97414
must supply genuine runtime activation outcomes and the backend availability gate.
Deployment scale-down is temporary containment and does not complete that gate.

## Current attempt

`Catalog.status.imageUpdate.currentAttempt` contains Catalog UID/generation,
an opaque attempt ID, the independent requested selections, a selection outcome,
and the resolved catalog/benchmark pair when both references are immutable.
Each resolved image records its mode, reference, digest, and release or ImageStream
source evidence. Different images and different selection modes are supported.

Attempt IDs survive resyncs and controller restarts. Meaningful selection,
repository/digest, source UID/generation, and import-outcome changes create new
attempts. Failed import followed by success creates a new attempt even for an
unchanged digest. Resource versions, import timestamps, unrelated tags and
ordinary successful imports of unchanged images do not create new attempts.
Catalog edits unrelated to data images update observed generation without
discarding valid activation of the same attempt.

The operator persists the attempt and unready status before applying the workload.
The pod template annotation `aihub.opendatahub.io/data-image-attempt` identifies
the candidate. Runtime environment variables are `CATALOG_IMAGE_ATTEMPT_ID`,
`CATALOG_CR_UID`, `CATALOG_DATA_IMAGE_REFERENCE`, and
`CATALOG_BENCHMARK_IMAGE_REFERENCE`. An unresolved selection is not a candidate;
mutable development release tags cannot supply trusted activation evidence.

## Outcome ownership and transport

The operator owns `currentAttempt.selectionOutcome`, `lastFailure`,
`lastSuccessfulActivation`, and aggregate conditions. A trusted runtime adapter
owns `imageUpdate.activationOutcome`. The runtime is not granted Catalog status
write access by this change. 97414 must implement the adapter and its authenticated
runtime transport; it may not infer success from deployment health or imports.

An activation outcome has this shape:

```yaml
catalogUID: <current Catalog UID>
attemptID: <current persisted attempt ID>
images:
  catalog: repository@sha256:<catalog digest>
  benchmark: repository@sha256:<benchmark digest>
state: Succeeded
stage: Activation
reason: Activated
message: Both required datasets were validated and atomically activated
```

The same outcome type accepts `Pending` and `Failed` for `Pull`, `Initialization`,
`Loading`, and `Activation`. Successful pulling or loading is not successful
activation: `Succeeded` is accepted only at `Activation`. Every runtime outcome,
including failures, must identify both immutable references and the current UID
and attempt. `CurrentActivation` validates these bindings and current selection
intent before the reducer accepts an outcome.

Writers must patch only their owned fields, with optimistic concurrency. Conflicts
require a fresh read and rechecking current intent before retrying. Controller
reconciliation retries conflicts without overwriting activation-producer fields.
Late results from superseded attempts or another Catalog UID cannot change
readiness or last-success metadata. A previous success is recovery material, not
permission to serve another attempt.

## Readiness and required runtime gate

One reducer owns `Ready` and `Available`. It requires current selection success,
current pair activation success, and workload/resource health. Resolution permits
the candidate to start; it does not clear aggregate failure. A confirmed failure
remains degraded during pending recovery and clears only after accepted activation.
The owned Catalog watch carries readiness into AIHub's `CatalogDataReady`, and
existing parent module aggregation carries AIHub readiness into DSC status.

97414 must enforce serving permission in the backend for the current attempt and
image pair. Close the gate during substantive candidate transitions and confirmed
failures. Return HTTP 503 with a stable `CatalogUnavailable` code and current
stage/reason for Catalog data requests while retaining health/status endpoints.
Use bounded revocation and default closed after restart or loss of valid current
authorization. Existing replicas, direct clients, and cached responses must not
bypass the gate. Preserve user-managed source definitions.

The adapter must stage and validate both required datasets, activate them together,
and report success only when that complete pair is active. It must provide defined
timeouts and terminal outcomes so pending activation cannot hide a permanent
failure. An unchanged healthy digest pair retains its attempt and activation.
No runtime producer, backend gate, atomic activation, or structured unavailable
response is implemented here. Until 97414 provides these, the combined feature
is incomplete and Catalog/AIHub stay unready without trustworthy activation evidence.

## Joint validation

Activate pair A, then request B and observe progressing/unready status. Fail B's
import and verify Catalog/AIHub/DSC failure plus backend unavailability. Resolve B
but fail benchmark validation; neither deployment health nor a late A success may
restore serving. Pin A explicitly and require activation for the new rollback
attempt before recovery. Repeat across controller/runtime restarts, separate-image
pairs, same-digest import recovery, cleared fields, and another component failure.
Verify source configuration survives and last-success metadata changes only after
accepted activation. Backend unavailability and atomicity require 97414 integration
tests; operator status tests alone do not prove these runtime properties.
