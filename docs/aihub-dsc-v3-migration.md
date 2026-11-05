# AI Hub and Model Registry configuration in ODH 3.6

In Open Data Hub (ODH) 3.6, the DataScienceCluster (DSC) v3 API groups Model
Registry and the model catalog under `spec.components.aiHub`. This guide also
applies to Red Hat OpenShift AI (RHOAI) 3.6. Use it to configure AI Hub or update
the AI Hub portion of a saved DSC v2 manifest before applying it.

The upgrade automatically converts the existing DSC to v3, including its Model
Registry configuration. No manual changes to the live DSC are needed for this
conversion. Update your saved manifests and configuration tools to use v3.

## Configure AI Hub

The following DSC v3 example enables AI Hub in the default ODH namespace,
`odh-model-registries`:

```yaml
apiVersion: datasciencecluster.opendatahub.io/v3
kind: DataScienceCluster
metadata:
  name: default-dsc
spec:
  components:
    aiHub:
      managementState: Managed
      instancesNamespace: odh-model-registries
```

This example shows only the AI Hub configuration. When updating an existing DSC,
keep its actual name and the configuration for its other components.

| Field under `spec.components.aiHub` | Behavior |
| --- | --- |
| `managementState: Managed` | Enables AI Hub; the operator installs and manages its services. |
| `managementState: Removed` | Disables AI Hub; the operator removes its managed services. |
| `instancesNamespace` | Selects the namespace for Model Registry instances and the model catalog service. |

The supported management states are `Managed` and `Removed`. Omitting AI Hub or
its management state leaves it disabled. A namespace value alone does not enable
AI Hub.

## Preserve the instances namespace

The default namespace has not changed in v3: ODH uses `odh-model-registries`,
and RHOAI uses `rhoai-model-registries`. Omitting `instancesNamespace` uses the
distribution's default.

Automatic conversion preserves a configured namespace. When updating a saved
manifest, rename `registriesNamespace` to `instancesNamespace` and keep its value.
If you previously omitted the namespace to use the default, you can keep omitting
it in v3.

The namespace cannot change while AI Hub remains `Managed`. Migrating to v3 does
not require changing namespaces or disabling and re-enabling AI Hub.

## DSC v2 to v3 field mapping

The following DSC v2 example is equivalent to the v3 example above:

```yaml
apiVersion: datasciencecluster.opendatahub.io/v2
kind: DataScienceCluster
metadata:
  name: default-dsc
spec:
  components:
    modelregistry:
      managementState: Managed
      registriesNamespace: odh-model-registries
```

Automatic conversion maps these fields from v2 to v3:

| DSC v2 | DSC v3 |
| --- | --- |
| `spec.components.modelregistry.managementState` | `spec.components.aiHub.managementState` |
| `spec.components.modelregistry.registriesNamespace` | `spec.components.aiHub.instancesNamespace` |
| `status.components.modelregistry.managementState` | `status.components.aiHub.managementState` |
| `status.components.modelregistry.registriesNamespace` | `status.components.aiHub.instancesNamespace` |
| `status.components.modelregistry.releases` | `status.components.aiHub.releases` |
| `ModelRegistryReady` in `status.conditions` | `AIHubReady` in `status.conditions` |

Management state, explicit namespace values, and component release information
are preserved. Update scripts that read status fields or condition names to use
the v3 paths and `AIHubReady` condition.

In a v3 manifest, use the exact field name `aiHub` and the namespace field
`instancesNamespace`. Replace the v2 `modelregistry` and `registriesNamespace`
fields with these names in your saved manifests.

## Update a saved DSC v2 manifest

These steps update the manifest you maintain in source control or configuration
tools. The live DSC is converted automatically during the upgrade; you do not
need to retrieve it through the v2 API. These steps cover the AI Hub fields;
migrate other components using their DSC v3 guidance as needed.

1. In your saved DSC manifest, change `apiVersion` from
   `datasciencecluster.opendatahub.io/v2` to
   `datasciencecluster.opendatahub.io/v3`.

2. Under `spec.components`, rename `modelregistry` to `aiHub`. Within that block,
   rename `registriesNamespace` to `instancesNamespace` if present. The examples
   above show the before and after configuration.

3. Keep the same `metadata.name`, management state, and namespace value. If the
   namespace was omitted to use the default, leave it omitted. Preserve unrelated
   component settings using their v3 equivalents.

4. Once ODH 3.6 or RHOAI 3.6 is installed, apply the updated manifest through your
   normal configuration workflow. For example, if your manifest is `dsc.yaml`:

   ```bash
   oc apply -f dsc.yaml
   ```

These field changes update the configuration of the existing DSC. No recreation
of registry instances or relocation of registry data is required.

## References

- [RHOAIENG-98668: AI Hub and Model Registry documentation for DSC v3](https://redhat.atlassian.net/browse/RHOAIENG-98668)
- [AI Hub public API contract](https://redhat.atlassian.net/browse/RHOAIENG-95340)
- [ODH namespace configuration](https://github.com/opendatahub-io/opendatahub-operator/blob/main/api/components/v1alpha1/aihub_types.odh.go)
- [ODH DSC v2 ↔ v3 conversion](https://github.com/opendatahub-io/opendatahub-operator/blob/main/api/datasciencecluster/v2/datasciencecluster_conversion.go)
