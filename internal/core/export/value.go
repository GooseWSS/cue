// Copyright 2020 CUE Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package export

import (
	"fmt"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/ast/astutil"
	"cuelang.org/go/cue/token"
	"cuelang.org/go/internal/core/adt"
)

func (e *exporter) bareValue(v adt.Value) ast.Expr {
	switch x := v.(type) {
	case *adt.Vertex:
		return e.vertex(x)
	case adt.Value:
		a := &adt.Vertex{Value: x}
		return e.vertex(a)
	default:
		panic("unreachable")
	}
	// TODO: allow a Value context wrapper.
}

// TODO: if the original value was a single reference, we could replace the
// value with a reference in graph mode.

func (e *exporter) vertex(n *adt.Vertex) (result ast.Expr) {
	switch x := n.Value.(type) {
	case nil:
		// bare
	case *adt.StructMarker:
		result = e.structComposite(n)

	case *adt.ListMarker:
		result = e.listComposite(n)

	case *adt.Bottom:
		if x.IsIncomplete() {
			// fall back to expression mode
			result = stripRefs(e.expr(n))
			break
		}
		result = e.bottom(x)

	default:
		result = e.value(n.Value, n.Conjuncts...)
	}
	return result
}

// TODO: do something more principled. Best would be to have a similar
// mechanism in ast.Ident as others do.
func stripRefs(x ast.Expr) ast.Expr {
	ast.Walk(x, nil, func(n ast.Node) {
		switch x := n.(type) {
		case *ast.Ident:
			switch x.Node.(type) {
			case *ast.ImportSpec:
			default:
				x.Node = nil
			}
		}
	})
	return x
}

func (e *exporter) value(n adt.Value, a ...adt.Conjunct) (result ast.Expr) {
	if e.cfg.TakeDefaults {
		n = adt.Default(n)
	}
	// Evaluate arc if needed?

	// if e.concrete && !adt.IsConcrete(n.Value) {
	// 	return e.errf("non-concrete value: %v", e.bareValue(n.Value))
	// }

	switch x := n.(type) {
	case *adt.Bottom:
		result = e.bottom(x)

	case *adt.Null:
		result = e.null(x)

	case *adt.Bool:
		result = e.bool(x)

	case *adt.Num:
		result = e.num(x, a)

	case *adt.String:
		result = e.string(x, a)

	case *adt.Bytes:
		result = e.bytes(x, a)

	case *adt.BasicType:
		result = e.basicType(x)

	case *adt.Top:
		result = ast.NewIdent("_")

	case *adt.BoundValue:
		result = e.boundValue(x)

	case *adt.Builtin:
		result = e.builtin(x)

	case *adt.BuiltinValidator:
		result = e.builtinValidator(x)

	case *adt.Vertex:
		result = e.vertex(x)

	case *adt.Conjunction:
		switch len(x.Values) {
		case 0:
			return ast.NewIdent("_")
		case 1:
			if e.cfg.Simplify {
				return e.expr(x.Values[0])
			}
			return e.bareValue(x.Values[0])
		}

		a := []adt.Value{}
		b := boundSimplifier{e: e}
		for _, v := range x.Values {
			if !e.cfg.Simplify || !b.add(v) {
				a = append(a, v)
			}
		}

		result = b.expr(e.ctx)
		if result == nil {
			a = x.Values
		}

		for _, x := range a {
			result = wrapBin(result, e.bareValue(x), adt.AndOp)
		}

	case *adt.Disjunction:
		a := []ast.Expr{}
		for i, v := range x.Values {
			var expr ast.Expr
			if e.cfg.Simplify {
				expr = e.bareValue(v)
			} else {
				expr = e.expr(v)
			}
			if i < x.NumDefaults {
				expr = &ast.UnaryExpr{Op: token.MUL, X: expr}
			}
			a = append(a, expr)
		}
		result = ast.NewBinExpr(token.OR, a...)

	default:
		panic(fmt.Sprintf("unsupported type %T", x))
	}

	// TODO: Add comments from original.

	return result
}

func (e *exporter) bottom(n *adt.Bottom) *ast.BottomLit {
	err := &ast.BottomLit{}
	if x := n.Err; x != nil {
		msg := x.Error()
		comment := &ast.Comment{Text: "// " + msg}
		err.AddComment(&ast.CommentGroup{
			Line:     true,
			Position: 2,
			List:     []*ast.Comment{comment},
		})
	}
	return err
}

