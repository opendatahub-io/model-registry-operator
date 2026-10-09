# model-registry-operator

![build checks status](https://github.com/opendatahub-io/model-registry-operator/actions/workflows/build.yml/badge.svg?branch=main)

The Model Registry Operator is a controller for deploying [OpenShift AI Model Registry](https://github.com/opendatahub-io/model-registry) in a Kubernetes namespace. 

## Description
The controller reconciles `ModelRegistry` Custom Resources to deploy a Model Registry instance and creates a service for its API. See [docs/architecture.md](docs/architecture.md) for an architecture overview and diagram.

## Getting Started
You’ll need a Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for testing, or run against a remote cluster.
**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever cluster `kubectl cluster-info` shows).

`ModelRegistry` service needs a PostgreSQL or MySQL database. A sample Postgres configuration for testing (without TLS security) is included 
in [postgres-db.yaml](config/samples/postgres/postgres-db.yaml) and a sample MySQL configuration for testing is included
in [mysql-db.yaml](config/samples/mysql/mysql-db.yaml).
To use another PostgreSQL instance, comment the line that includes the sample DB in [kustomization.yaml](config/samples/postgres/kustomization.yaml) and to use 
your own mysql instance comment the line that includes the sample DB in [kustomization.yaml](config/samples/mysql/kustomization.yaml).

### Secure Model Registry
The operator supports creating secure model registries using [OpenShift OAuth Proxy](#openshift-oauth-proxy-configuration).
OpenShift OAuth Proxy is recommended for its simplicity and ease of use.

## OpenShift OAuth Proxy Configuration
_Skip this section if you are not using OpenShift OAuth._

The operator supports creating secure model registries using [OpenShift OAuth Proxy](https://github.com/openshift/oauth-proxy).
It leverages [OpenShift Service Certificates](https://docs.redhat.com/en/documentation/openshift_container_platform/4.18/html/security_and_compliance/configuring-certificates#add-service-certificate_service-serving-certificate)
for generating service certificates and
[OpenShift Routes](https://docs.redhat.com/en/documentation/openshift_container_platform/4.18/html/networking/configuring-routes)
for exposing service to external clients outside the OpenShift Cluster.

OpenShift OAuth Proxy samples work best in an OpenShift cluster. It can leverage serving certificates and OpenShift Routes with practically zero extra configuration for security.
For non-OpenShift clusters or for custom service certificates, a secret needs to be created with the name `<registry-name>-oauth-proxy` in the registry namespace. 
This secret can then be configured in the `spec.oauthProxy.tlsCertificateSecret`, and `spec.oauthProxy.tlsKeySecret` properties of the model registry resource. 
To disable external access to the service from outside the cluster, set the property `spec.oauthProxy.serviceRoute` to `disabled`. 
For non-OpenShift clusters, external access has to be manually configured using [Kubernetes Gateway](https://kubernetes.io/docs/concepts/services-networking/gateway/) configuration. 

### Running on the cluster
1. Deploy the controller to the cluster using the `latest` docker image:

```sh
make deploy
```

2. The operator includes multiple samples that use [kustomize](https://kustomize.io/) to create a sample model registry `modelregistry-sample`.

* [MySQL](config/samples/mysql) plain Kubernetes model registry services with a sample MySQL database
* [MySQL with OAuth Proxy](config/samples/oauth/mysql) MySQL database, and OAuth Proxy secured model registry service
* [Secure MySQL](config/samples/secure-db/mysql) plain Kubernetes model registry services with a sample SSL secured MySQL database
* [Secure MySQL with OAuth Proxy](config/samples/secure-db/mysql-oauth) SSL secured MySQL database, and OAuth Proxy secured model registry service
* [PostgreSQL](config/samples/postgres) plain Kubernetes model registry services with a sample PostgreSQL database
* [PostgreSQL with OAuth Proxy](config/samples/oauth/postgres) PostgreSQL database, and OAuth Proxy secured model registry service
* [Secure PostgreSQL](config/samples/secure-db/postgres) plain Kubernetes model registry services with a sample TLS secured PostgreSQL database
* [Async Upload Jobs](config/jobs-async-upload) OpenShift Templates for running async model upload jobs (S3, URI, or Hugging Face to OCI)

### Deploying the Catalog operator

The Catalog operator manages a shared model catalog and is deployed from its own
self-contained overlay (CRD, RBAC, and webhook included), separate from the
model registry operator above. It requires an OpenShift cluster, since its webhook
relies on the OpenShift service-serving-certificate annotation.

```sh
# Deploy the Catalog operator using the published image
make deploy OVERLAY=overlays/catalog

# Or deploy your own image, built and pushed as described below
make deploy OVERLAY=overlays/catalog IMG=<some-registry>/model-registry-operator:tag
```

To remove it:

```sh
make undeploy OVERLAY=overlays/catalog
```

The Catalog selects catalog and benchmark data independently. They may use the
same combined image today or separate images and versions:

| Each field (`catalogDataImageStream`, `benchmarkDataImageStream`) | Behavior |
| --- | --- |
| Omitted or empty | Use that image's current product release default. |
| `stable` | Follow scheduled imports from its managed ImageStream (OpenShift only). |
| `sha256:<64 lowercase hex>` | Pin that digest in the configured import repository, including rollback. |

AIHub leaves both fields unset when it creates the Catalog, using the release
images supplied by the product operator on all clusters. The standalone sample
also leaves both selections empty. AIHub preserves later admin selections.
Scheduled updates require an admin to select `stable` and configure a published
import source. For example, mixed selections are supported:

```yaml
spec:
  catalogDataImageStream: stable
  benchmarkDataImageStream: sha256:1111111111111111111111111111111111111111111111111111111111111111
```

The release defaults come from `RELATED_IMAGE_ODH_MODEL_METADATA_COLLECTION_IMAGE`
and `RELATED_IMAGE_ODH_MODEL_PERFORMANCE_DATA_IMAGE`, respectively. They remain
bootstrap and recovery images shipped with the product. Each import source defaults
to `:stable` in its release repository. `CATALOG_DATA_IMAGE_STREAM_SOURCE` and
`BENCHMARK_DATA_IMAGE_STREAM_SOURCE` can override the full source references for
standalone delivery, including a different repository or floating tag such as
`:latest`. The ImageStream's local selection tag remains `stable`.

The operator creates its ImageStreams from an embedded template with scheduled
imports and watches their status. Identical sources share `model-catalog-data:stable`;
separate sources use `model-catalog-data:stable` for catalog data and
`model-catalog-benchmark-data:stable` for benchmarks. A successful new digest rolls
out the Catalog without changing its spec. Pins ignore subsequent imports.
Clearing either field returns that image to its current release default.

A pending first import uses the bundled image, and ordinary pending updates retain
the applied image. A reported import failure, an unusable imported image, or an
invalid selection **stops Catalog serving** by scaling its deployment to zero;
it does not keep serving an older image. The pod template retains its prior image
references for diagnosis. A later successful import automatically restarts the
Catalog. An admin can also recover by pinning a working digest, clearing the
affected field, or correcting its selection. ImageStream API errors also stop
serving when the Kubernetes API allows the deployment to be updated.

After an import failure, a pending retry or changed source keeps serving stopped
until that image imports successfully. It does not temporarily revive old content.

| Catalog condition | Meaning |
| --- | --- |
| `CatalogDataImageResolved`, `BenchmarkDataImageResolved` | Each selection's resolution, including image references and failures. |
| `CatalogDataImageImportHealthy`, `BenchmarkDataImageImportHealthy` | Each import's success, pending state, or failure, with registry error details. |
| `DataImageResolved`, `DataImageImportHealthy` | Aggregate observations of both images. |
| `DataImageUpdateBlocked` | Whether an image fault currently blocks serving, with recovery guidance. |
| `Degraded` | Catalog image selection/import fault; pending imports alone do not degrade it. |
| `Available` | False while image faults stop serving; otherwise follows workload readiness. |

A warning Event surfaces new failures. These Catalog image conditions are not
propagated into AIHub/DSC readiness, so an image update fault does not declare the
whole installation Not Ready. Frontend consumers can use the image-specific
conditions to display the failure; the stopped service also makes the problem
visible to users rather than silently serving old content.

Failures from an older ImageStream source generation are ignored after a new
import has been requested. Manually selected ImageStream sources, import history,
and unrelated tags survive reconciliation. Unchanged observations do not rewrite
status, and concurrent Catalog edits reject stale status writes.

#### Authorization
For all OAuth Proxy samples, a Kubernetes user or serviceaccount authorization token MUST be passed in calls to model registry services using the header:

```http request
Authorization: Bearer sha256~xxx
```

In OpenShift clusters, the user session token can be obtained using the command:

```shell
oc whoami -t
```

To help authorize users and service accounts to access the registry, the model registry operator creates a `Role` named `registry-user-<registry-name>`. 
This role has the required permission to perform a GET on the model registry instance service, e.g. `modelregistry-sample` service. 
In addition, if running in an OpenShift cluster the operator creates an OpenShift user `Group` called `<registry-name>-users`. 
So, for included samples it creates a Role named `registry-user-modelregistry-sample` and a Group named `modelregistry-sample-users`.

A Kubernetes or OpenShift cluster administrator can then add users to the `Group`, or create `RoleBinding` for the `Role` to grant permission to specific users and serviceaccounts to access the model registry. 

NOTE: The operator deletes the Group and the Role when the model registry custom resource is deleted. 
If you have created your own RoleBindings to this Role, the operator will not remove them automatically and hence must be removed manually. 

##### TLS Certificates
The project [Makefile](Makefile) includes targets to manage test TLS certificates using a self signed CA certificate. 
To create test certificates in the directory [certs](certs) and Kubernetes secrets in the current namespace, use the command:

```shell
make certificates
```

The test CA certificate is generated in the file [certs/domain.crt](certs/domain.crt) along with certificate for Database service. See [generate_certs.sh](scripts/generate_certs.sh) for details. 

To cleanup the certificates and Kubernetes secrets, use the command:

```shell
make certificates/clean
```

NOTE: The sample database secret `model-registry-db-credential` is created with the CA cert, server key and server cert. However, in production the model registry only needs a secret with the CA cert(s). The production database server will be configured with a secret containing the private key and server cert. 
The sample certificates use a self-signed CA and does not do cert management like cert rotation, etc. Use your own certificate manager, e.g. https://cert-manager.io/ and create generic kubernetes secrets for REST and database with the the keys `tls.key`, `tls.crt`, and `ca.crt`.

Install a model registry instance using **ONE** of the following commands:

NOTE: For Open Data Hub, or OpenShift AI, use the correct registries namespace i.e. `odh-model-registries` or `rhoai-model-registries` respectively.
Which can be provided using either the `-n` option, or setting it as the current namespace first.

```shell
kubectl apply -k config/samples/mysql
kubectl apply -k config/samples/oauth/mysql
kubectl apply -k config/samples/secure-db/mysql
kubectl apply -k config/samples/secure-db/mysql-oauth
kubectl apply -k config/samples/postgres
kubectl apply -k config/samples/oauth/postgres
kubectl apply -k config/samples/secure-db/postgres
```

This will create the appropriate database and model registry resources, which will be reconciled in the controller to create a model registry deployment with other Kubernetes, and OpenShift resources as needed.

4. Check that the sample model registry was created using the command:

```shell
kubectl describe mr modelregistry-sample
```

Check the `Status` of the model registry resource for failed Conditions. 

#### Verifying the Model Registry REST Service

##### Verifying OAuth Proxy Sample
For non-OpenShift clusters or when using a custom service certificate, create a TLS secret using the command:

```shell
kubectl create secret tls modelregistry-sample-oauth-proxy --cert=path/to/cert/file --key=path/to/key/file
```

Verify that the Route was created using the command:

```shell
kubectl get routes.route.openshift.io modelregistry-sample-https
```

Use the following command to get the OpenShift Route Host:

```shell
export ROUTE_HOST=`kubectl get routes.route.openshift.io modelregistry-sample-https -ojsonpath='{.status.ingress[0].host}'`
```

Then verify the REST service using the command:

```shell
curl -H "Authorization: Bearer $TOKEN" https://$ROUTE_HOST/api/model_registry/v1alpha3/registered_models
```

Where, `$TOKEN` environment variable is set to the client token. If using OpenShift, the token can be set using the command:

```shell
export TOKEN=`oc whoami -t`
```

Note: If the OpenShift cluster uses a non-public CA, it needs be provided using `--cacert` curl option,
or use the `-k` option to skip certificate verification in development and testing environments.

##### Verifying non-Auth Sample
If using a non-Auth sample and using OpenShift, enable the OpenShift Route using the command:

```shell
kubectl patch mr modelregistry-sample --type='json' -p='[{"op": "replace", "path": "/spec/rest/serviceRoute", "value": "enabled"}]'
```

Verify that the Route was created using the command:

```shell
kubectl get routes.route.openshift.io modelregistry-sample-http
```

Use the following command to get the OpenShift Route Host:

```shell
export ROUTE_HOST=`kubectl get routes.route.openshift.io modelregistry-sample-http -ojsonpath='{.status.ingress[0].host}'`
```

Then verify the REST service using the command:

```shell
curl http://$ROUTE_HOST/api/model_registry/v1alpha3/registered_models
```

##### REST Service Output
The output should be a list of all registered models in the registry, e.g. for an empty registry:

```json
{"items":[],"nextPageToken":"","pageSize":0,"size":0}
```

5. To delete the sample model registry, use **ONE** of the following commands based on the sample type deployed earlier:

NOTE: For Open Data Hub, or OpenShift AI, use the correct registries namespace i.e. `odh-model-registries` or `rhoai-model-registries` respectively.
Which can be provided using either the `-n` option, or setting it as the current namespace first.

```shell
kubectl delete -k config/samples/mysql
kubectl delete -k config/samples/oauth/mysql
kubectl delete -k config/samples/secure-db/mysql
kubectl delete -k config/samples/secure-db/mysql-oauth
kubectl delete -k config/samples/postgres
kubectl delete -k config/samples/oauth/postgres
kubectl delete -k config/samples/secure-db/postgres
```

### Building local docker image for development
1. Build and push your image to the location specified by `IMG`:

```sh
make docker-build docker-push IMG=<some-registry>/model-registry-operator:tag
```

2. Deploy the controller to the cluster with the image specified by `IMG`:

```sh
make deploy IMG=<some-registry>/model-registry-operator:tag
```

### Uninstall CRDs
To delete the CRDs from the cluster:

```sh
make uninstall
```

### Undeploy controller
UnDeploy the controller from the cluster:

```sh
make undeploy
```

## Contributing
See [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, lint, and submission guidelines.

### How it works
This project aims to follow the Kubernetes [Operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/).

It uses [Controllers](https://kubernetes.io/docs/concepts/architecture/controller/),
which provide a reconcile function responsible for synchronizing resources until the desired state is reached on the cluster.

### Test It Out
1. Install the CRDs into the cluster:

```sh
make install
```

2. Run your controller (this will run in the foreground, so switch to a new terminal if you want to leave it running):

```sh
make run
```

**NOTE:** You can also run this in one step by running: `make install run`

### Modifying the API definitions
If you are editing the API definitions, generate the manifests such as CRs or CRDs using:

```sh
make manifests
```

**NOTE:** Run `make --help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
