package exe

import (
	"context"
	"fmt"
	"strings"

	docspkg "exe.dev/docs"
	"exe.dev/exemenu"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
)

func (ss *SSHServer) handleDocCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if ss.server == nil || ss.server.docs == nil {
		return cc.Errorf("documentation is not available")
	}

	store := ss.server.docs.Store()
	if store == nil {
		return cc.Errorf("documentation is not available")
	}

	if len(cc.Args) == 0 {
		return ss.writeDocList(cc, store)
	}

	slug := normalizeDocSlug(cc.Args[0])
	if slug == "" {
		return cc.Errorf("invalid doc slug")
	}

	if slug == "list" {
		return ss.writeDocList(cc, store)
	}

	entry, ok := store.EntryBySlug(slug)
	if !ok {
		return cc.Errorf("doc not found: %s", slug)
	}

	if entry.Markdown == "" {
		return cc.Errorf("doc %q is not yet available in the terminal", slug)
	}

	// Non-interactive sessions (no TTY) just print the rendered markdown
	if cc.Terminal == nil || cc.SSHSession == nil {
		rendered, err := renderMarkdown(entry.Markdown, 80)
		if err != nil {
			return err
		}
		fmt.Fprintf(cc.Output, "%s\n", strings.TrimRight(rendered, "\n"))
		return nil
	}

	width, height := 80, 24
	if pty, _, ok := cc.SSHSession.Pty(); ok {
		if pty.Window.Width > 0 {
			width = pty.Window.Width
		}
		if pty.Window.Height > 0 {
			height = pty.Window.Height
		}
	}

	title := entry.Title
	if ss.server != nil && ss.server.devMode != "" && !entry.Published {
		title += " [hidden]"
	}
	model := newDocViewerModel(title, slug, entry.Markdown, width, height)
	program := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithInput(cc.SSHSession),
		tea.WithOutput(cc.SSHSession),
	)

	if _, err := program.Run(); err != nil {
		return err
	}

	fmt.Fprint(cc.Output, "\r\n")
	return nil
}

func (ss *SSHServer) writeDocList(cc *exemenu.CommandContext, store *docspkg.Store) error {
	cc.Writeln("Usage: doc <slug>")
	cc.Writeln("")
	cc.Writeln("Available docs:")
	showHidden := ss.server != nil && ss.server.devMode != ""
	for _, group := range store.Groups() {
		heading := strings.TrimSpace(group.Heading)
		if heading != "" {
			cc.Writeln("  %s", heading)
		}
		for _, entry := range group.Docs {
			title := entry.Title
			if showHidden && !entry.Published {
				title += " [hidden]"
			}
			cc.Writeln("    %-20s %s", entry.Slug, title)
		}
	}
	return nil
}

func normalizeDocSlug(arg string) string {
	slug := strings.TrimSpace(arg)
	slug = strings.TrimPrefix(slug, "/")
	slug = strings.TrimPrefix(slug, "docs/")
	slug = strings.TrimPrefix(slug, "/")
	slug = strings.TrimPrefix(slug, "docs/")
	slug = strings.TrimSuffix(slug, "/")
	slug = strings.ToLower(slug)
	slug = strings.ReplaceAll(slug, " ", "-")
	return slug
}

func renderMarkdown(markdown string, width int) (string, error) {
	wrap := width - 4
	if wrap < 20 {
		wrap = width
	}
	if wrap < 20 {
		wrap = 80
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithEnvironmentConfig(),
		glamour.WithWordWrap(wrap),
	)
	if err != nil {
		renderer, err = glamour.NewTermRenderer(
			glamour.WithWordWrap(wrap),
		)
		if err != nil {
			return "", err
		}
	}

	rendered, err := renderer.Render(markdown)
	if err != nil {
		return "", err
	}
	return rendered, nil
}

type docViewerModel struct {
	title    string
	slug     string
	markdown string
	viewport viewport.Model
	width    int
	height   int
	err      error
}

func newDocViewerModel(title, slug, markdown string, width, height int) *docViewerModel {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}

	vpHeight := height - 4
	if vpHeight < 5 {
		vpHeight = height - 1
	}
	if vpHeight < 1 {
		vpHeight = 1
	}

	vp := viewport.New(width, vpHeight)
	model := &docViewerModel{
		title:    title,
		slug:     slug,
		markdown: markdown,
		viewport: vp,
		width:    width,
		height:   height,
	}
	model.refreshContent()
	return model
}

func (m *docViewerModel) Init() tea.Cmd {
	return nil
}

func (m *docViewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *docViewerModel) View() string {
	if m.err != nil {
		return fmt.Sprintf("error rendering doc: %v", m.err)
	}

	header := fmt.Sprintf("\033[1m%s\033[0m  (%s)  - press q to exit\n\n", m.title, m.slug)
	return header + m.viewport.View()
}

func (m *docViewerModel) resize(width, height int) {
	if width <= 0 {
		width = m.width
	}
	if height <= 0 {
		height = m.height
	}

	if width == m.width && height == m.height {
		return
	}

	m.width = width
	m.height = height
	vpHeight := height - 4
	if vpHeight < 5 {
		vpHeight = height - 1
	}
	if vpHeight < 1 {
		vpHeight = 1
	}

	m.viewport.Width = width
	m.viewport.Height = vpHeight
	m.refreshContent()
}

func (m *docViewerModel) refreshContent() {
	rendered, err := renderMarkdown(m.markdown, m.width)
	if err != nil {
		m.err = err
		return
	}
	m.err = nil
	m.viewport.SetContent(strings.TrimRight(rendered, "\n"))
}
