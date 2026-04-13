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
	"sync"

	apiextclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	cfgOnce sync.Once
	cfg     *rest.Config
	cfgErr  error
)

func getConfig() (*rest.Config, error) {
	cfgOnce.Do(func() {
		cfg, cfgErr = ctrl.GetConfig()
	})
	return cfg, cfgErr
}

var (
	dynOnce   sync.Once
	dynClient *dynamic.DynamicClient
	dynErr    error
)

// GetDynClient returns the dynamic client singleton.
func GetDynClient() (*dynamic.DynamicClient, error) {
	dynOnce.Do(func() {
		cfgLocal, err := getConfig()
		if err != nil {
			dynErr = err
			return
		}
		dynClient, dynErr = dynamic.NewForConfig(cfgLocal)
	})
	return dynClient, dynErr
}

// GetRuntimeClient returns a new controller-runtime client.
func GetRuntimeClient(opt client.Options) (runtimeClient client.Client, err error) {
	cfg, err := getConfig()
	if err != nil {
		return nil, err
	}
	runtimeClient, err = client.New(cfg, opt)
	if err != nil {
		return nil, err
	}
	return runtimeClient, nil
}

var (
	apiextOnce   sync.Once
	apiextClient *apiextclientset.Clientset
	apiextErr    error
)

// GetApiextensionsv1Client returns the apiextensionsv1 client singleton.
func GetApiextensionsv1Client() (apiextensionsv1Client *apiextclientset.Clientset, err error) {
	apiextOnce.Do(func() {
		cfg, err := getConfig()
		if err != nil {
			apiextErr = err
			return
		}
		apiextClient, apiextErr = apiextclientset.NewForConfig(cfg)
	})
	return apiextClient, apiextErr
}

var (
	csOnce sync.Once
	cs     *kubernetes.Clientset
	csErr  error
)

// GetClientSet returns the Kubernetes clientset singleton.
func GetClientSet() (*kubernetes.Clientset, error) {
	csOnce.Do(func() {
		cfgLocal, err := getConfig()
		if err != nil {
			csErr = err
			return
		}
		cs, csErr = kubernetes.NewForConfig(cfgLocal)
	})
	return cs, csErr
}

var (
	dcOnce sync.Once
	dc     *discovery.DiscoveryClient
	dcErr  error
)

// GetDiscoveryClient returns the DiscoveryClient singleton.
func GetDiscoveryClient() (*discovery.DiscoveryClient, error) {
	dcOnce.Do(func() {
		cfgLocal, err := getConfig()
		if err != nil {
			dcErr = err
			return
		}
		dc, dcErr = discovery.NewDiscoveryClientForConfig(cfgLocal)
	})
	return dc, dcErr
}
