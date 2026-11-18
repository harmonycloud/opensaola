//go:build envtest

/*
Copyright 2025 The OpenSaola Authors.

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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	middlewarecnv1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/resource/logger"
	"github.com/harmonycloud/opensaola/internal/service/watcher"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

// Ordinary Go tests run before the Ginkgo suite and can initialize the shared
// discovery/config singleton while rendering. Isolate the entire process before
// m.Run, not just BeforeSuite, so dynamic watchers cannot retain another cluster.
func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start isolated controller test API:", err)
		os.Exit(1)
	}

	// Dynamic primary-resource watchers resolve their client through kubeconfig.
	// Pin that path to this temporary API server before any controller runs;
	// tests must never fall back to the developer's current cluster.
	kubeconfigDir, err := os.MkdirTemp("", "opensaola-controller-envtest-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create controller test kubeconfig directory:", err)
		_ = testEnv.Stop()
		os.Exit(1)
	}
	previousKubeconfig, hadKubeconfig := os.LookupEnv("KUBECONFIG")
	cleanup := func() error {
		watcher.StopAllCRWatchers()
		stopErr := testEnv.Stop()
		if stopErr != nil {
			fmt.Fprintln(os.Stderr, "stop isolated controller test API:", stopErr)
		}
		if hadKubeconfig {
			_ = os.Setenv("KUBECONFIG", previousKubeconfig)
		} else {
			_ = os.Unsetenv("KUBECONFIG")
		}
		_ = os.RemoveAll(kubeconfigDir)
		return stopErr
	}
	configPath := filepath.Join(kubeconfigDir, "kubeconfig")
	if err := clientcmd.WriteToFile(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"envtest": {Server: cfg.Host, CertificateAuthorityData: cfg.CAData}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"envtest": {ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}},
		Contexts:       map[string]*clientcmdapi.Context{"envtest": {Cluster: "envtest", AuthInfo: "envtest"}},
		CurrentContext: "envtest",
	}, configPath); err != nil {
		fmt.Fprintln(os.Stderr, "write controller test kubeconfig:", err)
		_ = cleanup()
		os.Exit(1)
	}
	if err := os.Setenv("KUBECONFIG", configPath); err != nil {
		fmt.Fprintln(os.Stderr, "select controller test kubeconfig:", err)
		_ = cleanup()
		os.Exit(1)
	}
	code := m.Run()
	if err := cleanup(); err != nil {
		code = 1
	}
	os.Exit(code)
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	logger.Initialize(zerolog.DebugLevel)
	ctx, cancel = context.WithCancel(context.TODO())
	Expect(middlewarecnv1.AddToScheme(scheme.Scheme)).To(Succeed())
	// +kubebuilder:scaffold:scheme

	var err error
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
	resolver, err := k8s.NewIntentSchemaResolver(cfg)
	Expect(err).NotTo(HaveOccurred())
	// Dynamic watchers outlive individual specs. Install a single direct reader
	// and schema resolver before they start, as the production manager does.
	k8s.ConfigureManagedResources(k8sClient)
	k8s.ConfigureResourceIntents(resolver)
})

var _ = AfterSuite(func() {
	By("stopping controller suite observers")
	stopSuiteWatchers()
	cancel()
	k8s.ConfigureManagedResources(nil)
	k8s.ConfigureResourceIntents(nil)
})

// Stop and join the informer callbacks before resetting the shared clients
// or letting another test suite install its own fake reader.
func stopSuiteWatchers() {
	watcher.CustomResourceWatcherMap.Range(func(_, value any) bool {
		cw, ok := value.(*watcher.CustomResourceWatcher)
		Expect(ok).To(BeTrue())
		cw.Stop()
		Eventually(cw.Done()).WithTimeout(10 * time.Second).Should(BeClosed())
		return true
	})
}

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

func isRunningUnderMakefileWithEnvtestAssets() bool {
	return os.Getenv("KUBEBUILDER_ASSETS") != ""
}

func TestEnvtestRequired(t *testing.T) {
	if !isRunningUnderMakefileWithEnvtestAssets() {
		t.Skip("envtest tests require KUBEBUILDER_ASSETS; run via `make test-envtest`")
	}
}
