# Installation of Model Registry (Terminal Based)

This document outlines the instructions to install the Model Registry using "Open Data Hub" Operator on OpenShift. You can find this operator in the OpenShift Opererator market place. Please note that "Model Registry Operator" is sub-component of this operator and can not be used independently.

## Prerequisites
The following prerequisites are needed for the Operator to work correctly.
<ol>
<li> Access to OpenShift Cluster 4.17 + (recommend)
<li> To install the operators you need "cluster-admin" access
<li> Make sure you have enough capacity on the cluster to install a "data science cluster" minimum is with HCP cluster is 2 nodes of m6i
<li> Model registry currently only works MySQL or MariaDB database, if you have an access to external database collect the credentials for it. You  need `user-id`, `password`, `host`, `port`, `database-name`
</ol>


## Installing needed Operators
Once you have the OpenShift cluster available,
<ol>
<li> Log in "Openshift Console", you can go to your name on top right use "Copy Login Command" to find the below command and use the terminal to login.

```
oc login --token=xxxx --server=xxx
```

<li> Search and install "Authorino" operator, use the one with name `Authorino Operator`

```
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: authorino-operator
  namespace: openshift-operators
spec:
  channel: stable
  installPlanApproval: Automatic
  name: authorino-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
  startingCSV: authorino-operator.v0.16.0
EOF
```

verify subscription "Succeeded" in install
```
oc get csv authorino-operator.v0.16.0 -n openshift-operators -o json | jq '.status.phase'
```

<li> Search and install "Service Mesh" operator

```
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: servicemeshoperator
  namespace: openshift-operators
spec:
  channel: stable
  installPlanApproval: Automatic
  name: servicemeshoperator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
  startingCSV: servicemeshoperator.v2.6.5
EOF
```

verify subscription "Succeeded" in install

```
oc get csv servicemeshoperator.v2.6.5 -n openshift-operators -o json | jq '.status.phase'
```

<li> Search and install Serverless Operator

```
oc create namespace openshift-serverless ;
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: serverless-operator
  namespace: openshift-serverless
spec:
  channel: stable
  installPlanApproval: Automatic
  name: serverless-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
  startingCSV: serverless-operator.v1.35.0
EOF
```
verify subscription "Succeeded" in install

```
oc get csv serverless-operator.v1.35.0 -n openshift-serverless -o json | jq '.status.phase'
```

<li> Search and install "Open Data Hub" operator 2.23.0+ (latest is recommended, edit the odh-subscription.yaml file to update the version)

```
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: opendatahub-operator
  namespace: openshift-operators
spec:
  channel: fast
  installPlanApproval: Automatic
  name: opendatahub-operator
  source: community-operators
  sourceNamespace: openshift-marketplace
  startingCSV: opendatahub-operator.v2.23.0
EOF
```

verify subscription "Succeeded" in install
```
oc get csv opendatahub-operator.v2.23.0 -n openshift-operators -o json | jq '.status.phase'
```

<li> If you are using local storage mechanisms for S3 bucket, try installing "minio" operator and configure its access (TDB..)
</ol>

## Install "Data Science Cluster"
Once all the operators all installed and no errors reported, then proceed to installing a "Data Science Cluster". You can navigate "Open data Hub" operator install from left navigation under "Operators --> Installed Operators" and click on "Open Data Hub Operator" and switch to the tab "Data Science Cluster" and create DSC and make sure edit the YAML set the `modelregistry` to `managed` state like shown below

```
modelregistry:
    managementState: Managed
```

Or you can also use following commands to create a `DSCInitialization` and a `DataScienceCluster`.

```
oc apply -f - <<EOF
apiVersion: dscinitialization.opendatahub.io/v1
kind: DSCInitialization
metadata:
  name: default-dsci
spec:
  applicationsNamespace: opendatahub
  devFlags:
    logmode: production
  monitoring:
    managementState: Managed
    namespace: opendatahub
  serviceMesh:
    auth:
      audiences:
        - 'https://kubernetes.default.svc'
    controlPlane:
      metricsCollection: Istio
      name: data-science-smcp
      namespace: istio-system
    managementState: Managed
  trustedCABundle:
    customCABundle: ''
    managementState: Managed
EOF
```

make sure it is ready before proceeding to next step
```
oc get dsci default-dsci
```

create data science cluster
```
oc apply -f - <<EOF
apiVersion: datasciencecluster.opendatahub.io/v1
kind: DataScienceCluster
metadata:
  labels:
    app.kubernetes.io/created-by: opendatahub-operator
    app.kubernetes.io/instance: default
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: datasciencecluster
    app.kubernetes.io/part-of: opendatahub-operator
  name: default-dsc
spec:
  components:
    codeflare:
      managementState: Removed
    kserve:
      managementState: Managed
      serving:
        ingressGateway:
          certificate:
            type: OpenshiftDefaultIngress
        managementState: Managed
        name: knative-serving
    modelregistry:
      managementState: Managed
      registriesNamespace: odh-model-registries
    trustyai:
      managementState: Removed
    ray:
      managementState: Removed
    kueue:
      managementState: Removed
    workbenches:
      managementState: Managed
    dashboard:
      managementState: Managed
    modelmeshserving:
      managementState: Managed
    datasciencepipelines:
      managementState: Managed
    trainingoperator:
      managementState: Removed
EOF
```
check to make sure cluster is in `Ready` state

