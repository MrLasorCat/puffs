// Copyright 2017 The Puffs Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package parse

// TODO: write a formal grammar for the language.

import (
	"fmt"

	"github.com/google/puffs/lang/base38"

	a "github.com/google/puffs/lang/ast"
	t "github.com/google/puffs/lang/token"
)

func Parse(tm *t.Map, filename string, src []t.Token) (*a.File, error) {
	p := &parser{
		src:      src,
		tm:       tm,
		filename: filename,
	}
	if len(src) > 0 {
		p.lastLine = src[len(src)-1].Line
	}
	return p.parseFile()
}

func ParseExpr(tm *t.Map, filename string, src []t.Token) (*a.Expr, error) {
	p := &parser{
		src:      src,
		tm:       tm,
		filename: filename,
	}
	if len(src) > 0 {
		p.lastLine = src[len(src)-1].Line
	}
	return p.parseExpr()
}

type parser struct {
	src      []t.Token
	tm       *t.Map
	filename string
	lastLine uint32
}

func (p *parser) line() uint32 {
	if len(p.src) != 0 {
		return p.src[0].Line
	}
	return p.lastLine
}

func (p *parser) peek1() t.ID {
	if len(p.src) > 0 {
		return p.src[0].ID
	}
	return 0
}

func (p *parser) parseFile() (*a.File, error) {
	topLevelDecls := []*a.Node(nil)
	for len(p.src) > 0 {
		d, err := p.parseTopLevelDecl()
		if err != nil {
			return nil, err
		}
		topLevelDecls = append(topLevelDecls, d)
	}
	return a.NewFile(p.filename, topLevelDecls), nil
}

