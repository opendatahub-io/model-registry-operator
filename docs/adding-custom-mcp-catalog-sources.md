# Adding Custom MCP Catalog Sources

This guide explains how to add user-defined MCP servers to the RHOAI Model Catalog using `oc` and YAML ConfigMaps.

## Background

The model-registry-operator creates three user-managed ConfigMaps on first
reconcile and **never overwrites them afterward**:

| ConfigMap | Mount path inside catalog pod | Purpose |
|-----------|-------------------------------|---------|
| `model-catalog-sources` | `/data/user-model-sources/` | Custom model sources |
| `mcp-catalog-sources` | `/data/user-mcp-sources/` | Custom MCP server sources |
| `agent-catalog-sources` | `/data/user-agent-sources/` | Custom agent sources |

Each ConfigMap starts with an empty `sources.yaml`. Adding entries to
these ConfigMaps is safe — the operator will not overwrite your changes.

The operator also discovers any ConfigMap in the target namespace
carrying the label `opendatahub.io/catalog-source: "true"`, mounts it
into the catalog Deployment, and passes its `sources.yaml` as a
`--catalogs-path` argument.

## Prerequisites

- A running RHOAI cluster with Model Catalog enabled
- `oc` CLI authenticated with cluster-admin or namespace-admin privileges
- The target namespace where the catalog is deployed (typically `rhoai-model-registries`)

Verify the catalog is running:

```bash
oc get pods -n rhoai-model-registries -l component=model-catalog
```

## Option A — Edit the user-managed ConfigMap

This is the simplest approach. The `mcp-catalog-sources` ConfigMap is
already mounted into the catalog pod at `/data/user-mcp-sources/`.

### 1. Prepare the MCP server data file

Create a YAML file describing your MCP servers. The format mirrors the
built-in catalogs:

```yaml
# custom-mcp-servers.yaml
source: Custom MCP
mcp_servers:
  - name: weather-mcp-server
    provider: Acme Corp
    version: "1.0.0"
    license: Apache-2.0
    description: >-
      A custom MCP server providing real-time weather data and forecasts.
    transports:
      - http
      - stdio
    deploymentMode: local
    documentationUrl: https://example.com/weather-mcp
    repositoryUrl: https://github.com/example/weather-mcp
    tools:
      - name: get_current_weather
        description: Get current weather conditions for a specific location
        accessType: read_only
      - name: get_forecast
        description: Get weather forecast for up to 5 days
        accessType: read_only
  - name: jira-mcp-server
    provider: Acme Corp
    version: "2.1.0"
    license: MIT
    description: >-
      An MCP server for interacting with Jira. Create, update, and search
      issues using natural language.
    transports:
      - http
    deploymentMode: local
    tools:
      - name: search_issues
        description: Search Jira issues using JQL
        accessType: read_only
      - name: create_issue
        description: Create a new Jira issue
        accessType: read_write
```

**Required fields** per server: `name`, `provider`, `description`, `version`.

**Optional fields**: `license`, `license_link`, `readme`, `transports`,
`deploymentMode` (`local` or `remote`), `documentationUrl`,
`repositoryUrl`, `publishedDate`, `tags`, `tools`, `logo`,
`runtimeMetadata`.

Each tool entry has: `name`, `description`, and `accessType`
(`read_only`, `read_write`, or `execute`). Tools can also include a
`parameters` list.

### 2. Update the ConfigMap

The ConfigMap needs two data keys:

- **`sources.yaml`** — a catalog source definition pointing to the data file
- **A data YAML file** (e.g. `custom-mcp-servers.yaml`) — the actual MCP server definitions

Since the ConfigMap is mounted as a volume, all data keys become files
under `/data/user-mcp-sources/`. The `yamlCatalogPath` in `sources.yaml`
is resolved relative to the directory containing `sources.yaml`, so you
can use a plain filename.

```bash
oc apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-catalog-sources
  namespace: rhoai-model-registries
  labels:
    app: model-catalog
    app.kubernetes.io/component: model-catalog
    app.kubernetes.io/created-by: model-registry-operator
    app.kubernetes.io/instance: model-catalog
    app.kubernetes.io/managed-by: model-registry-operator
    app.kubernetes.io/name: model-catalog
    app.kubernetes.io/part-of: model-catalog
    component: model-catalog
data:
  sources.yaml: |
    mcp_catalogs:
      - name: Custom MCP Servers
        id: custom_mcp_servers
        type: yaml
        enabled: true
        properties:
          yamlCatalogPath: custom-mcp-servers.yaml
        labels:
          - Custom
  custom-mcp-servers.yaml: |
    source: Custom MCP
    mcp_servers:
      - name: weather-mcp-server
        provider: Acme Corp
        version: "1.0.0"
        license: Apache-2.0
        description: >-
          A custom MCP server providing real-time weather data and forecasts.
        transports:
          - http
          - stdio
        deploymentMode: local
        tools:
          - name: get_current_weather
            description: Get current weather conditions for a specific location
            accessType: read_only
          - name: get_forecast
            description: Get weather forecast for up to 5 days
            accessType: read_only
      - name: jira-mcp-server
        provider: Acme Corp
        version: "2.1.0"
        license: MIT
        description: >-
          An MCP server for interacting with Jira.
        transports:
          - http
        deploymentMode: local
        tools:
          - name: search_issues
            description: Search Jira issues using JQL
            accessType: read_only
          - name: create_issue
            description: Create a new Jira issue
            accessType: read_write
EOF
```

