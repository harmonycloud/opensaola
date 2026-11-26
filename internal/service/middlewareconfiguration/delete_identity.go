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
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template/parse"

	"github.com/google/uuid"
	"github.com/harmonycloud/opensaola/pkg/tools"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var errOpaqueDeleteIdentity = errors.New("configuration generates its resource dynamically; recorded identity is required")

var errMutableDeleteIdentity = errors.New("configuration can modify identity inputs before rendering; recorded identity is required")

type deleteIdentity struct {
	APIVersion, Kind, Name, Namespace deleteIdentityField
}

type deleteIdentityField struct {
	node       *yaml.Node
	projection *identityProjection
	scope      string
	scopeInit  string
}

func (f deleteIdentityField) Present() bool { return f.node != nil }
func (f deleteIdentityField) Dynamic() bool {
	return f.Present() && strings.Contains(f.node.Value, f.projection.prefix)
}

func (f deleteIdentityField) Render(ctx context.Context, values *tools.TemplateValues) (string, error) {
	return f.render(ctx, values, nil)
}

func (f deleteIdentityField) render(ctx context.Context, values *tools.TemplateValues, inputs func(string) (*tools.TemplateValues, error)) (string, error) {
	if !f.Present() {
		return "", fmt.Errorf("identity field is missing")
	}
	if f.node.Kind != yaml.ScalarNode || f.node.ShortTag() != "!!str" {
		return "", fmt.Errorf("identity field must be a string")
	}
	value := f.node.Value
	if f.Dynamic() {
		position := f.projection.lastAction(value)
		if takesEffectAt(f.projection.firstMutation, position) {
			return "", errMutableDeleteIdentity
		}
		// Restore the original scalar quoting before running its actions. For a
		// bare {{ .Values.name | quote }} the quotes are supplied at runtime.
		node := *f.node
		node.HeadComment, node.LineComment, node.FootComment = "", "", ""
		wire, err := yaml.Marshal(&node)
		if err != nil {
			return "", err
		}
		template := f.projection.restore(string(wire))
		if f.scope != "" {
			name := strconv.Quote(f.projection.prefix + "scope")
			template = f.scopeInit + "{{define " + name + "}}" + template + "{{end}}{{template " + name + " " + f.scope + "}}"
		}
		bindings, err := f.projection.bindings(template, position)
		if err != nil {
			return "", err
		}
		template = f.projection.definitions + bindings + template
		if inputs != nil {
			values, err = inputs(template)
			if err != nil {
				return "", err
			}
		}
		rendered, err := tools.TemplateParse(ctx, template, values)
		if err != nil {
			return "", err
		}
		scalar, err := identityDocument(rendered)
		if err != nil || scalar.Kind != yaml.ScalarNode || scalar.ShortTag() != "!!str" {
			return "", fmt.Errorf("rendered identity field must be one YAML string")
		}
		value = scalar.Value
	}
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") || strings.Contains(value, "<no value>") {
		return "", fmt.Errorf("identity field is empty or unresolved")
	}
	return value, nil
}

func identityDocument(text string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 {
		return nil, fmt.Errorf("expected one YAML document")
	}
	for {
		var next yaml.Node
		err := decoder.Decode(&next)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(next.Content) == 1 && emptyYAMLDocument(next.Content[0]) {
			continue
		}
		return nil, fmt.Errorf("multiple configuration documents: %w", errOpaqueDeleteIdentity)
	}
	return doc.Content[0], nil
}

func emptyYAMLDocument(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null" && node.Value == "" && node.Style&yaml.TaggedStyle == 0
}

// Build a YAML document without executing the template. Each action is an
// inert scalar token. A structural key or identity value inside a conditional
// cannot identify a single target; such templates require deployed inventory.
type identityProjection struct {
	strings.Builder
	prefix        string
	definitions   string
	locals        []*parse.ActionNode
	firstMutation parse.Pos
	helperMutates func(string) bool
	assigned      map[string]parse.Pos
	actions       []identityAction
	line          int
	conditional   map[int]bool
}

type identityAction struct {
	expression string
	position   parse.Pos
}

func takesEffectAt(effect, position parse.Pos) bool { return effect > 0 && effect <= position }