func (p *parser) parseTopLevelDecl() (*a.Node, error) {
	flags := a.Flags(0)
	line := p.src[0].Line
	switch k := p.peek1().Key(); k {
	case t.KeyPackageID, t.KeyUse:
		p.src = p.src[1:]
		path := p.peek1()
		if !path.IsStrLiteral() {
			got := p.tm.ByID(path)
			return nil, fmt.Errorf(`parse: expected string literal, got %q at %s:%d`, got, p.filename, p.line())
		}
		p.src = p.src[1:]
		if x := p.peek1().Key(); x != t.KeySemicolon {
			got := p.tm.ByKey(x)
			return nil, fmt.Errorf(`parse: expected (implicit) ";", got %q at %s:%d`, got, p.filename, p.line())
		}
		p.src = p.src[1:]
		if k == t.KeyPackageID {
			raw := path.String(p.tm)
			s, ok := t.Unescape(raw)
			if !ok {
				return nil, fmt.Errorf(`parse: %q is not a valid packageid`, raw)
			}
			if u, ok := base38.Encode(s); !ok || u == 0 {
				return nil, fmt.Errorf(`parse: %q is not a valid packageid`, s)
			}
			return a.NewPackageID(p.filename, line, path).Node(), nil
		} else {
			return a.NewUse(p.filename, line, path).Node(), nil
		}

	case t.KeyPub:
		flags |= a.FlagsPublic
		fallthrough
	case t.KeyPri:
		p.src = p.src[1:]
		switch p.peek1().Key() {
		case t.KeyConst:
			p.src = p.src[1:]
			id, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			typ, err := p.parseTypeExpr()
			if err != nil {
				return nil, err
			}
			if p.peek1().Key() != t.KeyEq {
				return nil, fmt.Errorf(`parse: const %q has no value at %s:%d`,
					p.tm.ByID(id), p.filename, p.line())
			}
			p.src = p.src[1:]
			value := (*a.Expr)(nil)
			// TODO: parse lists of lists.
			if p.peek1().Key() == t.KeyDollar {
				value, err = p.parseDollarExpr()
				if err != nil {
					return nil, err
				}
			} else {
				value, err = p.parseExpr()
				if err != nil {
					return nil, err
				}
			}
			if x := p.peek1().Key(); x != t.KeySemicolon {
				got := p.tm.ByKey(x)
				return nil, fmt.Errorf(`parse: expected (implicit) ";", got %q at %s:%d`, got, p.filename, p.line())
			}
			p.src = p.src[1:]
			return a.NewConst(flags, p.filename, line, id, typ, value).Node(), nil

		case t.KeyFunc:
			p.src = p.src[1:]
			id0, id1, err := p.parseQualifiedIdent()
			if err != nil {
				return nil, err
			}
			if id0 != 0 && id0.IsBuiltIn() {
				return nil, fmt.Errorf(`parse: built-in %q used for func receiver at %s:%d`,
					p.tm.ByID(id0), p.filename, p.line())
			}
			if id1.IsBuiltIn() {
				return nil, fmt.Errorf(`parse: built-in %q used for func name at %s:%d`,
					p.tm.ByID(id1), p.filename, p.line())
			}
			switch p.peek1().Key() {
			case t.KeyExclam:
				flags |= a.FlagsImpure
				p.src = p.src[1:]
			case t.KeyQuestion:
				flags |= a.FlagsImpure | a.FlagsSuspendible
				p.src = p.src[1:]
			}
			inFields, err := p.parseList(t.KeyCloseParen, (*parser).parseFieldNode)
			if err != nil {
				return nil, err
			}
			outFields, err := p.parseList(t.KeyCloseParen, (*parser).parseFieldNode)
			if err != nil {
				return nil, err
			}
			asserts := []*a.Node(nil)
			if p.peek1().Key() == t.KeyComma {
				p.src = p.src[1:]
				asserts, err = p.parseList(t.KeyOpenCurly, (*parser).parseAssertNode)
				if err != nil {
					return nil, err
				}
				if err := p.assertsSorted(asserts); err != nil {
					return nil, err
				}
			}
			body, err := p.parseBlock()
			if err != nil {
				return nil, err
			}
			if x := p.peek1().Key(); x != t.KeySemicolon {
				got := p.tm.ByKey(x)
				return nil, fmt.Errorf(`parse: expected (implicit) ";", got %q at %s:%d`, got, p.filename, p.line())
			}
			p.src = p.src[1:]
			in := a.NewStruct(0, p.filename, line, t.IDIn, inFields)
			out := a.NewStruct(0, p.filename, line, t.IDOut, outFields)
			return a.NewFunc(flags, p.filename, line, id0, id1, in, out, asserts, body).Node(), nil

		case t.KeyError, t.KeySuspension:
			keyword := p.src[0].ID
			p.src = p.src[1:]
			message := p.peek1()
			if !message.IsStrLiteral() {
				got := p.tm.ByID(message)
				return nil, fmt.Errorf(`parse: expected string literal, got %q at %s:%d`, got, p.filename, p.line())
			}
			p.src = p.src[1:]
			if x := p.peek1().Key(); x != t.KeySemicolon {
				got := p.tm.ByKey(x)
				return nil, fmt.Errorf(`parse: expected (implicit) ";", got %q at %s:%d`, got, p.filename, p.line())
			}
			p.src = p.src[1:]
			return a.NewStatus(flags, p.filename, line, keyword, message).Node(), nil

		case t.KeyStruct:
			p.src = p.src[1:]
			name, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			if name.IsBuiltIn() {
				return nil, fmt.Errorf(`parse: built-in %q used for struct name at %s:%d`,
					p.tm.ByID(name), p.filename, p.line())
			}
			if p.peek1().Key() == t.KeyQuestion {
				flags |= a.FlagsSuspendible
				p.src = p.src[1:]
			}
			fields, err := p.parseList(t.KeyCloseParen, (*parser).parseFieldNode)
			if err != nil {
				return nil, err
			}
			if x := p.peek1().Key(); x != t.KeySemicolon {
				got := p.tm.ByKey(x)
				return nil, fmt.Errorf(`parse: expected (implicit) ";", got %q at %s:%d`, got, p.filename, p.line())
			}
			p.src = p.src[1:]
			return a.NewStruct(flags, p.filename, line, name, fields).Node(), nil
		}
	}
	return nil, fmt.Errorf(`parse: unrecognized top level declaration at %s:%d`, p.filename, line)
}

// parseQualifiedIdent parses "foo.bar" or "bar".
func (p *parser) parseQualifiedIdent() (t.ID, t.ID, error) {
	x, err := p.parseIdent()
	if err != nil {
		return 0, 0, err
	}

	if p.peek1().Key() != t.KeyDot {
		return 0, x, nil
	}
	p.src = p.src[1:]

	y, err := p.parseIdent()
	if err != nil {
		return 0, 0, err
	}
	return x, y, nil
}

