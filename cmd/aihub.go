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
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
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

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
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
		Deployer: deploy.NewDeployer(
			deploy.WithFieldOwner("aihub"),
			deploy.WithApplyOrder(),
			deploy.WithCache(),
			deploy.WithMergeStrategy(
				schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
				deploy.MergeDeployments,
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
