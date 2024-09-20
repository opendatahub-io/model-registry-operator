# Installation of Model Registry (Terminal Based)

This document outlines the instructions to install the Model Registry using "Open Data Hub" Operator on OpenShift. You can find this operator in the OpenShift Opererator market place. Please note that "Model Registry Operator" is sub-component of this operator and can not be used independently.

## Prerequisites 
The following prerequisites are needed for the Operator to work correctly.
<ol>
<li> Access to OpenShift Cluster 4.17 + (recommened)
<li> To install the operators you need "cluster-admin" access
<li> Make sure you have enough capacity on the cluster to install a "data science cluster" minimum is with HCP cluster is 2 nodes of m6i
<li> Model registry currently only works MySQL or MariaDB database, if you have an access to external database collect the credentials for it. you would need `user-id`, `password`, `host`, `port`, `database-name`
</ol>


## Installing needed Operators
Once you have the OpenShift cluster available, 
<ol>
<li> Log in "Openshift Console", you can go to your name on top right use "Copy Login Command" to find the below command and use the terminal to login.

```
oc login --token=xxxx --server=xxx
```

<li> Search and install "Authorino" operator, use the one with name `Red Hat - Authorino (Technical Preview)`

```
oc apply -f authorino-subscription.yaml

oc get csv authorino-operator.v1.0.2 -n openshift-operators -o json | jq '.status.phase' # verify it "Succeeded"
```

<li> Search and install "Service Mesh" operator

```
oc apply -f sm-subscription.yaml

oc get csv servicemeshoperator.v2.6.1 -n openshift-operators -o json | jq '.status.phase' # verify it "Succeeded"
```

<li> Search and install Server Less Operator

```
oc apply -f serverless-subscription.yaml

oc get csv serverless-operator.v1.33.2 -n openshift-operators -o json | jq '.status.phase' # verify it "Succeeded"
```

<li> Search and install "Open Data Hub" operator 2.18.1+ (latest is recommended)

```
oc apply -f odh-subscription.yaml

oc get csv opendatahub-operator.v2.18.1 -n openshift-operators -o json | jq '.status.phase'  # verify it "Succeeded"
```

<li> If you are using local storage mechanisams for S3 bucket, try installing "minio" operator and configure its access (TDB..)
<li>
</ol>

## Install "Data Science Cluster" 
Once all the operators all installed and no errors reported, then proceed to installing a "Data Science Cluster". You can navigate "Open data Hub" operator install from left navigation under "Operators --> Installed Operators" and click on "Open Data Hub Operator" and switch to the tab "DSC" and create DSC and make sure edit the YAML set the modelregistry to `managed` state like shown below

```
modelregistry:
    managementState: Managed
```

Or You can also use following script

```
oc apply -f dsci.yaml
oc get dsci default-dsci   # make sure it is ready before proceeding to next step

oc apply -f dsc.yaml
oc get dsc default-dsc -o json | jq '.status.phase' # make sure you see `Ready` state
```

## Install Database (skip if you using existing database)

Model registry currently requires MySQL database 8.0.3 or above to function correctly. If you have a database already available you can skip this section all toghether. For "Development" or "NON-PRODUCTION" scenarios you can use following script to install MySQL database.

```
oc new-project test-database
oc apply -f mysql-db.yaml
oc get deployment model-registry-db    # make sure the database in Ready state
```

## install Model Registry

To install Model Registry use the following script

```
oc project odh-model-registries
oc apply -f registry.yaml
oc get modelregistry modelregistry-public  # make sure the registry is available and running
```

## Dashboard Install

You do not need to install dashboard separately, however if for any reason Dashboard is not showing the Model Registry in the left navigation, you can use following script to enable it

```
oc patch odhdashboardconfig.opendatahub.io odh-dashboard-config -n opendatahub --type merge -p '{"spec": {"dashboardConfig": {"disableModelRegistry": false}}}'
```
## Find Model Registry URL
Model Registry uses service mesh and opens a Gateway for external clients to reach. Execute following to find the URL where Model Registry is available.

```
URL=`echo "https://$(oc get routes -n istio-system -o json | jq '.items[2].status.ingress[0].host')" | tr -d '"'`
```

## Validation 
Now we can validate if everyhing working correctly by executing the following 

```
export TOKEN=`oc whoami -t`
curl -k -H "Authorization: Bearer $TOKEN" $URL/api/model_registry/v1alpha3/registered_models
```

and you should see an output like 

```
{"items":[],"nextPageToken":"","pageSize":0,"size":0}
```

Model Registry is fully installed and ready to go. You can log into ODH Console and find "Model Registry" on left navigation to see the available models.