func (p *identityProjection) lastAction(text string) parse.Pos {
	var position parse.Pos
	for i, a := range p.actions {
		if strings.Contains(text, p.prefix+strconv.Itoa(i)+"_") {
			position = max(position, a.position)
		}
	}
	return position
}

func (p *identityProjection) write(text string, conditional bool) {
	p.WriteString(text)
	for line := range strings.SplitSeq(text, "\n") {
		if conditional && strings.TrimSpace(line) != "" {
			p.conditional[p.line] = true
		}
		p.line++
	}
	p.line--
}

func (p *identityProjection) restore(text string) string {
	// Each encoded token ends in an underscore so index 1 cannot match 10.
	for i, action := range p.actions {
		text = strings.ReplaceAll(text, p.prefix+strconv.Itoa(i)+"_", action.expression)
	}
	return text
}

// Keep the lexical bindings used by this field. Ambiguous assignments are
// left unbound so that rendering fails instead of using a different scope.
func (p *identityProjection) bindings(field string, position parse.Pos) (string, error) {
	if !strings.Contains(field, "$") {
		return "", nil
	}
	needed := map[string]bool{}
	// Match actual $variable tokens, excluding Go string literals.
	for _, tok := range identityVariables.FindAllString(field, -1) {
		if tok[0] == '$' {
			needed[tok] = true
		}
	}
	seen := map[string]int{}
	for _, a := range p.locals {
		if a.Position() > position {
			continue
		}
		for _, v := range a.Pipe.Decl {
			seen[v.Ident[0]]++
		}
	}
	var prefix []string
	for i := len(p.locals) - 1; i >= 0; i-- {
		a := p.locals[i]
		if len(a.Pipe.Decl) != 1 || a.Position() > position {
			continue
		}
		key := a.Pipe.Decl[0].Ident[0]
		if !needed[key] || seen[key] != 1 || a.Pipe.IsAssign {
			continue
		}
		prefix = append(prefix, a.String())
		for _, cmd := range a.Pipe.Cmds {
			for _, tok := range identityVariables.FindAllString(cmd.String(), -1) {
				if tok[0] == '$' {
					needed[tok] = true
				}
			}
		}
	}
	for key := range needed {
		if takesEffectAt(p.assigned[key], position) {
			return "", errMutableDeleteIdentity
		}
	}
	slices.Reverse(prefix)
	return strings.Join(prefix, ""), nil
}

var identityVariables = regexp.MustCompile(`"(?:\\.|[^"\\])*"|` + "`[^`]*`" + `|'(?:\\.|[^'\\])*'|\$[a-zA-Z_][a-zA-Z0-9_]*`)

func (p *identityProjection) project(list *parse.ListNode, conditional bool) {
	if list == nil {
		return
	}
	for _, n := range list.Nodes {
		if _, ok := n.(*parse.IfNode); ok && noIdentityOutput(n) {
			continue
		}
		switch n := n.(type) {
		case *parse.CommentNode:
			continue
		case *parse.TextNode:
			p.write(string(n.Text), conditional)
		case *parse.ActionNode:
			if len(n.Pipe.Decl) > 0 || n.Pipe.Cmds[0].Args[0].String() == "fail" {
				continue
			}
			if len(n.Pipe.Cmds) == 1 && len(n.Pipe.Cmds[0].Args) == 1 {
				if lit, ok := n.Pipe.Cmds[0].Args[0].(*parse.StringNode); ok && strings.Contains(lit.Text, "\n") {
					p.write(lit.Text, conditional)
					continue
				}
			}
			if len(n.Pipe.Cmds) == 1 && len(n.Pipe.Cmds[0].Args) == 2 && n.Pipe.Cmds[0].Args[0].String() == "printf" {
				if lit, ok := n.Pipe.Cmds[0].Args[1].(*parse.StringNode); ok && !strings.Contains(lit.Text, "%") {
					p.write(lit.Text, conditional)
					continue
				}
			}
			last := n.Pipe.Cmds[len(n.Pipe.Cmds)-1]
			if len(last.Args) >= 2 && (last.Args[0].String() == "nindent" || last.Args[0].String() == "indent") {
				if size, err := strconv.Atoi(last.Args[1].String()); err == nil && size >= 0 && size < 256 {
					line := ""
					if last.Args[0].String() == "nindent" {
						line = "\n"
					}
					p.write(line+strings.Repeat(" ", size)+p.prefix+"block_: null", conditional)
					continue
				}
			}
			p.write(p.prefix+strconv.Itoa(len(p.actions))+"_", conditional)
			p.actions = append(p.actions, identityAction{n.String(), n.Position()})
		case *parse.TemplateNode:
			p.write(p.prefix+strconv.Itoa(len(p.actions))+"_", conditional)
			p.actions = append(p.actions, identityAction{n.String(), n.Position()})
		case *parse.IfNode:
			if n.ElseList != nil && p.branchFields(n, conditional) {
				continue
			}
			if !strings.Contains(n.String(), "\n") {
				p.write(p.prefix+strconv.Itoa(len(p.actions))+"_", conditional)
				p.actions = append(p.actions, identityAction{n.String(), lastBranchPosition(n)})
				continue
			}
			p.project(n.List, true)
			p.project(n.ElseList, true)
		case *parse.WithNode:
			p.project(n.List, true)
			p.project(n.ElseList, true)
		case *parse.RangeNode:
			p.project(n.List, true)
			p.project(n.ElseList, true)
		default:
			// A named template or a loop-control action may alter structure.
			p.write(p.prefix+"unsupported_", true)
		}
	}
}

