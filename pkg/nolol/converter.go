package nolol

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dbaumgarten/yodk/pkg/nolol/nast"
	"github.com/dbaumgarten/yodk/pkg/optimizers"
	"github.com/dbaumgarten/yodk/pkg/parser"
	"github.com/dbaumgarten/yodk/pkg/parser/ast"
)

// Converter can convert a nolol-ast to a yolol-ast
type Converter struct {
	files      FileSystem
	jumpLabels map[string]int
	// the names of definitions are case-insensitive. Keys are converted to lowercase before using them
	// all lookups MUST also use lowercased keys
	definitions      map[string]*nast.Definition
	usesTimeTracking bool
	iflabelcounter   int
	waitlabelcounter int
	loopcounter      int
	// keeps track of the current loop we are in while converting
	// the last element in the list is the current innermost loop
	loopLevel           []int
	sexpOptimizer       *optimizers.StaticExpressionOptimizer
	boolexpOptimizer    *optimizers.ExpressionInversionOptimizer
	varnameOptimizer    *optimizers.VariableNameOptimizer
	includecount        int
	macros              map[string]*nast.MacroDefinition
	macroLevel          []string
	macroInsertionCount int
	debug               bool
	// UseSpaces disables the default spaceless-mode
	UseSpaces bool
}

// NewConverter creates a new converter
func NewConverter() *Converter {
	return &Converter{
		jumpLabels:       make(map[string]int),
		definitions:      make(map[string]*nast.Definition),
		macros:           make(map[string]*nast.MacroDefinition),
		macroLevel:       make([]string, 0),
		sexpOptimizer:    optimizers.NewStaticExpressionOptimizer(),
		boolexpOptimizer: &optimizers.ExpressionInversionOptimizer{},
		varnameOptimizer: optimizers.NewVariableNameOptimizer(),
		loopLevel:        make([]int, 0),
	}
}

// GetVariableTranslations returns a table that can be used to find the original names
// of the variables whos names where shortened during conversion
func (c *Converter) GetVariableTranslations() map[string]string {
	return c.varnameOptimizer.GetReversalTable()
}

// ConvertFile is a shortcut that loads a file from the file-system, parses it and directly convertes it.
// mainfile is the path to the file on the disk.
// All included are loaded relative to the mainfile.
func (c *Converter) ConvertFile(mainfile string) (*ast.Program, error) {
	files := DiskFileSystem{
		Dir: filepath.Dir(mainfile),
	}
	return c.ConvertFileEx(filepath.Base(mainfile), files)
}

// ConvertFileEx acts like ConvertFile, but allows the passing of a custom filesystem from which the source files
// are retrieved. This way, files that are not stored on disk can be converted
func (c *Converter) ConvertFileEx(mainfile string, files FileSystem) (*ast.Program, error) {
	file, err := files.Get(mainfile)
	if err != nil {
		return nil, err
	}
	p := NewParser()
	p.Debug(c.debug)
	parsed, err := p.Parse(file)
	if err != nil {
		return nil, err
	}
	return c.Convert(parsed, files)
}

// Debug enables/disables debug logging
func (c *Converter) Debug(b bool) {
	c.debug = b
}

