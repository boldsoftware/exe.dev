package blog

import (
	htmpl "html/template"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// codeRenderer customizes rendering for code blocks so we can
// differentiate between tab-indented (ast.KindCodeBlock) and fenced
// (ast.KindFencedCodeBlock) markdown.
type codeRenderer struct{}

func (r *codeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindCodeBlock, r.renderCodeBlock)
	reg.Register(ast.KindFencedCodeBlock, r.renderFencedCodeBlock)
}

func (r *codeRenderer) renderCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	// Tab-indented blocks: wrap and inherit font, not code styling
	_, _ = w.WriteString("<pre class=\"indented\"><code>")
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		htmpl.HTMLEscape(w, seg.Value(source))
	}
	_, _ = w.WriteString("</code></pre>")
	return ast.WalkSkipChildren, nil
}

func (r *codeRenderer) renderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	fcb, _ := node.(*ast.FencedCodeBlock)
	lang := ""
	if fcb != nil {
		lang = string(fcb.Language(source))
	}
	// Fenced code blocks: keep code styling; add class="code" to <pre>
	if lang != "" {
		_, _ = w.WriteString("<pre class=\"code\"><code class=\"language-")
		htmpl.HTMLEscape(w, []byte(lang))
		_, _ = w.WriteString("\">")
	} else {
		_, _ = w.WriteString("<pre class=\"code\"><code>")
	}
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		htmpl.HTMLEscape(w, seg.Value(source))
	}
	_, _ = w.WriteString("</code></pre>")
	return ast.WalkSkipChildren, nil
}

// Renderer returns a goldmark renderer that includes our codeRenderer
func Renderer() renderer.Renderer {
	return renderer.NewRenderer(
		renderer.WithNodeRenderers(
			// Lower priority value ensures the custom renderer registers after html.
			util.Prioritized(&codeRenderer{}, 500),
			util.Prioritized(html.NewRenderer(html.WithUnsafe(), html.WithXHTML()), 1000),
		),
	)
}
