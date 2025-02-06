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

package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/model-registry-operator/internal/utils"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
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
			url:      "https://raw.githubusercontent.com/openshift/api/refs/heads/master/config/v1/zz_generated.crd-manifests/0000_10_config-operator_01_ingresses.crd.yaml",
			fileName: "ingress.openshift.io_ingresses.yaml",
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

	err = configv1.AddToScheme(schm)
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

	clusterIngress := &configv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.IngressSpec{
			Domain: "domain3",
		},
	}

	err = k8sClient.Create(context.Background(), clusterIngress)
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
