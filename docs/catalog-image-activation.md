# Operator-owned Catalog image validation

RHOAIENG-97414 builds on the independent image selections in operator PR #722.
ImageStream selection, rollout, attempt tracking, retry, and failure policy belong
in the operator. Model-registry consumes mounted catalog and benchmark files
through its existing configuration. It receives no activation flags, attempt
identity, stable-channel information, or report-writing responsibility.
There is no companion runtime change or additional HTTP proxy to ship.

The optional `spec.catalogDataImage` and `spec.benchmarkDataImage` fields are
deprecated and retained so existing manifests continue to pass validation.
Their values are preserved but ignored by the controller. Use
`spec.catalogDataImageStream` and `spec.benchmarkDataImageStream` to select images
with the choices described below.

## Deployment flow

1. Resolve the catalog and benchmark selections independently. Empty fields use
   their release images; `stable` follows the relevant managed ImageStream; a
   SHA-256 selection pins that image's configured repository. Release tags remain
   supported for upstream development, as in #722.
2. Persist an opaque `status.activation.attemptID` before changing the deployment
   when selections, references, relevant import outcomes, runtime/operator image,
   or explicit retry token change. Repeated imports of an unchanged healthy pair
   preserve the attempt.
3. Use a `Recreate` deployment so the old catalog writer stops before the new
   pair starts. Both copy init containers must succeed. The benchmark image must
   contain `/app/benchmarks`, but that directory may be empty. Older release
   images lack the serving-runtime catalog;
   empty Catalog image selections and `stable` imports from the release source
   (or of its known digest) retain the copy init container's empty serving-runtime
   list when that file is absent. Migration to ImageStreams therefore preserves
   release-image compatibility. Other selections must contain the file and fail
   preflight when it is missing. A shipped serving-runtime file is never replaced.
4. Run `/manager catalog-content-validate` from the operator image as a third init
   container. It reads shipped source YAML and benchmark files before the catalog
   process starts. It makes no database or Kubernetes API calls. On failure, it
   writes a bounded JSON termination message with a stage, reason, and safe
   summary. Raw source data and container output are not copied into Catalog
   status or Warning events.
5. Start the unchanged catalog with its existing listener, catalogs-path,
   performance-metrics, PostgreSQL settings, and readiness probe. Administrator
   source ConfigMaps remain mounted and preserved.
6. Require successful validator termination in a current-attempt pod, the selected
   image pair, a Ready catalog container, and a completed deployment rollout
   before recording `WorkloadValidated` and restoring aggregate readiness.

## Structural validation

Enabled shipped model, MCP, agent, and serving-runtime YAML sources referenced by
`default-catalog-sources` are checked for syntax, required lists, non-null entries,
identities, duplicate names (MCP name plus version), model artifact types and URI
schemes, MCP tools/endpoints, runtime version/image fields, and embedded template syntax.
Empty lists are valid; missing required files, duplicate YAML keys, and multiple
YAML documents are rejected. Enabled shipped skill sources are rejected until a
corresponding contract exists. Administrator sources retain runtime validation.

Model artifacts accept `model-artifact` and `metrics-artifact`. Omitted, null, or
empty types use the runtime's legacy model-artifact default and still require a
valid URI. Unknown types and non-string values fail preflight with
`CatalogContentInvalid`; metrics artifacts do not require a URI.

An empty benchmark directory is valid, and individual models may have no
benchmark records. When present, benchmark checks require unique
case-insensitive model IDs in metadata and JSON object records in known NDJSON
files. Records require adjacent model metadata and are interpreted by the runtime
using it. This is a structural file check,
not a duplicate implementation of every catalog business rule or a PostgreSQL
connectivity check. Existing runtime validation remains responsible for ingestion
semantics.

## Failure and recovery

The controller observes pull, copy, validator, and catalog startup failures.
Kubernetes keeps retrying `ErrImagePull` and `ImagePullBackOff` during the
activation deadline. A catalog process restart can also recover; a prior
termination does not fail a running, ready container. An invalid image name,
copy or validation error, failed pod, or exhausted retry deadline records a
failure, emits a deduplicated Warning, makes Catalog unready, and scales the
deployment to zero. A recorded failure is not cleared by deletion of its pod,
replacement containers, old ready endpoints, or controller restart. Failed
attempts remain scaled down across reconciliation; there is no automatic
fallback rollout.

The shared readiness reducer propagates Catalog failure to AIHub and the existing
parent DSC aggregation path. A pending attempt has a ten-minute validation/startup
deadline. A previously successful attempt gets a ten-minute recovery window
when its pod encounters a retryable pull or process restart. Brief returns to
Ready do not reset that deadline, so repeated restarts eventually fail. Ten
uninterrupted healthy minutes reset the recovery window for a later fault.
Successful image import alone does not prove workload validation.

Correcting the selection, a new successful relevant import, or changing the retry
annotation creates a new attempt. It must pass validation and workload readiness.
To retry unchanged references after correcting credentials or an external problem:

```sh
oc annotate catalog catalog -n <namespace> \
  aihub.opendatahub.io/retry-activation=retry-2 --overwrite
```

`lastSuccessful` records references and the observation time of the previous
successful workload. It is recovery metadata, not a database snapshot. A release
tag is not an immutable digest; pin published digests for reproducible rollback.

## Scope and acceptance limits

- No extra serving proxy, runtime report ConfigMaps, Catalog access permissions,
  PostgreSQL schema changes, transaction markers, or operator-aware runtime code
  are introduced. Services, routes, and the existing authentication proxy keep
  their normal request path.
- Scale-down and endpoint removal are asynchronous. Availability follows ordinary
  Kubernetes workload termination and routing; this change does not promise an
  immediate per-request gate or a custom JSON 503. Unavailability responses come
  from the existing Service/ingress. No dashboard-specific change is included.
- Successful preflight plus runtime/deployment readiness is **workload evidence**,
  not an ingestion revision acknowledgement. Runtime loaders remain asynchronous;
  this work does not prove exact candidate record/benchmark completion, transaction
  atomicity, retained datasets, or atomic database rollback. Such generic runtime
  guarantees, if required separately, belong upstream and must not mention
  operators or ImageStream channels.
- `RELATED_IMAGE_ODH_MODEL_REGISTRY_OPERATOR_IMAGE` must reference an operator
  release containing the validation command. It is wired alongside the manager
  image in the standalone Catalog and ODH overlays and already supplied by AIHub.
  No coordinated model-registry release is required.
- Local envtest exercises watches and persisted Kubernetes state. Live OpenShift
  pulls, credentials, SCCs, Routes, and the full Catalog -> AIHub -> DSC integration
  remain cluster acceptance checks.

The runtime implementation in model-registry PR #1914 is superseded and removed.
Generic catalog defects, if independently demonstrated, belong upstream in
Kubeflow Hub; this operator feature needs no downstream runtime patch.
