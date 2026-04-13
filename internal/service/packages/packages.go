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

package packages

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/cache"
	"github.com/OpenSaola/opensaola/internal/k8s"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	"github.com/OpenSaola/opensaola/internal/service/consts"
	"github.com/OpenSaola/opensaola/pkg/tools"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/klauspost/compress/zstd"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var (
	Immutable     = true
	DataNamespace = "default"

	// apiReader bypasses the informer cache to read full Secret objects
	// (including Data) directly from the API server. The informer cache
	// strips Secret.Data via TransformFunc to reduce memory usage.
	apiReader client.Reader
)

const (
	Release = "package"
	// LabelFmt         = "%s=%s"
	// DefaultLabelsFmt = "%s=%s,%s=%s"
)

func init() {
	metrics.Registry.MustRegister(PackageCacheHitTotal, PackageCacheMissTotal)
}

// SetAPIReader sets the direct API reader, bypassing the informer cache.
// Must be called after manager creation.
func SetAPIReader(r client.Reader) {
	apiReader = r
}

// --- Package parse cache ---

type cacheEntry struct {
	resourceVersion string
	pkg             *Package
}

var packageCache = cache.New[string, *cacheEntry](0)

var (
	PackageCacheHitTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "opensaola_package_cache_hit_total",
		Help: "Total number of package parse cache hits.",
	})
	PackageCacheMissTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "opensaola_package_cache_miss_total",
		Help: "Total number of package parse cache misses.",
	})
)

// InvalidatePackageCache removes the cache entry for the specified package.
func InvalidatePackageCache(name string) {
	packageCache.Delete(name)
}

