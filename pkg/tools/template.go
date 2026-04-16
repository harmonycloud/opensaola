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

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/google/uuid"
	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s/kubeclient"
	"github.com/harmonycloud/opensaola/pkg/tools/ctxkeys"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Quoter interface {
	GetName() string
	GetNamespace() string
	GetLabels() map[string]string
	GetConfigurations() []v1.Configuration
	GetAnnotations() map[string]string
	GetUnified() *runtime.RawExtension
	GetPreActions() []v1.PreAction
	GetMiddlewareName() string
}

const (
	GlobeKeyName           = "Name"
	GlobeKeyNamespace      = "Namespace"
	GlobeKeyLabels         = "Labels"
	GlobeKeyAnnotations    = "Annotations"
	GlobeKeyPackageName    = "PackageName"
	GlobeKeyMiddlewareName = "MiddlewareName"
)

type TemplateValues struct {
	Values       map[string]interface{} `json:"values"`
	Globe        map[string]interface{} `json:"globe"`
	Necessary    map[string]interface{} `json:"necessary"`
	Step         map[string]interface{} `json:"step"`
	Capabilities Capabilities           `json:"capabilities"`
	Parameters   map[string]interface{} `json:"parameters"`
}

type Capabilities struct {
	KubeVersion KubeVersion  `json:"kubeVersion"`
	APIVersions *APIVersions `json:"apiVersions"`
}

type KubeVersion struct {
	Version    string `json:"version"`
	Major      string `json:"major"`
	Minor      string `json:"minor"`
	GitVersion string `json:"git_version"`
}

// APIVersions stores all available API versions and resource types.
type APIVersions struct {
	// versions stores all API versions, e.g.: "apps/v1", "batch/v1", "v1"
	versions map[string]bool
	// resources stores all resource types, e.g.: "apps/v1/Deployment", "batch/v1/Job"
	resources map[string]bool
}

// Has checks whether the specified API version or resource exists.
// The parameter can be:
// - An API version: "apps/v1", "batch/v1"
// - A resource type: "apps/v1/Deployment", "batch/v1/Job"
func (av *APIVersions) Has(apiVersionOrResource string) bool {
	if av == nil {
		return false
	}
	// First check if it is a resource type (format: "group/version/Kind")
	if av.resources != nil && av.resources[apiVersionOrResource] {
		return true
	}
	// Then check if it is an API version (format: "group/version" or "v1")
	if av.versions != nil && av.versions[apiVersionOrResource] {
		return true
	}
	return false
}

func GetNecessaryGlobe(name, namespace, labels, annotations, packageName, middlewareName interface{}) map[string]interface{} {
	return map[string]interface{}{
		GlobeKeyName:           name,
		GlobeKeyNamespace:      namespace,
		GlobeKeyLabels:         labels,
		GlobeKeyAnnotations:    annotations,
		GlobeKeyPackageName:    packageName,
		GlobeKeyMiddlewareName: middlewareName,
	}
}

const LintMode = false