// Both arms of some resource templates supply a small mapping of identity
// fields (for example namespaced Role versus ClusterRole). Retain each
// field's condition and value as one action, including its original quoting.
func lastBranchPosition(n *parse.IfNode) parse.Pos {
	pos := n.Position()
	for _, list := range []*parse.ListNode{n.List, n.ElseList} {
		if list != nil && len(list.Nodes) > 0 {
			pos = max(pos, list.Nodes[len(list.Nodes)-1].Position())
		}
	}
	return pos
}

func (p *identityProjection) branchFields(n *parse.IfNode, conditional bool) bool {
	if n.Pipe.IsAssign || len(n.Pipe.Decl) > 0 {
		return false
	}
	type branchField struct {
		column int
		text   string
	}
	arms := make([]map[string]branchField, 2)
	var keys []string
	for i, list := range []*parse.ListNode{n.List, n.ElseList} {
		if len(list.Nodes) == 0 {
			return false
		}
		t, ok := list.Nodes[0].(*parse.TextNode)
		if !ok || !strings.HasPrefix(string(t.Text), "\n") {
			return false
		}
		part := &identityProjection{prefix: p.prefix, line: 1, conditional: map[int]bool{}}
		for _, node := range list.Nodes {
			switch node.(type) {
			case *parse.TextNode, *parse.ActionNode, *parse.TemplateNode:
			default:
				return false
			}
		}
		part.project(list, false)
		doc, err := identityDocument(part.String())
		if err != nil || doc.Kind != yaml.MappingNode {
			return false
		}
		arms[i] = map[string]branchField{}
		for j := 0; j < len(doc.Content); j += 2 {
			k, v := doc.Content[j], doc.Content[j+1]
			if !slices.Contains([]string{"apiVersion", "kind", "name", "namespace"}, k.Value) || v.Kind != yaml.ScalarNode {
				return false
			}
			if _, exists := arms[i][k.Value]; exists {
				return false
			}
			raw, err := yaml.Marshal(v)
			if err != nil {
				return false
			}
			arms[i][k.Value] = branchField{k.Column, part.restore(strings.TrimSpace(string(raw)))}
			if !slices.Contains(keys, k.Value) {
				keys = append(keys, k.Value)
			}
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		a, b := arms[0][key], arms[1][key]
		column := max(a.column, b.column) - 1
		p.write("\n"+strings.Repeat(" ", column)+key+": "+p.prefix+strconv.Itoa(len(p.actions))+"_", conditional)
		p.actions = append(p.actions, identityAction{"{{if " + n.Pipe.String() + "}}" + a.text + "{{else}}" + b.text + "{{end}}", lastBranchPosition(n)})
	}
	return true
}

// Packages often wrap the entire resource in a single if with no alternative.
// This gates its existence, while leaving the possible resource identity unique.
// A validation guard or variable declaration does not emit any YAML.
func noIdentityOutput(node parse.Node) bool {
	switch n := node.(type) {
	case *parse.CommentNode:
		return true
	case *parse.TextNode:
		return strings.TrimSpace(string(n.Text)) == ""
	case *parse.ActionNode:
		return len(n.Pipe.Decl) > 0 || n.Pipe.Cmds[0].Args[0].String() == "fail"
	case *parse.IfNode:
		return noIdentityOutput(n.List) && noIdentityOutput(n.ElseList)
	case *parse.ListNode:
		if n == nil {
			return true
		}
		for _, child := range n.Nodes {
			if !noIdentityOutput(child) {
				return false
			}
		}
		return true
	}
	return false
}

func soleResourceGuard(list *parse.ListNode) *parse.IfNode {
	var guard *parse.IfNode
	for _, n := range list.Nodes {
		if noIdentityOutput(n) {
			continue
		}
		switch n := n.(type) {
		case *parse.IfNode:
			if guard != nil || n.ElseList != nil {
				return nil
			}
			guard = n
		default:
			return nil
		}
	}
	return guard
}

func (p *identityProjection) fields(node *yaml.Node, wanted ...string) (map[string]*yaml.Node, error) {
	result := map[string]*yaml.Node{}
	if node == nil {
		return result, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("identity parent is not a YAML mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if p.conditional[key.Line] && slices.Contains(wanted, key.Value) && (key.Value == "kind" || key.Value == "apiVersion" || key.Value == "metadata") {
			return nil, errOpaqueDeleteIdentity
		}
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" || strings.Contains(key.Value, p.prefix) || p.conditional[key.Line] && slices.Contains(wanted, key.Value) {
			return nil, fmt.Errorf("identity mapping has a dynamic or ambiguous key on line %d", key.Line)
		}
		for _, name := range wanted {
			if name != key.Value {
				continue
			}
			if result[name] != nil || value.Kind != yaml.MappingNode && p.conditional[value.Line] {
				return nil, fmt.Errorf("ambiguous identity field %s", name)
			}
			result[name] = value
		}
	}
	return result, nil
}