func (p *parser) parseIdent() (t.ID, error) {
	if len(p.src) == 0 {
		return 0, fmt.Errorf(`parse: expected identifier at %s:%d`, p.filename, p.line())
	}
	x := p.src[0]
	if !x.IsIdent() {
		got := p.tm.ByToken(x)
		return 0, fmt.Errorf(`parse: expected identifier, got %q at %s:%d`, got, p.filename, p.line())
	}
	p.src = p.src[1:]
	return x.ID, nil
}

func (p *parser) parseList(stop t.Key, parseElem func(*parser) (*a.Node, error)) ([]*a.Node, error) {
	if stop == t.KeyCloseParen {
		if x := p.peek1().Key(); x != t.KeyOpenParen {
			return nil, fmt.Errorf(`parse: expected "(", got %q at %s:%d`,
				p.tm.ByKey(x), p.filename, p.line())
		}
		p.src = p.src[1:]
	}

	ret := []*a.Node(nil)
	for len(p.src) > 0 {
		if p.src[0].ID.Key() == stop {
			if stop == t.KeyCloseParen {
				p.src = p.src[1:]
			}
			return ret, nil
		}

		elem, err := parseElem(p)
		if err != nil {
			return nil, err
		}
		ret = append(ret, elem)

		switch x := p.peek1().Key(); x {
		case stop:
			if stop == t.KeyCloseParen {
				p.src = p.src[1:]
			}
			return ret, nil
		case t.KeyComma:
			p.src = p.src[1:]
		default:
			return nil, fmt.Errorf(`parse: expected %q, got %q at %s:%d`,
				p.tm.ByKey(stop), p.tm.ByKey(x), p.filename, p.line())
		}
	}
	return nil, fmt.Errorf(`parse: expected %q at %s:%d`, p.tm.ByKey(stop), p.filename, p.line())
}

func (p *parser) parseFieldNode() (*a.Node, error) {
	name, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	typ, err := p.parseTypeExpr()
	if err != nil {
		return nil, err
	}
	defaultValue := (*a.Expr)(nil)
	if p.peek1().Key() == t.KeyEq {
		p.src = p.src[1:]
		defaultValue, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	}
	return a.NewField(name, typ, defaultValue).Node(), nil
}

func (p *parser) parseTypeExpr() (*a.TypeExpr, error) {
	if p.peek1().Key() == t.KeyPtr {
		p.src = p.src[1:]
		rhs, err := p.parseTypeExpr()
		if err != nil {
			return nil, err
		}
		return a.NewTypeExpr(t.IDPtr, 0, nil, nil, rhs), nil
	}

	if p.peek1().Key() == t.KeyOpenBracket {
		p.src = p.src[1:]
		decorator, lhs := t.IDColon, (*a.Expr)(nil)
		if p.peek1().Key() != t.KeyCloseBracket {
			decorator = t.IDOpenBracket
			var err error
			lhs, err = p.parseExpr()
			if err != nil {
				return nil, err
			}
			if x := p.peek1().Key(); x != t.KeyCloseBracket {
				got := p.tm.ByKey(x)
				return nil, fmt.Errorf(`parse: expected "]", got %q at %s:%d`, got, p.filename, p.line())
			}
		}
		p.src = p.src[1:]
		rhs, err := p.parseTypeExpr()
		if err != nil {
			return nil, err
		}
		return a.NewTypeExpr(decorator, 0, lhs, nil, rhs), nil
	}

	pkg, name, err := p.parseQualifiedIdent()
	if err != nil {
		return nil, err
	}

	lhs, mhs := (*a.Expr)(nil), (*a.Expr)(nil)
	if p.peek1().Key() == t.KeyOpenBracket {
		_, lhs, mhs, err = p.parseBracket(t.IDDotDot)
		if err != nil {
			return nil, err
		}
	}

	return a.NewTypeExpr(pkg, name, lhs, mhs, nil), nil
}

