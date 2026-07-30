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
	"flag"
	"os"

	"github.com/spf13/cobra"

	"github.com/opendatahub-io/model-registry-operator/internal/setup"
	"github.com/opendatahub-io/model-registry-operator/internal/webhook"

	routev1 "github.com/openshift/api/route/v1"
	tlspkg "github.com/openshift/controller-runtime-common/pkg/tls"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"

	oapiconfig "github.com/openshift/api/config/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/opendatahub-io/model-registry-operator/internal/controller"
	"github.com/opendatahub-io/model-registry-operator/internal/migration"
)

var (
	metricsAddr          string
	metricsCertDir       string
	metricsCertName      string
	metricsKeyName       string
	secureMetrics        bool
	enableLeaderElection bool
	probeAddr            string
)

// modelRegistryCmd runs the ModelRegistry and ModelCatalog controllers in a
// single manager. This is the historical behavior of the operator binary and
// is also what runs when the binary is invoked without a subcommand.
var modelRegistryCmd = &cobra.Command{
	Use:   "modelregistry",
	Short: "Run the Model Registry operator (ModelRegistry and ModelCatalog controllers)",
	Long: `Starts the ModelRegistry and ModelCatalog controllers in a single manager.

This is the default behavior when the binary is invoked without a subcommand.`,
	RunE: runModelRegistry,
}

// bindModelRegistryFlags registers the modelregistry flags (including zap
// logger options) on the provided flag set.
func bindModelRegistryFlags(fs *flag.FlagSet) {
	fs.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "The address the metric endpoint binds to.")
	fs.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	fs.StringVar(&metricsCertDir, "metrics-cert-dir", "", "The directory that contains the metrics endpoint key and certificate.\n"+
		"Generates and uses a self-signed certificate if not specified.\n"+
		"MUST be specified in production.")
	fs.StringVar(&metricsCertName, "metrics-cert-name", "", "The metrics endpoint server certificate filename.")
	fs.StringVar(&metricsKeyName, "metrics-key-name", "", "The metrics endpoint key filename.")

	fs.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	fs.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	zapOpts.BindFlags(fs)
}

func runModelRegistry(_ *cobra.Command, _ []string) error {
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

	// On OpenShift, fetch the cluster TLS security profile for webhook and metrics servers
	tlsResult, err := setup.ConfigureTLS(scheme, capabilities.HasConfigAPI, setupLog)
	if err != nil {
		setupLog.Error(err, "unable to configure TLS")
		os.Exit(1)
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

	registriesNamespace := os.Getenv(config.RegistriesNamespace)
	enableWebhooks := os.Getenv(config.EnableWebhooks) != "false"
	defaultDomain := os.Getenv(config.DefaultDomain)
	setupLog.Info("default registry config", config.RegistriesNamespace, registriesNamespace, config.DefaultDomain, defaultDomain)

	// set default values for defaulting webhook
	config.SetRegistriesNamespace(registriesNamespace)

	// Only cache the instances of these objects that are created by this operator.
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
			&networkingv1.NetworkPolicy{}:   objOptions,
			&rbacv1.ClusterRoleBinding{}:    objOptions,
			&rbacv1.RoleBinding{}:           objOptions,
			&rbacv1.Role{}:                  objOptions,
			// ConfigMaps: cache all in the target namespace (no label filter)
			// because user-created catalog source ConfigMaps won't have operator labels.
			// Namespace-scoping keeps the cache bounded.
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
		// Secrets are read but not created by the operator (user DB credentials, TLS certs),
		// so they can't be label-filtered like operator-created resources above.
		// DisableFor bypasses the cache for Secrets, using direct API reads instead.
		// This is safe because no Owns()/Watches()/For() registers an informer for Secrets.
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
		LeaderElectionID:       "85f368d1.opendatahub.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
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

	mgrRestConfig := mgr.GetConfig()
	client := mgr.GetClient()

	clientset, err := kubernetes.NewForConfig(mgrRestConfig)
	if err != nil {
		setupLog.Error(err, "error getting kubernetes clientset")
		os.Exit(1)
	}

	config.SetDefaultDomain(defaultDomain, mgr.GetClient(), capabilities.IsOpenShift)

	gatewayDomain := os.Getenv(config.GatewayDomainEnv)
	gatewayName := config.GetStringConfigWithDefault(config.GatewayNameEnv, config.DefaultGatewayName)
	gatewayNamespace := config.GetStringConfigWithDefault(config.GatewayNamespaceEnv, config.DefaultGatewayNamespace)
	httpRouteNamespace := config.GetStringConfigWithDefault(config.HTTPRouteNamespaceEnv, config.DefaultHTTPRouteNamespace)
	setupLog.Info("gateway config", config.GatewayDomainEnv, gatewayDomain,
		config.GatewayNameEnv, gatewayName, config.GatewayNamespaceEnv, gatewayNamespace,
		config.HTTPRouteNamespaceEnv, httpRouteNamespace)
	if gatewayDomain == "" {
		hasPartialGatewayConfig := os.Getenv(config.GatewayNameEnv) != "" ||
			os.Getenv(config.GatewayNamespaceEnv) != "" ||
			os.Getenv(config.HTTPRouteNamespaceEnv) != ""
		if hasPartialGatewayConfig {
			setupLog.Info("WARNING: gateway-related env vars are set but GATEWAY_DOMAIN is empty — gateway mode is disabled, set GATEWAY_DOMAIN to enable it")
		}
	}

	if err = (&controller.ModelRegistryReconciler{
		Client:             client,
		ClientSet:          clientset,
		Scheme:             mgr.GetScheme(),
		Recorder:           mgr.GetEventRecorder("modelregistry-controller"),
		Log:                ctrl.Log.WithName("controller"),
		Template:           template,
		EnableWebhooks:     enableWebhooks,
		Capabilities:       capabilities,
		GatewayDomain:      gatewayDomain,
		GatewayName:        gatewayName,
		GatewayNamespace:   gatewayNamespace,
		HTTPRouteNamespace: httpRouteNamespace,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ModelRegistry")
		os.Exit(1)
	}

	enableModelCatalog := os.Getenv(config.EnableModelCatalog) != "false"
	skipCatalogDBCreation := config.GetBoolConfigWithDefault(config.SkipModelCatalogDBCreation, false)
	setupLog.Info("model catalog config", "enabled", enableModelCatalog, "db_enabled", !skipCatalogDBCreation)

	if err = (&controller.ModelCatalogReconciler{
		Client:                client,
		Scheme:                mgr.GetScheme(),
		Recorder:              mgr.GetEventRecorder("modelcatalog-controller"),
		Log:                   ctrl.Log.WithName("modelcatalog-controller"),
		Template:              template,
		Capabilities:          capabilities,
		TargetNamespace:       config.GetRegistriesNamespace(),
		Enabled:               enableModelCatalog,
		SkipCatalogDBCreation: skipCatalogDBCreation,
		GatewayDomain:         gatewayDomain,
		GatewayName:           gatewayName,
		GatewayNamespace:      gatewayNamespace,
		HTTPRouteNamespace:    httpRouteNamespace,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ModelCatalog")
		os.Exit(1)
	}
	if enableWebhooks {
		if err = webhook.SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "ModelRegistry")
			os.Exit(1)
		}
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Register SecurityProfileWatcher on OpenShift: cancel context on TLS profile change so pod restarts
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

	// Start storage migration monitor
	migrationMgr := migration.NewStorageMigrationManager(mgr.GetClient())
	migrationMgr.StartMigrationMonitor(ctx)

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}

	return nil
}