func (e *exporter) null(n *adt.Null) *ast.BasicLit {
	return &ast.BasicLit{Kind: token.NULL, Value: "null"}
}

func (e *exporter) bool(n *adt.Bool) (b *ast.BasicLit) {
	return ast.NewBool(n.B)
}

func extractBasic(a []adt.Conjunct) *ast.BasicLit {
	for _, v := range a {
		if b, ok := v.Source().(*ast.BasicLit); ok {
			return &ast.BasicLit{Kind: b.Kind, Value: b.Value}
		}
	}
	return nil
}

func (e *exporter) num(n *adt.Num, orig []adt.Conjunct) *ast.BasicLit {
	// TODO: take original formatting into account.
	if b := extractBasic(orig); b != nil {
		return b
	}
	kind := token.FLOAT
	if n.K&adt.IntKind != 0 {
		kind = token.INT
	}
	return &ast.BasicLit{Kind: kind, Value: n.X.String()}

}

func (e *exporter) string(n *adt.String, orig []adt.Conjunct) *ast.BasicLit {
	// TODO: take original formatting into account.
	if b := extractBasic(orig); b != nil {
		return b
	}
	return &ast.BasicLit{
		Kind:  token.STRING,
		Value: quote(n.Str, '"'),
	}
}

func (e *exporter) bytes(n *adt.Bytes, orig []adt.Conjunct) *ast.BasicLit {
	// TODO: take original formatting into account.
	if b := extractBasic(orig); b != nil {
		return b
	}
	return &ast.BasicLit{
		Kind:  token.STRING,
		Value: quote(string(n.B), '\''),
	}
}

func (e *exporter) basicType(n *adt.BasicType) ast.Expr {
	// TODO: allow multi-bit types?
	return ast.NewIdent(n.K.String())
}

func (e *exporter) boundValue(n *adt.BoundValue) ast.Expr {
	return &ast.UnaryExpr{Op: n.Op.Token(), X: e.value(n.Value)}
}

func (e *exporter) builtin(x *adt.Builtin) ast.Expr {
	if x.Package == 0 {
		return ast.NewIdent(x.Name)
	}
	spec := ast.NewImport(nil, x.Package.StringValue(e.index))
	info, _ := astutil.ParseImportSpec(spec)
	ident := ast.NewIdent(info.Ident)
	ident.Node = spec
	return ast.NewSel(ident, x.Name)
}

func (e *exporter) builtinValidator(n *adt.BuiltinValidator) ast.Expr {
	call := ast.NewCall(e.builtin(n.Builtin))
	for _, a := range n.Args {
		call.Args = append(call.Args, e.value(a))
	}
	return call
}

func (e *exporter) listComposite(v *adt.Vertex) ast.Expr {
	l := &ast.ListLit{}
	for _, a := range v.Arcs {
		if !a.Label.IsInt() {
			continue
		}
		elem := e.vertex(a)

		docs := ExtractDoc(a)
		ast.SetComments(elem, docs)

		l.Elts = append(l.Elts, elem)
	}
	return l
}

func (e *exporter) structComposite(v *adt.Vertex) ast.Expr {
	s, saved := e.pushFrame(v.Conjuncts)
	e.top().upCount++
	defer func() {
		e.top().upCount--
		e.popFrame(saved)
	}()

	p := e.cfg
	for _, label := range VertexFeatures(v) {
		if label.IsDef() && !p.ShowDefinitions {
			continue
		}
		if label.IsHidden() && !p.ShowHidden {
			continue
		}

		f := &ast.Field{Label: e.stringLabel(label)}

		if label.IsDef() {
			e.inDefinition++
		}

		arc := v.Lookup(label)
		switch {
		case arc == nil:
			if !p.ShowOptional {
				continue
			}
			f.Optional = token.NoSpace.Pos()

			arc = &adt.Vertex{Label: label}
			v.MatchAndInsert(e.ctx, arc)
			if len(v.Conjuncts) == 0 {
				continue
			}

			// fall back to expression mode.
			f.Value = stripRefs(e.expr(arc))

			// TODO: remove use of stripRefs.
			// f.Value = e.expr(arc)

		default:
			f.Value = e.vertex(arc)
		}

		if label.IsDef() {
			e.inDefinition--
		}

		if p.ShowAttributes {
			f.Attrs = ExtractFieldAttrs(arc.Conjuncts)
		}

		if p.ShowDocs {
			docs := ExtractDoc(arc)
			ast.SetComments(f, docs)
		}

		s.Elts = append(s.Elts, f)
	}

	return s
}
