## Async Upload Template

This OpenShift Template generates the Job 'async-upload' developed in: https://github.com/opendatahub-io/model-registry/tree/main/jobs/async-upload

## Examples

### Update Artifact

```sh
oc process --local \
  -f jobs-async-upload-s3-to-oci-template.yaml \
  -p MODEL_SYNC_MODEL_UPLOAD_INTENT=update_artifact \
  -p MODEL_SYNC_MODEL_ID=1 \
  -p MODEL_SYNC_MODEL_VERSION_ID=3 \
  -p MODEL_SYNC_MODEL_ARTIFACT_ID=6 \
  -p MODEL_SYNC_REGISTRY_SERVER_ADDRESS=https://... \
  -p MODEL_SYNC_REGISTRY_PORT=443 \
  -p SOURCE_CONNECTION=my-s3-credentials \
  -p DESTINATION_CONNECTION=my-oci-credentials \
  -p MODEL_SYNC_SOURCE_AWS_KEY=path/in/bucket \
  -o yaml
```

### Create Model

```sh
oc process --local \
  -f jobs-async-upload-s3-to-oci-template.yaml \
  -p MODEL_SYNC_MODEL_UPLOAD_INTENT=create_model \
  -p MODEL_SYNC_REGISTRY_SERVER_ADDRESS=https://... \
  -p MODEL_SYNC_REGISTRY_PORT=443 \
  -p SOURCE_CONNECTION=my-s3-credentials \
  -p DESTINATION_CONNECTION=my-oci-credentials \
  -p MODEL_SYNC_SOURCE_AWS_KEY=path/in/bucket \
  -o yaml
```

### Create Version

```sh
oc process --local \
  -f jobs-async-upload-s3-to-oci-template.yaml \
  -p MODEL_SYNC_MODEL_UPLOAD_INTENT=create_version \
  -p MODEL_SYNC_MODEL_ID=1 \
  -p MODEL_SYNC_REGISTRY_SERVER_ADDRESS=https://... \
  -p MODEL_SYNC_REGISTRY_PORT=443 \
  -p SOURCE_CONNECTION=my-s3-credentials \
  -p DESTINATION_CONNECTION=my-oci-credentials \
  -p MODEL_SYNC_SOURCE_AWS_KEY=path/in/bucket \
  -o yaml
```

## Getting IDs

A custom script to get and use the IDs of the latest registered model, version, and artifact, could look something like the below. Replace `my-model-registry-namespace` and `CLUSTER-NAME` at a minimum.

```sh
#!/bin/bash
set -e

MR_NAMESPACE=my-model-registry-namespace

MR_TOKEN=$(oc whoami -t)
MR_BASE_URL="https://$(oc get route -n odh-model-registries "$MR_NAMESPACE"-https -o 'jsonpath={.status.ingress[0].host}')"

MODEL_ID=$(curl -sk -H"Authorization: Bearer $MR_TOKEN" "$MR_BASE_URL/api/model_registry/v1alpha3/registered_models" | jq -r '.items | max_by(.lastUpdateTimeSinceEpoch | tonumber) | .id')
MODEL_VERSION_ID=$(curl -sk -H"Authorization: Bearer $MR_TOKEN" "$MR_BASE_URL/api/model_registry/v1alpha3/registered_models/"$MODEL_ID"/versions" | jq -r '.items | max_by(.lastUpdateTimeSinceEpoch | tonumber) | .id')
MODEL_ARTIFACT_ID=$(curl -sk -H"Authorization: Bearer $MR_TOKEN" "$MR_BASE_URL/api/model_registry/v1alpha3/model_versions/$MODEL_VERSION_ID/artifacts" | jq -r '.items | max_by(.lastUpdateTimeSinceEpoch | tonumber) | .id')

oc process --local \
  -f jobs-async-upload-uri-to-oci-template.yaml \
  -p MODEL_SYNC_MODEL_UPLOAD_INTENT="update_artifact" \
  -p MODEL_SYNC_MODEL_ID="$MODEL_ID" \
  -p MODEL_SYNC_MODEL_VERSION_ID="$MODEL_VERSION_ID" \
  -p MODEL_SYNC_MODEL_ARTIFACT_ID="$MODEL_ARTIFACT_ID" \
  -p MODEL_SYNC_REGISTRY_SERVER_ADDRESS="$MR_BASE_URL" \
  -p MODEL_SYNC_REGISTRY_PORT="443" \
  -p MODEL_SYNC_SOURCE_URI="https://huggingface.co/RedHatAI/granite-3.1-8b-instruct-quantized.w4a16/resolve/main/model.safetensors" \
  -p MODEL_SYNC_DESTINATION_OCI_URI="default-route-openshift-image-registry.apps.rosa.CLUSTER-NAME.d4bs.p3.openshiftapps.com/minio-manual/model2:latest" \
  -p MODEL_SYNC_DESTINATION_OCI_REGISTRY="default-route-openshift-image-registry.apps.rosa.CLUSTER-NAME.d4bs.p3.openshiftapps.com" \
  -o yaml > job.yaml
```
