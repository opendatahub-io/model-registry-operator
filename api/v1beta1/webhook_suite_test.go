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

package v1beta1_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/api/v1beta1"
	mrwebhook "github.com/opendatahub-io/model-registry-operator/internal/webhook"

	"github.com/opendatahub-io/model-registry-operator/internal/controller/config"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"

	//+kubebuilder:scaffold:imports
	apimachineryruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	namespaceBase = "webhook-test-ns"
	mrNameBase    = "webhook-test"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var ctx context.Context
var cancel context.CancelFunc

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Webhook Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: false,

		// The BinaryAssetsDirectory is only required if you want to run the tests directly
		// without call the makefile target test. If not informed it will look for the
		// default path defined in controller-runtime which is /usr/local/kubebuilder/.
		// Note that you must have the required binaries setup under the bin directory to perform
		// the tests directly. When we run make test it will be setup and used automatically.
		BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.28.0-%s-%s", runtime.GOOS, runtime.GOARCH)),

		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "config", "webhook")},
		},
	}

	var err error
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	scheme := apimachineryruntime.NewScheme()
	err = v1alpha1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())
	err = v1beta1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	err = admissionv1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	err = corev1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// start webhook server using Manager
	webhookInstallOptions := &testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookInstallOptions.LocalServingHost,
			Port:    webhookInstallOptions.LocalServingPort,
			CertDir: webhookInstallOptions.LocalServingCertDir,
		}),
		LeaderElection: false,
		Metrics:        metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())

	err = mrwebhook.SetupWebhookWithManager(mgr)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:webhook

	go func() {
		defer GinkgoRecover()
		err = mgr.Start(ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// wait for the webhook server to get ready
	dialer := &net.Dialer{Timeout: time.Second}
	addrPort := fmt.Sprintf("%s:%d", webhookInstallOptions.LocalServingHost, webhookInstallOptions.LocalServingPort)
	Eventually(func() error {
		conn, err := tls.DialWithDialer(dialer, "tcp", addrPort, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			return err
		}
		conn.Close()
		return nil
	}).Should(Succeed())

})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

var _ = Describe("Model Registry validating webhook", func() {
	// TODO add tests for defaulting webhook and MR config validation

	It("Should not allow creation of duplicate MR instance in cluster", func(ctx context.Context) {
		Eventually(func() error {
			config.SetRegistriesNamespace("") // run this test with no ns restrictions
			suffix1 := "-mr1"
			suffix2 := "-mr2"
			// create mr1
			mr1 := newModelRegistry(ctx, mrNameBase+suffix1, namespaceBase+suffix1)
			Expect(k8sClient.Create(ctx, mr1)).Should(Succeed())
			// mr1 creation with same name and suffix, in another ns should fail
			mr2 := newModelRegistry(ctx, mrNameBase+suffix1, namespaceBase+suffix2)
			Expect(k8sClient.Create(ctx, mr2)).ShouldNot(Succeed())
			// mr2 creation with another suffix, in another ns should succeed
			mr2 = newModelRegistry(ctx, mrNameBase+suffix2, namespaceBase+suffix2)
			Expect(k8sClient.Create(ctx, mr2)).Should(Succeed())

			return nil
		}).Should(Succeed())
	})

	It("Should not allow creation of MR instance with invalid database config", func(ctx context.Context) {
		Eventually(func() error {
			config.SetRegistriesNamespace(namespaceBase)
			mr := newModelRegistry(ctx, mrNameBase+"-invalid-db-create", namespaceBase)
			mr.Spec = v1beta1.ModelRegistrySpec{
				MySQL: &v1beta1.MySQLConfig{},
			}

			Expect(k8sClient.Create(ctx, mr)).ShouldNot(Succeed())

			return nil
		}).Should(Succeed())
	})

	It("Should not allow update of MR instance with invalid database config", func(ctx context.Context) {
		Eventually(func() error {
			config.SetRegistriesNamespace(namespaceBase)
			mr := newModelRegistry(ctx, mrNameBase+"-invalid-db-update", namespaceBase)
			Expect(k8sClient.Create(ctx, mr)).Should(Succeed())

			mr.Spec = v1beta1.ModelRegistrySpec{
				MySQL: &v1beta1.MySQLConfig{},
			}

			Expect(k8sClient.Update(ctx, mr)).ShouldNot(Succeed())

			return nil
		}).Should(Succeed())
	})

	It("Should not allow creating MR instance in a different namespace when registries namespace is set", func(ctx context.Context) {
		Eventually(func() error {
			config.SetRegistriesNamespace(namespaceBase)
			mr := newModelRegistry(ctx, mrNameBase, namespaceBase)
			Expect(k8sClient.Create(ctx, mr)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, mr)).Should(Succeed())

			// use a different namespace
			mr.Namespace = namespaceBase + "-invalid"
			Expect(k8sClient.Create(ctx, mr)).ShouldNot(Succeed())

			return nil
		}).Should(Succeed())
	})

	It("Should support creating MR instance with OAuth Proxy configured", func(ctx context.Context) {
		Eventually(func() error {
			config.SetRegistriesNamespace(namespaceBase)
			mr := newModelRegistry(ctx, mrNameBase, namespaceBase)
			mr.Spec.OAuthProxy = &v1beta1.OAuthProxyConfig{}
			Expect(k8sClient.Create(ctx, mr)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, mr)).Should(Succeed())

			return nil
		}).Should(Succeed())
	})

	It("Should support creating MR instance with KubeRBACProxy configured", func(ctx context.Context) {
		Eventually(func() error {
			config.SetRegistriesNamespace(namespaceBase)
			mr := newModelRegistry(ctx, mrNameBase, namespaceBase)
			mr.Spec.KubeRBACProxy = &v1beta1.KubeRBACProxyConfig{}
			Expect(k8sClient.Create(ctx, mr)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, mr)).Should(Succeed())

			return nil
		}).Should(Succeed())
	})
})