// TemplateParse parses and executes a template.
func TemplateParse(ctx context.Context, temp string, templateValues *TemplateValues) (string, error) {
	var err error
	tpl := template.New(uuid.NewString()).
		Funcs(sprig.FuncMap())

	funcMap := funcMap()
	includedNames := make(map[string]int)

	// Add the template-rendering functions here so we can close over t.
	funcMap["include"] = includeFun(tpl, includedNames)
	funcMap["tpl"] = tplFun(tpl, includedNames)

	// Add the `required` function here so we can use lintMode
	tplLog := log.FromContext(ctx).WithName("template")
	funcMap["required"] = func(warn string, val interface{}) (interface{}, error) {
		if val == nil {
			if LintMode {
				// Don't fail on missing required values when linting
				tplLog.Info("Missing required value", "warning", warn)
				return "", nil
			}
			return val, errors.New(warn)
		} else if _, ok := val.(string); ok {
			if val == "" {
				if LintMode {
					// Don't fail on missing required values when linting
					tplLog.Info("Missing required value", "warning", warn)
					return "", nil
				}
				return val, errors.New(warn)
			}
		}

		return val, nil
	}

	// Override sprig fail function for linting and wrapping message
	funcMap["fail"] = func(msg string) (string, error) {
		if LintMode {
			tplLog.Info("Fail", "message", msg)
			return "", nil
		}
		return "", errors.New(msg)
	}

	// If we are not linting and have a cluster connection, provide a Kubernetes-backed
	// implementation.
	if !LintMode {
		funcMap["lookup"] = newLookupFunction(ctx)
	}

	// When DNS lookups are not enabled override the sprig function and return
	// an empty string.
	//if !e.EnableDNS {
	//	funcMap["getHostByName"] = func(_ string) string {
	//		return ""
	//	}
	//}

	tpl, err = tpl.
		Funcs(funcMap).
		Parse(temp)
	if err != nil {
		return "", err
	}
	buf := bytes.NewBuffer([]byte{})
	err = tpl.Execute(buf, templateValues)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

// ReplaceQuotedNumbersAndBools replaces quoted numeric and boolean values in raw YAML data.
//func ReplaceQuotedNumbersAndBools(yamlData []byte) []byte {
//	// Regex to match single-quoted or double-quoted values
//	re := regexp.MustCompile(`(?m)(?:(-?[ \t]*|[^\n:]+:[ \t]*)(?:('|"))([^'"\n]+)('|")?)`)
//
//	// Replacement function
//	return []byte(re.ReplaceAllStringFunc(string(yamlData), func(match string) string {
//		// Extract prefix, quote type, and value
//		submatches := re.FindStringSubmatch(match)
//		prefix := submatches[1] // prefix (key name or `- `)
//		quote := submatches[2]  // quote type (single `'` or double `"`)
//		value := submatches[3]  // value
//
//		// Try to convert to boolean
//		if boolVal, err := strconv.ParseBool(value); err == nil {
//			return fmt.Sprintf("%s%v", prefix, boolVal)
//		}
//
//		// Try to convert to integer
//		if intVal, err := strconv.Atoi(value); err == nil {
//			return fmt.Sprintf("%s%d", prefix, intVal)
//		}
//
//		// Try to convert to float
//		if floatVal, err := strconv.ParseFloat(value, 64); err == nil {
//			return fmt.Sprintf("%s%v", prefix, floatVal)
//		}
//
//		// If conversion fails, keep the original value (including quotes)
//		return fmt.Sprintf("%s%s%s%s", prefix, quote, value, quote)
//	}))
//}

const TemplateValuesPreActionNameKey = "PreActionName"

var toolsLog = ctrl.Log.WithName("tools")

// buildAPIVersions retrieves all API versions and resources from the Discovery Client.
func buildAPIVersions(disCli discovery.DiscoveryInterface) (*APIVersions, error) {
	av := &APIVersions{
		versions:  make(map[string]bool),
		resources: make(map[string]bool),
	}

	// 1. Get all API groups
	apiGroups, err := disCli.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("failed to get server groups: %w", err)
	}

	// 2. Iterate over each API group and get all its versions and resources
	for _, group := range apiGroups.Groups {
		for _, version := range group.Versions {
			groupVersion := version.GroupVersion
			// Add API version to the set
			av.versions[groupVersion] = true
			// For core/v1, also add "v1" as an alias
			if groupVersion == "v1" {
				av.versions["v1"] = true
			}

			// Get all resources for this group version
			resourceList, err := disCli.ServerResourcesForGroupVersion(groupVersion)
			if err != nil {
				// Some group versions may not be accessible; log a warning but continue
				toolsLog.Info("failed to get resources for group version", "groupVersion", groupVersion, "error", err)
				continue
			}

			// 3. Add all resource types to the set
			for _, resource := range resourceList.APIResources {
				// Skip sub-resources (containing '/', e.g. "pods/exec")
				if strings.Contains(resource.Name, "/") {
					continue
				}
				// Add resource type, format: "apps/v1/Deployment"
				resourceType := fmt.Sprintf("%s/%s", groupVersion, resource.Kind)
				av.resources[resourceType] = true
			}
		}
	}

	return av, nil
}

func GetTemplateValues(ctx context.Context, quoter Quoter) (*TemplateValues, error) {
	templateValues := new(TemplateValues)

	templateValues.Globe = make(map[string]interface{})

	// Assign Globe values
	if quoter.GetUnified() != nil && quoter.GetUnified().Raw != nil {
		unifiedByres, err := quoter.GetUnified().MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("marshal globe error: %w", err)
		}
		err = json.Unmarshal(unifiedByres, &templateValues.Globe)
		if err != nil {
			return nil, fmt.Errorf("unmarshal globe error: %w", err)
		}
		templateValues.Necessary = make(map[string]interface{})
		err = json.Unmarshal(unifiedByres, &templateValues.Necessary)
		if err != nil {
			return nil, fmt.Errorf("unmarshal necessary error: %w", err)
		}
	}

	// Assign Globe values
	for k, v := range GetNecessaryGlobe(
		quoter.GetName(),
		quoter.GetNamespace(),
		quoter.GetLabels(),
		quoter.GetAnnotations(),
		quoter.GetLabels()[v1.LabelPackageName],
		quoter.GetMiddlewareName(),
	) {
		if _, ok := templateValues.Globe[k]; !ok {
			templateValues.Globe[k] = v
		}
	}

	templateValues.Step = ctxkeys.StepFrom(ctx)

	disCli, err := kubeclient.GetDiscoveryClient()
	if err != nil {
		return nil, err
	}
	var serverVersion *version.Info
	serverVersion, err = disCli.ServerVersion()
	if err != nil {
		return nil, err
	}

	templateValues.Capabilities.KubeVersion.Version = serverVersion.String()
	templateValues.Capabilities.KubeVersion.Major = serverVersion.Major
	templateValues.Capabilities.KubeVersion.Minor = serverVersion.Minor
	templateValues.Capabilities.KubeVersion.GitVersion = serverVersion.GitVersion

	// Build APIVersions
	apiVersions, err := buildAPIVersions(disCli)
	if err != nil {
		// If building API versions fails, log but don't block the overall flow
		log.FromContext(ctx).Info("Failed to build API versions", "error", err)
		// Create an empty APIVersions to avoid nil pointer
		apiVersions = &APIVersions{
			versions:  make(map[string]bool),
			resources: make(map[string]bool),
		}
	}
	templateValues.Capabilities.APIVersions = apiVersions

	return templateValues, nil
}

// ParseTemplate parses and executes a template.
func ParseTemplate(ctx context.Context, tmpl []byte) ([]byte, error) {
	// Create the template
	t, err := template.New("template").Parse(string(tmpl))
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	// Execute the template
	var buf bytes.Buffer
	if err := t.Execute(&buf, nil); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.Bytes(), nil
}