### 3. Wait for the catalog pod to pick up the change

Kubernetes propagates ConfigMap volume updates automatically (typically
within 60-90 seconds). The catalog server watches for file changes and
reloads sources without needing a pod restart.

You can monitor the reload in the catalog logs:

```bash
oc logs -n rhoai-model-registries -l component=model-catalog \
  -c catalog --tail=20 -f | grep -i "custom\|mcp.*source\|loaded"
```

Look for lines like:

```
loaded MCP source custom_mcp_servers of type yaml
Loading MCP servers from source: Custom MCP Servers (id: custom_mcp_servers)
```

## Option B — Create a labeled ConfigMap

Use this approach when you want to keep custom sources separate from the
operator-managed ConfigMaps, or when you have multiple teams managing
their own sources independently.

Any ConfigMap in the catalog namespace with the label
`opendatahub.io/catalog-source: "true"` and a `sources.yaml` data key
is automatically discovered, volume-mounted, and passed as a
`--catalogs-path` argument to the catalog server.

```bash
oc apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: team-alpha-mcp-sources
  namespace: rhoai-model-registries
  labels:
    opendatahub.io/catalog-source: "true"
data:
  sources.yaml: |
    mcp_catalogs:
      - name: Team Alpha MCP Servers
        id: team_alpha_mcp_servers
        type: yaml
        enabled: true
        properties:
          yamlCatalogPath: team-alpha-mcp-data.yaml
        labels:
          - Team Alpha
    labels:
      - name: Team Alpha
        assetType: mcp_servers
        displayName: Team Alpha MCP servers
        description: MCP servers managed by Team Alpha.
  team-alpha-mcp-data.yaml: |
    source: Team Alpha
    mcp_servers:
      - name: internal-db-mcp
        provider: Team Alpha
        version: "0.5.0"
        description: Internal database query MCP server.
        transports:
          - http
        deploymentMode: local
        tools:
          - name: query
            description: Run a read-only SQL query
            accessType: read_only
EOF
```

**Constraints on the ConfigMap name:**

- Must be a valid DNS label (lowercase alphanumeric and hyphens)
- Must be at most 55 characters

**Key difference from Option A:** When using a labeled ConfigMap, the
operator reconciler detects the new ConfigMap, updates the catalog
Deployment to mount it, and triggers a pod restart. This means the
catalog pod will be recreated — expect a brief interruption (~30 seconds).

## Verifying

After the catalog pod reloads, confirm your servers appear:

```bash
# Get the catalog route
CATALOG_URL="https://$(oc get route model-catalog-https \
  -n rhoai-model-registries -o jsonpath='{.spec.host}')"
TOKEN=$(oc whoami -t)

# List all MCP servers (default page size is 10, use pageSize for more)
curl -sk -H "Authorization: Bearer $TOKEN" \
  "$CATALOG_URL/api/mcp_catalog/v1alpha1/mcp_servers?pageSize=100" \
  | python3 -m json.tool

# Filter by your custom source label
curl -sk -H "Authorization: Bearer $TOKEN" \
  "$CATALOG_URL/api/mcp_catalog/v1alpha1/mcp_servers?sourceLabel=Custom" \
  | python3 -m json.tool
```

## Removing custom sources

### Option A — Reset the user-managed ConfigMap

```bash
oc apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: mcp-catalog-sources
  namespace: rhoai-model-registries
  labels:
    app: model-catalog
    app.kubernetes.io/component: model-catalog
    app.kubernetes.io/created-by: model-registry-operator
    app.kubernetes.io/instance: model-catalog
    app.kubernetes.io/managed-by: model-registry-operator
    app.kubernetes.io/name: model-catalog
    app.kubernetes.io/part-of: model-catalog
    component: model-catalog
data:
  sources.yaml: 'mcp_catalogs: []'
EOF
```

### Option B — Delete the labeled ConfigMap

```bash
oc delete configmap team-alpha-mcp-sources -n rhoai-model-registries
```

The operator will detect the deletion, remove the volume mount from the
Deployment, and the catalog pod will restart without the removed source.

## Reference: sources.yaml format

The `sources.yaml` file supports these top-level keys:

```yaml
mcp_catalogs:           # MCP server source definitions
  - name: <display name>
    id: <unique identifier>
    type: yaml          # currently the only supported type
    enabled: true       # set to false to disable without removing
    properties:
      yamlCatalogPath: <filename.yaml>   # relative to sources.yaml dir
    labels:             # optional; used for filtering in the UI
      - <label name>

labels:                 # optional; label definitions for UI display
  - name: <label name>
    assetType: mcp_servers
    displayName: <UI display name>
    description: <short description>
```

The same file can also contain `model_catalogs` and `agent_catalogs`
sections for model and agent sources respectively.
