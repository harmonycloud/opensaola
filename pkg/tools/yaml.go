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
	"regexp"
	"strings"
)

func ProcessYAMLGoTemp(yamlBytes []byte) []byte {
	yamlString := string(yamlBytes)
	lines := strings.Split(yamlString, "\n")

	// Regex to match Go templates wrapped in quotes within YAML key-value pairs
	// Match formats:
	// 1. key: '{{...}}' -> key: {{...}}
	// 2. key: "{{...}}" -> key: {{...}}
	// 3. key: '"{{...}}"' -> key: '{{...}}'
	// 4. key: "'{{...}}'" -> key: "{{...}}"
	singleQuotePattern := regexp.MustCompile(`(\s*\w+\s*:\s*)'(\{\{.*?\}\})'`)
	doubleQuotePattern := regexp.MustCompile(`(\s*\w+\s*:\s*)"(\{\{.*?\}\})"`)
	nestedDoubleInSinglePattern := regexp.MustCompile(`(\s*\w+\s*:\s*)'("(\{\{.*?\}\})")'`)
	nestedSingleInDoublePattern := regexp.MustCompile(`(\s*\w+\s*:\s*)"('(\{\{.*?\}\})')"`)

	for idx, line := range lines {
		// Handle nested quotes: '"{{...}}"' -> '{{...}}'
		if nestedDoubleInSinglePattern.MatchString(line) {
			line = nestedDoubleInSinglePattern.ReplaceAllString(line, `${1}'${3}'`)
		} else if nestedSingleInDoublePattern.MatchString(line) {
			// Handle nested quotes: "'{{...}}'" -> "{{...}}"
			line = nestedSingleInDoublePattern.ReplaceAllString(line, `${1}"${3}"`)
		} else if singleQuotePattern.MatchString(line) {
			// Handle single-quoted Go template: '{{...}}' -> {{...}}
			line = singleQuotePattern.ReplaceAllString(line, `${1}${2}`)
		} else if doubleQuotePattern.MatchString(line) {
			// Handle double-quoted Go template: "{{...}}" -> {{...}}
			line = doubleQuotePattern.ReplaceAllString(line, `${1}${2}`)
		}
		lines[idx] = line
	}

	// Convert the processed string back to a byte slice and return
	return []byte(strings.Join(lines, "\n"))
}
