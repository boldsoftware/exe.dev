// Package reservevms checks that every Test function in a package that defines reserveVMs calls it, either directly or via a one-level helper.
package reservevms

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer is the reservevms analysis.Analyzer.
var Analyzer = &analysis.Analyzer{
	Name:     "reservevms",
	Doc:      "checks that every Test* function calls reserveVMs (directly or via helper)",
	Run:      run,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

func run(pass *analysis.Pass) (any, error) {
	// Only check packages that define reserveVMs.
	if !definesReserveVMs(pass) {
		return nil, nil
	}

	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	// Collect files that have a file-level //reservevms:ok directive
	// (comment before the package keyword).
	skipFiles := make(map[string]bool)
	for _, f := range pass.Files {
		for _, cg := range f.Comments {
			if cg.Pos() >= f.Package {
				break
			}
			for _, c := range cg.List {
				if strings.Contains(c.Text, "//reservevms:ok") {
					skipFiles[pass.Fset.File(f.Pos()).Name()] = true
				}
			}
		}
	}

	// First pass: collect all functions that directly call reserveVMs.
	callsReserve := make(map[string]bool)
	ins.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(n ast.Node) {
		fn := n.(*ast.FuncDecl)
		if fn.Body != nil && containsCall(fn.Body, "reserveVMs") {
			callsReserve[fn.Name.Name] = true
		}
	})

	// Second pass: check every Test* function.
	ins.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(n ast.Node) {
		fn := n.(*ast.FuncDecl)
		name := fn.Name.Name
		if !strings.HasPrefix(name, "Test") || fn.Body == nil {
			return
		}
		if !isTestFunc(fn) {
			return
		}
		pos := pass.Fset.Position(fn.Pos())
		if skipFiles[pos.Filename] {
			return
		}
		if hasDirective(fn) {
			return
		}
		if callsReserve[name] {
			return
		}
		if callsAnyOf(fn.Body, callsReserve) {
			return
		}
		pass.Reportf(fn.Name.Pos(), "%s does not call reserveVMs (directly or via helper)", name)
	})

	return nil, nil
}

func definesReserveVMs(pass *analysis.Pass) bool {
	return pass.Pkg.Scope().Lookup("reserveVMs") != nil
}

// isTestFunc reports whether fn has the signature func(t *testing.T).
func isTestFunc(fn *ast.FuncDecl) bool {
	params := fn.Type.Params
	if params == nil || len(params.List) != 1 {
		return false
	}
	star, ok := params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "testing" && sel.Sel.Name == "T"
}

// hasDirective reports whether fn has a //reservevms:ok comment in the doc comment block immediately preceding it.
func hasDirective(fn *ast.FuncDecl) bool {
	if fn.Doc != nil {
		for _, c := range fn.Doc.List {
			if strings.Contains(c.Text, "//reservevms:ok") {
				return true
			}
		}
	}
	return false
}

// containsCall reports whether body contains a direct call to funcName.
func containsCall(body *ast.BlockStmt, funcName string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == funcName {
			found = true
		}
		return !found
	})
	return found
}

// callsAnyOf reports whether body directly calls any function in fns.
func callsAnyOf(body *ast.BlockStmt, fns map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && fns[ident.Name] {
			found = true
		}
		return !found
	})
	return found
}