// parseBracket parses "[i:j]", "[i:]", "[:j]" and "[:]". A double dot replaces
// the colon if sep is t.IDDotDot instead of t.IDColon. If sep is t.IDColon, it
// also parses "[x]". The returned op is sep for a range or refinement and
// t.IDOpenBracket for an index.
func (p *parser) parseBracket(sep t.ID) (op t.ID, ei *a.Expr, ej *a.Expr, err error) {
	if x := p.peek1().Key(); x != t.KeyOpenBracket {
		got := p.tm.ByKey(x)
		return 0, nil, nil, fmt.Errorf(`parse: expected "[", got %q at %s:%d`, got, p.filename, p.line())
	}
	p.src = p.src[1:]

	if p.peek1() != sep {
		ei, err = p.parseExpr()
		if err != nil {
			return 0, nil, nil, err
		}
	}

	switch x := p.peek1().Key(); {
	case x == sep.Key():
		p.src = p.src[1:]

	case x == t.KeyCloseBracket && sep.Key() == t.KeyColon:
		p.src = p.src[1:]
		return t.IDOpenBracket, nil, ei, nil

	default:
		extra := ``
		if sep.Key() == t.KeyColon {
			extra = ` or "]"`
		}
		got := p.tm.ByKey(x)
		return 0, nil, nil, fmt.Errorf(`parse: expected %q%s, got %q at %s:%d`,
			p.tm.ByID(sep), extra, got, p.filename, p.line())
	}

	if p.peek1().Key() != t.KeyCloseBracket {
		ej, err = p.parseExpr()
		if err != nil {
			return 0, nil, nil, err
		}
	}

	if x := p.peek1().Key(); x != t.KeyCloseBracket {
		got := p.tm.ByKey(x)
		return 0, nil, nil, fmt.Errorf(`parse: expected "]", got %q at %s:%d`, got, p.filename, p.line())
	}
	p.src = p.src[1:]

	return sep, ei, ej, nil
}

func (p *parser) parseBlock() ([]*a.Node, error) {
	if x := p.peek1().Key(); x != t.KeyOpenCurly {
		got := p.tm.ByKey(x)
		return nil, fmt.Errorf(`parse: expected "{", got %q at %s:%d`, got, p.filename, p.line())
	}
	p.src = p.src[1:]

	block := []*a.Node(nil)
	for len(p.src) > 0 {
		if p.src[0].ID.Key() == t.KeyCloseCurly {
			p.src = p.src[1:]
			return block, nil
		}

		s, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		block = append(block, s)

		if x := p.peek1().Key(); x != t.KeySemicolon {
			got := p.tm.ByKey(x)
			return nil, fmt.Errorf(`parse: expected (implicit) ";", got %q at %s:%d`, got, p.filename, p.line())
		}
		p.src = p.src[1:]
	}
	return nil, fmt.Errorf(`parse: expected "}" at %s:%d`, p.filename, p.line())
}

func (p *parser) assertsSorted(asserts []*a.Node) error {
	seenInv, seenPost := false, false
	for _, a := range asserts {
		switch a.Assert().Keyword().Key() {
		case t.KeyAssert:
			return fmt.Errorf(`parse: assertion chain cannot contain "assert", `+
				`only "pre", "inv" and "post" at %s:%d`, p.filename, p.line())
		case t.KeyPre:
			if seenPost || seenInv {
				break
			}
			continue
		case t.KeyInv:
			if seenPost {
				break
			}
			seenInv = true
			continue
		default:
			seenPost = true
			continue
		}
		return fmt.Errorf(`parse: assertion chain not in "pre", "inv", "post" order at %s:%d`,
			p.filename, p.line())
	}
	return nil
}

func (p *parser) parseAssertNode() (*a.Node, error) {
	switch x := p.peek1(); x.Key() {
	case t.KeyAssert, t.KeyPre, t.KeyInv, t.KeyPost:
		p.src = p.src[1:]
		condition, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		reason, args := t.ID(0), []*a.Node(nil)
		if p.peek1().Key() == t.KeyVia {
			p.src = p.src[1:]
			reason = p.peek1()
			if !reason.IsStrLiteral() {
				got := p.tm.ByID(reason)
				return nil, fmt.Errorf(`parse: expected string literal, got %q at %s:%d`, got, p.filename, p.line())
			}
			p.src = p.src[1:]
			args, err = p.parseList(t.KeyCloseParen, (*parser).parseArgNode)
			if err != nil {
				return nil, err
			}
		}
		return a.NewAssert(x, condition, reason, args).Node(), nil
	}
	return nil, fmt.Errorf(`parse: expected "assert", "pre" or "post" at %s:%d`, p.filename, p.line())
}

