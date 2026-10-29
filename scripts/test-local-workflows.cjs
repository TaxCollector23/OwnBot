#!/usr/bin/env node
'use strict';

// End-to-end CLI + daemon workflow using only loopback fixtures and Node built-ins.
const { spawn } = require('node:child_process');
const { createHash, randomUUID } = require('node:crypto');
const fs = require('node:fs/promises');
const net = require('node:net');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');

const repo = path.resolve(__dirname, '..');
const cliBinary = process.env.OWNBOT_BINARY && path.resolve(process.env.OWNBOT_BINARY);
const daemonBinary = process.env.OWNBOTD_BINARY && path.resolve(process.env.OWNBOTD_BINARY);
const forbiddenPorts = new Set([8765, 11434, 11435]);
const passed = [];
let activeDaemon = null;
const activeCLIs = new Set();
let modelServer = null;
let mcpServer = null;
let privateRoot = null;
let stateDir = null;
let cliEnv = null;
let baseURL = '';

function check(condition, label) {
  if (!condition) throw new Error(label);
  passed.push(label);
}

function jsonResponse(res, status, value) {
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' });
  res.end(value === undefined ? '' : JSON.stringify(value));
}

async function readJSON(req) {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : {};
}

async function listenLoopback(handler) {
  const server = http.createServer((req, res) => {
    Promise.resolve(handler(req, res)).catch(() => {
      if (!res.headersSent) jsonResponse(res, 500, { error: 'fixture error' });
      else res.destroy();
    });
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  return { server, url: 'http://127.0.0.1:' + server.address().port };
}

async function unusedPort() {
  for (;;) {
    const server = net.createServer();
    await new Promise((resolve, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolve));
    const port = server.address().port;
    await new Promise(resolve => server.close(resolve));
    if (!forbiddenPorts.has(port)) return port;
  }
}

async function closeServer(handle) {
  if (!handle?.server?.listening) return;
  await new Promise(resolve => handle.server.close(resolve));
}

function makeProviderResponse(message) {
  const hasCalls = Array.isArray(message.tool_calls);
  return {
    id: 'ownbot-local-workflow-fixture',
    model: 'fixture-model',
    choices: [{ index: 0, message: Object.assign({ role: 'assistant' }, message), finish_reason: hasCalls ? 'tool_calls' : 'stop' }],
    usage: { prompt_tokens: 8, completion_tokens: 4, total_tokens: 12 },
  };
}

async function startModelFixture(argSentinel) {
  const state = { requests: 0, probeRequests: 0, runRequests: 0 };
  const handle = await listenLoopback(async (req, res) => {
    const route = new URL(req.url, 'http://127.0.0.1').pathname;
    if (req.method !== 'POST' || route !== '/v1/chat/completions') return jsonResponse(res, 404, { error: 'not found' });
    const payload = await readJSON(req);
    state.requests++;
    const messages = Array.isArray(payload.messages) ? payload.messages : [];
    const prompt = messages.map(message => String(message.content || '')).join('\n');
    let answer;
    if (prompt.includes('Return only JSON')) {
      state.probeRequests++;
      answer = { content: '{"ok":true}' };
    } else if (prompt.includes('Call probe_one')) {
      state.probeRequests++;
      answer = { tool_calls: [{ id: 'probe-one', type: 'function', function: { name: 'probe_one', arguments: '{"value":"alpha"}' } }] };
    } else if (prompt.includes('Call probe_two')) {
      state.probeRequests++;
      answer = { tool_calls: [{ id: 'probe-two', type: 'function', function: { name: 'probe_two', arguments: '{"value":"beta"}' } }] };
    } else if (messages.some(message => message.role === 'tool')) {
      answer = { content: 'Fixture task completed.' };
    } else if (/approval-check|denial-check/i.test(prompt) && Array.isArray(payload.tools) && payload.tools.length) {
      state.runRequests++;
      const toolName = payload.tools[0]?.function?.name || '';
      answer = { tool_calls: [{
        id: 'fixture-call-' + state.runRequests,
        type: 'function',
        function: { name: toolName, arguments: JSON.stringify({ title: argSentinel }) },
      }] };
    } else {
      answer = { content: 'Fixture response.' };
    }
    jsonResponse(res, 200, makeProviderResponse(answer));
  });
  handle.stats = state;
  return handle;
}

async function startMCPFixture(resultSentinel) {
  const state = { methods: [], calls: [] };
  const handle = await listenLoopback(async (req, res) => {
    if (req.method === 'GET' && new URL(req.url, 'http://127.0.0.1').pathname === '/__fixture/stats') {
      return jsonResponse(res, 200, { methods: state.methods.slice(), callCount: state.calls.length, writeCount: state.calls.length });
    }
    if (req.method !== 'POST') return jsonResponse(res, 404, { error: 'not found' });
    const payload = await readJSON(req);
    const method = payload.method || '';
    state.methods.push(method);
    if (method === 'notifications/initialized') {
      res.writeHead(202, { 'Cache-Control': 'no-store' }).end();
      return;
    }
    let result;
    if (method === 'initialize') {
      result = { protocolVersion: '2025-11-25', capabilities: { tools: {} }, serverInfo: { name: 'ownbot local fixture', version: '1' } };
    } else if (method === 'tools/list') {
      result = { tools: [{
        name: 'create_note',
        description: 'Create a note after approval.',
        inputSchema: { type: 'object', properties: { title: { type: 'string' } }, required: ['title'], additionalProperties: false },
      }] };
    } else if (method === 'tools/call') {
      const name = payload.params?.name || '';
      const args = payload.params?.arguments || {};
      state.calls.push({ name, args });
      if (name !== 'create_note') return jsonResponse(res, 404, { error: 'unexpected tool' });
      result = { content: [{ type: 'text', text: 'Fixture result: ' + resultSentinel }], isError: false };
    } else {
      return jsonResponse(res, 404, { error: 'unexpected method' });
    }
    return jsonResponse(res, 200, { jsonrpc: '2.0', id: payload.id, result });
  });
  handle.fixtureState = state;
  return handle;
}

function runCLI(args, label, expectedExit = 0) {
  return new Promise((resolve, reject) => {
    let child;
    try {
      child = spawn(cliBinary, args, { cwd: repo, env: cliEnv, stdio: ['ignore', 'pipe', 'pipe'] });
    } catch {
      reject(new Error(label + ' command could not start'));
      return;
    }
    activeCLIs.add(child);
    const out = [];
    const err = [];
    let captured = 0;
    let settled = false;
    let timedOut = false;
    let killTimer = null;
    const timeout = setTimeout(() => {
      timedOut = true;
      child.kill('SIGTERM');
      killTimer = setTimeout(() => child.kill('SIGKILL'), 2000);
    }, 120_000);
    const capture = (target, chunk) => {
      captured += chunk.length;
      if (captured > 16 * 1024 * 1024) {
        timedOut = true;
        child.kill('SIGTERM');
        return;
      }
      target.push(chunk);
    };
    child.stdout.on('data', chunk => capture(out, chunk));
    child.stderr.on('data', chunk => capture(err, chunk));
    child.once('error', () => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      if (killTimer) clearTimeout(killTimer);
      activeCLIs.delete(child);
      reject(new Error(label + ' command could not start'));
    });
    child.once('close', (code, signal) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      if (killTimer) clearTimeout(killTimer);
      activeCLIs.delete(child);
      if (timedOut) {
        reject(new Error(label + ' command timed out or exceeded the output limit'));
      } else if (signal || code !== expectedExit) {
        const safeStderr = Buffer.concat(err).toString('utf8').replace(/[a-f0-9]{64}/gi, '[redacted]').slice(0, 300);
        reject(new Error(label + ' command returned ' + (signal || 'exit ' + code) + ' (expected exit ' + expectedExit + ')' + (safeStderr ? ': ' + safeStderr : '')));
      } else {
        resolve({ stdout: Buffer.concat(out).toString('utf8'), stderr: Buffer.concat(err).toString('utf8') });
      }
    });
  });
}

