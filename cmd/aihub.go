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

package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/opendatahub-io/model-registry-operator/internal/controller"
	"github.com/opendatahub-io/model-registry-operator/internal/setup"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
	platformlabels "github.com/opendatahub-io/odh-platform-utilities/pkg/metadata/labels"
	"github.com/spf13/cobra"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const manifestsTemplatePath = "/opt/manifests-template"

var aihubCmd = &cobra.Command{
	Use:   "aihub",
	Short: "Run the AI Hub operator",
	Long: `Runs the AI Hub operator as an independent process.

This subcommand starts the AIHub controller that reconciles AIHub CRs.`,
	RunE: runAIHub,
}

func runAIHub(_ *cobra.Command, _ []string) error {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	if fi, err := os.Stat(manifestsTemplatePath); err != nil {
		return fmt.Errorf("manifests template path is not accessible %q: %w", manifestsTemplatePath, err)
	} else if !fi.IsDir() {
		return fmt.Errorf("manifests template path is not a directory: %s", manifestsTemplatePath)
	}

	capabilities, err := setup.GetCapabilities()
	if err != nil {
		return fmt.Errorf("error detecting cluster capabilities: %w", err)
	}
	setupLog.Info("cluster capabilities detected",
		"isOpenShift", capabilities.IsOpenShift,
		"hasUserAPI", capabilities.HasUserAPI,
		"hasConfigAPI", capabilities.HasConfigAPI,
		"hasAuthAPI", capabilities.HasAuthAPI)

	// On OpenShift, fetch the cluster TLS security profile for the metrics server
	tlsResult, err := setup.ConfigureTLS(scheme, capabilities.HasConfigAPI, setupLog)
	if err != nil {
		return fmt.Errorf("unable to configure TLS: %w", err)
	}

	// set metrics server options, including custom cert if provided
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		CertDir:       metricsCertDir,
		CertName:      metricsCertName,
		KeyName:       metricsKeyName,
		TLSOpts:       tlsResult.Opts,
	}
	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	leaderNS := os.Getenv("POD_NAMESPACE")
	if enableLeaderElection && leaderNS == "" {
		return errors.New("leader election requires POD_NAMESPACE to be set")
	}

	// Scope the cache for high-cardinality Deployer-managed types to only
	// resources stamped with the part-of label by the Deployer.
	deployerSelector := k8slabels.SelectorFromSet(map[string]string{
		platformlabels.PlatformPartOf: "aihub",
	})
	deployerCacheObj := cache.ByObject{Label: deployerSelector}

	// The AIHub operator's own metrics ServiceMonitor is created at runtime by
	// the reconciler (RHOAIENG-88196) rather than shipped in the module
	// bundle, and applied through the Deployer like every other type above.
	// Registered as unstructured (not the typed prometheus-operator type) to
	// avoid adding that dependency + scheme registration just for this cache
	// scoping key.
	serviceMonitor := &unstructured.Unstructured{}
	serviceMonitor.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "monitoring.coreos.com",
		Version: "v1",
		Kind:    "ServiceMonitor",
	})

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&appsv1.Deployment{}:         deployerCacheObj,
				&corev1.Service{}:            deployerCacheObj,
				&corev1.ServiceAccount{}:     deployerCacheObj,
				&corev1.ConfigMap{}:          deployerCacheObj,
				&rbacv1.Role{}:               deployerCacheObj,
				&rbacv1.RoleBinding{}:        deployerCacheObj,
				&rbacv1.ClusterRole{}:        deployerCacheObj,
				&rbacv1.ClusterRoleBinding{}: deployerCacheObj,
				&admissionregistrationv1.ValidatingWebhookConfiguration{}: deployerCacheObj,
				&admissionregistrationv1.MutatingWebhookConfiguration{}:   deployerCacheObj,
				serviceMonitor: deployerCacheObj,
			},
		},
		Metrics:                 metricsServerOptions,
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "aihub-controller-manager",
		LeaderElectionNamespace: leaderNS,
	})
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	setupLog.Info("setting up aihub controller")
	if err = (&controller.AIHubReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		ManifestsTemplatePath: manifestsTemplatePath,
		Getenv:                os.Getenv,
		APIReader:             mgr.GetAPIReader(),
		HasServiceMonitorCRD:  capabilities.HasServiceMonitor,
		Deployer: deploy.NewDeployer(
			deploy.WithFieldOwner("aihub"),
			deploy.WithApplyOrder(),
			deploy.WithCache(),
			deploy.WithMergeStrategy(
				schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
				deploy.MergeDeployments,
			),
			// Adopt resources previously deployed and owned by the legacy
			// opendatahub-operator ModelRegistry *component* (pre component→module
			// migration). ownerReferences is a merge-by-uid list, so SSA alone cannot
			// drop the stale ModelRegistry controller owner-ref written by a different
			// field manager; WithLegacyOwners removes it before AIHub takes ownership.
			deploy.WithLegacyOwners(
				schema.GroupVersionKind{
					Group:   "components.platform.opendatahub.io",
					Version: "v1alpha1",
					Kind:    "ModelRegistry",
				},
			),
		),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create aihub controller: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("unable to run the manager: %w", err)
	}

	return nil
}
