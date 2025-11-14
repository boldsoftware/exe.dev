package linter

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

var Analyzer = &analysis.Analyzer{
	Name:     "slogcontext",
	Doc:      "checks that slog calls use Context variants when a ctx variable is in scope",
	Run:      run,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

// slogFunctions maps non-context slog functions to their context variants
var slogFunctions = map[string]string{
	"Debug":     "DebugContext",
	"Info":      "InfoContext",
	"Warn":      "WarnContext",
	"Error":     "ErrorContext",
	"Log":       "LogContext",
	"DebugCtx":  "DebugContext",
	"InfoCtx":   "InfoContext",
	"WarnCtx":   "WarnContext",
	"ErrorCtx":  "ErrorContext",
	"LogCtx":    "LogContext",
	"Debugf":    "DebugContext",
	"Infof":     "InfoContext",
	"Warnf":     "WarnContext",
	"Errorf":    "ErrorContext",
	"Logf":      "LogContext",
}

func run(pass *analysis.Pass) (interface{}, error) {
	inspect := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{
		(*ast.CallExpr)(nil),
	}

	inspect.Preorder(nodeFilter, func(n ast.Node) {
		call := n.(*ast.CallExpr)

		// Check if this is a slog function call
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}

		// Check if the package is slog
		obj := pass.TypesInfo.ObjectOf(sel.Sel)
		if obj == nil {
			return
		}

		pkg := obj.Pkg()
		if pkg == nil || pkg.Path() != "log/slog" {
			return
		}

		// Check if this is one of the non-context functions
		funcName := sel.Sel.Name
		contextFunc, needsContext := slogFunctions[funcName]
		if !needsContext {
			return
		}

		// Check if there's a ctx variable in scope
		ctxVar := findCtxInScope(pass, call)
		if ctxVar == nil {
			return
		}

		// Build the suggested fix
		var newArgs []ast.Expr
		newArgs = append(newArgs, ast.NewIdent(ctxVar.Name()))
		newArgs = append(newArgs, call.Args...)

		// Create the fix
		var edits []analysis.TextEdit

		// Replace function name
		edits = append(edits, analysis.TextEdit{
			Pos:     sel.Sel.Pos(),
			End:     sel.Sel.End(),
			NewText: []byte(contextFunc),
		})

		// Insert ctx as first argument
		if len(call.Args) > 0 {
			edits = append(edits, analysis.TextEdit{
				Pos:     call.Lparen + 1,
				End:     call.Lparen + 1,
				NewText: []byte(ctxVar.Name() + ", "),
			})
		} else {
			edits = append(edits, analysis.TextEdit{
				Pos:     call.Lparen + 1,
				End:     call.Lparen + 1,
				NewText: []byte(ctxVar.Name()),
			})
		}

		pass.Report(analysis.Diagnostic{
			Pos:     call.Pos(),
			End:     call.End(),
			Message: "should use slog." + contextFunc + " instead of slog." + funcName + " when ctx is available",
			SuggestedFixes: []analysis.SuggestedFix{
				{
					Message:   "Replace with " + contextFunc,
					TextEdits: edits,
				},
			},
		})
	})

	return nil, nil
}

// findCtxInScope looks for a variable named "ctx" in scope at the given position
func findCtxInScope(pass *analysis.Pass, pos ast.Node) *types.Var {
	// Find the innermost scope containing this position
	var scope *types.Scope
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				return false
			}
			// Find function or block containing our position
			if fn, ok := n.(*ast.FuncDecl); ok && nodeContains(fn, pos) {
				if s := pass.TypesInfo.Scopes[fn.Type]; s != nil {
					scope = s
				}
			} else if fn, ok := n.(*ast.FuncLit); ok && nodeContains(fn, pos) {
				if s := pass.TypesInfo.Scopes[fn.Type]; s != nil {
					scope = s
				}
			}
			return true
		})
	}

	if scope == nil {
		return nil
	}

	// Walk up the scope chain looking for ctx
	for s := scope; s != nil; s = s.Parent() {
		if obj := s.Lookup("ctx"); obj != nil {
			if v, ok := obj.(*types.Var); ok {
				// Verify it's a context.Context type
				if isContextType(v.Type()) {
					return v
				}
			}
		}
	}

	return nil
}

// nodeContains checks if outer contains inner
func nodeContains(outer, inner ast.Node) bool {
	return outer.Pos() <= inner.Pos() && inner.End() <= outer.End()
}

// isContextType checks if a type is context.Context
func isContextType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}

	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}

	return obj.Pkg().Path() == "context" && obj.Name() == "Context"
}
