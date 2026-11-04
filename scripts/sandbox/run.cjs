#!/usr/bin/env node
'use strict';

// Starts an isolated interactive demo against an actual OwnBot daemon binary.
// All mutable state, owner credentials, and logs live in a private temp folder.
const { spawn } = require('node:child_process');
const { createWriteStream } = require('node:fs');
const fs = require('node:fs/promises');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { MODEL_ID, startFixtures } = require('./fixtures.cjs');

const rootRepo = path.resolve(__dirname, '../..');
let child = null;
let fixtures = null;
let privateRoot = null;
let logStream = null;
let stopRequested = false;

function options(args) {
  const parsed = {};
  for (let index = 0; index < args.length; index += 1) {
    if (!args[index].startsWith('--')) throw new Error(`unexpected argument: ${args[index]}`);
    const key = args[index].slice(2);
    if (key === 'help') {
      parsed.help = true;
      continue;
    }
    if (!args[index + 1] || args[index + 1].startsWith('--')) throw new Error(`missing value for --${key}`);
    parsed[key] = args[++index];
  }
  return parsed;
}

function showHelp() {
  console.log('Usage: node scripts/sandbox/run.cjs --daemon /path/to/ownbotd [--cli /path/to/ownbot]');
  console.log('Starts loopback fixtures and the real daemon in disposable private state, then prints the private ready-file path.');
}

async function requireBinary(value, label) {
  if (!value) throw new Error(`${label} is required; build the OwnBot binary first or set its environment variable`);
  const absolute = path.resolve(rootRepo, value);
  const info = await fs.stat(absolute).catch(() => null);
  if (!info?.isFile()) throw new Error(`${label} is not a file`);
  return absolute;
}

async function unusedLoopbackPort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolve));
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return port;
}

async function waitForReady(baseURL) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null) throw new Error('OwnBot daemon exited during startup; inspect daemon.log in the private sandbox folder');
    try {
      const response = await fetch(`${baseURL}/api/status`, { signal: AbortSignal.timeout(500) });
      if (response.ok) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error('OwnBot daemon did not become ready; inspect daemon.log in the private sandbox folder');
}