// Convert converts a nolol-program to a yolol-program
// files is an object to access files that are referenced in prog's include directives
func (c *Converter) Convert(prog *nast.Program, files FileSystem) (*ast.Program, error) {
	c.files = files

	c.usesTimeTracking = usesTimeTracking(prog)
	// reserve a name for use in time-tracking
	c.varnameOptimizer.OptimizeVarName(reservedTimeVariable)

	err := c.convertNodes(prog)
	if err != nil {
		return nil, err
	}

	err = c.addFinalGoto(prog)
	if err != nil {
		return nil, err
	}

	err = c.resolveGotoChains(prog)
	if err != nil {
		return nil, err
	}

	err = c.removeUnusedLabels(prog)
	if err != nil {
		return nil, err
	}

	// merge the statemens of the program as good as possible
	merged, err := c.mergeNololElements(prog.Elements)
	if err != nil {
		return nil, err
	}
	prog.Elements = merged

	err = c.removeDuplicateGotos(prog)
	if err != nil {
		return nil, err
	}

	// find all line-labels
	err = c.findJumpLabels(prog)
	if err != nil {
		return nil, err
	}

	// resolve jump-labels
	err = c.replaceGotoLabels(prog)
	if err != nil {
		return nil, err
	}

	// now that all line-positions are fixed, the line() calls can be replaced by their line-number
	err = c.convertLineFuncCalls(prog)
	if err != nil {
		return nil, err
	}

	// convertLineFuncCalls might have introduced un-optimized expression
	// re-run the static-expression optimizer
	err = c.sexpOptimizer.Optimize(prog)
	if err != nil {
		return nil, err
	}

	if c.usesTimeTracking {
		c.insertLineCounter(prog)
	}

	// at this point the program consists entirely of statement-lines which contain pure yolol-code
	out := &ast.Program{
		Lines: make([]*ast.Line, len(prog.Elements)),
	}

	for i, element := range prog.Elements {
		line := element.(*nast.StatementLine)
		out.Lines[i] = &ast.Line{
			Position:   line.Position,
			Statements: line.Statements,
		}
	}

	c.removeFinalGotoIfNeeded(out)

	if len(out.Lines) > 20 {
		return out, &parser.Error{
			Message: "Program is too large to be compiled into 20 lines of yolol.",
			StartPosition: ast.Position{
				Line:    1,
				Coloumn: 1,
			},
			EndPosition: ast.Position{
				Line:    30,
				Coloumn: 70,
			},
		}
	}

	return out, nil
}

func (c *Converter) maxLineLength() int {
	if !c.usesTimeTracking {
		return 70
	}
	return 70 - 4
}

func (c *Converter) convertNodes(node ast.Node) error {
	f := func(node ast.Node, visitType int) error {
		switch n := node.(type) {
		case *ast.Assignment:
			if visitType == ast.PostVisit {
				return c.convertAssignment(n)
			}
		case *nast.Definition:
			if visitType == ast.PreVisit {
				return c.convertDefinition(n)
			}
		case *nast.MacroDefinition:
			// using pre-visit here is important
			// the definition must be resolved, BEFORE its contents are processed
			if visitType == ast.PreVisit {
				return c.convertMacroDef(n)
			}
		case *nast.MacroInsetion:
			if visitType == ast.PreVisit {
				c.macroLevel = append(c.macroLevel, n.Function+":"+strconv.Itoa(n.Start().Line))
				return c.convertMacroInsertion(n)
			}
		case *nast.IncludeDirective:
			return c.convertInclude(n)
		case *nast.WaitDirective:
			if visitType == ast.PostVisit {
				return c.convertWait(n)
			}
		case *nast.FuncCall:
			if visitType == ast.PreVisit {
				return c.convertFuncCall(n)
			}
		case *ast.Dereference:
			return c.convertDereference(n)
		case *nast.MultilineIf:
			if visitType == ast.PostVisit {
				return c.convertIf(n)
			}
		case *nast.WhileLoop:
			if visitType == ast.PreVisit {
				c.loopcounter++
				c.loopLevel = append(c.loopLevel, c.loopcounter)
			}
			if visitType == ast.PostVisit {
				result := c.convertWhileLoop(n)
				c.loopLevel = c.loopLevel[:len(c.loopLevel)-1]
				return result
			}
		case *ast.UnaryOperation:
		case *ast.BinaryOperation:
			if visitType == ast.PostVisit {
				repl := c.sexpOptimizer.OptimizeExpressionNonRecursive(n)
				if repl != nil {
					return ast.NewNodeReplacementSkip(repl)
				}
				return nil
			}
		case *nast.Trigger:
			if n.Kind == "macroleft" {
				c.macroLevel = c.macroLevel[:len(c.macroLevel)-1]
				return ast.NewNodeReplacement()
			}
		case *nast.BreakStatement:
			return c.convertBreakStatement(n)
		case *nast.ContinueStatement:
			return c.convertContinueStatement(n)
		}

		return nil
	}
	return node.Accept(ast.VisitorFunc(f))
}

