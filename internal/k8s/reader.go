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

package k8s

import "sigs.k8s.io/controller-runtime/pkg/client"

// statusAPIReader is the API reader used inside RetryOnConflict for status updates.
// It bypasses the informer cache to always fetch the latest resourceVersion,
// preventing stale-cache 409 loops under high concurrency.
//
// (see English comment above)
var statusAPIReader client.Reader

// SetStatusAPIReader sets the API reader for status update retries.
// Call this once after the controller-runtime manager is created, passing mgr.GetAPIReader().
//
// (see English comment above)
func SetStatusAPIReader(r client.Reader) {
	statusAPIReader = r
}
