package linter

import (
	"go/ast"
	"go/token"
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
// Note: Log/LogCtx/Logf are not included because slog.Log already takes context as first param
var slogFunctions = map[string]string{
	"Debug":    "DebugContext",
	"Info":     "InfoContext",
	"Warn":     "WarnContext",
	"Error":    "ErrorContext",
	"DebugCtx": "DebugContext",
	"InfoCtx":  "InfoContext",
	"WarnCtx":  "WarnContext",
	"ErrorCtx": "ErrorContext",
	"Debugf":   "DebugContext",
	"Infof":    "InfoContext",
	"Warnf":    "WarnContext",
	"Errorf":   "ErrorContext",
}

type contextSource struct {
	expr string // The expression to use (e.g., "ctx" or "r.Context()")
	name string // The variable name for reporting (e.g., "ctx" or "r")
}

func run(pass *analysis.Pass) (interface{}, error) {
	inspect := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{
		(*ast.CallExpr)(nil),
	}

	inspect.WithStack(nodeFilter, func(n ast.Node, push bool, stack []ast.Node) bool {
		if !push {
			return true
		}

		call := n.(*ast.CallExpr)

		// Check if this is a slog function call
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		// Check if the package is slog
		obj := pass.TypesInfo.ObjectOf(sel.Sel)
		if obj == nil {
			return true
		}

		pkg := obj.Pkg()
		if pkg == nil || pkg.Path() != "log/slog" {
			return true
		}

		// Check if this is one of the non-context functions
		funcName := sel.Sel.Name
		contextFunc, needsContext := slogFunctions[funcName]
		if !needsContext {
			return true
		}

		// Find all containing functions from the stack (innermost first)
		containingFuncs := findContainingFuncs(stack)
		if len(containingFuncs) == 0 {
			return true
		}

		// Find a context source (ctx variable or r.Context())
		ctxSource := findContextSource(pass, call, containingFuncs)
		if ctxSource == nil {
			return true
		}

		// Create the fix
		var edits []analysis.TextEdit

		// Replace function name
		edits = append(edits, analysis.TextEdit{
			Pos:     sel.Sel.Pos(),
			End:     sel.Sel.End(),
			NewText: []byte(contextFunc),
		})

		// Insert context as first argument
		if len(call.Args) > 0 {
			edits = append(edits, analysis.TextEdit{
				Pos:     call.Lparen + 1,
				End:     call.Lparen + 1,
				NewText: []byte(ctxSource.expr + ", "),
			})
		} else {
			edits = append(edits, analysis.TextEdit{
				Pos:     call.Lparen + 1,
				End:     call.Lparen + 1,
				NewText: []byte(ctxSource.expr),
			})
		}

		pass.Report(analysis.Diagnostic{
			Pos:     call.Pos(),
			End:     call.End(),
			Message: "should use slog." + contextFunc + " instead of slog." + funcName + " when " + ctxSource.name + " is available",
			SuggestedFixes: []analysis.SuggestedFix{
				{
					Message:   "Replace with " + contextFunc,
					TextEdits: edits,
				},
			},
		})

		return true
	})

	return nil, nil
}

// findContainingFuncs finds all FuncDecl or FuncLit in the stack (innermost first)
func findContainingFuncs(stack []ast.Node) []ast.Node {
	var funcs []ast.Node
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i].(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			funcs = append(funcs, stack[i])
		}
	}
	return funcs
}

// findContextSource looks for a way to get a context at the given position
// It first checks for a ctx variable declared before the call, then checks
// for an *http.Request parameter named 'r'
func findContextSource(pass *analysis.Pass, call ast.Node, containingFuncs []ast.Node) *contextSource {
	// First, try to find a ctx variable that's declared before this call
	if ctxVar := findCtxDeclaredBefore(pass, call, containingFuncs); ctxVar != nil {
		return &contextSource{
			expr: ctxVar.Name(),
			name: ctxVar.Name(),
		}
	}

	// If no ctx variable, check if there's an *http.Request parameter named 'r'
	// Check all containing functions (outermost to innermost)
	for i := len(containingFuncs) - 1; i >= 0; i-- {
		if hasHTTPRequestParam(pass, containingFuncs[i]) {
			return &contextSource{
				expr: "r.Context()",
				name: "r",
			}
		}
	}

	return nil
}