async function ownerAPI(baseURL, token, route, { method = 'GET', body, expected = 200 } = {}) {
  const response = await fetch(`${baseURL}${route}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      Origin: baseURL,
      Accept: 'application/json',
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(10_000),
  });
  if (response.status !== expected) throw new Error(`sandbox setup request failed (${method} ${route}, HTTP ${response.status})`);
  if (response.status === 204) return null;
  return response.json();
}

async function configureDemo(baseURL, token, modelBaseURL, mcpEndpoint) {
  const model = await ownerAPI(baseURL, token, '/api/models', {
    method: 'POST',
    expected: 201,
    body: { provider: 'openai_compatible', name: 'Simulated sandbox model', model: MODEL_ID, base_url: modelBaseURL },
  });
  const probe = await ownerAPI(baseURL, token, `/api/models/${encodeURIComponent(model.id)}/probe`, { method: 'POST', body: {} });
  if (probe.probe?.score !== 100 || probe.probe?.restricted) throw new Error('simulated model capability probe did not pass');

  const mcp = await ownerAPI(baseURL, token, '/api/mcp/servers', {
    method: 'POST',
    expected: 201,
    body: {
      name: 'Local sandbox notes',
      transport: 'streamable_http',
      endpoint: mcpEndpoint,
      allowed_tools: ['create_note'],
      read_tools: [],
      write_tools: ['create_note'],
    },
  });
  await ownerAPI(baseURL, token, `/api/mcp/servers/${encodeURIComponent(mcp.id)}/connect`, { method: 'POST' });
  const tools = await ownerAPI(baseURL, token, '/api/tools');
  const noteTool = tools.find((item) => item.provider_tool === 'create_note' && item.server === mcp.id);
  if (!noteTool) throw new Error('sandbox note tool was not registered');

  const agent = await ownerAPI(baseURL, token, '/api/agents', {
    method: 'POST',
    expected: 201,
    body: {
      name: 'Sandbox demo agent',
      model: model.id,
      enabled: true,
      max_steps: 8,
      max_tokens: 512,
      timeout_seconds: 120,
      prompt: 'Use create_note only when the task asks for a note. Then summarize the result in one short sentence.',
    },
  });
  await ownerAPI(baseURL, token, `/api/agents/${encodeURIComponent(agent.id)}`, {
    method: 'PATCH',
    body: { mcp_enabled: true, tools: [noteTool.name], mcp_servers: [mcp.id] },
  });
  return { model, mcp, agent, tool: noteTool };
}

async function cleanup() {
  if (child && child.exitCode === null && child.signalCode === null) {
    child.kill('SIGTERM');
    await Promise.race([
      new Promise((resolve) => child.once('exit', resolve)),
      new Promise((resolve) => setTimeout(resolve, 7000)),
    ]);
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGKILL');
      await new Promise((resolve) => child.once('exit', resolve));
    }
  }
  if (logStream) await new Promise((resolve) => logStream.end(resolve));
  await fixtures?.close();
  if (privateRoot) await fs.rm(privateRoot, { recursive: true, force: true });
}

async function main() {
  const args = options(process.argv.slice(2));
  if (args.help) return showHelp();
  const daemonBinary = await requireBinary(args.daemon || process.env.OWNBOTD_BINARY, '--daemon / OWNBOTD_BINARY');
  const cliBinary = args.cli || process.env.OWNBOT_BINARY ? await requireBinary(args.cli || process.env.OWNBOT_BINARY, '--cli / OWNBOT_BINARY') : '';

  privateRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'ownbot-sandbox-'));
  await fs.chmod(privateRoot, 0o700);
  const stateDir = path.join(privateRoot, 'state');
  await fs.mkdir(stateDir, { recursive: true, mode: 0o700 });
  const readyPath = path.join(privateRoot, 'sandbox-ready.json');
  const logPath = path.join(privateRoot, 'daemon.log');
  fixtures = await startFixtures();
  const daemonPort = await unusedLoopbackPort();
  const daemonURL = `http://127.0.0.1:${daemonPort}`;
  logStream = createWriteStream(logPath, { flags: 'a', mode: 0o600 });
  child = spawn(daemonBinary, ['--data-dir', stateDir, '--listen', `127.0.0.1:${daemonPort}`], {
    cwd: rootRepo,
    env: { ...process.env, OWNBOT_DATA_DIR: stateDir },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  child.stdout.pipe(logStream, { end: false });
  child.stderr.pipe(logStream, { end: false });
  child.once('error', () => {});
  await waitForReady(daemonURL);

  const tokenPath = path.join(stateDir, 'api.token');
  const tokenInfo = await fs.stat(tokenPath);
  const ownerToken = (await fs.readFile(tokenPath, 'utf8')).trim();
  if (!/^[a-f0-9]{64}$/i.test(ownerToken)) throw new Error('daemon did not create a valid private owner token');
  if (process.platform !== 'win32' && (tokenInfo.mode & 0o777) !== 0o600) throw new Error('daemon owner token file permissions are not 0600');

  const configured = await configureDemo(daemonURL, ownerToken, fixtures.model.baseURL, fixtures.mcp.endpoint);
  const bootstrap = await ownerAPI(daemonURL, ownerToken, '/api/access/bootstrap', { method: 'POST', expected: 201, body: {} });
  if (!bootstrap.ticket) throw new Error('daemon did not create a dashboard bootstrap ticket');
  const ready = {
    schema: 'ownbot-sandbox-ready-v1',
    daemonURL,
    dashboardURL: `${daemonURL}/dashboard#bootstrap=${encodeURIComponent(bootstrap.ticket)}`,
    dataDir: stateDir,
    ownerToken,
    daemonPID: child.pid,
    daemonLog: logPath,
    readyFile: readyPath,
    model: { id: configured.model.id, name: configured.model.name, model: MODEL_ID, baseURL: fixtures.model.baseURL, statsURL: fixtures.model.statsURL },
    mcp: { id: configured.mcp.id, endpoint: fixtures.mcp.endpoint, statsURL: fixtures.mcp.statsURL },
    agent: { id: configured.agent.id, name: configured.agent.name },
    tool: { id: configured.tool.name, providerTool: configured.tool.provider_tool },
    cliBinary,
  };
  await fs.writeFile(readyPath, `${JSON.stringify(ready, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  await fs.chmod(readyPath, 0o600);

  console.log('Sandbox ready. The simulated model is deterministic and runs no model weights.');
  console.log(`Private runtime manifest: ${readyPath} (owner token and one-time dashboard URL inside; mode 0600)`);
  console.log(`Daemon log: ${logPath}`);
  console.log('Open the dashboard using dashboardURL from the private manifest. Press Ctrl-C to stop and remove all sandbox state.');
  await new Promise((resolve) => {
    const onSignal = () => {
      if (stopRequested) return;
      stopRequested = true;
      resolve();
    };
    process.once('SIGINT', onSignal);
    process.once('SIGTERM', onSignal);
  });
}

main()
  .catch((error) => {
    console.error(`FAIL: ${error?.message || 'sandbox startup failed'}`);
    process.exitCode = 1;
  })
  .finally(cleanup);
