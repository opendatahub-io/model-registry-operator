# Catalog activation failures: first implementation pass

This branch starts from PR #722 at `7e3f3449d8fe73cae351ab929d56052813dd609e`.
It adds the failure-reporting and required-content initialization portion of
RHOAIENG-97414. It is not the complete no-fallback implementation.

## Implemented

- Benchmark copying no longer ignores failures with `|| true`; both init
  containers expose failure logs through Kubernetes termination messages.
- The Catalog runtime receives required shipped-source and benchmark validation
  flags. Administrator source paths are not designated required or modified.
- The runtime must include the companion validation change in `model-registry`.
  It validates shipped content before database/plugin initialization and writes
  structured required-content failures to `/dev/termination-log`.
- The controller watches Catalog pod changes, examines init-container status and
  runtime termination reports, and diagnoses Pull, Initialization, Loading, and
  generic Activation/startup failures.
- A reported current-image-pair failure overrides deployment availability with
  `Available=False`, sets `Degraded=True` and `DataImageActivationFailed=True`,
  and emits a deduplicated Warning event.
- Failed pods for a different complete data-image/runtime-image combination are
  ignored. Container creation is progress; a running recovered container's old
  termination state is not reported as a new failure.

The runtime report has the following failure-only shape:

```json
{"stage":"Loading","reason":"CatalogContentInvalid","message":"..."}
```

Only the known required-content reasons are accepted as Loading reports. Other
nonzero runtime exits are reported as `CatalogStartupFailed`.

## Deliberate limits and handoff to 97413

`DataImageActivationFailed=False` means no current failure was observed. It
does not certify successful activation. Existing deployment availability is
still the legacy success signal until the activation reducer is integrated.

Image-pair matching is only a provisional failure-diagnosis filter; it cannot
distinguish two attempts that reuse the same pair. Replace that filter with
current attempt/workload correlation when 97413 supplies its persisted identity.

The branch does not independently write DSC status or change the AIHub
aggregation policy owned by 97413. AIHub must consume Catalog data readiness
before DSC propagation satisfies the agreed behavior.

No serving gate or activation-success report is implemented here. Older
replicas may remain reachable after a candidate failure. Scale-down in #722 is
still containment, not serving authorization. Database activation after valid
preflight is still asynchronous and is not made transactional by this pass.

Do not ship this as the completed feature: pair-based attempt metadata, atomic
activation evidence, runtime authorization, AIHub/DSC propagation, and user-facing
unavailability still require integration.

Deploy the companion runtime image before enabling this operator template;
existing runtime releases do not understand its added required-content flags.
