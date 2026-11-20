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
	"errors"
	"fmt"
	"io"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/cache"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/pkg/tools"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/klauspost/compress/zstd"
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

func compatibleProjectListOption() client.ListOption {
	return client.MatchingLabelsSelector{
		Selector: consts.OpenSaolaProjectSelector(v1.LabelProject),
	}
}

func SelectPreferredMiddlewarePackages(items []v1.MiddlewarePackage) []v1.MiddlewarePackage {
	if len(items) == 0 {
		return nil
	}
	var current []v1.MiddlewarePackage
	var legacy []v1.MiddlewarePackage
	for _, item := range items {
		switch item.Labels[v1.LabelProject] {
		case consts.ProjectOpenSaola:
			current = append(current, item)
		case consts.ProjectZeusOperator:
			legacy = append(legacy, item)
		}
	}
	if len(current) > 0 {
		return current
	}
	return legacy
}

func ListCompatibleMiddlewarePackages(ctx context.Context, cli client.Client, labels client.MatchingLabels) ([]v1.MiddlewarePackage, error) {
	items, err := k8s.ListMiddlewarePackages(ctx, cli, compatibleProjectListOption(), labels)
	if err != nil {
		return nil, err
	}
	return SelectPreferredMiddlewarePackages(items), nil
}

// List reads middleware packages.
// name is required, version is optional.
func List(ctx context.Context, cli client.Client, opt Option) ([]*Package, error) {
	lbs := make(client.MatchingLabels)
	if opt.LabelComponent != "" {
		lbs[v1.LabelComponent] = opt.LabelComponent
	}
	if opt.LabelPackageVersion != "" {
		lbs[v1.LabelPackageVersion] = opt.LabelPackageVersion
	}
	secrets, err := k8s.GetSecrets(ctx, cli, DataNamespace, compatibleProjectListOption(), lbs)
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

// ErrInvalidArchive identifies a deterministic package-content error. API read
// failures are kept separate so an activation can retry them.
var ErrInvalidArchive = errors.New("invalid package archive")

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
			return ce.pkg, nil
		}
	}

	// Decompress data
	decompressData, err := DeCompress(s.Data[Release])
	if err != nil {
		return nil, fmt.Errorf("%w: decompress data failed: %w", ErrInvalidArchive, err)
	}
	// Read file info from tar
	info, err := tools.ReadTarInfo(decompressData)
	if err != nil {
		return nil, fmt.Errorf("%w: read tar info failed: %w", ErrInvalidArchive, err)
	}

	var metadata Metadata
	err = yaml.Unmarshal(info.Files["metadata.yaml"], &metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: unmarshal metadata failed: %w", ErrInvalidArchive, err)
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

func Compress(data []byte) ([]byte, int, error) {
	buf := bytes.NewBuffer([]byte{})
	w, err := zstd.NewWriter(buf, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return nil, 0, err
	}
	num, err := w.Write(data)
	if err != nil {
		_ = w.Close()
		return nil, 0, err
	}
	if err := w.Close(); err != nil {
		return nil, 0, err
	}
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