```
oc get dsc default-dsc -o json | jq '.status.phase'
```

## Install Database (skip if using existing database)

Model Registry currently requires MySQL database 8.0.3 or above to function correctly. If you have a database already available you can skip this section all together.

> [!IMPORTANT]  
> The `mysql_native_password` authentication plugin is required for the ML Metadata component to successfully connect to your database. `mysql_native_password` is disabled by default in MySQL 8.4 and later. If your database uses MySQL 8.4 or later, you must update your MySQL deployment to enable the `mysql_native_password` plugin. 
> For more information about enabling the `mysql_native_password` plugin, see [Native Pluggable Authentication](https://dev.mysql.com/doc/refman/8.4/en/native-pluggable-authentication.html) in the MySQL documentation.

For "Development" or "NON-PRODUCTION" scenarios you can use following script to install MySQL database.

Create `namespace` where you want to host the database

```sh
oc new-project test-database
```

Create the MySQL database.

> [!WARNING]  
> If copy-pasting the yaml below, pay attention to the `\$` sequence needed for escaping `$` on the command line; if you are copying the content in an editor/console, make sure to remove the escape or use [this](../config/samples/mysql/mysql-db.yaml) sample instead.

```sh
oc apply -f - <<EOF
apiVersion: v1
items:
- apiVersion: v1
  kind: Service
  metadata:
    labels:
      app.kubernetes.io/name: model-registry-db
      app.kubernetes.io/instance: model-registry-db
      app.kubernetes.io/part-of: model-registry-db
      app.kubernetes.io/managed-by: kustomize
    annotations:
      template.openshift.io/expose-uri: mysql://{.spec.clusterIP}:{.spec.ports[?(.name==\mysql\)].port}
    name: model-registry-db
  spec:
    ports:
    - name: mysql
      nodePort: 0
      port: 3306
      protocol: TCP
      appProtocol: tcp
      targetPort: 3306
    selector:
      name: model-registry-db
    sessionAffinity: None
    type: ClusterIP
- apiVersion: v1
  kind: PersistentVolumeClaim
  metadata:
    labels:
      app.kubernetes.io/name: model-registry-db
      app.kubernetes.io/instance: model-registry-db
      app.kubernetes.io/part-of: model-registry-db
      app.kubernetes.io/managed-by: kustomize
    name: model-registry-db
  spec:
    accessModes:
    - ReadWriteOnce
    resources:
      requests:
        storage: 5Gi
- apiVersion: apps/v1
  kind: Deployment
  metadata:
    labels:
      app.kubernetes.io/name: model-registry-db
      app.kubernetes.io/instance: model-registry-db
      app.kubernetes.io/part-of: model-registry-db
      app.kubernetes.io/managed-by: kustomize
    annotations:
      template.alpha.openshift.io/wait-for-ready: "true"
    name: model-registry-db
  spec:
    replicas: 1
    revisionHistoryLimit: 0
    selector:
      matchLabels:
        name: model-registry-db
    strategy:
      type: Recreate
    template:
      metadata:
        labels:
          name: model-registry-db
          sidecar.istio.io/inject: "false"
      spec:
        containers:
        - env:
          - name: MYSQL_USER
            valueFrom:
              secretKeyRef:
                key: database-user
                name: model-registry-db
          - name: MYSQL_PASSWORD
            valueFrom:
              secretKeyRef:
                key: database-password
                name: model-registry-db
          - name: MYSQL_ROOT_PASSWORD
            valueFrom:
              secretKeyRef:
                key: database-password
                name: model-registry-db
          - name: MYSQL_DATABASE
            valueFrom:
              secretKeyRef:
                key: database-name
                name: model-registry-db
          args:
            - --datadir
            - /var/lib/mysql/datadir
            - --default-authentication-plugin=mysql_native_password
          image: mysql:8.3.0
          imagePullPolicy: IfNotPresent
          livenessProbe:
            exec:
              command:
                - /bin/bash
                - -c
                - mysqladmin -u\${MYSQL_USER} -p\${MYSQL_ROOT_PASSWORD} ping
            initialDelaySeconds: 15
            periodSeconds: 10
            timeoutSeconds: 5
          name: mysql
          ports:
          - containerPort: 3306
            protocol: TCP
          readinessProbe:
            exec:
              command:
              - /bin/bash
              - -c
              - mysql -D \${MYSQL_DATABASE} -u\${MYSQL_USER} -p\${MYSQL_ROOT_PASSWORD} -e 'SELECT 1'
            initialDelaySeconds: 10
            timeoutSeconds: 5
          securityContext:
            capabilities: {}
            privileged: false
          terminationMessagePath: /dev/termination-log
          volumeMounts:
          - mountPath: /var/lib/mysql
            name: model-registry-db-data
        dnsPolicy: ClusterFirst
        restartPolicy: Always
        volumes:
        - name: model-registry-db-data
          persistentVolumeClaim:
            claimName: model-registry-db
- apiVersion: v1
  kind: Secret
  metadata:
    labels:
      app.kubernetes.io/name: model-registry-db
      app.kubernetes.io/instance: model-registry-db
      app.kubernetes.io/part-of: model-registry-db
      app.kubernetes.io/managed-by: kustomize
    annotations:
      template.openshift.io/expose-database_name: '{.data[''database-name'']}'
      template.openshift.io/expose-password: '{.data[''database-password'']}'
      template.openshift.io/expose-username: '{.data[''database-user'']}'
    name: model-registry-db
  stringData:
    database-name: "model_registry"
    database-password: "TheBlurstOfTimes" # notsecret
    database-user: "mlmduser" # notsecret
kind: List
metadata: {}
EOF
```
make sure the database in `available` state

```sh
oc wait --for=condition=available deployment/model-registry-db --timeout=5m
```

If you encounter image pull limits, you could replace the sample db image with analogous one from (upstream [example](https://github.com/kubeflow/model-registry?tab=readme-ov-file#pull-image-rate-limiting)).

## Install Model Registry

To install Model Registry use the following script. Create a namespace where you going to be installing the model registry

```
oc project odh-model-registries
```

Create Model Registry
```
oc apply -f - <<EOF
apiVersion: v1
items:
  - apiVersion: v1
    kind: Secret
    metadata:
      labels:
        app.kubernetes.io/name: model-registry-db
        app.kubernetes.io/instance: model-registry-db
        app.kubernetes.io/part-of: model-registry-db
        app.kubernetes.io/managed-by: kustomize
      annotations:
        template.openshift.io/expose-database_name: '{.data[''database-name'']}'
        template.openshift.io/expose-password: '{.data[''database-password'']}'
        template.openshift.io/expose-username: '{.data[''database-user'']}'
      name: model-registry-db
    stringData:
      database-name: model_registry
      database-password: TheBlurstOfTimes
      database-user: mlmduser
  - apiVersion: modelregistry.opendatahub.io/v1alpha1
    kind: ModelRegistry
    metadata:
      labels:
        app.kubernetes.io/created-by: model-registry-operator
        app.kubernetes.io/instance: modelregistry-sample
        app.kubernetes.io/managed-by: kustomize
        app.kubernetes.io/name: modelregistry
        app.kubernetes.io/part-of: model-registry-operator
      name: modelregistry-public
      namespace: odh-model-registries
    spec:
      grpc: {}
      rest: {}
      istio:
        authProvider: opendatahub-auth-provider
        gateway:
          grpc:
            tls: {}
          rest:
            tls: {}
      mysql:
        host: model-registry-db.test-database.svc.cluster.local
        database: model_registry
        passwordSecret:
          key: database-password
          name: model-registry-db
        port: 3306
        skipDBCreation: false
        username: mlmduser
kind: List
metadata: {}
EOF
```

Check to make sure Model Registry available and is running
```
oc wait --for=condition=available modelregistries.modelregistry.opendatahub.io/modelregistry-public --timeout=5m
```

## Dashboard Install

You do not need to install dashboard separately, however if for any reason Dashboard is not showing the Model Registry in the left navigation, you can use following script to enable it

```
oc patch odhdashboardconfig.opendatahub.io odh-dashboard-config -n opendatahub --type merge -p '{"spec": {"dashboardConfig": {"disableModelRegistry": false}}}'
```
## Find Model Registry URL
Model Registry uses service mesh and opens a Gateway for external clients to reach. Execute following to find the URL where Model Registry is available.

```
URL=`echo "https://$(oc get routes -n istio-system -l app.kubernetes.io/name=modelregistry-public -o json | jq '.items[].status.ingress[].host | select(contains("-rest"))')" | tr -d '"'`
```

## Validation of the overall Install
Now we can validate if everyhing working correctly by executing the following

```
export TOKEN=`oc whoami -t`
curl -k -H "Authorization: Bearer $TOKEN" $URL/api/model_registry/v1alpha3/registered_models
```

and you should see an output like

```
{"items":[],"nextPageToken":"","pageSize":0,"size":0}
```

Model Registry is fully installed and ready to go.

## Log into Open Data Hub Dashboard

ODH Dashboard is UI component for all AI/ML operations that user can use for handling various operations. Use following script to find out URL for the ODH dashboard


```
echo "https://$(oc get routes -n opendatahub -l app=odh-dashboard -o json | jq '.items[].status.ingress[].host | select(contains("odh-dashboard"))')" | tr -d '"'
```

Copy the above URL to your web-browser and login using credentials used above or user credentials you may have received from your ODH administrator. Once you are logged into the Dashboard find "Model Registry" on left navigation to see the available models, please note initially this list may be empty.

Now, there are a couple different ways you can "register" model using Python code or directly using the Dashboard UI into Model Registry. For these instructions please follow to [python library usage](./getting-started.md)
