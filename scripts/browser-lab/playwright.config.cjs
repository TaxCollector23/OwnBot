const {defineConfig} = require('@playwright/test');
const path = require('node:path');

module.exports = defineConfig({
  testDir: '.',
  testMatch: '*.spec.cjs',
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: 'list',
  outputDir: path.join(__dirname, 'artifacts/browser-results'),
  use: {
    browserName: 'chromium',
    launchOptions: process.env.OWNBOT_CHROMIUM_EXECUTABLE ? {executablePath: process.env.OWNBOT_CHROMIUM_EXECUTABLE} : {},
    trace: 'off',
    screenshot: 'off',
    video: 'off',
  },
  projects: [
    {name: 'desktop-1440', use: {viewport: {width: 1440, height: 900}}},
    {name: 'phone-375', use: {viewport: {width: 375, height: 812}, isMobile: true, hasTouch: true}},
    {name: 'phone-390', use: {viewport: {width: 390, height: 844}, isMobile: true, hasTouch: true}},
  ],
  webServer: [
    {command: 'node scripts/browser-lab/providers.cjs model', cwd: path.join(__dirname, '../..'), url: 'http://127.0.0.1:11434/__fixture/health', reuseExistingServer: !process.env.CI, timeout: 10_000},
    {command: 'node scripts/browser-lab/providers.cjs mcp', cwd: path.join(__dirname, '../..'), url: 'http://127.0.0.1:11435/__fixture/health', reuseExistingServer: !process.env.CI, timeout: 10_000},
  ],
});
