package nolol

import "github.com/dbaumgarten/yodk/parser"

func (g *GoToLabelStatement) Accept(v parser.Visitor) error {
	return v.Visit(g, parser.SingleVisit)
}

func (p *ExtProgramm) Accept(v parser.Visitor) error {
	err := v.Visit(p, parser.PreVisit)
	if err != nil {
		return err
	}
	for i := 0; i < len(p.Lines); i++ {
		err = v.Visit(p, i)
		if err != nil {
			return err
		}
		err = p.Lines[i].Accept(v)
		if repl, is := err.(parser.NodeReplacement); is {
			p.Lines = patchExtLines(p.Lines, i, repl)
			i += len(repl.Replacement) - 1
			err = nil
		}
		if err != nil {
			return err
		}
	}
	return v.Visit(p, parser.PostVisit)
}

func (l *ExecutableLine) Accept(v parser.Visitor) error {
	err := v.Visit(l, parser.PreVisit)
	if err != nil {
		return err
	}
	for i := 0; i < len(l.Statements); i++ {
		err = v.Visit(l, i)
		if err != nil {
			return err
		}
		err = l.Statements[i].Accept(v)
		if repl, is := err.(parser.NodeReplacement); is {
			l.Statements = parser.PatchStatements(l.Statements, i, repl)
			i += len(repl.Replacement) - 1
			err = nil
		}
		if err != nil {
			return err
		}
	}
	return v.Visit(l, parser.PostVisit)
}

func (l *ConstDeclaration) Accept(v parser.Visitor) error {
	err := v.Visit(l, parser.PreVisit)
	if err != nil {
		return err
	}
	err = l.Value.Accept(v)
	if repl, is := err.(parser.NodeReplacement); is {
		l.Value = repl.Replacement[0].(parser.Expression)
		err = nil
	}
	if err != nil {
		return err
	}
	return v.Visit(l, parser.PostVisit)
}

func patchExtLines(old []ExtLine, position int, repl parser.NodeReplacement) []ExtLine {
	newv := make([]ExtLine, 0, len(old)+len(repl.Replacement)-1)
	newv = append(newv, old[:position]...)
	for _, elem := range repl.Replacement {
		if line, is := elem.(ExtLine); is {
			newv = append(newv, line)
		} else {
			panic("Could not patch slice. Wrong type.")
		}
	}
	newv = append(newv, old[position+1:]...)
	return newv
}
