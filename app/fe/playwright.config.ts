import { defineConfig } from '@playwright/test';
import { existsSync } from 'node:fs';

const localChrome = process.platform === 'darwin' && existsSync('/Applications/Google Chrome.app');

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  use: {
    baseURL: 'http://127.0.0.1:3000',
    browserName: 'chromium',
    channel: localChrome ? 'chrome' : undefined,
    trace: 'on-first-retry',
  },
  webServer: {
    command: 'npm run dev',
    url: 'http://127.0.0.1:3000',
    reuseExistingServer: true,
  },
  projects: [
    { name: 'desktop-1440', use: { viewport: { width: 1440, height: 900 } } },
    { name: 'tablet-1024', use: { viewport: { width: 1024, height: 768 } } },
    { name: 'mobile-390', use: { viewport: { width: 390, height: 844 } } },
  ],
});
