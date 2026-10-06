import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './tests',
  forbidOnly: !!process.env.CI,
  workers: 1,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:3001',
    browserName: 'chromium',
    channel: process.env.PLAYWRIGHT_CHANNEL,
    viewport: { width: 1440, height: 1000 },
    // Auth responses and the one-time key must not be captured in traces.
    trace: 'off',
    screenshot: 'off',
    video: 'off',
  },
  webServer: {
    command: 'pnpm exec vite --host 127.0.0.1 --port 3001 --strictPort',
    url: 'http://127.0.0.1:3001',
    reuseExistingServer: false,
    env: {
      API_PROXY_TARGET:
        process.env.UI_TEST_API_TARGET ?? 'http://127.0.0.1:8080',
    },
  },
})
