#!/usr/bin/env node
'use strict';

// Record real OwnBot CLI commands against the deterministic loopback sandbox.
// The terminal view is rendered by Playwright so the recording is portable.
const { spawn, execFile } = require('node:child_process');
const { promisify } = require('node:util');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('@playwright/test');

const execFileAsync = promisify(execFile);
const root = path.resolve(__dirname, '../..');
const runner = path.join(__dirname, 'run.cjs');
const daemon = process.env.OWNBOTD_BINARY;
const cli = process.env.OWNBOT_BINARY;
const output = path.resolve(process.env.OWNBOT_TERMINAL_DEMO_VIDEO || path.join(root, 'demo-site/scripts/explainer/output/ownbot-terminal-demo.webm'));
const task = 'Create a note about testing OwnBot locally.';

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
      lines.forEach(onLine);
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

function parseJSON(stdout) {
  const start = stdout.search(/[\[{]/);
  if (start < 0) throw new Error(`CLI did not return JSON: ${stdout.slice(0, 180)}`);
  return JSON.parse(stdout.slice(start));
}

async function runCLI(binary, args, env) {
  const result = await execFileAsync(binary, args, { cwd: root, env, timeout: 300_000, maxBuffer: 2_000_000 });
  return { stdout: result.stdout.trim(), stderr: result.stderr.trim() };
}

async function main() {
  if (!daemon || !cli) throw new Error('set OWNBOTD_BINARY and OWNBOT_BINARY to binaries built from this checkout');
  if (await fs.stat(output).then(() => true, () => false)) throw new Error(`recording already exists; choose another OWNBOT_TERMINAL_DEMO_VIDEO path: ${output}`);

  const sandbox = spawn(process.execPath, [runner, '--daemon', daemon, '--cli', cli], { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] });
  let browser;
  let context;
  let videoDir;
  try {
    const readyPath = await waitForSandbox(sandbox);
    const ready = JSON.parse(await fs.readFile(readyPath, 'utf8'));
    const env = { ...process.env, OWNBOT_API_URL: ready.daemonURL, OWNBOT_DATA_DIR: ready.dataDir, NO_COLOR: '1', TERM: 'dumb' };
    const lines = [];
    const show = (command, outputLines) => lines.push({ kind: 'command', text: `$ ${command }` }, ...outputLines.map((text) => ({ kind: 'output', text })));

    const check = parseJSON((await runCLI(cli, ['inspect', 'check', '--json'], env)).stdout);
    if (check.overall_status === 'warning' || check.overall_status === 'unknown') throw new Error(`sandbox CLI diagnostics did not pass: ${check.overall_status}`);
    show('ownbot inspect check --json', [
      '{',
      `  "overall_status": "${check.overall_status}",`,
      '  "checks": [',
      ...check.checks.map((entry, index) => `    { "name": "${entry.name}", "status": "${entry.status}" }${index < check.checks.length - 1 ? ',' : ''}`),
      '  ]',
      '}',
    ]);

    const runOutput = await runCLI(cli, ['setup', 'agent', 'run', '--id', ready.agent.id, '--input', task], env);
    const run = parseJSON(runOutput.stdout);
    if (run.status !== 'awaiting_approval') throw new Error(`sandbox task did not stop for approval: ${run.status}`);
    show(`ownbot setup agent run --id ${ready.agent.id} --input "${task}"`, [
      `Run ${run.id}: ${run.status} (prompt version ${run.prompt_version})`,
    ]);

    const pending = parseJSON((await runCLI(cli, ['inspect', 'approvals', 'list'], env)).stdout).find((entry) => entry.status === 'pending' && entry.run_id === run.id);
    if (!pending) throw new Error('CLI did not find the sandbox task approval');
    show('ownbot inspect approvals list', [
      '[',
      `  { "id": "${pending.id}", "run_id": "${pending.run_id}",`,
      `    "tool": "${pending.tool}", "status": "${pending.status}",`,
      `    "args": ${JSON.stringify(typeof pending.args === 'string' ? JSON.parse(pending.args) : pending.args)} }`,
      ']',
    ]);

    const decision = parseJSON((await runCLI(cli, ['inspect', 'approvals', 'approve', pending.id], env)).stdout);
    if (decision.status !== 'approved') throw new Error(`CLI approval did not succeed: ${decision.status}`);
    show(`ownbot inspect approvals approve ${pending.id}`, [
      `{ "id": "${decision.id}", "status": "${decision.status}" }`,
    ]);

    const resumeOutput = await runCLI(cli, ['inspect', 'runs', 'resume', run.id], env);
    const completed = parseJSON(resumeOutput.stdout);
    if (completed.status !== 'completed') throw new Error(`sandbox task did not complete after approval: ${completed.status}`);
    const statsResponse = await fetch(ready.mcp.statsURL, { signal: AbortSignal.timeout(3000) });
    const stats = await statsResponse.json();
    if (stats.writes !== 1) throw new Error(`expected one isolated MCP sample write; found ${stats.writes}`);
    const outputText = JSON.stringify(completed.output || 'Sandbox task complete. The approved sample note was created by the local MCP fixture.');
    show(`ownbot inspect runs resume ${run.id}`, [
      `Run ${completed.id}: ${completed.status}`,
      `{ "status": "${completed.status}", "output": ${outputText} }`,
    ]);
    lines.push({ kind: 'notice', text: 'Loopback sandbox · deterministic model fixture · one sample write after approval · no model weights downloaded' });

    await fs.mkdir(path.dirname(output), { recursive: true });
    videoDir = await fs.mkdtemp(path.join(os.tmpdir(), 'ownbot-terminal-video-'));
    browser = await chromium.launch({ headless: true, ...(process.env.OWNBOT_CHROMIUM_EXECUTABLE ? { executablePath: process.env.OWNBOT_CHROMIUM_EXECUTABLE } : {}) });
    context = await browser.newContext({ viewport: { width: 1280, height: 720 }, recordVideo: { dir: videoDir, size: { width: 1280, height: 720 } } });
    const page = await context.newPage();
    await page.setContent(`<!doctype html><html><head><meta charset="utf-8"><style>
      *{box-sizing:border-box}html,body{margin:0;width:1280px;height:720px;overflow:hidden;background:#101318;color:#dfe6ee;font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace}
      .frame{width:1100px;margin:48px auto 0;border:1px solid #343b47;border-radius:12px;overflow:hidden;background:#151a21;box-shadow:0 22px 80px #0008}
      .bar{height:44px;background:#202630;border-bottom:1px solid #343b47;display:flex;align-items:center;gap:8px;padding:0 16px;color:#aab5c3;font-size:13px}
      .dot{width:10px;height:10px;border-radius:50%;display:inline-block;background:#e06c65}.dot:nth-child(2){background:#dfb35d}.dot:nth-child(3){background:#63b87b}
      .title{margin-left:10px}.label{margin-left:auto;color:#91a0b2}.body{height:585px;padding:25px 30px 20px;overflow:hidden;font-size:17px;line-height:1.48}
      .line{white-space:pre-wrap;overflow-wrap:anywhere;min-height:1.48em}.command{color:#f4c26b;margin-top:8px}.command .prompt{color:#6fcf97}.output{color:#d7dee8}.notice{margin-top:16px;padding-top:12px;border-top:1px solid #343b47;color:#9fb0c4;font-size:14px}
      .cursor{display:inline-block;width:9px;height:19px;vertical-align:-3px;background:#68d391;animation:blink 1s steps(2,start) infinite}@keyframes blink{to{visibility:hidden}}
    </style></head><body><div class="frame"><div class="bar"><i class="dot"></i><i class="dot"></i><i class="dot"></i><span class="title">OwnBot — terminal demo</span><span class="label">127.0.0.1 · local fixture</span></div><div class="body" id="body"><div class="line output">OwnBot CLI · local task and approval flow</div></div></div></body></html>`);

    const body = page.locator('#body');
    for (const line of lines) {
      await page.evaluate(({ kind, text }) => {
        const element = document.createElement('div');
        element.className = `line ${kind}`;
        if (kind === 'command') {
          const prompt = document.createElement('span');
          prompt.className = 'prompt';
          prompt.textContent = 'rangan@ownbot:~/OwnBot$ ';
          element.append(prompt, document.createTextNode(text.slice(2)));
        } else {
          element.textContent = text;
        }
        document.querySelector('#body').append(element);
      }, line);
      await page.evaluate(() => document.querySelector('#body').lastElementChild.scrollIntoView({ block: 'nearest' }));
      if (line.kind === 'command') await page.waitForTimeout(500);
      else await page.waitForTimeout(line.kind === 'notice' ? 1100 : 90);
    }
    await body.evaluate((element) => {
      const cursor = document.createElement('span');
      cursor.className = 'cursor';
      element.append(cursor);
    });
    await page.waitForTimeout(2200);
    const video = page.video();
    if (!video) throw new Error('Playwright did not create a terminal video');
    await context.close();
    context = null;
    await video.saveAs(output);
    console.log(`Recorded terminal CLI demo: ${output}`);
    console.log('The command ran against deterministic loopback fixtures; no model weights or external providers were used.');
  } finally {
    await context?.close().catch(() => {});
    await browser?.close().catch(() => {});
    if (videoDir) await fs.rm(videoDir, { recursive: true, force: true });
    await stopSandbox(sandbox);
  }
}

main().catch((error) => {
  console.error(`Terminal demo recording failed: ${error?.message || String(error)}`);
  process.exitCode = 1;
});