func parseDeleteIdentity(template string) (deleteIdentity, error) {
	return parseIdentityFields(template, true)
}

// Legacy owner migration uses the static GVK and never evaluates identity fields.
func parseDeleteType(template string) (deleteIdentity, error) {
	return parseIdentityFields(template, false)
}

func parseIdentityFields(template string, withMetadata bool) (deleteIdentity, error) {
	tree := parse.New("delete identity")
	tree.Mode = parse.SkipFuncCheck | parse.ParseComments
	definitions := map[string]*parse.Tree{}
	if _, err := tree.Parse(template, "{{", "}}", definitions); err != nil {
		return deleteIdentity{}, fmt.Errorf("invalid configuration template: %w", err)
	}
	root := tree.Root
	var locals []*parse.ActionNode
	for {
		for _, n := range root.Nodes {
			if a, ok := n.(*parse.ActionNode); ok && len(a.Pipe.Decl) > 0 {
				locals = append(locals, a)
			}
		}
		guard := soleResourceGuard(root)
		if guard == nil {
			break
		}
		root = guard.List
	}
	projection := &identityProjection{prefix: "opensaola" + uuid.NewString() + "_", line: 1, conditional: map[int]bool{}}
	var def strings.Builder
	for name, t := range definitions {
		if name != tree.Name {
			fmt.Fprintf(&def, "{{define %q}}%s{{end}}", name, t.Root.String())
		}
	}
	projection.definitions = def.String()
	projection.locals = locals
	projection.assigned = map[string]parse.Pos{}
	projection.helperMutates = templateMutationLookup(definitions)
	projection.inspectBindings(tree.Root)
	projection.project(root, false)
	document, err := identityDocument(projection.String())
	if errors.Is(err, errOpaqueDeleteIdentity) && !strings.Contains(template, "{{") {
		if identity, ok := staticCRDBundle(template, projection); ok {
			return identity, nil
		}
	}
	if err != nil {
		document, err = identityDocument(stripIdentityBody(projection.String()))
	}
	// Empty projected text can also come from skipped template actions. Only
	// executing the original configuration can confirm that it produces nothing.
	if withMetadata && (errors.Is(err, io.EOF) || err == nil && emptyYAMLDocument(document)) {
		return deleteIdentity{}, errOpaqueDeleteIdentity
	}
	if err != nil {
		if generatesResource(root) {
			return deleteIdentity{}, errOpaqueDeleteIdentity
		}
		return deleteIdentity{}, fmt.Errorf("cannot resolve configuration identity: %w", err)
	}
	if document.Kind == yaml.ScalarNode && strings.Contains(document.Value, projection.prefix) {
		if identity, found, err := hintedDeleteIdentity(tree.Root); found || err != nil {
			return identity, err
		}
		if identity, found, err := literalTplIdentity(tree.Root, projection); found || err != nil {
			return identity, err
		}
		return deleteIdentity{}, errOpaqueDeleteIdentity
	}
	fields, err := projection.fields(document, "apiVersion", "kind", "metadata")
	if err != nil {
		return deleteIdentity{}, err
	}
	if !withMetadata {
		return deleteIdentity{APIVersion: deleteIdentityField{node: fields["apiVersion"], projection: projection}, Kind: deleteIdentityField{node: fields["kind"], projection: projection}}, nil
	}
	meta, err := projection.fields(fields["metadata"], "name", "namespace")
	if err != nil {
		return deleteIdentity{}, err
	}
	return deleteIdentity{
		APIVersion: deleteIdentityField{node: fields["apiVersion"], projection: projection}, Kind: deleteIdentityField{node: fields["kind"], projection: projection},
		Name: deleteIdentityField{node: meta["name"], projection: projection}, Namespace: deleteIdentityField{node: meta["namespace"], projection: projection},
	}, nil
}

