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
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type StructMergeType string

const (
	StructMergeMapType   StructMergeType = "map"
	StructMergeArrayType StructMergeType = "array"
)

func StructMerge(old, new any, typo StructMergeType) error {
	var (
		err                             error
		oldBytes, newBytes, resultBytes []byte
		oldMap, newMap                  map[string]any
		oldSlice, newSlice              []any
	)

	oldBytes, err = json.Marshal(old)
	if err != nil {
		return fmt.Errorf("failed to marshal old object: %w", err)
	}
	newBytes, err = json.Marshal(new)
	if err != nil {
		return fmt.Errorf("failed to marshal new object: %w", err)
	}

	if string(oldBytes) == "null" && string(newBytes) == "null" {
		return nil
	} else if string(oldBytes) == "null" {
		return nil
	} else if string(newBytes) == "null" {
		return json.Unmarshal(oldBytes, new)
	}

	switch typo {
	case StructMergeMapType:
		err = json.Unmarshal(oldBytes, &oldMap)
		if err != nil {
			return fmt.Errorf("failed to unmarshal old object: %w", err)
		}
		err = json.Unmarshal(newBytes, &newMap)
		if err != nil {
			return fmt.Errorf("failed to unmarshal new object: %w", err)
		}
		result := MergeMap(oldMap, newMap)
		resultBytes, err = json.Marshal(result)
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}
	case StructMergeArrayType:
		err = json.Unmarshal(oldBytes, &oldSlice)
		if err != nil {
			return fmt.Errorf("failed to unmarshal old object: %w", err)
		}
		err = json.Unmarshal(newBytes, &newSlice)
		if err != nil {
			return fmt.Errorf("failed to unmarshal new object: %w", err)
		}
		result := MergeArray(oldSlice, newSlice)
		resultBytes, err = json.Marshal(result)
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}

	}

	return json.Unmarshal(resultBytes, new)
}

func MergeMap(old, new map[string]any) map[string]any {
	var (
		k string
		v any
	)

	defer func() {
		if r := recover(); r != nil {
			logger.Log.Errorf("panic: %v key: %s value: %v", r, k, v)

			buf := make([]byte, 1024)
			n := runtime.Stack(buf, false) // false = print only the current goroutine's stack
			fmt.Printf("Stack trace:\n%s\n", string(buf[:n]))
		}
	}()
	for k, v = range new {
		switch v.(type) {
		case map[string]any:
			if old[k] == nil {
				old[k] = v
			} else {
				old[k] = MergeMap(old[k].(map[string]any), v.(map[string]any))
			}
		case []any:
			if old[k] == nil {
				old[k] = v
			} else {
				old[k] = MergeArray(old[k].([]any), v.([]any))
			}
		default:
			old[k] = v
		}
	}
	return old
}

func MergeMapString(old, new map[string]string) map[string]string {
	for k, v := range new {
		if old[k] == "" {
			old[k] = v
		} else {
			old[k] = v
		}
	}
	return old

}

var ArrayStructKey = []string{"name", "serviceAccountName"}

func MergeArray(old, new []any) []any {
	for newIdx, v := range new {
		switch v.(type) {
		case map[string]any:
			// If old contains a map with the same name, merge them
			if v, ok := v.(map[string]any); ok {
				var (
					found    bool
					keyFound bool
				)
				for oldIdx, o := range old {
					if o, ok := o.(map[string]any); ok {
						for _, ask := range ArrayStructKey {
							_, ok1 := o[ask]
							_, ok2 := v[ask]
							if ok1 && ok2 {
								keyFound = true
								if o[ask] == v[ask] {
									old[oldIdx] = MergeMap(o, v)
									found = true
									break
								}
							}
						}
					}
				}
				if !keyFound {
					if len(old) >= newIdx+1 {
						old[newIdx] = MergeMap(old[newIdx].(map[string]any), v)
					} else {
						old = append(old, v)
					}
				} else if !found {
					old = append(old, v)
				}
			}
		default:
			// If the value exists, don't add duplicates
			if len(new) > 0 {
				old = new
			}
		}
	}
	return old
}

// generateComparableKey generates a unique key to identify non-structured array elements, supporting multiple types.
func generateComparableKey(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case int, int64, float64, bool:
		return fmt.Sprintf("%v", v)
	default:
		// For complex types (e.g. map), use JSON serialization
		bytes, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(bytes)
	}
}

// ExtractJsonKey extracts the leaf keys from JSON in dot-notation format (xx.xx.x.x).
func ExtractJsonKey(jsonStr string) (jsonMap map[string]any, err error) {
	jsonNode := gjson.Parse(jsonStr)
	jsonMap = make(map[string]any)
	if jsonNode.Type != gjson.JSON {
		return nil, fmt.Errorf("invalid json format")
	}
	recursionHandleNode("", jsonNode, jsonMap)
	return
}

// CompareJson compares two JSON values.
// Note: only compares keys that exist in old.
func CompareJson(ctx context.Context, new, old any) (isSame bool, err error) {
	// Compare the CR spec
	newBytes, _ := json.Marshal(new)
	oldBytes, _ := json.Marshal(old)
	newKeyMap, err := ExtractJsonKey(string(newBytes))
	if err != nil {
		return false, fmt.Errorf("invalid new json format")
	}
	oldKeyMap, err := ExtractJsonKey(string(oldBytes))
	if err != nil {
		return false, fmt.Errorf("invalid old json format")
	}
	for k, v := range oldKeyMap {
		if v != nil && newKeyMap[k] != v {
			logger.Log.Warnf("json diff %s: old: %v new: %v", k, v, newKeyMap[k])
			return false, nil
		}
	}
	return true, nil
}

