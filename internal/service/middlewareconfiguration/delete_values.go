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

package middlewareconfiguration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"k8s.io/apimachinery/pkg/runtime"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template/parse"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/pkg/tools"
	kjson "sigs.k8s.io/json"
	"sigs.k8s.io/yaml"
)

// DeleteParameterProjection restricts a copy of merged MID parameters before
// evaluating their templates. A nil projection requests the complete input for
// the existing empty-output check.
type DeleteParameterProjection func(*runtime.RawExtension) error
type prepareDeleteParameters func(context.Context, *tools.TemplateValues, DeleteParameterProjection) error

var errInputProjection = errors.New("cannot determine deletion input dependencies")

// A nil selection consumes the entire value. An empty selection consumes no
// members, but preserves the distinction between an absent map and an empty one.
type inputSelection map[string]inputSelection

func (s inputSelection) add(path []string, whole bool) {
	for i, key := range path {
		child, ok := s[key]
		if ok && child == nil {
			return
		}
		if i == len(path)-1 && whole {
			s[key] = nil
			return
		}
		if !ok {
			child = inputSelection{}
			s[key] = child
		}
		s = child
	}
}

// Check for side effects before pruning template inputs. Parse only candidate
// expressions: syntax or execution errors in unrelated pure values do not
// affect an otherwise provable identity.
var possibleInputMutation = regexp.MustCompile(`\b(set|unset|merge|mergeOverwrite|mustMerge|mustMergeOverwrite|include|tpl|template)\b`)

func checkInputIsolation(raw *runtime.RawExtension) error {
	if !possibleInputMutation.Match(raw.Raw) {
		return nil
	}
	source, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	tree := parse.New("input projection")
	tree.Mode = parse.SkipFuncCheck
	defs := map[string]*parse.Tree{}
	if _, err = tree.Parse(string(tools.ProcessYAMLGoTemp(source)), "{{", "}}", defs); err != nil {
		return fmt.Errorf("cannot isolate template inputs: %w", err)
	}
	if templateMutationLookup(defs)(tree.Name) {
		return fmt.Errorf("%w: inputs may be modified during publication", errInputProjection)
	}
	return nil
}

func (s inputSelection) project(raw *runtime.RawExtension) error {
	if s == nil || len(raw.Raw) == 0 {
		return nil
	}
	var value any
	if err := kjson.UnmarshalCaseSensitivePreserveInts(raw.Raw, &value); err != nil {
		return err
	}
	projected, err := s.selectValue(value)
	if err != nil {
		return err
	}
	data, err := json.Marshal(projected)
	if err != nil {
		return err
	}
	if bytes.Contains(data, []byte("{{")) {
		if err := checkInputIsolation(raw); err != nil {
			return err
		}
	}
	raw.Raw = data
	return nil
}

