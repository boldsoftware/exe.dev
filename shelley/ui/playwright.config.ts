import { defineConfig, devices } from '@playwright/test';

/**
 * The test server is managed by globalSetup (scripts/global-setup.ts).
 * It starts shelley with --port 0 and exports the actual URL via
 * PLAYWRIGHT_TEST_BASE_URL, which Playwright's baseURL fixture reads
 * automatically. This eliminates hardcoded ports and port conflicts.
 *
 * To point at an already-running server, set TEST_SERVER_URL.
 *
 * @see https://playwright.dev/docs/test-configuration
 */
export default defineConfig({
  testDir: './e2e',
  globalSetup: './scripts/global-setup.ts',
  /* Run tests in files in parallel */
  fullyParallel: false, // Keep simple for now
  /* Fail the build on CI if you accidentally left test.only in the source code. */
  forbidOnly: !!process.env.CI,
  /* Retry on CI only */
  retries: process.env.CI ? 1 : 0,
  /* Single worker for predictable test database state */
  workers: 1,
  /* Reporter to use. See https://playwright.dev/docs/test-reporters */
  reporter: process.env.CI ? [['html', { open: 'never' }], ['list']] : 'list',
  /* Shared settings for all the projects below. See https://playwright.dev/docs/api/class-testoptions. */
  use: {
    /* baseURL is set automatically via PLAYWRIGHT_TEST_BASE_URL from global-setup */
    /* Collect trace on all tests, keep only on failure */
    trace: 'retain-on-failure',
    /* Take a screenshot after every test */
    screenshot: 'on',
    /* Record video on all tests, keep only on failure */
    video: 'retain-on-failure',
  },

  /* Just test mobile Chrome for simplicity */
  projects: [
    {
      name: 'Mobile Chrome',
      use: { ...devices['Pixel 5'] },
    },
  ],
});
