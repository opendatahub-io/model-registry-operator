# Model Registry Operator Environment Variables

This document describes environment variables that are injected into the model registry operator manager container at deploy time. These are set via the overlay `params.env` and the ODH operator, and are available to the manager process at runtime.

## GATEWAY_DOMAIN

**Purpose:** The cluster’s gateway/ingress domain. Used when the operator needs to build URLs that depend on the cluster domain (e.g. model registry service ingress routes, authentication callbacks, or URLs for the model registry UI and API).

**Manifest parameter:** `GATEWAY_DOMAIN` in `config/overlays/odh/params.env`. The ODH operator populates this value (e.g. from `GatewayConfig.Status.Domain`) in the `model-registry-operator-parameters` ConfigMap.

**Injected into:** The manager container (`manager`) of the model registry operator Deployment.

The value is taken from the deployment’s parameters ConfigMap and applied via Kustomize replacements so the ODH operator can override it when injecting configuration.

**How to consume:** In the operator (Go): `os.Getenv("GATEWAY_DOMAIN")` in the controller or any code that needs the gateway domain.

**Use cases:**

- **Model registry service ingress/routes:** Configuring routes or ingress that use the cluster’s gateway domain.
- **Authentication callbacks:** Setting correct callback URLs for OAuth or other auth flows that depend on the cluster domain.
- **Model registry UI and API URLs:** Generating correct URLs for the model registry UI and API endpoints when building configuration or status.

**Note:** If not set by the operator, the value is empty. Callers should treat an empty value appropriately (e.g. omit domain-dependent configuration or discover the domain at runtime where supported).