func (s inputSelection) selectValue(raw any) (any, error) {
	if s == nil {
		return raw, nil
	}
	switch value := raw.(type) {
	case map[string]any:
		// Preserve key presence for hasKey/len/truth tests without running the
		// unused members. A generated key could alias the requested member.
		result := make(map[string]any, len(value))
		for key, member := range value {
			if strings.Contains(key, "{{") {
				return nil, fmt.Errorf("%w: templated map key", errInputProjection)
			}
			child, ok := s[key]
			if !ok {
				result[key] = nil
				continue
			}
			v, err := child.selectValue(member)
			if err != nil {
				return nil, err
			}
			result[key] = v
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for key, child := range s {
			if idx, err := strconv.Atoi(key); err == nil && idx >= 0 && idx < len(value) {
				v, err := child.selectValue(value[idx])
				if err != nil {
					return nil, err
				}
				result[idx] = v
			}
		}
		return result, nil
	default:
		// A scalar template may itself produce a map or list.
		return raw, nil
	}
}

// Track paths from the template root through the projected field's lexical
// bindings. A function receiving a whole subtree consumes that subtree.
type inputReads struct {
	paths     inputSelection
	trees     map[string]*parse.Tree
	active    map[string]bool
	tracked   string
	base      *tools.TemplateValues
	ambiguous bool
}

type inputScope struct {
	dot  []string
	vars map[string][]string
}

func rootScope(dot []string) inputScope {
	return inputScope{dot: dot, vars: map[string][]string{"$": dot}}
}

func templateInputReads(source, tracked string, base *tools.TemplateValues) (inputSelection, error) {
	tree := parse.New("inputs")
	tree.Mode = parse.SkipFuncCheck
	reads := inputReads{tracked: tracked, base: base, paths: inputSelection{}, trees: map[string]*parse.Tree{}, active: map[string]bool{}}
	if _, err := tree.Parse(source, "{{", "}}", reads.trees); err != nil {
		return nil, err
	}
	reads.list(tree.Root, rootScope([]string{"$"}))
	if reads.ambiguous {
		return nil, fmt.Errorf("%w: computed key in %s", errInputProjection, tracked)
	}
	return reads.paths, nil
}

func (r *inputReads) read(path []string, whole bool) {
	if len(path) == 1 && whole {
		// A helper receiving the root can read either map.
		r.paths.add([]string{r.tracked}, true)
	} else if len(path) > 1 && path[1] == r.tracked {
		r.paths.add(path[1:], whole)
	}
}

func extendInput(path, tail []string) []string {
	if path == nil {
		return nil
	}
	return append(slices.Clip(path), tail...)
}

func (r *inputReads) arg(n parse.Node, s inputScope) []string {
	switch a := n.(type) {
	case *parse.DotNode:
		return s.dot
	case *parse.FieldNode:
		return extendInput(s.dot, a.Ident)
	case *parse.VariableNode:
		return extendInput(s.vars[a.Ident[0]], a.Ident[1:])
	case *parse.ChainNode:
		return extendInput(r.arg(a.Node, s), a.Field)
	case *parse.PipeNode:
		return r.pipe(a, s)
	}
	return nil
}

// Resolve a dynamic index only when its key is already available from the
// owner's immutable template inputs. Never guess a key from missing values.
func (r *inputReads) indexKey(key parse.Node, path []string) (string, bool) {
	switch n := key.(type) {
	case *parse.StringNode:
		return n.Text, true
	case *parse.NumberNode:
		if n.IsInt {
			return strconv.FormatInt(n.Int64, 10), true
		}
	}
	r.read(path, true)
	if len(path) < 3 {
		return "", false
	}
	var value any
	switch path[1] {
	case "Globe":
		value = r.base.Globe
	case "Necessary":
		value = r.base.Necessary
	default:
		return "", false
	}
	for _, key := range path[2:] {
		object, ok := value.(map[string]any)
		if !ok {
			return "", false
		}
		value = object[key]
	}
	switch key := value.(type) {
	case string:
		return key, true
	case int64:
		return strconv.FormatInt(key, 10), true
	}
	return "", false
}

// Pipelines pass their previous result as the last argument of the next call.
func (r *inputReads) command(c *parse.CommandNode, scope inputScope, previous []string, piped bool) []string {
	head := c.Args[0]
	if len(c.Args) == 1 && !piped {
		return r.arg(head, scope)
	}
	type argument struct {
		node parse.Node
		path []string
	}
	var args []argument
	for _, node := range c.Args[1:] {
		args = append(args, argument{node, r.arg(node, scope)})
	}
	if piped {
		args = append(args, argument{path: previous})
	}
	if fn, ok := head.(*parse.IdentifierNode); ok {
		switch fn.Ident {
		case "include":
			if len(args) == 2 {
				if name, ok := args[0].node.(*parse.StringNode); ok {
					r.helper(name.Text, args[1].path)
					return nil
				}
			}
		case "index", "get":
			if len(args) == 0 {
				return nil
			}
			path := args[0].path
			for _, key := range args[1:] {
				literal, known := r.indexKey(key.node, key.path)
				if !known {
					if len(path) > 1 && path[1] == r.tracked {
						r.ambiguous = true
					}
					r.read(path, true)
					path = nil
				} else {
					path = extendInput(path, []string{literal})
				}
			}
			return path
		case "hasKey":
			if len(args) == 2 {
				r.read(args[0].path, false)
				r.read(args[1].path, true)
				return nil
			}
		case "len", "keys", "empty":
			for _, a := range args {
				r.read(a.path, false)
			}
			return nil
		case "eq", "ne":
			if len(args) == 2 {
				for i, a := range args {
					if _, ok := a.node.(*parse.NilNode); ok {
						r.read(args[1-i].path, false)
						return nil
					}
				}
			}
		}
	}
	r.read(r.arg(head, scope), true)
	for _, a := range args {
		r.read(a.path, true)
	}
	return nil
}

func (r *inputReads) pipe(p *parse.PipeNode, s inputScope) []string {
	if p == nil {
		return nil
	}
	var path []string
	for i, c := range p.Cmds {
		path = r.command(c, s, path, i > 0)
	}
	for _, v := range p.Decl {
		key := v.Ident[0]
		if p.IsAssign {
			r.read(s.vars[key], true)
			r.read(path, true)
		}
		s.vars[key] = path
	}
	return path
}

func (r *inputReads) list(list *parse.ListNode, s inputScope) {
	if list == nil {
		return
	}
	for _, node := range list.Nodes {
		switch n := node.(type) {
		case *parse.ActionNode:
			path := r.pipe(n.Pipe, s)
			if len(n.Pipe.Decl) == 0 {
				r.read(path, true)
			}
		case *parse.IfNode:
			r.branch(&n.BranchNode, s)
		case *parse.WithNode:
			r.branch(&n.BranchNode, s)
		case *parse.RangeNode:
			r.branch(&n.BranchNode, s)
		case *parse.TemplateNode:
			r.helper(n.Name, r.pipe(n.Pipe, s))
		}
	}
}

func (r *inputReads) helper(name string, path []string) {
	tree := r.trees[name]
	if tree == nil || r.active[name] {
		r.read(path, true)
		return
	}
	r.active[name] = true
	r.list(tree.Root, rootScope(path))
	delete(r.active, name)
}

func (r *inputReads) branch(n *parse.BranchNode, s inputScope) {
	s.vars = maps.Clone(s.vars)
	path := r.pipe(n.Pipe, s)
	r.read(path, n.Type() == parse.NodeRange)
	r.list(n.ElseList, inputScope{dot: s.dot, vars: maps.Clone(s.vars)})
	switch n.Type() {
	case parse.NodeWith:
		s.dot = path
	case parse.NodeRange:
		s.dot = nil // The entire collection has already been selected.
	}
	r.list(n.List, s)
}

func configurationIdentityValues(ctx context.Context, cfg v1.Configuration, base *tools.TemplateValues, source string, prepare prepareDeleteParameters) (*tools.TemplateValues, error) {
	if len(cfg.Values.Raw) == 0 {
		return base, nil
	}
	values := *base
	if !bytes.Contains(cfg.Values.Raw, []byte("{{")) {
		// Literal values already have their publication types.
		err := kjson.UnmarshalCaseSensitivePreserveInts(cfg.Values.Raw, &values.Values)
		return &values, err
	}
	reads, err := templateInputReads(source, "Values", base)
	if err != nil {
		return nil, err
	}
	selected, usesValues := reads["Values"]
	if !usesValues {
		return base, nil
	}
	if err := selected.project(&cfg.Values); err != nil {
		return nil, err
	}
	cfg, err = renderDeleteReference(ctx, cfg, base, prepare, false)
	if err != nil {
		return nil, err
	}
	return &values, applyConfigurationValues(ctx, cfg, &values)
}

// Both the identity path and the empty-output proof render references as on
// publication. Only the latter may use complete inputs when projection is
// impossible: even a successful nonempty render never authorizes a deletion.
func renderDeleteReference(ctx context.Context, cfg v1.Configuration, base *tools.TemplateValues, prepare prepareDeleteParameters, checkEmpty bool) (v1.Configuration, error) {
	if !bytes.Contains(cfg.Values.Raw, []byte("{{")) {
		return cfg, nil
	}
	values, err := copyConfigurationRenderInputs(base)
	if err != nil {
		return cfg, err
	}
	if prepare != nil {
		source, err := yaml.Marshal(cfg.Values)
		if err != nil {
			return cfg, err
		}
		reads, err := templateInputReads(string(tools.ProcessYAMLGoTemp(source)), "Parameters", base)
		selection, used := reads["Parameters"]
		if checkEmpty && errors.Is(err, errInputProjection) {
			selection, used = nil, true
		} else if err != nil {
			return cfg, err
		}
		if used {
			project := func(raw *runtime.RawExtension) error {
				err := selection.project(raw)
				if checkEmpty && errors.Is(err, errInputProjection) {
					return nil
				}
				return err
			}
			if err := prepare(ctx, values, project); err != nil {
				return cfg, fmt.Errorf("prepare configuration reference parameters: %w", err)
			}
		}
	}
	refs, err := RenderConfigurationReferences(ctx, []v1.Configuration{cfg}, values)
	if err != nil {
		return cfg, err
	}
	if len(refs) != 1 || refs[0].Name != cfg.Name {
		return cfg, errors.New("rendered configuration reference changed its identity")
	}
	return refs[0], nil
}
