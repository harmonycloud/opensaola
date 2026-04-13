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

package ctxkeys

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/runtime"
)

type schemeKey struct{}

// WithScheme stores a *runtime.Scheme in the context.
func WithScheme(ctx context.Context, s *runtime.Scheme) context.Context {
	return context.WithValue(ctx, schemeKey{}, s)
}

// SchemeFrom retrieves the *runtime.Scheme from the context.
// Returns an error instead of panicking if scheme is not found.
func SchemeFrom(ctx context.Context) (*runtime.Scheme, error) {
	s, ok := ctx.Value(schemeKey{}).(*runtime.Scheme)
	if !ok || s == nil {
		return nil, errors.New("runtime.Scheme not found in context")
	}
	return s, nil
}
