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

package v1alpha1_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/model-registry-operator/api/v1alpha1"

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

	err = (&v1alpha1.ModelRegistry{}).SetupWebhookWithManager(mgr)
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
	})

	It("Should not allow creation of MR instance with invalid database config", func(ctx context.Context) {
		mr := newModelRegistry(ctx, mrNameBase+"-invalid-db-create", namespaceBase)
		mr.Spec = v1alpha1.ModelRegistrySpec{}

		Expect(k8sClient.Create(ctx, mr)).ShouldNot(Succeed())
	})

	It("Should not allow update of MR instance with invalid database config", func(ctx context.Context) {
		mr := newModelRegistry(ctx, mrNameBase+"-invalid-db-update", namespaceBase)
		Expect(k8sClient.Create(ctx, mr)).Should(Succeed())

		mr.Spec = v1alpha1.ModelRegistrySpec{
			MySQL: &v1alpha1.MySQLConfig{},
		}

		Expect(k8sClient.Update(ctx, mr)).ShouldNot(Succeed())
	})
})

func newModelRegistry(ctx context.Context, name string, namespace string) *v1alpha1.ModelRegistry {
	// create test namespace
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}))).Should(Succeed())

	// return
	return &v1alpha1.ModelRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
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
		},
	}
}