// Remove indented children of known business-only block mappings before YAML
// decoding. These children may consist of template-generated YAML fragments.
// Keep the key, the line numbers and every peer key for duplicate detection.
// Identity keys are still resolved exclusively from parsed YAML nodes.
var identityBodyBlock = regexp.MustCompile(`^( *)([A-Za-z_][A-Za-z0-9_./-]*): *(?:[|>][-+0-9]*)? *(?:#.*)?$`)

func stripIdentityBody(document string) string {
	lines := strings.Split(document, "\n")
	skip := -1
	for i, line := range lines {
		trim := strings.TrimLeft(line, " ")
		indent := len(line) - len(trim)
		if skip >= 0 && (trim == "" || indent > skip || indent == skip && strings.HasPrefix(trim, "- ")) {
			lines[i] = ""
			continue
		}
		skip = -1
		if match := identityBodyBlock.FindStringSubmatch(line); match != nil && !slices.Contains([]string{"apiVersion", "kind", "metadata", "name", "namespace"}, match[2]) {
			skip = len(match[1])
		}
	}
	return strings.Join(lines, "\n")
}

// These packaged templates intentionally publish only a GVK and a namespace
// for ownership-checked discovery. The hint can never select a resource name.
func hintedDeleteIdentity(root *parse.ListNode) (deleteIdentity, bool, error) {
	var result deleteIdentity
	found := false
	for _, n := range root.Nodes {
		comment, ok := n.(*parse.CommentNode)
		if !ok {
			continue
		}
		contents := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(comment.Text, "/*"), "*/"))
		header, body, ok := strings.Cut(contents, "\n")
		if !ok {
			continue
		}
		switch strings.TrimSpace(header) {
		case "Single-object cleanup identity: use GVK/app-label/ownership fallback.", "Single-object cleanup GVK and ownership fallback.":
		case "Cleanup identity for the single rendered object. The empty name selects the":
			var next string
			next, body, ok = strings.Cut(body, "\n")
			if !ok || strings.TrimSpace(next) != "OpenSaola GVK/app-label/ownership fallback, including failed-create cleanup." {
				continue
			}
		default:
			continue
		}
		if found {
			return deleteIdentity{}, true, fmt.Errorf("multiple cleanup identity hints")
		}
		fields, err := parseDeleteIdentity(body)
		if err != nil {
			return deleteIdentity{}, true, err
		}
		if !fields.APIVersion.Present() || !fields.Kind.Present() || fields.APIVersion.Dynamic() || fields.Kind.Dynamic() || !fields.Name.Dynamic() || fields.Name.projection.restore(fields.Name.node.Value) != `{{""}}` {
			return deleteIdentity{}, true, fmt.Errorf("invalid single-object cleanup identity hint")
		}
		fields.Name = deleteIdentityField{}
		found, result = true, fields
	}
	return result, found, nil
}