func (p *parser) parseStatement() (*a.Node, error) {
	line := uint32(0)
	if len(p.src) > 0 {
		line = p.src[0].Line
	}
	n, err := p.parseStatement1()
	if n != nil {
		n.Raw().SetFilenameLine(p.filename, line)
		if n.Kind() == a.KIterate {
			for _, o := range n.Iterate().Variables() {
				o.Raw().SetFilenameLine(p.filename, line)
			}
		}
	}
	return n, err
}

func (p *parser) parseLabel() (t.ID, error) {
	if p.peek1().Key() == t.KeyColon {
		p.src = p.src[1:]
		return p.parseIdent()
	}
	return 0, nil
}

func (p *parser) parseStatement1() (*a.Node, error) {
	switch x := p.peek1(); x.Key() {
	case t.KeyAssert, t.KeyPre, t.KeyPost:
		return p.parseAssertNode()

	case t.KeyBreak, t.KeyContinue:
		p.src = p.src[1:]
		label, err := p.parseLabel()
		if err != nil {
			return nil, err
		}
		return a.NewJump(x, label).Node(), nil

	case t.KeyIf:
		o, err := p.parseIf()
		return o.Node(), err

	case t.KeyIterate:
		p.src = p.src[1:]
		if x := p.peek1().Key(); x != t.KeyDot {
			got := p.tm.ByKey(x)
			return nil, fmt.Errorf(`parse: expected ".", got %q at %s:%d`, got, p.filename, p.line())
		}
		p.src = p.src[1:]
		unrollID := p.peek1()
		unrollStr := p.tm.ByKey(unrollID.Key())
		if !unrollID.IsLiteral() {
			return nil, fmt.Errorf(`parse: expected literal unroll count, got %q at %s:%d`,
				unrollStr, p.filename, p.line())
		}
		p.src = p.src[1:]
		switch unrollStr {
		default:
			return nil, fmt.Errorf(`parse: expected power-of-2 unroll count in [1..256], got %q at %s:%d`,
				unrollStr, p.filename, p.line())
		case "1", "2", "4", "8", "16", "32", "64", "128", "256":
		}
		unroll := a.NewExpr(0, 0, unrollID, nil, nil, nil, nil)

		label, err := p.parseLabel()
		if err != nil {
			return nil, err
		}
		vars, err := p.parseList(t.KeyCloseParen, (*parser).parseIterateVariableNode)
		if err != nil {
			return nil, err
		}
		asserts, err := p.parseAsserts()
		if err != nil {
			return nil, err
		}
		body, err := p.parseBlock()
		if err != nil {
			return nil, err
		}
		return a.NewIterate(label, unroll, vars, asserts, body).Node(), nil

	case t.KeyReturn:
		p.src = p.src[1:]
		value, err := (*a.Expr)(nil), error(nil)
		if p.peek1().Key() != t.KeySemicolon {
			value, err = p.parseExpr()
			if err != nil {
				return nil, err
			}
		}
		return a.NewReturn(value).Node(), nil

	case t.KeyVar:
		p.src = p.src[1:]
		return p.parseVar(false)

	case t.KeyWhile:
		p.src = p.src[1:]
		label, err := p.parseLabel()
		if err != nil {
			return nil, err
		}
		condition, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		asserts, err := p.parseAsserts()
		if err != nil {
			return nil, err
		}
		body, err := p.parseBlock()
		if err != nil {
			return nil, err
		}
		return a.NewWhile(label, condition, asserts, body).Node(), nil
	}

	lhs, err := p.parseExpr()
	if err != nil {
		return nil, err
	}

	if op := p.peek1(); op.IsAssign() {
		p.src = p.src[1:]
		rhs, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		return a.NewAssign(op, lhs, rhs).Node(), nil
	}

	return lhs.Node(), nil
}

