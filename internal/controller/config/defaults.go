/*
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
*/

package config

import (
	"embed"
	"github.com/spf13/viper"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"text/template"
	"time"
)

//go:embed templates/*.yaml.tmpl
var templateFS embed.FS

const (
	DefaultImageValue = "MustSetInConfig"

	MLPipelineUIConfigMapPrefix       = "ds-pipeline-ui-configmap-"
	ArtifactScriptConfigMapNamePrefix = "ds-pipeline-artifact-script-"
	ArtifactScriptConfigMapKey        = "artifact_script"
	DSPServicePrefix                  = "ds-pipeline"

	DBSecretNamePrefix = "ds-pipeline-db-"
	DBSecretKey        = "password"

	MariaDBName        = "mlpipeline"
	MariaDBHostPrefix  = "mariadb"
	MariaDBHostPort    = "3306"
	MariaDBUser        = "mlpipeline"
	MariaDBNamePVCSize = "10Gi"

	MinioHostPrefix    = "minio"
	MinioPort          = "9000"
	MinioScheme        = "http"
	MinioDefaultBucket = "mlpipeline"
	MinioPVCSize       = "10Gi"

	ObjectStorageSecretName = "mlpipeline-minio-artifact" // hardcoded in kfp-tekton
	ObjectStorageAccessKey  = "accesskey"
	ObjectStorageSecretKey  = "secretkey"

	GrpcImage        = "GRPC_IMAGE"
	RestImage        = "REST_IMAGE"
	DefaultGrpcImage = "gcr.io/tfx-oss-public/ml_metadata_store_server:1.14.0"
	DefaultRestImage = "quay.io/opendatahub/model-registry:latest"
)

// DSPO Config File Paths
const (
	APIServerImagePath            = "Images.ApiServer"
	APIServerArtifactImagePath    = "Images.Artifact"
	PersistenceAgentImagePath     = "Images.PersistentAgent"
	ScheduledWorkflowImagePath    = "Images.ScheduledWorkflow"
	APIServerCacheImagePath       = "Images.Cache"
	APIServerMoveResultsImagePath = "Images.MoveResultsImage"
	MariaDBImagePath              = "Images.MariaDB"
	OAuthProxyImagePath           = "Images.OAuthProxy"
	MlmdEnvoyImagePath            = "Images.MlmdEnvoy"
	MlmdGRPCImagePath             = "Images.MlmdGRPC"
	MlmdWriterImagePath           = "Images.MlmdWriter"
)

// ModelRegistry Status Condition Types
const (
	DatabaseAvailable      = "DatabaseAvailable"
	ObjectStoreAvailable   = "ObjectStoreAvailable"
	APIServerReady         = "APIServerReady"
	PersistenceAgentReady  = "PersistenceAgentReady"
	ScheduledWorkflowReady = "ScheduledWorkflowReady"
	CrReady                = "Ready"
)

// ModelRegistry Ready Status Condition Reasons
// As per k8s api convention: Reason is intended
// to be used in concise output, such as one-line
// kubectl get output, and in summarizing
// occurrences of causes
const (
	MinimumReplicasAvailable    = "MinimumReplicasAvailable"
	FailingToDeploy             = "FailingToDeploy"
	Deploying                   = "Deploying"
	ComponentDeploymentNotFound = "ComponentDeploymentNotFound"
)

// Any required Configmap paths can be added here,
// they will be automatically included for required
// validation check
var requiredFields = []string{
	APIServerImagePath,
	APIServerArtifactImagePath,
	PersistenceAgentImagePath,
	ScheduledWorkflowImagePath,
	APIServerCacheImagePath,
	APIServerMoveResultsImagePath,
	MariaDBImagePath,
	OAuthProxyImagePath,
}

// DefaultDBConnectionTimeout is the default DB storage healthcheck timeout
const DefaultDBConnectionTimeout = time.Second * 15

// DefaultObjStoreConnectionTimeout is the default Object storage healthcheck timeout
const DefaultObjStoreConnectionTimeout = time.Second * 15

const DefaultMaxConcurrentReconciles = 10

const MlmdGrpcPort = 8080

const MlmdRestPort = 9090

func GetConfigRequiredFields() []string {
	return requiredFields
}

// Default ResourceRequirements
var (
	MlmdRestResourceRequirements = createResourceRequirement(resource.MustParse("100m"), resource.MustParse("256Mi"), resource.MustParse("100m"), resource.MustParse("256Mi"))
	MlmdGRPCResourceRequirements = createResourceRequirement(resource.MustParse("100m"), resource.MustParse("256Mi"), resource.MustParse("100m"), resource.MustParse("256Mi"))
)

func createResourceRequirement(RequestsCPU resource.Quantity, RequestsMemory resource.Quantity, LimitsCPU resource.Quantity, LimitsMemory resource.Quantity) v1.ResourceRequirements {
	return v1.ResourceRequirements{
		Requests: v1.ResourceList{
			"cpu":    RequestsCPU,
			"memory": RequestsMemory,
		},
		Limits: v1.ResourceList{
			"cpu":    LimitsCPU,
			"memory": LimitsMemory,
		},
	}
}

func GetStringConfigWithDefault(configName, value string) string {
	if !viper.IsSet(configName) || len(viper.GetString(configName)) == 0 {
		return value
	}
	return viper.GetString(configName)
}

func ParseTemplates() (*template.Template, error) {
	template, err := template.ParseFS(templateFS, "templates/*.yaml.tmpl")
	if err != nil {
		return nil, err
	}
	return template, err
}
