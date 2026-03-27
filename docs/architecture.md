# Architecture

The **model-registry-operator** is a Kubebuilder-based Kubernetes operator that deploys [Model Registry](https://github.com/opendatahub-io/model-registry) instances from `ModelRegistry` Custom Resources, and optionally a shared Model Catalog service.

## Context & Upstream Dependencies

This repo is one piece of a larger system:
- **[opendatahub-io/model-registry](https://github.com/opendatahub-io/model-registry)** — the actual Model Registry server (Go/REST). This operator deploys it via the `REST_IMAGE` container image.
- **[opendatahub-io/opendatahub-operator](https://github.com/opendatahub-io/opendatahub-operator)** — the parent ODH operator. It deploys *this* operator via the `config/overlays/odh/` kustomize overlay when `modelregistry.managementState: Managed` is set in the DataScienceCluster CR.

```mermaid
graph TD
    ODH["opendatahub-operator<br/>(DataScienceCluster CR)"]
    MRO["model-registry-operator<br/><i>(this repo)</i>"]

    ODH -->|"deploys (kustomize overlay)"| MRO
    MRO -->|watches| CR["ModelRegistry CR"]
    MRO -->|"manages (channel source,<br/>ENABLE_MODEL_CATALOG)"| CAT_DEP

    subgraph "Created per ModelRegistry CR"
        direction LR
        DB[("Database<br/>user-provided PG/MySQL<br/>or auto-deployed PG pod")]
        DEP["Deployment<br/>(REST server +<br/>kube-rbac-proxy)"]
        SUPPORT["Service · Route (OCP)<br/>ServiceAccount · Role<br/>Group (OCP) · NetworkPolicy"]
    end

    CR --> DEP
    CR --> SUPPORT
    DEP --> DB

    subgraph "Model Catalog (single shared instance, optional)"
        direction LR
        CAT_PG[("Catalog PostgreSQL<br/>(PVC-backed)")]
        CAT_DEP["Catalog Deployment<br/>(catalog service +<br/>kube-rbac-proxy)"]
        CAT_SUPPORT["Service · Route (OCP)<br/>ConfigMaps (sources)<br/>ServiceAccount · Roles<br/>NetworkPolicy"]
    end

    CAT_DEP --> CAT_PG
    CAT_DEP --> CAT_SUPPORT
```