func (p *parser) parseAsserts() ([]*a.Node, error) {
	asserts := []*a.Node(nil)
	if p.peek1().Key() == t.KeyComma {
		p.src = p.src[1:]
		var err error
		if asserts, err = p.parseList(t.KeyOpenCurly, (*parser).parseAssertNode); err != nil {
			return nil, err
		}
		if err := p.assertsSorted(asserts); err != nil {
			return nil, err
		}
	}
	return asserts, nil
}

func (p *parser) parseIf() (*a.If, error) {
	if x := p.peek1().Key(); x != t.KeyIf {
		got := p.tm.ByKey(x)
		return nil, fmt.Errorf(`parse: expected "if", got %q at %s:%d`, got, p.filename, p.line())
	}
	p.src = p.src[1:]
	condition, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	bodyIfTrue, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	elseIf, bodyIfFalse := (*a.If)(nil), ([]*a.Node)(nil)
	if p.peek1().Key() == t.KeyElse {
		p.src = p.src[1:]
		if p.peek1().Key() == t.KeyIf {
			elseIf, err = p.parseIf()
			if err != nil {
				return nil, err
			}
		} else {
			bodyIfFalse, err = p.parseBlock()
			if err != nil {
				return nil, err
			}
		}
	}
	return a.NewIf(condition, elseIf, bodyIfTrue, bodyIfFalse), nil
}

func (p *parser) parseArgNode() (*a.Node, error) {
	name, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	if x := p.peek1().Key(); x != t.KeyColon {
		got := p.tm.ByKey(x)
		return nil, fmt.Errorf(`parse: expected ":", got %q at %s:%d`, got, p.filename, p.line())
	}
	p.src = p.src[1:]
	value, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return a.NewArg(name, value).Node(), nil
}

func (p *parser) parseIterateVariableNode() (*a.Node, error) {
	return p.parseVar(true)
}

func (p *parser) parseVar(inIterate bool) (*a.Node, error) {
	id, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	typ, err := p.parseTypeExpr()
	if err != nil {
		return nil, err
	}
	value := (*a.Expr)(nil)

	op := t.ID(0)
	if inIterate {
		op = t.IDColon
		if x := p.peek1().Key(); x != t.KeyColon {
			got := p.tm.ByKey(x)
			return nil, fmt.Errorf(`parse: expected ":", got %q at %s:%d`, got, p.filename, p.line())
		}
		p.src = p.src[1:]
		value, err = p.parseExpr()
		if err != nil {
			return nil, err
		}

	} else if p.peek1().Key() == t.KeyEq {
		op = t.IDEq
		p.src = p.src[1:]
		if p.peek1().Key() == t.KeyTry {
			value, err = p.parseTryExpr()
			if err != nil {
				return nil, err
			}
		} else {
			value, err = p.parseExpr()
			if err != nil {
				return nil, err
			}
		}
	}

	return a.NewVar(op, id, typ, value).Node(), nil
}

func (p *parser) parseDollarExpr() (*a.Expr, error) {
	if x := p.peek1().Key(); x != t.KeyDollar {
		got := p.tm.ByKey(x)
		return nil, fmt.Errorf(`parse: expected "$", got %q at %s:%d`, got, p.filename, p.line())
	}
	p.src = p.src[1:]
	args, err := p.parseList(t.KeyCloseParen, (*parser).parseExprNode)
	if err != nil {
		return nil, err
	}
	return a.NewExpr(0, t.IDDollar, 0, nil, nil, nil, args), nil
}

func (p *parser) parseTryExpr() (*a.Expr, error) {
	if x := p.peek1().Key(); x != t.KeyTry {
		got := p.tm.ByKey(x)
		return nil, fmt.Errorf(`parse: expected "try", got %q at %s:%d`, got, p.filename, p.line())
	}
	p.src = p.src[1:]
	call, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if call.ID0() != t.IDOpenParen {
		return nil, fmt.Errorf(`parse: expected function call after "try", got %q at %s:%d`,
			call.String(p.tm), p.filename, p.line())
	}
	return a.NewExpr(call.Node().Raw().Flags(), t.IDTry, call.ID1(),
		call.LHS(), call.MHS(), call.RHS(), call.Args()), nil
}

