# model-registry-operator

![build checks status](https://github.com/opendatahub-io/model-registry-operator/actions/workflows/build.yml/badge.svg?branch=main)

The Model Registry Operator is a controller for deploying [OpenShift AI Model Registry](https://github.com/opendatahub-io/model-registry) in a Kubernetes namespace. 

## Description
The controller reconciles `ModelRegistry` Custom Resources to deploy a Model Registry instance and creates a service for its API. 

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
The operator also supports securing model registries using [Istio and Authorino](#istio-configuration).
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

### Istio Configuration
_Skip this section if you are not using Istio._

*NOTE* that this section describes how to configure a couple of files in the kustomize samples in this project. However, there are only a handful of extra configuration properties needed in an Istio model registry. 

The operator supports creating secure model registries using [Istio](https://istio.io/). Authorization is handled using [Authorino](https://github.com/Kuadrant/authorino).
It also supports [Istio Gateways](https://istio.io/latest/docs/concepts/traffic-management/#gateways) for exposing service endpoints for clients outside the Istio service mesh. 

For using the Istio based samples, both Istio and Authorino MUST be installed before deploying the operator. 
An Authorino instance MUST also be configured as a [custom authz](https://istio.io/latest/docs/tasks/security/authorization/authz-custom/) provider in the Istio control plane. 

Both Istio and Authorino along with the authz provider can be easily enabled in the Open Data Hub data science cluster instance. 

If Istio or Authorino is installed in the cluster after deploying this controller, restart the controller by deleting the pod `model-registry-operator-controller-manager` in `model-registry-operator-system` namespace. 
If Model Registry component has been installed as an Open Data Hub operator component, the operator namespace will be `opendatahub`. 

If Authorino provider is from a non Open Data Hub cluster, configure its selector labels in the [authconfig-labels.yaml](config/samples/istio/components/authconfig-labels.yaml) file. 

To use the Istio model registry samples the following configuration data is needed in the [istio.env](config/samples/istio/components/istio.env) file:

* AUTH_PROVIDER - name of the authorino external auth provider configured in the Istio control plane (defaults to `opendatahub-auth-provider` for Open Data Hub data science cluster with OpenShift Service Mesh enabled).
* DOMAIN - hostname domain suffix for gateway endpoints. This field is optional in an OpenShift cluster and set automatically if left empty.
This depends upon your cluster's external load balancer config. In OpenShift clusters, it can be obtained with the command:
```shell
oc get ingresses.config/cluster -o jsonpath='{.spec.domain}'
```
* ISTIO_INGRESS - name of the Istio Ingress Gateway (defaults to `ingressgateway`). 
* REST_CREDENTIAL_NAME - Kubernetes secret in IngressGateway namespace (typically `istio-system`) containing TLS certificates for REST service (defaults to `modelregistry-sample-rest-credential`). 
* GRPC_CREDENTIAL_NAME - Kubernetes secret in IngressGateway namespace containing TLS certificates for gRPC service (defaults to `modelregistry-sample-grpc-credential`).

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
* [MySQL with Istio](config/samples/istio/mysql) MySQL database, Istio, and plaintext Gateway
* [MySQL with Istio and TLS](config/samples/istio/mysql-tls) MySQL database, Istio, and TLS Gateway endpoints
* [Secure MySQL with Istio](config/samples/secure-db/mysql-tls) SSL secured MySQL database, Istio, and TLS Gateway
* [PostgreSQL](config/samples/postgres) plain Kubernetes model registry services with a sample PostgreSQL database
* [PostgreSQL with OAuth Proxy](config/samples/oauth/postgres) PostgreSQL database, and OAuth Proxy secured model registry service
* [PostgreSQL with Istio](config/samples/istio/postgres) PostgreSQL database, Istio, and plaintext Gateway
* [PostgreSQL with Istio and TLS](config/samples/istio/postgres-tls) PostgreSQL database, Istio, and TLS Gateway endpoints

#### Authorization
For all OAuth Proxy and Istio samples, a Kubernetes user or serviceaccount authorization token MUST be passed in calls to model registry services using the header:

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

#### Istio Samples
**WARNING:** Istio samples without TLS are only meant for testing and demos to avoid having to create TLS certificates. They should only be used in local development clusters.

##### TLS Certificates
The project [Makefile](Makefile) includes targets to manage test TLS certificates using a self signed CA certificate. 
To create test certificates in the directory [certs](certs) and Kubernetes secrets in the `istio-system` namespace and current namespace, use the command:

```shell
make certificates
```

The test CA certificate is generated in the file [certs/domain.crt](certs/domain.crt) along with certificates for REST, gRPC, and Database service. See [generate_certs.sh](scripts/generate_certs.sh) for details. 

To cleanup the certificates and Kubernetes secrets, use the command:

```shell
make certificates/clean
```

NOTE: The sample database secret `model-registry-db-credential` is created with the CA cert, server key and server cert. However, in production the model registry only needs a secret with the CA cert(s). The production database server will be configured with a secret containing the private key and server cert. 
The sample certificates use a self-signed CA and does not do cert management like cert rotation, etc. Use your own certificate manager, e.g. https://cert-manager.io/ and create generic kubernetes secrets for REST, gRPC and database with the the keys `tls.key`, `tls.crt`, and `ca.crt`. 

To disable Istio Gateway creation, create a kustomize overlay that removes the `gateway` yaml section in model registry custom resource or manually edit a sample yaml and it's corresponding `replacements.yaml` helper. 

##### Enable Namespace Istio Injection
If using upstream Istio (i.e. not OpenShift Service Mesh), enable Istio proxy injection in your test namespace by using the command:

```shell
kubectl label namespace <namespace> istio-injection=enabled --overwrite
```

If using OpenShift Service Mesh, enable it by adding the namespace to the control plane (e.g. ODH Istio control plane `data-science-smcp` below) by using the command:

```shell
kubectl apply -f -<<EOF
apiVersion: maistra.io/v1
kind: ServiceMeshMember
metadata:
  name: default
spec:
  controlPlaneRef:
    name: data-science-smcp
    namespace: istio-system
EOF
```

##### OpenShift Gateway Route Creation
If using OpenShift, the operator will automatically create OpenShift Routes in the ingress gateway's namespace (`istio-system` by default). 
It will create two routes `<namespace>-modelregistry-sample-rest` and `<namespace>-modelregistry-sample-grpc` for the REST and gRPC gateway endpoints respectively.

This automatic route creation can be disabled by setting the properties `spec.istio.gateway.rest.gatewayRoute` or `spec.istio.gateway.grpc.gatewayRoute` to `disabled`. 


3. For Istio samples, first configure properties in [istio.env](config/samples/istio/components/istio.env). 
Install a model registry instance using **ONE** of the following commands:

NOTE: For Open Data Hub, or OpenShift AI, use the correct registries namespace i.e. `odh-model-registries` or `rhoai-model-registries` respectively.
Which can be provided using either the `-n` option, or setting it as the current namespace first.

```shell
kubectl apply -k config/samples/mysql
kubectl apply -k config/samples/oauth/mysql
kubectl apply -k config/samples/secure-db/mysql
kubectl apply -k config/samples/secure-db/mysql-oauth
kubectl apply -k config/samples/istio/mysql
kubectl apply -k config/samples/istio/mysql-tls
kubectl apply -k config/samples/secure-db/mysql-tls
kubectl apply -k config/samples/postgres
kubectl apply -k config/samples/oauth/postgres
kubectl apply -k config/samples/istio/postgres
kubectl apply -k config/samples/istio/postgres-tls
```

This will create the appropriate database and model registry resources, which will be reconciled in the controller to create a model registry deployment with other Kubernetes, Istio, and Authorino resources as needed.

4. Check that the sample model registry was created using the command:

```shell
kubectl describe mr modelregistry-sample
```

Check the `Status` of the model registry resource for failed Conditions. 

For Istio Gateway examples, consult your Istio configuration to verify gateway endpoint creation. When OpenShift gateway route creation is enabled (by default), look for model registry routes using the command:

```shell
kubectl get route -n istio-system
```

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

##### Verifying Istio TLS Gateway Sample
If using the Istio TLS Gateway sample, to verify the REST gateway service use the following command:

```shell
curl -H "Authorization: Bearer $TOKEN" --cacert certs/domain.crt https://modelregistry-sample-rest.$DOMAIN/api/model_registry/v1alpha3/registered_models
```

Where, `$TOKEN` and `$DOMAIN` environment variables are set to the client token and host domain. If using OpenShift, the token and domain can be set using the commands:

```shell
export TOKEN=`oc whoami -t`
export DOMAIN=`oc get ingresses.config/cluster -o jsonpath='{.spec.domain}'`
```

##### Verifying Istio non-TLS Gateway Sample
If using a non-TLS gateway sample, use the command:

```shell
curl -H "Authorization: Bearer $TOKEN" http://modelregistry-sample-rest.$DOMAIN/api/model_registry/v1alpha3/registered_models
```

##### Verifying non-Istio Sample
If using a non-Istio sample and using OpenShift, enable the OpenShift Route using the command:

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
kubectl delete -k config/samples/istio/mysql
kubectl delete -k config/samples/istio/mysql-tls
kubectl delete -k config/samples/secure-db/mysql-tls
kubectl delete -k config/samples/postgres
kubectl delete -k config/samples/oauth/postgres
kubectl delete -k config/samples/istio/postgres
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

