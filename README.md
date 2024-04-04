# model-registry-operator

![build checks status](https://github.com/opendatahub-io/model-registry-operator/actions/workflows/build.yml/badge.svg?branch=main)

Model Registry operator is a controller for deploying Openshift AI Model Registry service in a Kubernetes namespace. 

## Description
The controller reconciles `ModelRegistry` Custom Resources to create a service for the ModelRegistry API. 

## Getting Started
You’ll need a Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for testing, or run against a remote cluster.
**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever cluster `kubectl cluster-info` shows).

`ModelRegistry` service needs a PostgreSQL or MySQL database. A sample Postgres configuration for testing (without TLS security) is included 
in [postgres-db.yaml](config/samples/postgres/postgres-db.yaml) and a sample MySQL configuration for testing is included
in [mysql-db.yaml](config/samples/mysql/mysql-db.yaml).
To use another PostgreSQL instance, comment the line that includes the sample DB in [kustomization.yaml](config/samples/postgres/kustomization.yaml) and to use 
your own mysql instance comment the line that includes the sample DB in [kustomization.yaml](config/samples/mysql/kustomization.yaml).

The operator supports creating model registries using [Istio](https://istio.io/) for security including [Authorino](https://github.com/Kuadrant/authorino) for authorization. 
It also supports [Istio Gateways](https://istio.io/latest/docs/concepts/traffic-management/#gateways) for exposing service endpoints for clients outside the Istio service mesh.

### Istio Configuration
_Skip this section if you are not using Istio._

*NOTE* that this section describes how to configure a couple of files in the kustomize samples in this project. However, there are only a handful of extra configuration properties needed in an Istio model registry. 

For using the Istio based samples, both Istio and Authorino MUST be installed before deploying the operator. 
An Authorino instance MUST also be configured as a [custom authz](https://istio.io/latest/docs/tasks/security/authorization/authz-custom/) provider in the Istio control plane. 

Both Istio and Authorino along with the authz provider can be easily enabled in the Open Data Hub data science cluster instance. 
If Istio or Authorino is installed in the cluster after deploying this controller, restart the controller by deleting the pod `model-registry-operator-controller-manager` in `model-registry-operator-system` namespace. 

If Authorino provider is from a non Open Data Hub cluster, configure its selector labels in the [authconfig-labels.yaml](config/samples/istio/components/authconfig-labels.yaml) file. 

To use the Istio model registry samples the following configuration data is needed in the [istio.env](config/samples/istio/components/istio.env) file:

* AUTH_PROVIDER name of the authorino external auth provider configured in the Istio control plane (defaults to `opendatahub-auth-provider` for Open Data Hub data science cluster with OpenShift Servicemesh enabled).
* DOMAIN hostname domain suffix for gateway endpoints. 
This depends upon your cluster's external load balancer config. In OpenShift clusters, it can be obtained with the command:
```shell
oc get ingresses.config/cluster -o jsonpath='{.spec.domain}'
```
* ISTIO_INGRESS name of the Istio Ingress Gateway (defaults to `ingressgateway`). 
* REST_CREDENTIAL_NAME Kubernetes secret in IngressGateway namespace (typically `istio-system`) containing TLS certificates for REST service (defaults to `modelregistry-sample-rest-credential`). 
* GRPC_CREDENTIAL_NAME Kubernetes secret in IngressGateway namespace containing TLS certificates for gRPC service (defaults to `modelregistry-sample-grpc-credential`).

### Running on the cluster
1. Deploy the controller to the cluster using the `latest` docker image:

```sh
make deploy
```

2. The operator includes multiple samples that use [kustomize](https://kustomize.io/) to create a sample model registry `modelregistry-sample`.

* [MySQL without Istio](config/samples/mysql) plain Kubernetes model registry services with a sample MySQL database
* [PostgreSQL without Istio](config/samples/postgres) plain Kubernetes model registry services with a sample PostgreSQL database
* [MySQL with Istio](config/samples/istio/mysql) MySQL database, Istio, and plaintext Gateway
* [PostgreSQL with Istio](config/samples/istio/postgres) PostgreSQL database, Istio, and plaintext Gateway
* [MySQL with Istio and TLS](config/samples/istio/mysql-tls) MySQL database, Istio, and TLS Gateway endpoints
* [PostgreSQL with Istio and TLS](config/samples/istio/postgres-tls) PostgreSQL database, Istio, and TLS Gateway endpoints

#### Istio Samples
**WARNING:** Istio samples without TLS are only meant for testing and demos to avoid having to create TLS certificates. They should only be used in local development clusters. 

For all Istio samples, a Kubernetes user or serviceaccount authorization token MUST be passed in calls to model registry services using the header:

```http request
Authorization: Bearer sha256~xxx
```

In OpenShift clusters, the user session token can be obtained using the command:

```shell
oc whoami -t
```

The project [Makefile](Makefile) includes targets to manage test TLS certificates using a self signed CA certificate. 
To create test certificates in the directory [certs](certs) and Kubernetes secrets in the `istio-system` namespace, use the command:

```shell
make certificates
```

The test CA certificate is generated in the file [certs/domain.crt](certs/domain.crt) along with other certificates. See [generate_certs.sh](scripts/generate_certs.sh) for details. 

To cleanup the certificates and Kubernetes secrets, use the command:

```shell
make certificates/clean
```

To disable Istio Gateway creation, create a kustomize overlay that removes the `gateway` yaml section in model registry custom resource or manually edit a sample yaml and it's corresponding `replacements.yaml` helper. 

3. For Istio samples, first configure properties in [istio.env](config/samples/istio/components/istio.env). 
Install a model registry instance using **ONE** of the following commands:

```shell
kubectl apply -k config/samples/mysql
kubectl apply -k config/samples/postgres
kubectl apply -k config/samples/istio/mysql
kubectl apply -k config/samples/istio/postgres
kubectl apply -k config/samples/istio/mysql-tls
kubectl apply -k config/samples/istio/postgres-tls
```

This will create the appropriate model registry resource, which will be reconciled in the controller to create a model registry deployment with other Kubernetes, Istio, and Authorino resources as needed.

4. Check that the sample model registry was created using the command:

```shell
kubectl get mr
```

Check the status of the model registry resource for errors. 

For Istio Gateway examples, consult your Istio configuration to verify gateway endpoint creation. For OpenShift Servicemesh with gateway route creation enabled, look for model registry routes using the command:

```shell
kubectl get route -n istio-system
```

To verify the REST gateway service, use the following command:

```shell
curl -H "Authorization: Bearer $TOKEN" --cacert certs/domain.crt https://modelregistry-sample-rest.$DOMAIN/api/model_registry/v1alpha2/registered_models
```

Where, `$TOKEN` and $DOMAIN environment variables are set to the client token and host domain. 

If using a non-TLS gateway, use the command:

```shell
curl -H "Authorization: Bearer $TOKEN" http://modelregistry-sample-rest.$DOMAIN/api/model_registry/v1alpha2/registered_models
```

The output should be a list of all registered models in the registry, e.g. for an empty registry:

```json
{"items":[],"nextPageToken":"","pageSize":0,"size":0}
```

5. To delete the sample model registry, use **ONE** of the following commands based on the sample type deployed earlier:

```shell
kubectl delete -k config/samples/mysql
kubectl delete -k config/samples/postgres
kubectl delete -k config/samples/istio/mysql
kubectl delete -k config/samples/istio/postgres
kubectl delete -k config/samples/istio/mysql-tls
kubectl delete -k config/samples/istio/postgres-tls
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
// TODO(user): Add detailed information on how you would like others to contribute to this project

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

