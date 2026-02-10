import { defineConfig, devices } from '@playwright/test';

/**
 * @see https://playwright.dev/docs/test-configuration
 */
export default defineConfig({
  testDir: './e2e',
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
    /* Base URL to use in actions like `await page.goto('/')`. */
    baseURL: process.env.TEST_SERVER_URL || 'http://localhost:9001',
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

  /* Run our test server with isolated database */
  webServer: {
    command: 'node scripts/test-server.cjs',
    url: process.env.TEST_SERVER_URL || 'http://localhost:9001',
    reuseExistingServer: !process.env.CI, // Allow reuse in dev, always fresh in CI
    timeout: 60000,
  },
});
