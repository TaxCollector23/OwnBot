const {spawn} = require('node:child_process');
const {mkdtemp, readFile, rm} = require('node:fs/promises');
const {openSync, closeSync} = require('node:fs');
const os = require('node:os');
const net = require('node:net');
const path = require('node:path');
const {test: base, expect} = require('@playwright/test');

function privateIPv4() {
  const entries = Object.values(os.networkInterfaces()).flat().filter((item) => item && item.family === 'IPv4' && !item.internal);
  const isPrivate = (address) => /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(address);
  return entries.map((item) => item.address).find(isPrivate) || '';
}

async function unusedPort(host) {
  const server = net.createServer();
  await new Promise((resolve, reject) => server.once('error', reject).listen(0, host, resolve));
  const {port} = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}

async function stopProcess(child) {
  if (!child || child.exitCode !== null) return;
  child.kill('SIGTERM');
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 7000)),
  ]);
  if (child.exitCode === null) {
    child.kill('SIGKILL');
    await new Promise((resolve) => child.once('exit', resolve));
  }
}

async function waitForReady(runtime) {
  const deadline = Date.now() + 20_000;
  let lastError = 'not checked';
  while (Date.now() < deadline) {
    if (runtime.child.exitCode !== null) throw new Error(`check=daemon_startup exit=${runtime.child.exitCode} logs=${runtime.logPath}`);
    try {
      const response = await fetch(`${runtime.baseURL}/api/status`, {signal: AbortSignal.timeout(1000)});
      if (response.ok) return;
      lastError = `status=${response.status}`;
    } catch (error) { lastError = error.name || 'connection_failed'; }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(`check=daemon_readiness ${lastError} logs=${runtime.logPath}`);
}

async function launch(runtime, testInfo) {
  const binary = process.env.OWNBOTD_BINARY;
  if (!binary) throw new Error('check=ownbotd_binary_missing (CI builds and sets OWNBOTD_BINARY)');
  const lanIP = privateIPv4();
  if (!lanIP) throw new Error('check=private_lan_interface_available');
  if (!runtime.stateDir) {
    runtime.root = await mkdtemp(path.join(os.tmpdir(), 'ownbot-browser-lab-'));
    runtime.stateDir = path.join(runtime.root, 'state');
    runtime.logPath = path.join(runtime.root, 'ownbotd.log');
    runtime.localPort = await unusedPort('127.0.0.1');
    runtime.lanPort = await unusedPort(lanIP);
    runtime.lanIP = lanIP;
    runtime.baseURL = `http://127.0.0.1:${runtime.localPort}`;
  }
  const log = openSync(runtime.logPath, 'a');
  runtime.child = spawn(binary, [
    '--data-dir', runtime.stateDir,
    '--listen', `127.0.0.1:${runtime.localPort}`,
    '--lan',
    '--lan-address', `${runtime.lanIP}:${runtime.lanPort}`,
  ], {stdio:['ignore', log, log], env:{...process.env, OWNBOT_BROWSER_LAB:'1'}});
  closeSync(log);
  await waitForReady(runtime);
  runtime.ownerToken = (await readFile(path.join(runtime.stateDir, 'api.token'), 'utf8')).trim();
  if (!/^[a-f0-9]{64}$/.test(runtime.ownerToken)) throw new Error('check=private_owner_token_file');
  await testInfo.attach('browser-lab-daemon-log', {path:runtime.logPath, contentType:'text/plain'});
}

async function freshBootstrap(runtime) {
  const response = await fetch(`${runtime.baseURL}/api/access/bootstrap`, {
    method:'POST',
    headers:{Authorization:`Bearer ${runtime.ownerToken}`, Origin:runtime.baseURL, 'Content-Type':'application/json'},
    body:'{}',
  });
  if (response.status !== 201) throw new Error(`check=real_owner_bootstrap status=${response.status}`);
  const payload = await response.json();
  if (!payload.ticket) throw new Error('check=bootstrap_ticket_present');
  return payload.ticket;
}

const test = base.extend({
  daemon: async ({}, use, testInfo) => {
    const runtime = {};
    try {
      await fetch('http://127.0.0.1:11435/__fixture/reset', {method:'POST'});
      await launch(runtime, testInfo);
      await use(runtime);
    } finally {
      await stopProcess(runtime.child);
      if (runtime.root) await rm(runtime.root, {recursive:true, force:true});
    }
  },
  ownerPage: async ({page, daemon}, use) => {
    const browserErrors = [];
    const httpErrors = [];
    const requestFailures = [];
    const expectedHTTP = new Set();
    let allowExpiredOwnerSession = false;
    let restartingDaemon = false;
    let expectedNavigationAborts = false;
    page.on('pageerror', (error) => browserErrors.push(`pageerror: ${error.message}`));
    page.on('console', (message) => {
      if (message.type() !== 'error') return;
      // Chromium also emits console errors for HTTP responses. Track those by
      // response URL/status below so expected validation and auth responses
      // stay separate from uncaught JavaScript and real console.error calls.
      if (/^Failed to load resource:/.test(message.text())) return;
      browserErrors.push(`console: ${message.text()}`);
    });
    page.on('response', (response) => {
      if (response.status() < 400) return;
      const request = response.request();
      const url = new URL(response.url());
      const key = `${request.method()} ${url.pathname} ${response.status()}`;
      const isExpected = expectedHTTP.delete(key) ||
        (allowExpiredOwnerSession && url.origin === daemon.baseURL && url.pathname.startsWith('/api/') && response.status() === 401);
      if (!isExpected) httpErrors.push(key);
    });
    page.on('requestfailed', (request) => {
      const failure = request.failure()?.errorText || 'unknown';
      const url = new URL(request.url());
      if (restartingDaemon && url.origin === daemon.baseURL && failure.includes('ERR_CONNECTION_REFUSED')) return;
      if (expectedNavigationAborts && url.origin === daemon.baseURL && url.pathname.startsWith('/api/') && failure.includes('ERR_ABORTED')) return;
      requestFailures.push(`${request.method()} ${url.pathname}: ${failure}`);
    });
    page.allowExpectedHTTPError = (method, pathname, status) => expectedHTTP.add(`${method} ${pathname} ${status}`);
    page.allowExpiredOwnerSession = () => { allowExpiredOwnerSession = true; };
    page.expectDaemonRestart = (expected) => { restartingDaemon = Boolean(expected); };
    page.withExpectedNavigation = async (navigate) => {
      expectedNavigationAborts = true;
      try { return await navigate(); }
      finally {
        await new Promise((resolve) => setTimeout(resolve, 50));
        expectedNavigationAborts = false;
      }
    };
    const ticket = await freshBootstrap(daemon);
    await page.goto(`${daemon.baseURL}/dashboard#bootstrap=${encodeURIComponent(ticket)}`);
    await expect(page.locator('#token-panel')).toBeHidden();
    await expect(page.locator('#composer-empty')).toBeVisible();
    await use(page);
    expect(browserErrors, 'uncaught JavaScript and console.error messages').toEqual([]);
    expect(httpErrors, 'unexpected HTTP error responses').toEqual([]);
    expect(requestFailures, 'unexpected failed browser requests').toEqual([]);
  },
});

module.exports = {test, expect, freshBootstrap, launch, stopProcess};
