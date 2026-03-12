# styleguide

This package is a lightweight UI prototyping tool served at `/debug/styleguide`. New widgets are prototyped here as static HTML files, iterated on, and then migrated into the real templates (`templates/` and `execore/debug_templates/`).

When working on multiple components, spin up agents in parallel to prototype them concurrently.

## Structure

- `styleguide.go` — embeds `pages/` and serves them as static files.
- `pages/` — static HTML files, one per component. Add new files here and link them from the index.
- The index page is a Go template at `execore/debug_templates/styleguide.html` (rendered by the debug server so it gets the stage background color). It is organized into **sections** (e.g., Billing, Infrastructure) with component links under each section. When adding a new component, add it under the appropriate section or create a new one.

## Adding a new component

1. Create `pages/<name>.html` with hardcoded scenarios showing the component in various states. Use existing styles from the app — see `execore/debug_templates/base-head.html` and `templates/user-profile.html` for reference.
2. Add a link to `execore/debug_templates/styleguide.html` under the relevant section.
3. Iterate on the HTML until the design is right, then migrate the markup and styles into the real templates.
