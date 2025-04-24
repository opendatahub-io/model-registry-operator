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

package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	networkingscheme "k8s.io/api/networking/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	routev1 "github.com/openshift/api/route/v1"
	userv1 "github.com/openshift/api/user/v1"
	istioclientv1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	istiosecurityv1beta1 "istio.io/client-go/pkg/apis/security/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/scheme"

	modelregistryv1alpha1 "github.com/opendatahub-io/model-registry-operator/api/v1alpha1"
	"github.com/opendatahub-io/model-registry-operator/internal/utils"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

type remoteCRD struct {
	url      string
	fileName string
}

var (
	cfg              *rest.Config
	k8sClient        client.Client
	testEnv          *envtest.Environment
	testCRDLocalPath = "./testdata/crd"
	remoteCRDs       = []remoteCRD{
		{
			url:      "https://raw.githubusercontent.com/Kuadrant/authorino/refs/heads/main/install/crd/authorino.kuadrant.io_authconfigs.yaml",
			fileName: "authorino.kuadrant.io_authconfigs.yaml",
		},
		{
			url:      "https://raw.githubusercontent.com/istio/istio/refs/heads/master/manifests/charts/base/files/crd-all.gen.yaml",
			fileName: "istio.yaml",
		},
		{
			url:      "https://raw.githubusercontent.com/openshift/api/e7ac40fc1590efe8697d76691aa644d1ec3f07a7/route/v1/zz_generated.crd-manifests/routes.crd.yaml",
			fileName: "route.openshift.io_routes.yaml",
		},
	}
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	var err error

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	schm := apiruntime.NewScheme()

	err = corev1.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	err = appsv1.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	err = rbacv1.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	err = userv1.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	err = routev1.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	err = istiosecurityv1beta1.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	err = istioclientv1beta1.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	authorinoScheme := &scheme.Builder{GroupVersion: schema.GroupVersion{Group: "authorino.kuadrant.io", Version: "v1beta3"}}

	err = authorinoScheme.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	err = modelregistryv1alpha1.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	err = networkingscheme.AddToScheme(schm)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	// Download CRDs

	if err := os.MkdirAll(filepath.Dir(testCRDLocalPath), 0755); err != nil {
		Fail(err.Error())
	}

	for _, crd := range remoteCRDs {
		if err := utils.DownloadFile(crd.url, filepath.Join(testCRDLocalPath, crd.fileName)); err != nil {
			Fail(err.Error())
		}
	}

	By("bootstrapping test environment")
	useExistingCluster := false
	testEnv = &envtest.Environment{
		Scheme: schm,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("testdata", "crd"),
		},
		ErrorIfCRDPathMissing: true,

		// The BinaryAssetsDirectory is only required if you want to run the tests directly
		// without call the makefile target test. If not informed it will look for the
		// default path defined in controller-runtime which is /usr/local/kubebuilder/.
		// Note that you must have the required binaries setup under the bin directory to perform
		// the tests directly. When we run make test it will be setup and used automatically.
		BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s",
			fmt.Sprintf("1.28.0-%s-%s", runtime.GOOS, runtime.GOARCH)),
		UseExistingCluster: &useExistingCluster,
	}
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: schm})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