func newModelRegistry(ctx context.Context, name string, namespace string) *v1beta1.ModelRegistry {
	// create test namespace
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}))).Should(Succeed())

	// return
	return &v1beta1.ModelRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1beta1.ModelRegistrySpec{
			Rest: v1beta1.RestSpec{},
			Grpc: v1beta1.GrpcSpec{},
			MySQL: &v1beta1.MySQLConfig{
				Host:     "test-db",
				Username: "test-user",
				PasswordSecret: &v1beta1.SecretKeyValue{
					Name: "test-secret",
					Key:  "test-key",
				},
				Database: "test-db",
			},
		},
	}
}

var _ = Describe("ModelRegistry Conversion Webhook", func() {
	var (
		oldObj               *v1alpha1.ModelRegistry
		expectedObj          *v1beta1.ModelRegistry
		expectedConvertedObj *v1alpha1.ModelRegistry
		testNamespace        corev1.Namespace
	)

	BeforeEach(func(ctx context.Context) {
		httpsPort := int32(v1alpha1.DefaultHttpsPort)
		httpsRoutePort := int32(v1alpha1.DefaultRoutePort)

		oldObj = &v1alpha1.ModelRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-v1alpha1-mr",
				Namespace: namespaceBase,
			},
			Spec: v1alpha1.ModelRegistrySpec{
				Rest: v1alpha1.RestSpec{},
				Grpc: v1alpha1.GrpcSpec{},
				MySQL: &v1alpha1.MySQLConfig{
					Host:     "test-db",
					Username: "test-user",
					PasswordSecret: &v1alpha1.SecretKeyValue{
						Name: "test-secret",
						Key:  "test-key",
					},
					Database: "test-db",
				},
				Istio: &v1alpha1.IstioConfig{
					Gateway: &v1alpha1.GatewayConfig{
						Rest: v1alpha1.ServerConfig{TLS: &v1alpha1.TLSServerSettings{}},
						Grpc: v1alpha1.ServerConfig{TLS: &v1alpha1.TLSServerSettings{}},
					},
				},
			},
			Status: v1alpha1.ModelRegistryStatus{
				Hosts:        []string{"host1", "host2"},
				HostsStr:     "host1,host2",
				SpecDefaults: "{\"foo\":\"bar\"}",
				Conditions:   []metav1.Condition{{Type: "Ready", Status: "True"}},
			},
		}
		restPort := int32(8080)
		grpcPort := int32(9090)
		mysqlPort := int32(3306)
		expectedObj = &v1beta1.ModelRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-v1alpha1-mr",
				Namespace: namespaceBase,
			},
			Spec: v1beta1.ModelRegistrySpec{
				Rest: v1beta1.RestSpec{
					Port:         &restPort,
					ServiceRoute: config.RouteDisabled,
				},
				Grpc: v1beta1.GrpcSpec{
					Port: &grpcPort,
				},
				MySQL: &v1beta1.MySQLConfig{
					Host:     "test-db",
					Port:     &mysqlPort,
					Username: "test-user",
					PasswordSecret: &v1beta1.SecretKeyValue{
						Name: "test-secret",
						Key:  "test-key",
					},
					Database: "test-db",
				},
				OAuthProxy: &v1beta1.OAuthProxyConfig{
					Port:         &httpsPort,
					ServiceRoute: config.RouteEnabled,
					RoutePort:    &httpsRoutePort,
				},
			},
			Status: v1beta1.ModelRegistryStatus{
				Hosts:        []string{"host1", "host2"},
				HostsStr:     "host1,host2",
				SpecDefaults: "{\"foo\":\"bar\"}",
				Conditions:   []metav1.Condition{{Type: "Ready", Status: "True"}},
			},
		}
		expectedConvertedObj = &v1alpha1.ModelRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-v1alpha1-mr",
				Namespace: namespaceBase,
			},
			Spec: v1alpha1.ModelRegistrySpec{
				Rest: v1alpha1.RestSpec{
					Port:         &restPort,
					ServiceRoute: config.RouteDisabled,
				},
				Grpc: v1alpha1.GrpcSpec{
					Port: &grpcPort,
				},
				MySQL: &v1alpha1.MySQLConfig{
					Host:     "test-db",
					Port:     &mysqlPort,
					Username: "test-user",
					PasswordSecret: &v1alpha1.SecretKeyValue{
						Name: "test-secret",
						Key:  "test-key",
					},
					Database: "test-db",
				},
				OAuthProxy: &v1alpha1.OAuthProxyConfig{
					Port:         &httpsPort,
					ServiceRoute: config.RouteEnabled,
					RoutePort:    &httpsRoutePort,
				},
			},
			Status: v1alpha1.ModelRegistryStatus{
				Hosts:        []string{"host1", "host2"},
				HostsStr:     "host1,host2",
				SpecDefaults: "{\"foo\":\"bar\"}",
				Conditions:   []metav1.Condition{{Type: "Ready", Status: "True"}},
			},
		}

		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(expectedObj).NotTo(BeNil(), "Expected expectedObj to be initialized")
	})

	AfterEach(func(ctx context.Context) {
		// remove oldObj
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, oldObj))).Should(Succeed())
	})

	Context("When creating ModelRegistry under Conversion Webhook", func() {
		It("Should convert model registry v1alpha1 to v1beta1 and back correctly", func(ctx context.Context) {

			// create test namespace
			key := client.ObjectKey{Namespace: oldObj.Namespace, Name: oldObj.Name}
			testNamespace = corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: namespaceBase},
			}
			config.SetRegistriesNamespace(namespaceBase)
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &testNamespace))).Should(Succeed())

			// create oldObj
			Expect(k8sClient.Create(ctx, oldObj)).To(Succeed())

			// from v1alpha1 to v1beta1
			newObj := v1beta1.ModelRegistry{}
			Expect(k8sClient.Get(ctx, key, &newObj)).To(Succeed())
			Expect(newObj).ToNot(BeNil())
			Expect(newObj.GetObjectKind()).To(Equal(expectedObj.GetObjectKind()))
			Expect(newObj.Spec).To(Equal(expectedObj.Spec))
			// TODO figure out why status conversion is not working in suite test
			//Expect(newObj.Status).To(Equal(expectedObj.Status))

			// from v1beta1 back to v1alpha1
			oldConvertedObj := v1alpha1.ModelRegistry{}
			Expect(k8sClient.Get(ctx, key, &oldConvertedObj)).To(Succeed())
			Expect(oldConvertedObj).ToNot(BeNil())
			Expect(oldConvertedObj.GetObjectKind()).To(Equal(expectedConvertedObj.GetObjectKind()))
			Expect(oldConvertedObj.Spec).To(Equal(expectedConvertedObj.Spec))
			// TODO figure out why status conversion is not working in suite test
			//Expect(oldConvertedObj.Status).To(Equal(expectedConvertedObj.Status))

			Expect(k8sClient.Delete(ctx, oldObj)).To(Succeed())
		})
	})

})
