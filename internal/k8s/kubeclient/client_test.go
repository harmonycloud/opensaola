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

package kubeclient

import (
	"os"
	"sync"
	"testing"
)

// TestGetDynClient_NoKubeconfig verifies that GetDynClient returns a non-nil
// error when no kubeconfig is available.
// 验证在没有 kubeconfig 的情况下，GetDynClient 返回非 nil 的错误。
func TestGetDynClient_NoKubeconfig(t *testing.T) {
	// Ensure no kubeconfig is available by clearing relevant env vars.
	// 通过清除相关环境变量确保没有可用的 kubeconfig。
	origKubeconfig := os.Getenv("KUBECONFIG")
	origHome := os.Getenv("HOME")
	t.Cleanup(func() {
		os.Setenv("KUBECONFIG", origKubeconfig)
		os.Setenv("HOME", origHome)
		// Reset sync.Once state for other tests by resetting the package vars.
		// 重置 sync.Once 状态以便其他测试使用。
		dynOnce = syncOnceNew()
		dynClient = nil
		dynErr = nil
		cfgOnce = syncOnceNew()
		cfg = nil
		cfgErr = nil
	})

	os.Setenv("KUBECONFIG", "/nonexistent/kubeconfig")
	os.Setenv("HOME", "/nonexistent/home")

	client, err := GetDynClient()
	if err == nil {
		t.Error("expected error from GetDynClient when no kubeconfig is available, got nil")
	}
	if client != nil {
		t.Errorf("expected nil client when no kubeconfig, got %v", client)
	}
}

// TestGetDiscoveryClient_NoKubeconfig verifies that GetDiscoveryClient returns
// a non-nil error when no kubeconfig is available.
// 验证在没有 kubeconfig 的情况下，GetDiscoveryClient 返回非 nil 的错误。
func TestGetDiscoveryClient_NoKubeconfig(t *testing.T) {
	origKubeconfig := os.Getenv("KUBECONFIG")
	origHome := os.Getenv("HOME")
	t.Cleanup(func() {
		os.Setenv("KUBECONFIG", origKubeconfig)
		os.Setenv("HOME", origHome)
		dcOnce = syncOnceNew()
		dc = nil
		dcErr = nil
		cfgOnce = syncOnceNew()
		cfg = nil
		cfgErr = nil
	})

	os.Setenv("KUBECONFIG", "/nonexistent/kubeconfig")
	os.Setenv("HOME", "/nonexistent/home")

	client, err := GetDiscoveryClient()
	if err == nil {
		t.Error("expected error from GetDiscoveryClient when no kubeconfig is available, got nil")
	}
	if client != nil {
		t.Errorf("expected nil client when no kubeconfig, got %v", client)
	}
}

// TestGetClientSet_NoKubeconfig verifies that GetClientSet returns a non-nil
// error when no kubeconfig is available.
// 验证在没有 kubeconfig 的情况下，GetClientSet 返回非 nil 的错误。
func TestGetClientSet_NoKubeconfig(t *testing.T) {
	origKubeconfig := os.Getenv("KUBECONFIG")
	origHome := os.Getenv("HOME")
	t.Cleanup(func() {
		os.Setenv("KUBECONFIG", origKubeconfig)
		os.Setenv("HOME", origHome)
		csOnce = syncOnceNew()
		cs = nil
		csErr = nil
		cfgOnce = syncOnceNew()
		cfg = nil
		cfgErr = nil
	})

	os.Setenv("KUBECONFIG", "/nonexistent/kubeconfig")
	os.Setenv("HOME", "/nonexistent/home")

	clientset, err := GetClientSet()
	if err == nil {
		t.Error("expected error from GetClientSet when no kubeconfig is available, got nil")
	}
	if clientset != nil {
		t.Errorf("expected nil clientset when no kubeconfig, got %v", clientset)
	}
}

// syncOnceNew returns a fresh sync.Once to reset singleton state in tests.
// 返回一个新的 sync.Once 用于在测试中重置单例状态。
func syncOnceNew() (o sync.Once) { return }
