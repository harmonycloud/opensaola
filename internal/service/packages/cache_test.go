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
	"testing"
)

func TestPackageCache_HitMiss(t *testing.T) {
	// Clear cache
	packageCache.Delete("test-pkg")

	// Manually write a cache entry
	pkg := &Package{
		Name:    "test-pkg",
		Enabled: true,
		Files:   map[string][]byte{"a.yaml": []byte("hello")},
	}
	packageCache.Set("test-pkg", &cacheEntry{
		resourceVersion: "rv-100",
		pkg:             pkg,
	})

	// Verify cache hit
	ce, ok := packageCache.Get("test-pkg")
	if !ok {
		t.Fatal("cache entry not found")
	}
	if ce.resourceVersion != "rv-100" {
		t.Errorf("resourceVersion = %q, want %q", ce.resourceVersion, "rv-100")
	}
	if ce.pkg.Name != "test-pkg" {
		t.Errorf("pkg.Name = %q, want %q", ce.pkg.Name, "test-pkg")
	}

	// Verify Invalidate
	InvalidatePackageCache("test-pkg")
	if _, ok := packageCache.Get("test-pkg"); ok {
		t.Error("cache entry should be deleted after InvalidatePackageCache")
	}
}
