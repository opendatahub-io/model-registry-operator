# Catalog validation design revision

The runtime-dependent first pass is superseded by
[operator-owned image validation](catalog-image-activation.md).

The operator validates shipped files in an init container and controls rollout,
scale-down, diagnostics, attempts, and retry. Model-registry PR #1914's activation
flags, reports, PostgreSQL changes, and operator-aware documentation are removed.
There is no additional HTTP proxy.

Read the replacement document for structural validation scope, recovery behavior,
release image wiring, and the limits of ordinary workload readiness and routing.
