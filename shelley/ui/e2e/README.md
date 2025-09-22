# Shelley E2E Tests with Playwright

This directory contains end-to-end tests for the Shelley web interface using Playwright.

## Features

- **Mobile-focused testing**: Primary focus on mobile viewports (iPhone, Pixel)
- **Predictable LLM**: Uses the predictable LLM model for deterministic testing
- **Screenshot capture**: Automatic screenshot generation for visual inspection
- **Tool testing**: Tests bash tool, think tool, and patch tool interactions
- **Multi-browser support**: Tests across Chrome, Firefox, Safari, and mobile variants

## Running Tests

### Install Dependencies
```bash
cd ui/
npm install
npx playwright install
```

### Run All Tests
```bash
npm run test:e2e
```

### Run Specific Tests
```bash
# Run only mobile Chrome tests
npm run test:e2e -- --project="Mobile Chrome"

# Run specific test
npm run test:e2e -- --grep "should load the main page"

# Run with headed browser (visible)
npm run test:e2e:headed

# Open UI mode
npm run test:e2e:ui
```

### Debug Failed Tests
```bash
# View HTML report
npx playwright show-report

# View screenshots
ls -la test-results/*/
```

## Test Structure

### Basic Interactions (`basic-interactions.spec.ts`)
- Page loading
- Starting conversations
- Tool usage
- Conversation history
- Responsive design

### Mobile-Focused Tests (`mobile-focused.spec.ts`)
- Mobile layout verification
- Touch interactions
- Text input on mobile
- Scrolling behavior
- Mobile-specific UI patterns

### Predictable Behavior (`predictable-behavior.spec.ts`)
- Deterministic LLM responses
- Tool interaction patterns
- Error handling
- Multi-turn conversations

## Screenshot Inspection

Screenshots are automatically saved in `test-results/` directory:
- Failed tests: Screenshots at failure point
- All tests: Screenshots at key interaction points
- Mobile-optimized: Focus on mobile viewport sizes

## Predictable LLM

The tests use Shelley's predictable LLM model which provides:
- Consistent responses for the same inputs
- Deterministic tool usage
- Predictable conversation flows
- Special test commands (`echo`, `error`, `tool`)

## Configuration

Playwright configuration is in `playwright.config.ts`:
- Auto-starts Shelley server with predictable model
- Configures mobile-first viewports
- Sets up screenshot and video capture
- Handles test timeouts and retries

## Tips

1. **Mobile First**: Most tests are designed for mobile viewports
2. **Screenshots**: Check `e2e/screenshots/` for visual debugging
3. **Deterministic**: All tests should be repeatable and deterministic
4. **Fast Feedback**: Tests are designed to fail fast with meaningful errors
