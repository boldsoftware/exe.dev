# Rules

- Keep business logic server side when possible.
- Start with tests first and then build UI.
- Tests should focus on testing state and interactions.
- PrimeVue should be used over custom CSS. See "Use PrimeVue, not custom HTML" below.

# Commands

See `Makefile` for available commands (`make build`, `make dev`, `make test`, `make typecheck`). The Makefile bootstraps Node.js and pnpm automatically.

# Use PrimeVue, not custom HTML

This app uses PrimeVue 4 with a custom Aura preset. **Always reach for a
PrimeVue component before writing custom markup.** LLMs especially love
to roll their own — don't.

## Mandatory mappings

| Need              | Use                                | Don't                          |
| ----------------- | ---------------------------------- | ------------------------------ |
| Button            | `<Button>`                         | `<button class="...">`         |
| Modal / dialog    | `<Dialog>`                         | custom `.modal-overlay` div    |
| Confirm prompt    | `useConfirm()` + `<ConfirmDialog>` | `window.confirm()`             |
| Toast / notify    | `useToast()` + `<Toast>`           | inline banner                  |
| Form input        | `<InputText>` / `<Textarea>`       | bare `<input>`                 |
| Select / dropdown | `<Select>` / `<MultiSelect>`       | bare `<select>`                |
| Checkbox / radio  | `<Checkbox>` / `<RadioButton>`     | bare `<input type="...">`      |
| Table             | `<DataTable>` + `<Column>`         | hand-rolled `<table>`          |
| Status banner     | `<Message>`                        | custom `.banner` div           |
| Tag / chip        | `<Tag>` / `<Chip>`                 | custom `.tag` span             |
| Tooltip           | `v-tooltip`                        | `title=""` attribute           |
| Popover / menu    | `<Popover>` / `<Menu>`             | custom positioned div          |
| Spinner           | `<ProgressSpinner>`                | CSS keyframe spinner           |
| Card              | `<Card>`                           | custom `.card` div             |

If you find yourself reaching for one of the "Don't" patterns, stop and
check the [PrimeVue docs](https://primevue.org/). The component you want
almost certainly exists.

## When custom IS allowed

A handful of bespoke components have no PrimeVue equivalent and are
sanctioned: `StatusDot`, `TufteSpark`, `CoolS`, `EmojiPicker`. Adding to
this list requires explicit justification — you must explain why no
PrimeVue component fits.

## Styling

- All colors, spacing, and typography come from `src/styles/tokens.css`,
  which aliases PrimeVue's `--p-*` tokens. **Never hardcode hex values
  or pixel sizes for colors.**
- Use `var(--surface-card)`, `var(--text-color)`, etc. — never `#1a1a1a`.
- Light/dark is automatic via `darkModeSelector: 'system'`. Don't write
  `@media (prefers-color-scheme: dark)` unless adding a new token to
  `tokens.css`.

## Migration

The codebase is mid-migration to PrimeVue. When you touch a file that
uses custom markup for one of the mappings above, **migrate it as part
of your change**. Leaving the codebase more PrimeVue-ified than you
found it is a soft requirement.
