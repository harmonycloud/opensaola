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

package v1

// Label, Annotation, Finalizer constants
// These constants define the K8s resource API conventions and belong to the api/v1 layer.
const (
	LabelPackageVersion = "middleware.cn/packageversion"
	LabelComponent      = "middleware.cn/component"
	LabelProject        = "middleware.cn/project"
	LabelPackageName    = "middleware.cn/packagename"
	LabelApp            = "middleware.cn/app"
	LabelConfigurations = "middleware.cn/configurations"

	LabelBaseline  = "middleware.cn/baseline"
	LabelUpdate    = "middleware.cn/update"
	LabelEnabled   = "middleware.cn/enabled"
	LabelInstall   = "middleware.cn/install"
	LabelUnInstall = "middleware.cn/uninstall"

	AnnotationInstallDigest = "middleware.cn/installDigest"
	AnnotationInstallError  = "middleware.cn/installError"

	LabelSource     = "middleware.cn/source"
	LabelSourceName = "middleware.cn/sourcename"

	LabelNoOperator = "middleware.cn/nooperator"
)

const (
	AnnotationDisasterSyncer    = "middleware.cn/disasterSyncer"
	AnnotationDataSyncer        = "middleware.cn/dataSyncer"
	AnnotationOppositeClusterId = "middleware.cn/oppositeClusterId"
)

// Finalizer names
const (
	FinalizerMiddleware         = "middleware.cn/middleware-cleanup"
	FinalizerMiddlewareOperator = "middleware.cn/middlewareoperator-cleanup"
)

const (
	Secret     = "secret"
	FieldOwner = "opensaola"
)
