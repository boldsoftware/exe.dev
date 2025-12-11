package completeinit

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// completeInitFact is a fact that marks a type as requiring complete initialization.
type completeInitFact struct{}

func (*completeInitFact) AFact() {}

func (*completeInitFact) String() string {
	return "completeinit"
}

var Analyzer = &analysis.Analyzer{
	Name:      "completeinit",
	Doc:       "checks that struct literals for types annotated with //exe:completeinit include all fields",
	Run:       run,
	Requires:  []*analysis.Analyzer{inspect.Analyzer},
	FactTypes: []analysis.Fact{(*completeInitFact)(nil)},
}

// markerComment is the comment that marks a struct as requiring complete initialization.
const markerComment = "exe:completeinit"

func run(pass *analysis.Pass) (interface{}, error) {
	inspect := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	// First pass: find all structs annotated with //completeinit in this package
	// and export facts for them
	exportMarkedTypes(pass)

	// Second pass: check all composite literals
	nodeFilter := []ast.Node{
		(*ast.CompositeLit)(nil),
	}

	inspect.Preorder(nodeFilter, func(n ast.Node) {
		lit := n.(*ast.CompositeLit)

		// Get the type of this composite literal
		litType := pass.TypesInfo.TypeOf(lit)
		if litType == nil {
			return
		}

		// Handle pointer types (e.g., &Config{})
		if ptr, ok := litType.(*types.Pointer); ok {
			litType = ptr.Elem()
		}

		// Check if it's a named type
		named, ok := litType.(*types.Named)
		if !ok {
			return
		}

		// Check if this type has the completeinit fact (from this package or imported)
		var fact completeInitFact
		if !pass.ImportObjectFact(named.Obj(), &fact) {
			return
		}

		// Get the underlying struct type
		structType, ok := named.Underlying().(*types.Struct)
		if !ok {
			return
		}

		// Check if the literal uses keyed fields
		if len(lit.Elts) > 0 {
			if _, isKeyed := lit.Elts[0].(*ast.KeyValueExpr); !isKeyed {
				pass.Reportf(lit.Pos(), "struct literal of %s must use keyed fields", named.Obj().Name())
				return
			}
		}

		// Collect initialized fields
		initializedFields := make(map[string]bool)
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			initializedFields[key.Name] = true
		}

		// Check for missing fields
		// For exported types, only check exported fields (unexported fields can't be set from other packages)
		// For unexported types, check all fields (they can only be used within the same package)
		typeIsExported := named.Obj().Exported()
		var missingFields []string
		for i := 0; i < structType.NumFields(); i++ {
			field := structType.Field(i)
			// Skip unexported fields for exported types
			if typeIsExported && !field.Exported() {
				continue
			}
			if !initializedFields[field.Name()] {
				missingFields = append(missingFields, field.Name())
			}
		}

		if len(missingFields) > 0 {
			pass.Reportf(lit.Pos(), "struct literal of %s is missing fields: %s", named.Obj().Name(), strings.Join(missingFields, ", "))
		}
	})

	return nil, nil
}

// exportMarkedTypes finds all structs annotated with //completeinit in the current package
// and exports facts for them
func exportMarkedTypes(pass *analysis.Pass) {
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			// Look for type declarations
			genDecl, ok := n.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				return true
			}

			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}

				_, ok = typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}

				// Check for marker comment
				if !hasMarkerComment(genDecl, typeSpec) {
					continue
				}

				// Get the type object
				obj := pass.TypesInfo.ObjectOf(typeSpec.Name)
				if obj == nil {
					continue
				}

				// Export the fact
				pass.ExportObjectFact(obj, &completeInitFact{})
			}

			return true
		})
	}
}

// hasMarkerComment checks if a type spec has the completeinit marker comment
func hasMarkerComment(genDecl *ast.GenDecl, typeSpec *ast.TypeSpec) bool {
	// Check genDecl.Doc (comments immediately before the type declaration)
	if genDecl.Doc != nil {
		for _, comment := range genDecl.Doc.List {
			if containsMarker(comment.Text) {
				return true
			}
		}
	}

	// Check typeSpec.Doc (for grouped type declarations)
	if typeSpec.Doc != nil {
		for _, comment := range typeSpec.Doc.List {
			if containsMarker(comment.Text) {
				return true
			}
		}
	}

	// Check typeSpec.Comment (end-of-line comment)
	if typeSpec.Comment != nil {
		for _, comment := range typeSpec.Comment.List {
			if containsMarker(comment.Text) {
				return true
			}
		}
	}

	return false
}

// containsMarker checks if a comment contains the exe:completeinit marker
func containsMarker(text string) bool {
	// Remove comment prefix
	text = strings.TrimPrefix(text, "//")
	text = strings.TrimPrefix(text, "/*")
	text = strings.TrimSuffix(text, "*/")
	text = strings.TrimSpace(text)

	// Check for exact marker match
	return text == markerComment
}
