#!/usr/bin/env node
'use strict';

// Record the loopback-only OwnBot sandbox demo with deterministic fixtures.
const { spawn } = require('node:child_process');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('@playwright/test');

const root = path.resolve(__dirname, '../..');
const runner = path.join(__dirname, 'run.cjs');
const daemon = process.env.OWNBOTD_BINARY;
const cli = process.env.OWNBOT_BINARY;
const output = path.resolve(process.env.OWNBOT_DEMO_VIDEO || path.join(root, 'demo-site/scripts/explainer/output/ownbot-sandbox-demo.webm'));

function waitForSandbox(child) {
  return new Promise((resolve, reject) => {
    let remaining = '';
    let readyPath = '';
    let sandboxStarted = false;
    const timer = setTimeout(() => reject(new Error('sandbox startup timed out')), 30_000);
    const onLine = (line) => {
      const match = /^Private runtime manifest: (.+) \(owner token and one-time dashboard URL inside; mode 0600\)$/.exec(line);
      if (match) readyPath = match[1];
      if (line.startsWith('Sandbox ready.')) sandboxStarted = true;
      if (sandboxStarted && readyPath) {
        clearTimeout(timer);
        resolve(readyPath);
      }
    };
    child.stdout.setEncoding('utf8').on('data', (chunk) => {
      remaining += chunk;
      const lines = remaining.split(/\r?\n/);
      remaining = lines.pop() || '';
      for (const line of lines) onLine(line);
    });
    child.stderr.setEncoding('utf8').on('data', (chunk) => process.stderr.write(chunk));
    child.once('error', reject);
    child.once('exit', (status) => {
      if (!readyPath) reject(new Error(`sandbox exited during startup (${status})`));
    });
  });
}

async function stopSandbox(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  child.kill('SIGTERM');
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 8_000)),
  ]);
  if (child.exitCode === null && child.signalCode === null) {
    child.kill('SIGKILL');
    await new Promise((resolve) => child.once('exit', resolve));
  }
}

async function main() {
  if (!daemon) throw new Error('set OWNBOTD_BINARY to a daemon built from this checkout');
  const args = [runner, '--daemon', daemon];
  if (cli) args.push('--cli', cli);
  const sandbox = spawn(process.execPath, args, { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] });
  let videoDir;
  let browser;
  let context;
  try {
    const readyPath = await waitForSandbox(sandbox);
    const ready = JSON.parse(await fs.readFile(readyPath, 'utf8'));
    await fs.mkdir(path.dirname(output), { recursive: true });
    if (await fs.stat(output).then(() => true, () => false)) throw new Error(`recording already exists; choose a new OWNBOT_DEMO_VIDEO path: ${output}`);
    videoDir = await fs.mkdtemp(path.join(os.tmpdir(), 'ownbot-video-'));
    browser = await chromium.launch({ headless: true, ...(process.env.OWNBOT_CHROMIUM_EXECUTABLE ? { executablePath: process.env.OWNBOT_CHROMIUM_EXECUTABLE } : {}) });
    context = await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
      recordVideo: { dir: videoDir, size: { width: 390, height: 844 } },
    });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    await page.goto(ready.dashboardURL, { waitUntil: 'domcontentloaded' });
    await page.getByRole('heading', { name: 'New task' }).waitFor();
    await page.locator('#task-form').waitFor({ state: 'visible' });
    await page.waitForTimeout(1200);
    await page.locator('#task-input').fill('Create a note about testing OwnBot locally.');
    await page.waitForTimeout(900);
    await page.getByRole('button', { name: 'Run task' }).click();
    await page.locator('#approvals-panel').waitFor({ state: 'visible' });
    await page.getByRole('heading', { name: 'Needs your decision' }).waitFor();
    await page.locator('#approvals-panel').scrollIntoViewIfNeeded();
    await page.waitForTimeout(2200);
    await page.getByRole('button', { name: 'Approve and resume' }).click();
    await page.locator('#run-output-panel').waitFor({ state: 'visible' });
    await page.waitForFunction(() => document.querySelector('#run-detail .run-output-text pre')?.textContent.includes('Sandbox task complete'));
    await page.locator('#run-output-panel').scrollIntoViewIfNeeded();
    await page.waitForTimeout(2600);
    const video = page.video();
    if (!video) throw new Error('Playwright did not create a browser recording');
    await context.close();
    context = null;
    await video.saveAs(output);
    console.log(`Recorded local-fixture dashboard demo: ${output}`);
    console.log('The simulated model used scripted responses; no model weights or external services were used.');
  } finally {
    await context?.close().catch(() => {});
    await browser?.close().catch(() => {});
    if (videoDir) await fs.rm(videoDir, { recursive: true, force: true });
    await stopSandbox(sandbox);
  }
}

main().catch((error) => {
  console.error(`Sandbox recording failed: ${error?.message || String(error)}`);
  process.exitCode = 1;
});
