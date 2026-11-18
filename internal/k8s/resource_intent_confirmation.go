/*
Copyright 2026 The OpenSaola Authors.

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

import (
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const annotationResourceIntentVerified = "middleware.cn/resource-intent-verified"

// effectiveResourceIntent never promotes an unverified deletion write to the
// next deletion baseline. A lost commit response, failed compensation, or process
// restart still has the previous history in the active immutable Secret. Old
// records without DeletionBase need no confirmation marker.
func effectiveResourceIntent(record *resourceIntent, secret *corev1.Secret) (*resourceIntent, error) {
	if record == nil {
		return nil, managedWriteBlocked("missing resource intent data")
	}
	if record.DeletionBase == nil {
		return record, nil
	}
	base := record.DeletionBase
	if record.Version != currentResourceIntentVersion || record.Pause != nil ||
		!supportedResourceIntentVersion(base.Version) || base.DeletionBase != nil || base.Pause != nil ||
		record.Desired == nil || base.Desired == nil || base.SchemaHash == "" ||
		base.OwnerUID != record.OwnerUID || base.ResourceUID != record.ResourceUID || base.Configuration != record.Configuration ||
		base.Desired.GroupVersionKind() != record.Desired.GroupVersionKind() || client.ObjectKeyFromObject(base.Desired) != client.ObjectKeyFromObject(record.Desired) {
		return nil, managedWriteBlocked("invalid pending deletion history")
	}
	selected := record
	if secret == nil || secret.Annotations[annotationResourceIntentVerified] == "" {
		selected = base
	}
	result := *selected
	result.DeletionBase = nil
	result.pendingDeletion = selected == base
	return &result, nil
}