// A literal tpl argument exposes a second template, not an executable source
// expression. Keep its context for dynamic scalars and ignore its business body.
func literalTplIdentity(root *parse.ListNode, outer *identityProjection) (deleteIdentity, bool, error) {
	var action *parse.ActionNode
	declarations := map[string]*parse.ActionNode{}
	for _, n := range root.Nodes {
		if a, ok := n.(*parse.ActionNode); ok && len(a.Pipe.Decl) == 1 {
			declarations[a.Pipe.Decl[0].Ident[0]] = a
			continue
		}
		if noIdentityOutput(n) {
			continue
		}
		a, ok := n.(*parse.ActionNode)
		if !ok || action != nil {
			return deleteIdentity{}, false, nil
		}
		action = a
	}
	if action == nil || len(action.Pipe.Cmds) != 1 {
		return deleteIdentity{}, false, nil
	}
	args := action.Pipe.Cmds[0].Args
	if len(args) != 3 || args[0].String() != "tpl" {
		return deleteIdentity{}, false, nil
	}
	literal, ok := args[1].(*parse.StringNode)
	if !ok {
		return deleteIdentity{}, false, nil
	}
	inner, err := parseDeleteIdentity(literal.Text)
	if err != nil {
		return deleteIdentity{}, true, err
	}
	contextExpr := args[2].String()
	var init string
	if v, ok := args[2].(*parse.VariableNode); ok && len(v.Ident) == 1 {
		a := declarations[v.Ident[0]]
		if a == nil || a.Pipe.IsAssign || takesEffectAt(outer.assigned[v.Ident[0]], action.Position()) || takesEffectAt(outer.firstMutation, action.Position()-1) {
			return deleteIdentity{}, true, errOpaqueDeleteIdentity
		}
		init = a.String()
	} else if contextExpr != "." {
		return deleteIdentity{}, true, errOpaqueDeleteIdentity
	}
	for _, field := range []*deleteIdentityField{&inner.APIVersion, &inner.Kind, &inner.Name, &inner.Namespace} {
		if !field.Dynamic() {
			continue
		}
		field.scope, field.scopeInit = contextExpr, init
	}
	return inner, true, nil
}

// Mutating a shared input, or reassigning a variable in an omitted branch,
// makes replaying that binding in isolation unsafe.
func (p *identityProjection) inspectBindings(list *parse.ListNode) {
	if list == nil {
		return
	}
	for _, node := range list.Nodes {
		var pipe *parse.PipeNode
		switch n := node.(type) {
		case *parse.TemplateNode:
			pipe = n.Pipe
			if p.helperMutates(n.Name) {
				p.noteMutation(node.Position())
			}
		case *parse.ActionNode:
			pipe = n.Pipe
		case *parse.IfNode:
			pipe = n.Pipe
			p.inspectBindings(n.List)
			p.inspectBindings(n.ElseList)
		case *parse.WithNode:
			pipe = n.Pipe
			p.inspectBindings(n.List)
			p.inspectBindings(n.ElseList)
		case *parse.RangeNode:
			pipe = n.Pipe
			p.inspectBindings(n.List)
			p.inspectBindings(n.ElseList)
		}
		if pipe == nil {
			continue
		}
		if pipe.IsAssign {
			for _, v := range pipe.Decl {
				key := v.Ident[0]
				if first := p.assigned[key]; first == 0 || node.Position() < first {
					p.assigned[key] = node.Position()
				}
			}
		}
		if p.pipeMutatesInputs(pipe) {
			p.noteMutation(node.Position())
		}
	}
}