type Metadata struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	App         App    `json:"app"`
	Owner       string `json:"owner"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type App struct {
	Version           []string `json:"version"`
	DeprecatedVersion []string `json:"deprecatedVersion"`
}

type Package struct {
	Name      string            `json:"name"`
	Created   string            `json:"created"`
	Files     map[string][]byte `json:"file"`
	Component string            `json:"component"`
	Metadata  *Metadata         `json:"metadata"`
	Enabled   bool              `json:"enabled"`
}

type Option struct {
	LabelComponent      string
	LabelPackageVersion string
}

func SetDataNamespace(namespace string) {
	DataNamespace = namespace
}

// GetInstallStatus returns whether the package Secret is enabled and the last recorded install error (if any).
// It is safe to call even when the package content is invalid (no decompression/parse performed).
func GetInstallStatus(ctx context.Context, cli client.Client, name string) (enabled bool, installError string, err error) {
	var secret *corev1.Secret
	secret, err = k8s.GetSecret(ctx, cli, name, DataNamespace)
	if err != nil {
		return false, "", err
	}
	enabled = secret.Labels[v1.LabelEnabled] == "true"
	if secret.Annotations != nil {
		installError = secret.Annotations[v1.AnnotationInstallError]
	}
	return enabled, installError, nil
}

// List reads middleware packages.
// name is required, version is optional.
func List(ctx context.Context, cli client.Client, opt Option) ([]*Package, error) {
	lbs := make(client.MatchingLabels)
	lbs[v1.LabelProject] = consts.ProjectOpenSaola
	if opt.LabelComponent != "" {
		lbs[v1.LabelComponent] = opt.LabelComponent
	}
	if opt.LabelPackageVersion != "" {
		lbs[v1.LabelPackageVersion] = opt.LabelPackageVersion
	}
	secrets, err := k8s.GetSecrets(ctx, cli, DataNamespace, lbs)
	if err != nil {
		return nil, err
	}

	var pkgs []*Package
	for _, item := range secrets.Items {
		// Read file
		var pkg *Package
		pkg, err = Get(ctx, cli, item.Name)
		if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, nil
}

func Get(ctx context.Context, cli client.Client, name string) (*Package, error) {
	// Use APIReader (direct API call) if available, because the informer
	// cache strips Secret.Data via TransformFunc to save memory.
	var s *corev1.Secret
	var err error
	if apiReader != nil {
		s = new(corev1.Secret)
		err = apiReader.Get(ctx, client.ObjectKey{Name: name, Namespace: DataNamespace}, s)
	} else {
		s, err = k8s.GetSecret(ctx, cli, name, DataNamespace)
	}
	if err != nil {
		return nil, fmt.Errorf("get secret failed: %w", err)
	}

	// Use cache: skip decompress/parse if Secret resourceVersion is unchanged.
	if ce, ok := packageCache.Get(name); ok {
		if ce.resourceVersion == s.ResourceVersion {
			PackageCacheHitTotal.Inc()
			return ce.pkg, nil
		}
	}
	PackageCacheMissTotal.Inc()

	// Decompress data
	decompressData, err := DeCompress(s.Data[Release])
	if err != nil {
		return nil, fmt.Errorf("decompress data failed: %w", err)
	}
	// Read file info from tar
	info, err := tools.ReadTarInfo(decompressData)
	if err != nil {
		return nil, fmt.Errorf("read tar info failed: %w", err)
	}

	var metadata Metadata
	err = yaml.Unmarshal(info.Files["metadata.yaml"], &metadata)
	if err != nil {
		return nil, fmt.Errorf("unmarshal metadata failed: %w", err)
	}
	pkg := &Package{
		Name:      s.Name,
		Created:   s.CreationTimestamp.Format(time.DateTime),
		Files:     info.Files,
		Component: s.Labels[v1.LabelComponent],
		Metadata:  &metadata,
		Enabled:   s.Labels[v1.LabelEnabled] == "true",
	}

	packageCache.Set(name, &cacheEntry{
		resourceVersion: s.ResourceVersion,
		pkg:             pkg,
	})

	return pkg, nil
}

func GetMetadata(ctx context.Context, cli client.Client, packageName string) (*Metadata, error) {
	pkg, err := Get(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}

	// Read metadata
	var metadata Metadata
	err = yaml.Unmarshal(pkg.Files["metadata.yaml"], &metadata)
	if err != nil {
		return nil, err
	}

	return &metadata, nil
}

func GetConfigurations(ctx context.Context, cli client.Client, packageName string) (map[string]*v1.MiddlewareConfiguration, error) {
	pkg, err := Get(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}

	// Get the configurations list from the package
	var configurations = make(map[string]*v1.MiddlewareConfiguration)
	for _, v := range pkg.Files {
		if bytes.Contains(v, []byte("kind: MiddlewareConfiguration")) {
			configuration := new(v1.MiddlewareConfiguration)
			err = yaml.Unmarshal(v, &configuration)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("unmarshal file failed: %s", err.Error()))
				return nil, err
			}
			configurations[configuration.Name] = configuration
		}
	}
	return configurations, nil
}

func GetMiddlewareBaselines(ctx context.Context, cli client.Client, packageName string) ([]*v1.MiddlewareBaseline, error) {
	pkg, err := Get(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}

	var baselines []*v1.MiddlewareBaseline
	for _, v := range pkg.Files {
		if bytes.Contains(v, []byte("kind: MiddlewareBaseline")) {
			baseline := new(v1.MiddlewareBaseline)
			err = yaml.Unmarshal(v, baseline)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("unmarshal file failed: %s", err.Error()))
				return nil, err
			}
			baselines = append(baselines, baseline)
		}
	}
	return baselines, nil
}

func GetMiddlewareOperatorBaselines(ctx context.Context, cli client.Client, packageName string) ([]*v1.MiddlewareOperatorBaseline, error) {
	pkg, err := Get(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}

	var baselines []*v1.MiddlewareOperatorBaseline
	for _, v := range pkg.Files {
		if bytes.Contains(v, []byte("kind: MiddlewareOperatorBaseline")) {
			baseline := new(v1.MiddlewareOperatorBaseline)
			err = yaml.Unmarshal(v, baseline)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("unmarshal file failed: %s", err.Error()))
				return nil, err
			}
			baselines = append(baselines, baseline)
		}
	}
	return baselines, nil
}

func GetMiddlewareActionBaselines(ctx context.Context, cli client.Client, packageName string) ([]*v1.MiddlewareActionBaseline, error) {
	pkg, err := Get(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}

	var baselines []*v1.MiddlewareActionBaseline
	for _, v := range pkg.Files {
		if bytes.Contains(v, []byte("kind: MiddlewareActionBaseline")) {
			baseline := new(v1.MiddlewareActionBaseline)
			err = yaml.Unmarshal(v, baseline)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("unmarshal file failed: %s", err.Error()))
				return nil, err
			}
			baselines = append(baselines, baseline)
		}
	}
	return baselines, nil
}

func GetMiddlewareBaseline(ctx context.Context, cli client.Client, name, packageName string) (*v1.MiddlewareBaseline, error) {
	pkg, err := Get(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}

	var isExist bool

	// Get the baseline from the package
	var md = new(v1.MiddlewareBaseline)
	for _, v := range pkg.Files {
		if bytes.Contains(v, []byte("kind: MiddlewareBaseline")) {
			err = yaml.Unmarshal(v, md)
			if err != nil {
				logger.Log.Debug(fmt.Sprintf("unmarshal file failed: %s", err.Error()))
				return nil, err
			}
			if md.Name == name {
				isExist = true
				break
			}
		}
	}
	if !isExist {
		return nil, apiErrors.NewNotFound(k8s.MiddlewareBaselineGroupResource(), name)
	}
	return md, nil
}

func GetMiddlewareOperatorBaseline(ctx context.Context, cli client.Client, name, packageName string) (*v1.MiddlewareOperatorBaseline, error) {
	pkg, err := Get(ctx, cli, packageName)
	if err != nil {
		return nil, err
	}

	var isExist bool

	// Get the baseline from the package
	var mod = new(v1.MiddlewareOperatorBaseline)
	for _, v := range pkg.Files {
		if bytes.Contains(v, []byte("kind: MiddlewareOperatorBaseline")) &&
			bytes.Contains(v, []byte(fmt.Sprintf("name: %s", name))) {
			err = yaml.Unmarshal(v, mod)
			if err != nil {
				logger.Log.Debug(fmt.Sprintf("unmarshal file failed: %s", err.Error()))
				return nil, err
			}
			isExist = true
			break
		}
	}
	if !isExist {
		return nil, apiErrors.NewNotFound(k8s.MiddlewareOperatorBaselineGroupResource(), name)
	}
	return mod, nil
}

// GetMiddlewareActionBaseline retrieves a middleware action baseline definition
// func GetMiddlewareActionBaseline(ctx context.Context, cli client.Client, name, packageName string) (*v1.MiddlewareActionBaseline, error) {
//	pkg, err := Get(ctx, cli, packageName)
//	if err != nil {
//		return nil, err
//	}
//
//	var isExist bool
//
//	// Get the baseline from the package
//	var mad = new(v1.MiddlewareActionBaseline)
//	for k, v := range pkg.Files {
//		if strings.Contains(k, "baseline") &&
//			strings.Contains(string(v), mad.GroupVersionKind().Kind) &&
//			strings.Contains(string(v), name) {
//			err = yaml.Unmarshal(v, mad)
//			if err != nil {
//				logger.Log.Debug(fmt.Sprintf("unmarshal file failed: %s", err.Error()))
//				return nil, err
//			}
//			isExist = true
//			break
//		}
//	}
//	if !isExist {
//		return nil, apiErrors.NewNotFound(k8s.MiddlewareActionBaselineGroupResource(), name)
//	}
//	return mad, nil
//
// }

func Compress(data []byte) ([]byte, int, error) {
	buf := bytes.NewBuffer([]byte{})
	w, err := zstd.NewWriter(buf, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return nil, 0, err
	}
	num, err := w.Write(data)
	if err != nil {
		w.Close()
		return nil, 0, err
	}
	w.Close()
	return buf.Bytes(), num, nil
}

func DeCompress(data []byte) ([]byte, error) {
	buf := bytes.NewBuffer(data)
	decoder, err := zstd.NewReader(buf, zstd.IgnoreChecksum(true))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(decoder)
}