func (p *parser) parseExprNode() (*a.Node, error) {
	n, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return n.Node(), err
}

func (p *parser) parseExpr() (*a.Expr, error) {
	lhs, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	if x := p.peek1(); x.IsBinaryOp() {
		p.src = p.src[1:]
		rhs := (*a.Node)(nil)
		if x.Key() == t.KeyAs {
			o, err := p.parseTypeExpr()
			if err != nil {
				return nil, err
			}
			rhs = o.Node()
		} else {
			o, err := p.parseOperand()
			if err != nil {
				return nil, err
			}
			rhs = o.Node()
		}

		if !x.IsAssociativeOp() || x != p.peek1() {
			op := x.BinaryForm()
			if op == 0 {
				return nil, fmt.Errorf(`parse: internal error: no binary form for token.Key 0x%#02x`, x.Key())
			}
			return a.NewExpr(0, op, 0, lhs.Node(), nil, rhs, nil), nil
		}

		args := []*a.Node{lhs.Node(), rhs}
		for p.peek1() == x {
			p.src = p.src[1:]
			arg, err := p.parseOperand()
			if err != nil {
				return nil, err
			}
			args = append(args, arg.Node())
		}
		op := x.AssociativeForm()
		if op == 0 {
			return nil, fmt.Errorf(`parse: internal error: no associative form for token.Key 0x%#02x`, x.Key())
		}
		return a.NewExpr(0, op, 0, nil, nil, nil, args), nil
	}
	return lhs, nil
}

func (p *parser) parseOperand() (*a.Expr, error) {
	switch x := p.peek1(); {
	case x.IsUnaryOp():
		p.src = p.src[1:]
		rhs, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		op := x.UnaryForm()
		if op == 0 {
			return nil, fmt.Errorf(`parse: internal error: no unary form for token.Key 0x%#02x`, x.Key())
		}
		return a.NewExpr(0, op, 0, nil, nil, rhs.Node(), nil), nil

	case x.IsLiteral():
		p.src = p.src[1:]
		return a.NewExpr(0, 0, x, nil, nil, nil, nil), nil

	default:
		switch x.Key() {
		case t.KeyOpenParen:
			p.src = p.src[1:]
			expr, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if x := p.peek1().Key(); x != t.KeyCloseParen {
				got := p.tm.ByKey(x)
				return nil, fmt.Errorf(`parse: expected ")", got %q at %s:%d`, got, p.filename, p.line())
			}
			p.src = p.src[1:]
			return expr, nil

		case t.KeyError, t.KeyStatus, t.KeySuspension:
			keyword := x
			p.src = p.src[1:]
			message := p.peek1()
			if !message.IsStrLiteral() {
				got := p.tm.ByID(message)
				return nil, fmt.Errorf(`parse: expected string literal, got %q at %s:%d`, got, p.filename, p.line())
			}
			p.src = p.src[1:]
			return a.NewExpr(0, keyword, message, nil, nil, nil, nil), nil
		}
	}

	id, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	lhs := a.NewExpr(0, 0, id, nil, nil, nil, nil)

	for {
		flags := a.Flags(0)
		switch p.peek1().Key() {
		default:
			return lhs, nil

		case t.KeyExclam, t.KeyQuestion:
			flags |= a.FlagsImpure | a.FlagsCallImpure
			if p.src[0].Key() == t.KeyQuestion {
				flags |= a.FlagsSuspendible | a.FlagsCallSuspendible
			}
			p.src = p.src[1:]
			fallthrough

		case t.KeyOpenParen:
			args, err := p.parseList(t.KeyCloseParen, (*parser).parseArgNode)
			if err != nil {
				return nil, err
			}
			lhs = a.NewExpr(flags, t.IDOpenParen, 0, lhs.Node(), nil, nil, args)

		case t.KeyOpenBracket:
			id0, mhs, rhs, err := p.parseBracket(t.IDColon)
			if err != nil {
				return nil, err
			}
			lhs = a.NewExpr(0, id0, 0, lhs.Node(), mhs.Node(), rhs.Node(), nil)

		case t.KeyDot:
			p.src = p.src[1:]
			selector, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			lhs = a.NewExpr(0, t.IDDot, selector, lhs.Node(), nil, nil, nil)
		}
	}
}