function parseCommandJSON(output, label) {
  const text = String(output).trim();
  const candidates = [];
  for (let index = 0; index < text.length; index++) {
    if (text[index] === '{' || text[index] === '[') candidates.push(index);
  }
  for (const index of candidates) {
    try { return JSON.parse(text.slice(index)); } catch {}
  }
  throw new Error(label + ' did not return JSON');
}

function commonOptions() {
  return ['--url', baseURL, '--data-dir', stateDir];
}

async function ownerAPI(route, { method = 'GET', body, expected = 200 } = {}) {
  const token = (await fs.readFile(path.join(stateDir, 'api.token'), 'utf8')).trim();
  const response = await fetch(baseURL + route, {
    method,
    headers: { Authorization: 'Bearer ' + token, Accept: 'application/json', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(10_000),
  });
  const text = await response.text();
  if (response.status !== expected) throw new Error('disposable daemon API returned an unexpected status');
  if (!text) return null;
  try { return JSON.parse(text); } catch { throw new Error('disposable daemon API returned invalid JSON'); }
}

async function fixtureStats() {
  const model = modelServer.stats;
  const mcpResponse = await fetch(mcpServer.url + '/__fixture/stats', { signal: AbortSignal.timeout(3000) });
  if (!mcpResponse.ok) throw new Error('local MCP fixture stats unavailable');
  const mcp = await mcpResponse.json();
  return { modelRequests: model.requests, modelProbeRequests: model.probeRequests, modelRunRequests: model.runRequests, mcpCalls: mcp.callCount, mcpWrites: mcp.writeCount };
}

async function waitForDaemon(child, url) {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null) throw new Error('disposable daemon exited before readiness');
    try {
      const response = await fetch(url + '/api/status', { signal: AbortSignal.timeout(500) });
      if (response.ok) {
        const status = await response.json();
        if (status.ok && status.service === 'ownbot') return;
      }
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error('disposable daemon did not become ready');
}

async function stopDaemon(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  child.kill('SIGTERM');
  let stopTimer;
  await Promise.race([
    new Promise(resolve => child.once('exit', resolve)),
    new Promise(resolve => { stopTimer = setTimeout(resolve, 7000); }),
  ]);
  clearTimeout(stopTimer);
  if (child.exitCode === null && child.signalCode === null) {
    child.kill('SIGKILL');
    await new Promise(resolve => child.once('exit', resolve));
  }
}

async function runReport(command, code = 0) {
  const args = command.includes('--url') ? command : [...command, ...commonOptions()];
  const { stdout } = await runCLI(args, command[0] + ' ' + command[1], code);
  return parseCommandJSON(stdout, command[0] + ' ' + command[1]);
}

function parseJSONLines(file) {
  const text = require('node:fs').readFileSync(file, 'utf8').trim();
  if (!text) return [];
  return text.split('\n').map(line => {
    try { return JSON.parse(line); } catch { throw new Error('audit export contains invalid JSONL'); }
  });
}

async function main() {
  check(cliBinary && daemonBinary, 'OWNBOT_BINARY and OWNBOTD_BINARY are set');
  for (const binary of [cliBinary, daemonBinary]) {
    const info = await fs.stat(binary).catch(() => null);
    check(info?.isFile(), 'provided binary exists');
  }

  privateRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'ownbot-local-workflows-'));
  stateDir = path.join(privateRoot, 'state');
  await fs.mkdir(stateDir, { recursive: true, mode: 0o700 });
  const argSentinel = 'ARG_SENTINEL_' + randomUUID();
  const resultSentinel = 'RESULT_SENTINEL_' + randomUUID();
  modelServer = await startModelFixture(argSentinel);
  mcpServer = await startMCPFixture(resultSentinel);

  const port = await unusedPort();
  baseURL = 'http://127.0.0.1:' + port;
  cliEnv = { OWNBOT_DATA_DIR: stateDir };
  for (const key of ['PATH', 'LANG', 'LC_ALL', 'HOME', 'USERPROFILE', 'SystemRoot', 'WINDIR', 'TMP', 'TEMP', 'TMPDIR']) {
    if (process.env[key] !== undefined) cliEnv[key] = process.env[key];
  }

  const child = spawn(daemonBinary, ['--data-dir', stateDir, '--listen', '127.0.0.1:' + port], {
    cwd: repo,
    env: cliEnv,
    stdio: ['ignore', 'ignore', 'ignore'],
  });
  activeDaemon = child;
  child.on('error', () => {});
  await waitForDaemon(child, baseURL);
  check(true, 'real daemon starts on a random loopback port');

  const tokenPath = path.join(stateDir, 'api.token');
  const tokenInfo = await fs.stat(tokenPath);
  const ownerToken = (await fs.readFile(tokenPath, 'utf8')).trim();
  check(/^[a-f0-9]{64}$/i.test(ownerToken), 'owner token is present only in the private state fixture');
  if (process.platform !== 'win32') check((tokenInfo.mode & 0o777) === 0o600, 'private fixture token has mode 0600');

  const beforeEmptyCheck = await fixtureStats();
  const empty = await runReport(['inspect', 'check', '--json']);
  check(empty.overall_status === 'info', 'empty inspect check reports a valid information-only setup');
  check(Array.isArray(empty.checks) && empty.checks.length === 6, 'inspect check returns all six metadata groups');
  check(['daemon', 'models', 'agents', 'tools', 'mcp', 'approvals'].every(name => empty.checks.some(item => item.name === name)), 'inspect check names daemon, models, agents, tools, mcp, and approvals');
  const afterEmptyCheck = await fixtureStats();
  check(JSON.stringify(beforeEmptyCheck) === JSON.stringify(afterEmptyCheck), 'empty diagnostics makes no model or MCP provider call');

  const modelAdd = await runCLI([
    'setup', 'model', 'add', '--provider', 'openai_compatible', '--model', 'fixture-model',
    '--name', 'Local HTTP fixture', '--base-url', modelServer.url + '/v1', ...commonOptions(),
  ], 'model add');
  const model = parseCommandJSON(modelAdd.stdout, 'model add');
  check(typeof model.id === 'string' && model.provider === 'openai_compatible', 'CLI saves the loopback model configuration');

  const probe = await runReport(['setup', 'model', 'probe', ...commonOptions(), model.id]);
  check(probe.probe?.score === 100 && probe.probe?.restricted === false, 'CLI capability probe passes against deterministic HTTP fixture');
  check(probe.probe?.json_output && probe.probe?.single_tool && probe.probe?.tool_choice && probe.probe?.argument_accuracy, 'all four recorded capability checks pass');
  check(modelServer.stats.probeRequests === 3, 'probe performs the expected three fixture requests');

  const agentAdd = await runCLI([
    'setup', 'agent', 'add', '--name', 'Local workflow fixture', '--model', model.id,
    '--prompt', 'Use configured tools only and keep the result concise.', ...commonOptions(),
  ], 'agent add');
  const agent = parseCommandJSON(agentAdd.stdout, 'agent add');
  check(typeof agent.id === 'string' && agent.enabled === false, 'CLI creates a disabled agent before enabling it');
  const beforeConfiguredCheck = await fixtureStats();
  const configured = await runReport(['inspect', 'check', '--json']);
  check(configured.overall_status === 'info' || configured.overall_status === 'ok', 'configured model and tool-free agent remain safe');
  check(JSON.stringify(beforeConfiguredCheck) === JSON.stringify(await fixtureStats()), 'configured diagnostics remains metadata-only');

  const mcpAdd = await runCLI([
    'connect', 'add', '--name', 'Local notes fixture', '--transport', 'streamable_http',
    '--endpoint', mcpServer.url + '/mcp', ...commonOptions(),
  ], 'MCP add');
  const mcpConfig = parseCommandJSON(mcpAdd.stdout, 'MCP add');
  check(typeof mcpConfig.id === 'string' && mcpConfig.enabled === false, 'CLI saves disabled MCP metadata');

  const discovery = await runCLI(['connect', 'discover', ...commonOptions(), mcpConfig.id], 'MCP discover');
  const discovered = parseCommandJSON(discovery.stdout, 'MCP discover');
  check(Array.isArray(discovered) && discovered.length === 1 && discovered[0].name === 'create_note', 'CLI discovers one fixture tool without calling it');
  await runCLI(['connect', 'allow', '--write-tool', 'create_note', ...commonOptions(), mcpConfig.id], 'MCP write permission');
  await runCLI(['connect', 'connect', ...commonOptions(), mcpConfig.id], 'MCP connect');
  const tools = await ownerAPI('/api/tools');
  const tool = tools.find(item => item.provider_tool === 'create_note' && item.server === mcpConfig.id);
  check(!!tool && tool.source === 'mcp', 'connected fixture registers its exact tool alias');

  await runCLI([
    'setup', 'agent', 'update', '--id', agent.id, '--enabled', '--mcp=true', '--tool', tool.name,
    ...commonOptions(),
  ], 'agent enable without MCP server binding');
  const beforeWarningCheck = await fixtureStats();
  const unbound = await runReport(['inspect', 'check', '--json'], 1);
  const mcpCheck = unbound.checks?.find(item => item.name === 'mcp');
  check(unbound.overall_status === 'warning' && mcpCheck?.status === 'warning', 'diagnostics warns and exits nonzero for the missing server allowlist');
  check(mcpCheck.details?.some(item => /not in the agent.s MCP server allowlist/i.test(item)), 'warning identifies the absent agent/server binding');
  check(JSON.stringify(beforeWarningCheck) === JSON.stringify(await fixtureStats()), 'warning diagnostics still makes no model or MCP provider call');

  await runCLI([
    'setup', 'agent', 'update', '--id', agent.id, '--enabled', '--mcp=true', '--tool', tool.name,
    '--mcp-server', mcpConfig.id, ...commonOptions(),
  ], 'agent bind MCP server');
  const beforeBoundCheck = await fixtureStats();
  const bound = await runReport(['inspect', 'check', '--json']);
  check(bound.overall_status === 'ok' || bound.overall_status === 'info', 'correct agent/server binding removes the MCP warning');
  check(bound.checks.find(item => item.name === 'mcp')?.status !== 'warning', 'MCP check is no longer warning after explicit binding');
  check(JSON.stringify(beforeBoundCheck) === JSON.stringify(await fixtureStats()), 'bound diagnostics remains metadata-only');

  const firstRun = await runReport([
    'setup', 'agent', 'run', '--id', agent.id, '--input', 'approval-check', ...commonOptions(),
  ]);
  check(firstRun.status === 'awaiting_approval' && typeof firstRun.id === 'string', 'write request pauses at a pending approval');
  const pendingCalls = (await fixtureStats()).mcpWrites;
  check(pendingCalls === 0, 'fixture has received zero writes before approval');
  const beforeApprovalReads = await fixtureStats();
  const approvals = await runReport(['inspect', 'approvals', 'list', ...commonOptions()]);
  const pending = approvals.find(item => item.run_id === firstRun.id && item.status === 'pending');
  check(!!pending && JSON.stringify(pending.args).includes(argSentinel), 'pending approval contains the fixture tool arguments');
  const approvalShown = await runReport(['inspect', 'approvals', 'show', ...commonOptions(), pending.id]);
  check(approvalShown.id === pending.id && approvalShown.status === 'pending' && JSON.stringify(approvalShown.args).includes(argSentinel), 'approval show fetches the exact pending request and full arguments');
  check(JSON.stringify(beforeApprovalReads) === JSON.stringify(await fixtureStats()), 'approval list and show make no model or MCP provider calls');

  const approved = await runReport(['inspect', 'approvals', 'approve', ...commonOptions(), pending.id]);
  check(approved.id === pending.id && approved.status === 'approved', 'approval CLI records the exact approval decision');
  const savedApproval = await ownerAPI('/api/approvals/' + encodeURIComponent(pending.id));
  check(savedApproval.status === 'approved' && !!savedApproval.decided_at && savedApproval.approver === 'local-user', 'daemon records the approved state and decision metadata');
  const approvedAuditPath = '/api/audit?event_type=approval.approved&run_id=' + encodeURIComponent(firstRun.id);
  const approvedEvents = await ownerAPI(approvedAuditPath);
  check(approvedEvents.length === 1 && approvedEvents[0].details?.approval_id === pending.id, 'approval decision creates one matching audit event');
  check((await fixtureStats()).mcpWrites === pendingCalls, 'manual approval does not dispatch before explicit run resume');

  const duplicate = await runCLI(['inspect', 'approvals', 'approve', ...commonOptions(), pending.id], 'duplicate approval decision', 1);
  check(/already "approved"|already approved/i.test(duplicate.stderr), 'duplicate approval decision exits nonzero as already decided');
  check((await ownerAPI(approvedAuditPath)).length === 1 && (await fixtureStats()).mcpWrites === pendingCalls, 'duplicate decision adds no audit decision and no provider write');

  const resumed = await runReport(['inspect', 'runs', 'resume', ...commonOptions(), firstRun.id]);
  check(resumed.status === 'completed', 'approved run resumes to completion');
  const afterApproved = await fixtureStats();
  check(afterApproved.mcpWrites === 1, 'approved write dispatches exactly once');
  check(mcpServer.fixtureState.calls.filter(call => call.name === 'create_note' && call.args.title === argSentinel).length === 1, 'approved fixture call carries the approved arguments exactly once');

  const secondRun = await runReport([
    'setup', 'agent', 'run', '--id', agent.id, '--input', 'denial-check', ...commonOptions(),
  ]);
  check(secondRun.status === 'awaiting_approval' && typeof secondRun.id === 'string', 'second write also pauses for approval');
  const beforeDenyWrites = (await fixtureStats()).mcpWrites;
  const secondApprovals = await runReport(['inspect', 'approvals', 'list', ...commonOptions()]);
  const secondPending = secondApprovals.find(item => item.run_id === secondRun.id && item.status === 'pending');
  check(!!secondPending, 'second run records a pending approval');
  const secondShown = await runReport(['inspect', 'approvals', 'show', ...commonOptions(), secondPending.id]);
  check(secondShown.id === secondPending.id && secondShown.status === 'pending', 'approval show resolves the second exact request');
  const denied = await runReport(['inspect', 'approvals', 'deny', ...commonOptions(), secondPending.id]);
  check(denied.id === secondPending.id && denied.status === 'denied', 'approval CLI records the denial for the exact request');
  const savedDenial = await ownerAPI('/api/approvals/' + encodeURIComponent(secondPending.id));
  check(savedDenial.status === 'denied' && !!savedDenial.decided_at, 'daemon records the denied state and decision time');
  const deniedEvents = await ownerAPI('/api/audit?event_type=approval.denied&run_id=' + encodeURIComponent(secondRun.id));
  check(deniedEvents.length === 1 && deniedEvents[0].details?.approval_id === secondPending.id, 'denial creates one matching audit event');
  const rejectedRun = await runReport(['inspect', 'runs', 'resume', ...commonOptions(), secondRun.id]);
  check(rejectedRun.status === 'failed', 'denied run ends without dispatch');
  check((await fixtureStats()).mcpWrites === beforeDenyWrites, 'denied write adds zero fixture dispatches');

  for (let index = 0; index < 550; index++) {
    await ownerAPI('/api/agents', {
      method: 'POST',
      expected: 201,
      body: { name: 'Audit pagination fixture ' + index, enabled: false, prompt: 'Disposable pagination record.' },
    });
  }
  check(true, 'daemon API creates 550 disposable agents to exercise audit pagination');

  const auditRoot = path.join(privateRoot, 'audit');
  await fs.mkdir(auditRoot, { mode: 0o700 });
  const allPath = path.join(auditRoot, 'all.jsonl');
  await runCLI(['inspect', 'audit', 'export', '--out', allPath, ...commonOptions()], 'all audit export');
  const allEvents = parseJSONLines(allPath);
  const allInfo = await fs.stat(allPath);
  check(allEvents.length > 500, 'CLI exports more than 500 events across audit pages');
  check(process.platform === 'win32' || (allInfo.mode & 0o777) === 0o600, 'audit export file has mode 0600');
  const ids = allEvents.map(event => event.id);
  check(new Set(ids).size === ids.length && ids.every((id, index) => index === 0 || ids[index - 1] > id), 'all-page audit IDs are unique and strictly newest-first');
  check(allEvents.filter(event => event.event_type === 'agent.created').length >= 550, 'pagination fixture uses 550 normal agent-create events');

  const runPath = path.join(auditRoot, 'approved-run.jsonl');
  await runCLI(['inspect', 'audit', 'export', '--run', firstRun.id, '--out', runPath, ...commonOptions()], 'run-filtered audit export');
  const runEvents = parseJSONLines(runPath);
  check(runEvents.length > 0 && runEvents.every(event => event.run_id === firstRun.id), 'run filter returns only the exact run ID');

  const eventPath = path.join(auditRoot, 'dispatch-event.jsonl');
  await runCLI(['inspect', 'audit', 'export', '--event', 'tool.dispatched', '--out', eventPath, ...commonOptions()], 'event-filtered audit export');
  const eventEvents = parseJSONLines(eventPath);
  check(eventEvents.length === 1 && eventEvents.every(event => event.event_type === 'tool.dispatched'), 'event filter returns only exact dispatched events');

  const auditText = allEvents.map(event => JSON.stringify(event)).join('\n');
  check(!auditText.includes(argSentinel) && !auditText.includes(resultSentinel), 'full audit export omits raw tool arguments and results');
  const dispatch = allEvents.find(event => event.event_type === 'tool.dispatched' && event.run_id === firstRun.id);
  const succeeded = allEvents.find(event => event.event_type === 'tool.succeeded' && event.run_id === firstRun.id);
  const expectedArgsHash = createHash('sha256').update(JSON.stringify({ title: argSentinel })).digest('hex');
  check(dispatch?.details?.args === '[redacted]' && dispatch.details.args_sha256 === expectedArgsHash, 'dispatch audit redacts arguments and records their SHA-256');
  check(succeeded?.details?.result === '[redacted]' && /^[a-f0-9]{64}$/.test(succeeded.details.result_sha256 || ''), 'success audit redacts result and records its SHA-256');

  await stopDaemon(activeDaemon);
  activeDaemon = null;
  const unknownResult = await runCLI(['inspect', 'check', '--json', ...commonOptions()], 'stopped daemon diagnostics', 1);
  const unknown = parseCommandJSON(unknownResult.stdout, 'stopped daemon diagnostics');
  check(unknown.overall_status === 'unknown' && unknown.checks.length === 6 && unknown.checks.every(item => item.status === 'unknown'), 'stopped daemon returns six unknown checks as JSON');
  check(true, 'stopped-daemon diagnostics exits nonzero while preserving JSON stdout');

  console.log('PASS: ' + passed.length + ' local workflow checks');
  console.log('Audit records exported: ' + allEvents.length + ' total, including 550 pagination-fixture agent creations.');
  console.log('Coverage: empty diagnostics; fixture model capability probe; MCP server binding; approval CLI list/show/approve/deny; explicit resume once; duplicate decision no-op; deny without dispatch; redacted audit pagination and filters; stopped-daemon unknown JSON.');
}

async function cleanup() {
  for (const child of activeCLIs) child.kill('SIGTERM');
  await Promise.all([...activeCLIs].map(child => new Promise(resolve => {
    if (child.exitCode !== null || child.signalCode !== null) return resolve();
    child.once('close', resolve);
    setTimeout(() => { child.kill('SIGKILL'); resolve(); }, 3000);
  })));
  await stopDaemon(activeDaemon);
  await closeServer(mcpServer);
  await closeServer(modelServer);
  if (privateRoot) await fs.rm(privateRoot, { recursive: true, force: true });
}

main()
  .catch(error => {
    console.error('FAIL: ' + (error?.message || 'local workflow test failed'));
    console.error('Checks before failure: ' + passed.length);
    process.exitCode = 1;
  })
  .finally(cleanup);