// mergeNololNestableElements is a type-wrapper for mergeStatementElements
func (c *Converter) mergeNololNestableElements(lines []nast.NestableElement) ([]nast.NestableElement, error) {
	inp := make([]*nast.StatementLine, len(lines))
	for i, elem := range lines {
		line, isline := elem.(*nast.StatementLine)
		if !isline {
			return nil, parser.Error{
				Message: fmt.Sprintf("Err: Found unconverted nolol-element: %T", elem),
			}
		}
		inp[i] = line
	}
	interm, err := c.mergeStatementElements(inp)
	if err != nil {
		return nil, err
	}
	outp := make([]nast.NestableElement, len(interm))
	for i, elem := range interm {
		outp[i] = elem
	}
	return outp, nil
}

// mergeNololElements is a type-wrapper for mergeStatementElements
func (c *Converter) mergeNololElements(lines []nast.Element) ([]nast.Element, error) {
	inp := make([]*nast.StatementLine, len(lines))
	for i, elem := range lines {
		line, isline := elem.(*nast.StatementLine)
		if !isline {
			return nil, parser.Error{
				Message: fmt.Sprintf("Err: Found unconverted nolol-element: %T", elem),
			}
		}
		inp[i] = line
	}
	interm, err := c.mergeStatementElements(inp)
	if err != nil {
		return nil, err
	}
	outp := make([]nast.Element, len(interm))
	for i, elem := range interm {
		outp[i] = elem
	}
	return outp, nil
}

// mergeStatementElements merges consectuive statementlines into as few lines as possible
func (c *Converter) mergeStatementElements(lines []*nast.StatementLine) ([]*nast.StatementLine, error) {
	maxlen := c.maxLineLength()
	newElements := make([]*nast.StatementLine, 0, len(lines))
	i := 0
	for i < len(lines) {
		current := &nast.StatementLine{
			Line: ast.Line{
				Statements: []ast.Statement{},
			},
			Label:    lines[i].Label,
			Position: lines[i].Position,
			HasEOL:   lines[i].HasEOL,
		}
		current.Statements = append(current.Statements, lines[i].Statements...)
		newElements = append(newElements, current)

		if current.HasEOL {
			// no lines may MUST be appended to a line having EOL
			i++
			continue
		}

		for i+1 < len(lines) {
			currlen := c.getLengthOfLine(&current.Line)

			if currlen > maxlen {
				return newElements, &parser.Error{
					Message:       "The line is too long (>70 characters) to be converted to yolol, even after optimization.",
					StartPosition: current.Start(),
					EndPosition:   current.End(),
				}
			}

			nextline := lines[i+1]

			if nextline.Label == "" && !nextline.HasBOL {
				prev := current.Statements
				current.Statements = make([]ast.Statement, 0, len(current.Statements)+len(nextline.Statements))
				current.Statements = append(current.Statements, prev...)
				current.Statements = append(current.Statements, nextline.Statements...)

				newlen := c.getLengthOfLine(&current.Line)
				if newlen > maxlen {
					// the newly created line is longer then allowed. roll back.
					current.Statements = prev
					break
				}

				i++
				if nextline.HasEOL {
					break
				}
			} else {
				break
			}
		}
		i++
	}
	return newElements, nil
}

//getLengthOfLine returns the amount of characters needed to represent the given line as yolol-code
func (c *Converter) getLengthOfLine(line ast.Node) int {
	ygen := parser.Printer{}
	ygen.Mode = parser.PrintermodeSpaceless
	if c.UseSpaces {
		ygen.Mode = parser.PrintermodeCompact
	}

	ygen.PrinterExtensionFunc = func(node ast.Node, visitType int, p *parser.Printer) (bool, error) {
		if _, is := node.(*nast.GoToLabelStatement); is {
			if c.UseSpaces {
				p.Write("goto XX")
			} else {
				p.Write("gotoXX")
			}
			return true, nil
		}
		if fc, is := node.(*nast.FuncCall); is && fc.Function == "line" {
			p.Write("00")
			return true, nil
		}
		return false, nil
	}
	generated, err := ygen.Print(line)
	if err != nil {
		panic(err)
	}

	linelen := len(generated)
	if strings.HasSuffix(generated, "\n") {
		linelen--
	}

	return linelen
}
