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

import (
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

const (
	StatusCondTypeCheck = "Check"
)

const (
	ActionNone   = iota // none
	ActionCreate        // create
	ActionUpdate        // update
)

const (
	GenerationZero = iota
	GenerationCreate
)

type HandleAction string

const (
	HandleActionPublish HandleAction = "publish"
	HandleActionDelete  HandleAction = "delete"
	HandleActionUpdate  HandleAction = "update"
)

const (
	ProjectOpenSaola        = "opensaola"
	ProjectZeusOperator     = "zeus-operator"
	projectLabelSelectorKey = "project label selector"
)

var CompatibleProjects = []string{
	ProjectOpenSaola,
	ProjectZeusOperator,
}

func IsOpenSaolaProject(project string) bool {
	for _, candidate := range CompatibleProjects {
		if project == candidate {
			return true
		}
	}
	return false
}

func OpenSaolaProjectSelector(labelKey string) labels.Selector {
	req, err := labels.NewRequirement(labelKey, selection.In, CompatibleProjects)
	if err != nil {
		panic(projectLabelSelectorKey + ": " + err.Error())
	}
	return labels.NewSelector().Add(*req)
}
