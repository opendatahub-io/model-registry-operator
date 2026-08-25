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
	"context"
	"os"

	"github.com/opendatahub-io/model-registry-operator/internal/controller"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	"github.com/opendatahub-io/model-registry-operator/internal/setup"
	"github.com/opendatahub-io/model-registry-operator/internal/webhook"
	oapiconfig "github.com/openshift/api/config/v1"
	routev1 "github.com/openshift/api/route/v1"
	tlspkg "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
)

var catalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "Run the Model Catalog operator",
	Long:  `Runs the Model Catalog operator as an independent process.`,
	RunE:  runCatalog,
}

func runCatalog(_ *cobra.Command, _ []string) error {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	capabilities, err := setup.GetCapabilities()
	if err != nil {
		setupLog.Error(err, "error detecting cluster capabilities")
		os.Exit(1)
	}
	setupLog.Info("cluster capabilities detected",
		"isOpenShift", capabilities.IsOpenShift,
		"hasUserAPI", capabilities.HasUserAPI,
		"hasConfigAPI", capabilities.HasConfigAPI,
		"hasAuthAPI", capabilities.HasAuthAPI)

	tlsResult, err := setup.ConfigureTLS(scheme, capabilities.HasConfigAPI, setupLog)
	if err != nil {
		setupLog.Error(err, "unable to configure TLS")
		os.Exit(1)
	}

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

	registriesNamespace := os.Getenv(config.RegistriesNamespace)
	enableWebhooks := os.Getenv(config.EnableWebhooks) != "false"
	defaultDomain := os.Getenv(config.DefaultDomain)
	setupLog.Info("default catalog config", config.RegistriesNamespace, registriesNamespace, config.DefaultDomain, defaultDomain)

	config.SetRegistriesNamespace(registriesNamespace)

	objOptions := cache.ByObject{
		Label: labels.SelectorFromSet(labels.Set{
			"app.kubernetes.io/created-by": "model-registry-operator",
		}),
	}
	cacheOptions := cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&appsv1.Deployment{}:            objOptions,
			&corev1.PersistentVolumeClaim{}: objOptions,
			&corev1.ServiceAccount{}:        objOptions,
			&corev1.Service{}:               objOptions,
			&corev1.Secret{}:                objOptions,
			&networkingv1.NetworkPolicy{}:   objOptions,
			&rbacv1.ClusterRoleBinding{}:    objOptions,
			&rbacv1.RoleBinding{}:           objOptions,
			&rbacv1.Role{}:                  objOptions,
			&corev1.ConfigMap{}: {
				Namespaces: map[string]cache.Config{
					registriesNamespace: {},
				},
			},
		},
	}

	if capabilities.IsOpenShift {
		cacheOptions.ByObject[&routev1.Route{}] = objOptions
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Cache:  cacheOptions,
		Client: client.Options{
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{&corev1.Secret{}},
			},
		},
		Metrics: metricsServerOptions,
		WebhookServer: ctrlwebhook.NewServer(ctrlwebhook.Options{
			TLSOpts: tlsResult.Opts,
		}),
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "catalog.opendatahub.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	template, err := config.ParseTemplates()
	if err != nil {
		setupLog.Error(err, "error parsing kubernetes resource templates")
		os.Exit(1)
	}
	setupLog.Info("parsed kubernetes templates", "templates", template.DefinedTemplates())

	config.SetDefaultDomain(defaultDomain, mgr.GetClient(), capabilities.IsOpenShift)

	gatewayDomain := os.Getenv(config.GatewayDomainEnv)
	gatewayName := config.GetStringConfigWithDefault(config.GatewayNameEnv, config.DefaultGatewayName)
	gatewayNamespace := config.GetStringConfigWithDefault(config.GatewayNamespaceEnv, config.DefaultGatewayNamespace)
	httpRouteNamespace := config.GetStringConfigWithDefault(config.HTTPRouteNamespaceEnv, config.DefaultHTTPRouteNamespace)

	skipCatalogDBCreation := config.GetBoolConfigWithDefault(config.SkipModelCatalogDBCreation, false)

	if err = (&controller.CatalogReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		Recorder:              mgr.GetEventRecorder("catalog-controller"),
		Log:                   ctrl.Log.WithName("catalog-controller"),
		Template:              template,
		Capabilities:          capabilities,
		SkipCatalogDBCreation: skipCatalogDBCreation,
		GatewayDomain:         gatewayDomain,
		GatewayName:           gatewayName,
		GatewayNamespace:      gatewayNamespace,
		HTTPRouteNamespace:    httpRouteNamespace,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Catalog")
		os.Exit(1)
	}

	if enableWebhooks {
		if err = webhook.SetupCatalogWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Catalog")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancel()
	if capabilities.HasConfigAPI {
		watcher := &tlspkg.SecurityProfileWatcher{
			Client:                mgr.GetClient(),
			InitialTLSProfileSpec: tlsResult.Profile,
			OnProfileChange: func(_ context.Context, _, _ oapiconfig.TLSProfileSpec) {
				setupLog.Info("TLS profile changed, initiating graceful shutdown to reload")
				cancel()
			},
		}
		if tlsResult.AdherenceFetched {
			watcher.InitialTLSAdherencePolicy = tlsResult.AdherencePolicy
			watcher.OnAdherencePolicyChange = func(_ context.Context, _, _ oapiconfig.TLSAdherencePolicy) {
				setupLog.Info("TLS adherence policy changed, initiating shutdown to reload")
				cancel()
			}
		}
		if err := watcher.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to register TLS security profile watcher")
			os.Exit(1)
		}
	}

	setupLog.Info("starting catalog manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running catalog manager")
		os.Exit(1)
	}

	return nil
}
