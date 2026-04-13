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

package consts

import "errors"

var (
	SameTypeMiddlewareExists         = errors.New("a Middleware of the same type already exists in this namespace; only one instance per type is allowed — delete the existing one first or use a different type label")
	SameTypeMiddlewareOperatorExists = errors.New("a MiddlewareOperator of the same type already exists; only one operator per middleware type is allowed — check existing MiddlewareOperators with 'kubectl get mo -A'")
	NoOperator                       = errors.New("no matching MiddlewareOperator found for this middleware type; create a MiddlewareOperator with the correct middleware.cn/component label first")
	ErrPackageNotReady               = errors.New("middleware package is not ready yet — the package Secret may still be installing; check package status with 'kubectl get mp'")
	ErrPackageInstallFailed          = errors.New("middleware package installation failed — check the package Secret annotations for error details: kubectl get secret <name> -o jsonpath='{.metadata.annotations}'")
	ErrPackageUnavailableExceeded    = errors.New("middleware package has been unavailable for too long — check if the package Secret exists and has valid content")
)
