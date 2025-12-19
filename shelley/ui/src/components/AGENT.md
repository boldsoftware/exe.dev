# Tool Components

When adding a new specialized tool component (e.g., `FooTool.tsx`), you must register it in **two places**:

1. **ChatInterface.tsx** - Add to the `TOOL_COMPONENTS` map for real-time streaming rendering
2. **Message.tsx** - Add to the switch statements in `renderContent()` for both `tool_use` and `tool_result` cases

If you only add it to one place, the tool will render inconsistently:
- Missing from `TOOL_COMPONENTS`: Falls back to generic rendering during streaming, but shows specialized widget after page reload
- Missing from `Message.tsx`: Shows specialized widget during streaming, but falls back to generic after page reload

Both files need the import statement and the rendering logic for the tool.
