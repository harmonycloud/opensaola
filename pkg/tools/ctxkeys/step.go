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

import "context"

type stepKey struct{}

// WithStep stores a step map in the context.
func WithStep(ctx context.Context, step map[string]interface{}) context.Context {
	return context.WithValue(ctx, stepKey{}, step)
}

// StepFrom retrieves the step map from the context.
// Returns an empty map (not nil, no panic) if not set.
func StepFrom(ctx context.Context) map[string]interface{} {
	if v, ok := ctx.Value(stepKey{}).(map[string]interface{}); ok && v != nil {
		return v
	}
	return make(map[string]interface{})
}