func recursionHandleNode(key string, node gjson.Result, jsonMap map[string]interface{}) {
	if node.Type == gjson.JSON {
		if node.IsObject() {
			// If it's a JSON object, recursively process each field
			for s, result := range node.Map() {
				if strings.Contains(s, ".") {
					s = strings.ReplaceAll(s, ".", "\\.") // escape `.`
				}
				recursionHandleNode(key+"."+s, result, jsonMap)
			}
		} else if node.IsArray() {
			// If it's an array, check if it's a struct array; if so, sort by `name` field
			array := node.Array()
			if isStructArray(array) {
				array = sortStructArrayByName(array) // sort struct array by name
			} else {
				array = sortArray(array) // sort the array
			}
			for i, r := range array {
				recursionHandleNode(fmt.Sprintf("%s.%d", key, i), r, jsonMap)
			}
		}
	} else {
		// Store non-object/non-array leaf values directly into the result
		jsonMap[key[1:]] = node.Value()
	}
}

func isStructArray(array []gjson.Result) bool {
	for _, item := range array {
		if !item.IsObject() {
			return false
		}
		if _, exists := item.Map()["name"]; !exists {
			return false
		}
	}
	return true
}

func sortStructArrayByName(array []gjson.Result) []gjson.Result {
	// Sort struct elements in the array by the `name` field
	sort.Slice(array, func(i, j int) bool {
		nameI := array[i].Get("name").String()
		nameJ := array[j].Get("name").String()
		return nameI < nameJ
	})
	return array
}

func sortArray(array []gjson.Result) []gjson.Result {
	// Create an array with original indices for sorting
	sortableArray := make([]struct {
		index int
		value string
		node  gjson.Result
	}, len(array))

	for i, elem := range array {
		// Serialize each element in the array to a string
		sortableArray[i] = struct {
			index int
			value string
			node  gjson.Result
		}{
			index: i,
			value: generateComparableKey(elem.Value()),
			node:  elem,
		}
	}

	// Sort the serialized strings
	sort.Slice(sortableArray, func(i, j int) bool {
		return sortableArray[i].value < sortableArray[j].value
	})

	// Build the sorted result
	sortedArray := make([]gjson.Result, len(array))
	for i, elem := range sortableArray {
		sortedArray[i] = elem.node
	}

	return sortedArray
}

// ParseJsonkvToMap parses a JSON key-value pair into a map.
func ParseJsonkvToMap(ctx context.Context, mp map[string]interface{}, key string, value interface{}) error {
	mpBytes, err := json.Marshal(mp)
	if err != nil {
		return err
	}
	result, err := sjson.Set(string(mpBytes), key, value)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(result), &mp)
}

func StructCompare(old, new any, typo StructMergeType) (bool, error) {
	var (
		err      error
		oldBytes []byte
		newBytes []byte
		oldMap   map[string]any
		newMap   map[string]any
		oldSlice []any
		newSlice []any
	)

	// Serialize the old object
	oldBytes, err = json.Marshal(old)
	if err != nil {
		return false, fmt.Errorf("failed to marshal old object: %w", err)
	}

	// Serialize the new object
	newBytes, err = json.Marshal(new)
	if err != nil {
		return false, fmt.Errorf("failed to marshal new object: %w", err)
	}

	// If both objects are null, consider them the same
	if string(oldBytes) == "null" && string(newBytes) == "null" {
		return true, nil
	}

	// Compare based on type
	switch typo {
	case StructMergeMapType:
		// Unmarshal to map
		err = json.Unmarshal(oldBytes, &oldMap)
		if err != nil {
			return false, fmt.Errorf("failed to unmarshal old object: %w", err)
		}
		err = json.Unmarshal(newBytes, &newMap)
		if err != nil {
			return false, fmt.Errorf("failed to unmarshal new object: %w", err)
		}

		// Compare whether the maps are the same
		if !compareMap(oldMap, newMap) {
			return false, nil
		}

	case StructMergeArrayType:
		// Unmarshal to slice
		err = json.Unmarshal(oldBytes, &oldSlice)
		if err != nil {
			return false, fmt.Errorf("failed to unmarshal old object: %w", err)
		}
		err = json.Unmarshal(newBytes, &newSlice)
		if err != nil {
			return false, fmt.Errorf("failed to unmarshal new object: %w", err)
		}

		// Compare whether the slices are the same
		if !compareSlice(oldSlice, newSlice) {
			return false, nil
		}
	}

	return true, nil
}

// compareMap compares two maps for equality.
func compareMap(oldMap, newMap map[string]any) bool {
	if len(oldMap) != len(newMap) {
		return false
	}
	for k, v := range oldMap {
		if newV, ok := newMap[k]; !ok || !compareValue(v, newV) {
			return false
		}
	}
	return true
}

// compareSlice compares two slices for equality.
func compareSlice(oldSlice, newSlice []any) bool {
	if len(oldSlice) != len(newSlice) {
		return false
	}
	for i := range oldSlice {
		if !compareValue(oldSlice[i], newSlice[i]) {
			return false
		}
	}
	return true
}

// compareValue compares two values for equality.
func compareValue(oldVal, newVal any) bool {
	switch old := oldVal.(type) {
	case map[string]any:
		if new, ok := newVal.(map[string]any); ok {
			return compareMap(old, new)
		}
	case []any:
		if new, ok := newVal.([]any); ok {
			return compareSlice(old, new)
		}
	default:
		return old == newVal
	}
	return false
}