// findCtxDeclaredBefore looks for a ctx variable that's declared before the given node
func findCtxDeclaredBefore(pass *analysis.Pass, node ast.Node, containingFuncs []ast.Node) *types.Var {
	if len(containingFuncs) == 0 {
		return nil
	}

	// Get the scope for the innermost function
	innermost := containingFuncs[0]
	var scope *types.Scope
	switch fn := innermost.(type) {
	case *ast.FuncDecl:
		scope = pass.TypesInfo.Scopes[fn.Type]
	case *ast.FuncLit:
		scope = pass.TypesInfo.Scopes[fn.Type]
	}

	if scope == nil {
		return nil
	}

	// Look for ctx in scope
	obj := scope.Lookup("ctx")
	if obj == nil {
		// Try parent scopes
		for s := scope.Parent(); s != nil; s = s.Parent() {
			if obj = s.Lookup("ctx"); obj != nil {
				break
			}
		}
	}

	if obj == nil {
		return nil
	}

	v, ok := obj.(*types.Var)
	if !ok {
		return nil
	}

	// Verify it's a context.Context type
	if !isContextType(v.Type()) {
		return nil
	}

	// Check that ctx is declared before the slog call
	if !isDeclaredBefore(pass, v, node, containingFuncs) {
		return nil
	}

	return v
}

// isDeclaredBefore checks if a variable is declared before a given node
func isDeclaredBefore(pass *analysis.Pass, v *types.Var, node ast.Node, containingFuncs []ast.Node) bool {
	var declPos token.Pos

	// Check if it's a function parameter in any of the containing functions
	for _, containingFunc := range containingFuncs {
		switch fn := containingFunc.(type) {
		case *ast.FuncDecl:
			if fn.Type.Params != nil {
				for _, field := range fn.Type.Params.List {
					for _, name := range field.Names {
						if obj := pass.TypesInfo.ObjectOf(name); obj == v {
							// It's a parameter, so it's always available
							return true
						}
					}
				}
			}
			if fn.Recv != nil {
				for _, field := range fn.Recv.List {
					for _, name := range field.Names {
						if obj := pass.TypesInfo.ObjectOf(name); obj == v {
							// It's a receiver, so it's always available
							return true
						}
					}
				}
			}
		case *ast.FuncLit:
			if fn.Type.Params != nil {
				for _, field := range fn.Type.Params.List {
					for _, name := range field.Names {
						if obj := pass.TypesInfo.ObjectOf(name); obj == v {
							// It's a parameter, so it's always available
							return true
						}
					}
				}
			}
		}
	}

	// Look for the declaration in any of the containing function bodies
	found := false
	for _, containingFunc := range containingFuncs {
		ast.Inspect(containingFunc, func(n ast.Node) bool {
			if n == nil {
				return false
			}

			switch decl := n.(type) {
			case *ast.AssignStmt:
				// Short variable declaration (ctx := ... or ctx, err := ...)
				for _, lhs := range decl.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok {
						if obj := pass.TypesInfo.ObjectOf(ident); obj == v {
							declPos = decl.Pos()
							found = true
							return false
						}
					}
				}
			case *ast.ValueSpec:
				// Var declaration (var ctx ...)
				for _, name := range decl.Names {
					if obj := pass.TypesInfo.ObjectOf(name); obj == v {
						declPos = decl.Pos()
						found = true
						return false
					}
				}
			}
			return true
		})
		if found {
			break
		}
	}

	if !found {
		// Couldn't find the declaration, assume it's not available
		return false
	}

	// Check if the declaration comes before the node
	return declPos < node.Pos()
}

// hasHTTPRequestParam checks if the containing function has an *http.Request parameter named 'r'
func hasHTTPRequestParam(pass *analysis.Pass, containingFunc ast.Node) bool {
	fn, ok := containingFunc.(*ast.FuncDecl)
	if !ok {
		return false
	}

	if fn.Type.Params == nil {
		return false
	}

	for _, field := range fn.Type.Params.List {
		// Check if the type is *http.Request
		starExpr, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}

		selExpr, ok := starExpr.X.(*ast.SelectorExpr)
		if !ok {
			continue
		}

		// Check if it's http.Request
		ident, ok := selExpr.X.(*ast.Ident)
		if !ok || ident.Name != "http" {
			continue
		}

		if selExpr.Sel.Name != "Request" {
			continue
		}

		// Verify it's the net/http package
		obj := pass.TypesInfo.ObjectOf(selExpr.Sel)
		if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "net/http" {
			continue
		}

		// Check if any of the names for this field is 'r'
		for _, name := range field.Names {
			if name.Name == "r" {
				return true
			}
		}
	}

	return false
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