func (p *identityProjection) noteMutation(at parse.Pos) {
	if p.firstMutation == 0 || at < p.firstMutation {
		p.firstMutation = at
	}
}

// Analyze only invoked helpers. A recursive or dynamically selected template
// cannot establish that the rendering context was left untouched.
func templateMutationLookup(definitions map[string]*parse.Tree) func(string) bool {
	known := map[string]bool{}
	var inspect func(string) bool
	inspect = func(name string) bool {
		if mutates, ok := known[name]; ok {
			return mutates
		}
		tree := definitions[name]
		if tree == nil {
			return true
		}
		known[name] = true
		p := identityProjection{helperMutates: inspect, assigned: map[string]parse.Pos{}}
		p.inspectBindings(tree.Root)
		known[name] = p.firstMutation != 0
		return known[name]
	}
	return inspect
}

func (p *identityProjection) pipeMutatesInputs(pipe *parse.PipeNode) bool {
	for _, cmd := range pipe.Cmds {
		for _, arg := range cmd.Args {
			switch n := arg.(type) {
			case *parse.IdentifierNode:
				switch n.Ident {
				case "include":
					if len(cmd.Args) < 2 {
						return true
					}
					name, ok := cmd.Args[1].(*parse.StringNode)
					if !ok || p.helperMutates(name.Text) {
						return true
					}
				case "tpl":
					return true
				case "set", "unset", "merge", "mergeOverwrite", "mustMerge", "mustMergeOverwrite":
					return true
				}
			case *parse.PipeNode:
				if p.pipeMutatesInputs(n) {
					return true
				}
			case *parse.ChainNode:
				if nested, ok := n.Node.(*parse.PipeNode); ok && p.pipeMutatesInputs(nested) {
					return true
				}
			}
		}
	}
	return false
}

// An entire object inside a computed with/range scope cannot be read in the
// caller's context. Recognize it separately from malformed literal YAML.
func generatesResource(root *parse.ListNode) bool {
	generated := false
	for _, n := range root.Nodes {
		if noIdentityOutput(n) {
			continue
		}
		switch n := n.(type) {
		case *parse.WithNode, *parse.RangeNode, *parse.IfNode:
			generated = true
		case *parse.TextNode:
			for line := range strings.SplitSeq(string(n.Text), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					return false
				}
			}
		case *parse.ActionNode:
			if len(n.Pipe.Cmds) != 1 || len(n.Pipe.Cmds[0].Args) != 2 || n.Pipe.Cmds[0].Args[0].String() != "printf" {
				return false
			}
			t, ok := n.Pipe.Cmds[0].Args[1].(*parse.StringNode)
			if !ok || !strings.HasPrefix(t.Text, "#") || strings.Contains(t.Text, "%") {
				return false
			}
		default:
			return false
		}
	}
	return generated
}

// Shared CRDs are always retained. A static bundle can use that policy
// without inventory only when every nonempty document has a CRD GVK.
func staticCRDBundle(text string, p *identityProjection) (deleteIdentity, bool) {
	var result deleteIdentity
	decoder := yaml.NewDecoder(strings.NewReader(text))
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err == io.EOF {
			return result, result.Kind.Present()
		}
		if err != nil || len(doc.Content) != 1 {
			return deleteIdentity{}, false
		}
		node := doc.Content[0]
		if node.ShortTag() == "!!null" && node.Value == "" {
			continue
		}
		fields, err := p.fields(node, "apiVersion", "kind")
		if err != nil {
			return deleteIdentity{}, false
		}
		version, kind := fields["apiVersion"], fields["kind"]
		if version == nil || kind == nil || version.ShortTag() != "!!str" || kind.ShortTag() != "!!str" {
			return deleteIdentity{}, false
		}
		gv, err := schema.ParseGroupVersion(version.Value)
		if err != nil || gv.Version == "" || gv.Group != "apiextensions.k8s.io" || kind.Value != "CustomResourceDefinition" {
			return deleteIdentity{}, false
		}
		if !result.Kind.Present() {
			result = deleteIdentity{APIVersion: deleteIdentityField{node: version, projection: p}, Kind: deleteIdentityField{node: kind, projection: p}}
		}
	}
}
