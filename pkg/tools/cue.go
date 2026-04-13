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
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/parser"
	"fmt"
)

// ParseAndAddCueFile Parse And Add CueFile
func ParseAndAddCueFile(bi *build.Instance, fieldName string, content interface{}) error {
	f, err := parser.ParseFile(fieldName, content, parser.DeclarationErrors)
	if err != nil {
		fmt.Println(err)
		return err
	}
	if err = bi.AddSyntax(f); err != nil {
		return err
	}
	return nil
}
