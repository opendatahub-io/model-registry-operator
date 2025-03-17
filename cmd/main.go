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

package main

import (
	"context"
	"flag"
	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"
	networking "istio.io/client-go/pkg/apis/networking/v1beta1"
	security "istio.io/client-go/pkg/apis/security/v1beta1"
	authentication "k8s.io/api/authentication/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	oapi "github.com/openshift/api"
	oapiconfig "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	modelregistryv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/controller"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	// openshift scheme
	utilruntime.Must(oapi.Install(scheme))
	utilruntime.Must(oapiconfig.Install(scheme))
	// istio security scheme
	utilruntime.Must(security.AddToScheme(scheme))
	// istio networking scheme
	utilruntime.Must(networking.AddToScheme(scheme))

	utilruntime.Must(modelregistryv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var metricsCertDir string
	var metricsCertName string
	var metricsKeyName string
	var secureMetrics bool

	var enableLeaderElection bool
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "The address the metric endpoint binds to.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&metricsCertDir, "metrics-cert-dir", "", "The directory that contains the metrics endpoint key and certificate.\n"+
		"Generates and uses a self-signed certificate if not specified.\n"+
		"MUST be specified in production.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "", "The metrics endpoint server certificate filename.")
	flag.StringVar(&metricsKeyName, "metrics-key-name", "", "The metrics endpoint key filename.")

	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// set metrics server options, including custom cert if provided
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		CertDir:       metricsCertDir,
		CertName:      metricsCertName,
		KeyName:       metricsKeyName,
	}
	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
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
	tokenReview := &authentication.TokenReview{
		Spec: authentication.TokenReviewSpec{
			Token: mgrRestConfig.BearerToken,
		},
	}
	ctx := context.Background()
	err = client.Create(ctx, tokenReview)
	if err != nil {
		setupLog.Error(err, "error getting controller serviceaccount audience")
		os.Exit(1)
	}
	setupLog.Info("default authorino authconfig audiences", "audiences", tokenReview.Status.Audiences)

	discoveryClient := discovery.NewDiscoveryClientForConfigOrDie(mgrRestConfig)
	groups, err := discoveryClient.ServerGroups()
	if err != nil {
		setupLog.Error(err, "error discovering server groups")
		os.Exit(1)
	}
	isOpenShift := false
	hasAuthorino := false
	hasIstio := false
	for _, g := range groups.Groups {
		if g.Name == "route.openshift.io" {
			isOpenShift = true
		}
		if g.Name == "authorino.kuadrant.io" {
			hasAuthorino = true
		}
		if g.Name == "networking.istio.io" {
			hasIstio = true
		}
	}
	setupLog.Info("cluster config", "isOpenShift", isOpenShift, "hasAuthorino", hasAuthorino, "hasIstio", hasIstio)

	clientset, err := kubernetes.NewForConfig(mgrRestConfig)
	if err != nil {
		setupLog.Error(err, "error getting kubernetes clientset")
		os.Exit(1)
	}

	registriesNamespace := os.Getenv(config.RegistriesNamespace)
	enableWebhooks := os.Getenv(config.EnableWebhooks) != "false"
	createAuthResources := os.Getenv(config.CreateAuthResources) != "false"
	defaultDomain := os.Getenv(config.DefaultDomain)
	defaultCert := os.Getenv(config.DefaultCert)
	setupLog.Info("default registry config", config.RegistriesNamespace, registriesNamespace, config.DefaultDomain, defaultDomain, config.DefaultCert, defaultCert)

	// default auth env variables
	defaultAuthProvider := os.Getenv(config.DefaultAuthProvider)
	defaultAuthConfigLabelsString := os.Getenv(config.DefaultAuthConfigLabels)
	setupLog.Info("default registry authorino config", config.DefaultAuthProvider, defaultAuthProvider, config.DefaultAuthConfigLabels, defaultAuthConfigLabelsString)

	// default smcp env variables
	defaultControlPlane := os.Getenv(config.DefaultControlPlane)
	defaultIstioIngress := os.Getenv(config.DefaultIstioIngress)
	setupLog.Info("default registry istio config", config.DefaultControlPlane, defaultControlPlane, config.DefaultIstioIngress, defaultIstioIngress)

	// set default values for defaulting webhook
	config.SetRegistriesNamespace(registriesNamespace)
	config.SetDefaultDomain(defaultDomain, mgr.GetClient(), isOpenShift)
	config.SetDefaultAudiences(tokenReview.Status.Audiences)
	config.SetDefaultCert(defaultCert)
	config.SetDefaultAuthProvider(defaultAuthProvider)
	config.SetDefaultAuthConfigLabels(defaultAuthConfigLabelsString)
	config.SetDefaultControlPlane(defaultControlPlane)
	config.SetDefaultIstioIngress(defaultIstioIngress)

	if err = (&controller.ModelRegistryReconciler{
		Client:              client,
		ClientSet:           clientset,
		Scheme:              mgr.GetScheme(),
		Recorder:            mgr.GetEventRecorderFor("modelregistry-controller"),
		Log:                 ctrl.Log.WithName("controller"),
		Template:            template,
		EnableWebhooks:      enableWebhooks,
		IsOpenShift:         isOpenShift,
		HasIstio:            hasAuthorino && hasIstio,
		CreateAuthResources: createAuthResources,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ModelRegistry")
		os.Exit(1)
	}
	if enableWebhooks {
		if err = (&modelregistryv1alpha1.ModelRegistry{}).SetupWebhookWithManager(mgr); err != nil {
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

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
