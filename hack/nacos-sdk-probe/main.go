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

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	dir := os.Getenv("NACOS_PROBE_DIR")
	password := os.Getenv("NACOS_TEST_PASSWORD")
	if dir == "" || password == "" {
		panic("expected NACOS_PROBE_DIR and NACOS_TEST_PASSWORD")
	}
	cc := constant.NewClientConfig(
		constant.WithTimeoutMs(5000),
		constant.WithNotLoadCacheAtStart(true),
		constant.WithUsername("nacos"),
		constant.WithPassword(password),
		constant.WithLogDir(filepath.Join(dir, "sdklog")),
		constant.WithCacheDir(filepath.Join(dir, "sdkcache")),
		constant.WithLogLevel("error"),
	)
	sc := []constant.ServerConfig{*constant.NewServerConfig("127.0.0.1", 28848, constant.WithContextPath("/nacos"))}
	p := vo.NacosClientParam{ClientConfig: cc, ServerConfigs: sc}
	config, err := clients.NewConfigClient(p)
	must(err)
	defer config.CloseClient()
	naming, err := clients.NewNamingClient(p)
	must(err)
	defer naming.CloseClient()
	stamp := fmt.Sprint(time.Now().UnixNano())
	data := "singleton-sdk-" + stamp
	service := "singleton-sdk-service-" + stamp
	group := "OPENSAOLA_PACKAGE_MATRIX"
	report := map[string]any{"sdk": "nacos-sdk-go/v2 v2.3.5", "endpoint": "Service 8848 + gRPC 9848"}
	// A Ready HTTP endpoint can precede the SDK's asynchronous gRPC handshake.
	// Use a read-only request to warm the real config client before mutating data.
	deadline := time.Now().Add(90 * time.Second)
	for {
		_, err = config.GetConfig(vo.ConfigParam{DataId: data, Group: group})
		if err == nil || !strings.Contains(err.Error(), "client not connected") {
			break
		}
		if time.Now().After(deadline) {
			panic(err)
		}
		fmt.Fprintln(os.Stderr, "Waiting for Nacos SDK client connection")
		time.Sleep(time.Second)
	}
	ok, err := config.PublishConfig(vo.ConfigParam{DataId: data, Group: group, Content: "singleton sdk config"})
	must(err)
	if !ok {
		panic("publish false")
	}
	report["publishConfig"] = true
	defer config.DeleteConfig(vo.ConfigParam{DataId: data, Group: group})
	content := ""
	for i := 0; i < 20; i++ {
		content, err = config.GetConfig(vo.ConfigParam{DataId: data, Group: group})
		if err == nil && content == "singleton sdk config" {
			break
		}
		time.Sleep(time.Second)
	}
	must(err)
	if content != "singleton sdk config" {
		panic("published configuration could not be read back")
	}
	report["readConfig"] = true
	ok, err = naming.RegisterInstance(vo.RegisterInstanceParam{Ip: "192.0.2.27", Port: 18080, ServiceName: service, GroupName: group, ClusterName: "DEFAULT", Weight: 1, Enable: true, Healthy: true, Ephemeral: true})
	must(err)
	if !ok {
		panic("register false")
	}
	report["grpcRegister"] = true
	defer naming.DeregisterInstance(vo.DeregisterInstanceParam{Ip: "192.0.2.27", Port: 18080, ServiceName: service, GroupName: group, Cluster: "DEFAULT", Ephemeral: true})
	found := false
	for i := 0; i < 20; i++ {
		instances, err := naming.SelectAllInstances(vo.SelectAllInstancesParam{ServiceName: service, GroupName: group, Clusters: []string{"DEFAULT"}})
		if err == nil {
			for _, v := range instances {
				if v.Ip == "192.0.2.27" && v.Port == 18080 {
					found = true
				}
			}
		}
		if found {
			break
		}
		time.Sleep(time.Second)
	}
	if !found {
		panic("SDK discovery did not see registered instance")
	}
	report["grpcDiscovery"] = true
	ok, err = naming.DeregisterInstance(vo.DeregisterInstanceParam{Ip: "192.0.2.27", Port: 18080, ServiceName: service, GroupName: group, Cluster: "DEFAULT", Ephemeral: true})
	must(err)
	if !ok {
		panic("deregister false")
	}
	report["grpcDeregister"] = true
	ok, err = config.DeleteConfig(vo.ConfigParam{DataId: data, Group: group})
	must(err)
	if !ok {
		panic("delete config false")
	}
	report["deleteConfig"] = true
	out, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(out))
}
