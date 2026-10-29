const {request: httpsRequest} = require('node:https');
const {readFile} = require('node:fs/promises');
const path = require('node:path');
const {test, expect, freshBootstrap, launch, stopProcess} = require('./daemon-fixture.cjs');

const screenshot = async (page, testInfo, name, fullPage = true) => page.screenshot({
  path:testInfo.outputPath(name),
  fullPage,
  animations:'disabled',
  mask:[page.locator('.access-url'), page.locator('#pairing-link'), page.locator('#pairing-link-meta'), page.locator('#pairing-fingerprint-meta')],
  maskColor:'#6e6e68',
});

async function ownerAPI(daemon, path, {method = 'GET', body} = {}) {
  const response = await fetch(`${daemon.baseURL}${path}`, {
    method,
    headers:{Authorization:`Bearer ${daemon.ownerToken}`, Accept:'application/json', ...(body === undefined ? {} : {'Content-Type':'application/json'})},
    ...(body === undefined ? {} : {body:JSON.stringify(body)}),
  });
  if (!response.ok) {
    let detail = '';
    try { detail = (await response.json()).error || ''; } catch {}
    throw new Error(`${method} ${path} failed (${response.status}) ${detail}`);
  }
  if (response.status === 204) return null;
  return response.json();
}

async function setUpModelAgent(page, daemon) {
  const model = await ownerAPI(daemon, '/api/models', {method:'POST', body:{provider:'ollama', model:'browser-fixture-model', base_url:'http://127.0.0.1:11434/v1'}});
  const probe = await ownerAPI(daemon, `/api/models/${encodeURIComponent(model.id)}/probe`, {method:'POST', body:{}});
  if (probe.probe?.score !== 100) throw new Error(`check=fixture_model_probe score=${probe.probe?.score}`);
  const agent = await ownerAPI(daemon, '/api/agents', {method:'POST', body:{name:'Browser fixture agent', model:model.id, enabled:true, max_steps:8, max_tokens:4096, timeout_seconds:120}});
  await page.locator('#refresh-button').click();
  await expect(page.locator('#task-form')).toBeVisible();
  await expect(page.locator('#task-input')).toBeEnabled();
  return {agentID:agent.id, modelID:model.id};
}

async function savePrompt(daemon, agentID, prompt, version) {
  const result = await ownerAPI(daemon, `/api/agents/${encodeURIComponent(agentID)}/prompts`, {method:'POST', body:{prompt}});
  expect(result.version).toBe(version);
}

async function createMCPConnection(daemon) {
  const server = await ownerAPI(daemon, '/api/mcp/servers', {method:'POST', body:{name:'Browser fixture notes', transport:'streamable_http', endpoint:'http://127.0.0.1:11435/mcp', allowed_tools:['create_note'], read_tools:[], write_tools:['create_note']}});
  await ownerAPI(daemon, `/api/mcp/servers/${encodeURIComponent(server.id)}/connect`, {method:'POST', body:{}});
  return server;
}

async function configureWriteTool(daemon, agentID, serverID) {
  const tools = await ownerAPI(daemon, '/api/tools');
  const tool = tools.find((item) => item.provider_tool === 'create_note');
  if (!tool) throw new Error('check=fixture_mcp_tool_registered');
  await ownerAPI(daemon, `/api/agents/${encodeURIComponent(agentID)}`, {method:'PATCH', body:{mcp_enabled:true, tools:[tool.name], mcp_servers:[serverID]}});
}

async function runTask(page, input) {
  await page.locator('#task-input').fill(input);
  await page.locator('#start-task-button').click();
}

async function fixtureStats() {
  const response = await fetch('http://127.0.0.1:11435/__fixture/stats');
  if (!response.ok) throw new Error(`check=provider_stats status=${response.status}`);
  return response.json();
}

async function pairThroughPinnedTLS(runtime, secret, name, expectedStatus = 201) {
  const cert = await readFile(path.join(runtime.stateDir, 'lan.cert.pem'));
  const address = `${runtime.lanIP}:${runtime.lanPort}`;
  const body = JSON.stringify({secret, name});
  const response = await new Promise((resolve, reject) => {
    const request = httpsRequest({
      host:runtime.lanIP,
      port:runtime.lanPort,
      localAddress:runtime.lanIP,
      path:'/api/devices/pair',
      method:'POST',
      ca:cert,
      rejectUnauthorized:true,
      headers:{Host:address, Origin:`https://${address}`, 'Content-Type':'application/json', 'Content-Length':Buffer.byteLength(body)},
      timeout:5000,
    }, (res) => {
      const chunks = [];
      res.on('data', (chunk) => chunks.push(chunk));
      res.on('end', () => resolve({status:res.statusCode, body:Buffer.concat(chunks).toString('utf8')}));
    });
    request.on('error', reject);
    request.on('timeout', () => request.destroy(new Error('TLS pairing request timed out')));
    request.end(body);
  });
  if (response.status !== expectedStatus) throw new Error(`check=pinned_lan_pairing expected=${expectedStatus} actual=${response.status}`);
  if (expectedStatus !== 201) return response;
  const result = JSON.parse(response.body);
  if (!result.device?.id || !result.token) throw new Error('check=paired_device_response');
  return result;
}

test('real owner bootstrap, task setup guide, screenshots, and responsive controls', async ({ownerPage}, testInfo) => {
  const page = ownerPage;
  await screenshot(page, testInfo, `home-${testInfo.project.name}.png`);
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.locator('#token-panel')).toBeHidden();
  await expect(page.locator('#composer-empty')).toBeVisible();
  await expect(page.locator('#composer-empty code')).toHaveText('ownbot setup');
  await expect(page.locator('#recommendations-heading')).toBeVisible();
  await expect(page.locator('#task-form')).toBeHidden();
  await expect(page.locator('#run-output-panel')).toBeHidden();
  await expect(page.locator('#task-input')).toHaveCount(1);
  await expect(page.locator('#api-token, #model-form, #agent-form, #mcp-form')).toHaveCount(0);
  await expect(page.locator('#settings-toggle, #settings-drawer')).toHaveCount(0);
  await expect(page.locator('#runs-list')).toContainText('No runs yet.');
  await page.keyboard.press('Tab');
  await expect(page.locator('.skip-link')).toBeFocused();
  await page.evaluate(() => document.activeElement.blur());
  const recommendationStatus = page.locator('#recommendations-status');
  await expect(recommendationStatus).not.toHaveText('Checking host hardware…');
  const suggestions = page.locator('#recommendations-list li');
  if (await suggestions.count()) {
    await expect(suggestions.first()).toContainText('qwen3:1.7b');
    await expect(suggestions.first().locator('details')).not.toHaveAttribute('open');
    await expect(suggestions.first()).not.toContainText('github.com');
    await expect(suggestions.first().locator('a.recommendation-evidence')).toHaveAttribute('href', /github\.com\/TaxCollector23\/OwnBot\/actions\/runs\/40738088838/);
    await suggestions.first().locator('button').click();
    await expect(page.locator('#recommendation-command')).toContainText('ollama pull qwen3:1.7b');
    const origin = new URL(page.url()).origin;
    if (origin === 'http://127.0.0.1:8765' || origin === 'http://localhost:8765') {
      await expect(page.locator('#recommendation-command')).toContainText('ownbot setup');
    } else {
      await expect(page.locator('#recommendation-command')).toContainText(`ownbot setup --url ${origin} --model qwen3:1.7b`);
    }
  } else {
    await expect(recommendationStatus).toContainText('recommendation unavailable');
  }
  await page.locator('#setup-toggle').click();
  await expect(page.locator('#setup-dialog')).toHaveJSProperty('open', true);
  await expect(page.locator('#setup-dialog')).toContainText('ownbot setup');
  await expect(page.locator('#setup-dialog')).toContainText('ownbot connect');
  await expect(page.locator('#setup-dialog')).toContainText('ownbot inspect');
  await expect(page.locator('#setup-dialog')).toContainText('ownbot daemon start');
  await page.locator('#setup-close').click();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport.width);
  if (testInfo.project.use.viewport.width <= 760) {
    await page.locator('#history-toggle').click();
    await expect(page.locator('#history-toggle')).toHaveAttribute('aria-expanded', 'true');
    await page.locator('#history-close').click();
    await expect(page.locator('#history-toggle')).toHaveAttribute('aria-expanded', 'false');
  }
});

test('pairing fragment opens the name form and removes the secret from browser history', async ({ownerPage, daemon}) => {
  const page = ownerPage;
  let pairExchanges = 0;
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/devices/pair' && request.method() === 'POST') pairExchanges += 1;
  });
  await page.withExpectedNavigation(() => page.goto('about:blank'));
  await page.goto(`${daemon.baseURL}/dashboard#pair=browser-fragment-fixture`);
  await expect(page.locator('#pair-panel')).toBeVisible();
  await expect(page.locator('#pair-device-name')).toBeFocused();
  await expect(page).toHaveURL(`${daemon.baseURL}/dashboard`);
  expect(page.url()).not.toContain('browser-fragment-fixture');
  expect(pairExchanges, 'the browser waits for a device name before exchanging a pairing secret').toBe(0);
});

test('real task runs preserve prompt versions, show results, and appear in history', async ({ownerPage, daemon}, testInfo) => {
  const page = ownerPage;
  const {agentID, modelID} = await setUpModelAgent(page, daemon);
  await expect(page.locator('#task-agent')).toBeHidden();
  await savePrompt(daemon, agentID, 'Use the available tools carefully.', 1);
  await savePrompt(daemon, agentID, 'Give a short, direct answer.', 2);
  const rollback = await ownerAPI(daemon, `/api/agents/${encodeURIComponent(agentID)}/prompts/rollback`, {method:'POST', body:{version:1}});
  expect(rollback).toMatchObject({version:3, source_version:1});
  const prompts = await ownerAPI(daemon, `/api/agents/${encodeURIComponent(agentID)}/prompts`);
  expect(prompts[0]).toMatchObject({version:3, prompt:'Use the available tools carefully.'});

  await ownerAPI(daemon, '/api/agents', {method:'POST', body:{name:'Second browser agent', model:modelID, enabled:true, max_steps:8, max_tokens:4096, timeout_seconds:120}});
  await page.locator('#refresh-button').click();
  await expect(page.locator('#task-agent')).toBeVisible();
  await page.locator('#task-agent').selectOption(agentID);

  await runTask(page, 'Answer this with one short sentence.');
  await expect(page.locator('#page-message')).toContainText('finished with status completed', {timeout:30_000});
  await expect(page.locator('#run-detail')).toContainText('Fixture run result.');
  await expect(page.locator('#run-detail')).toContainText('Prompt version 3');
  if (testInfo.project.use.viewport.width <= 760) {
    await page.locator('#history-toggle').click();
    await expect(page.locator('#history-toggle')).toHaveAttribute('aria-expanded', 'true');
  }
  const historyItem = page.locator('#runs-list [data-run-id]').first();
  await expect(historyItem).toBeVisible();
  await historyItem.click();
  await expect(page.locator('#run-detail')).toContainText('Fixture run result.');
  await expect(page.locator('#run-detail')).toContainText('Prompt version 3');
  expect(agentID).toMatch(/^[a-zA-Z0-9_-]+$/);
});

test('real LAN pairing link, one-time exchange, replay rejection, and revocation', async ({ownerPage, daemon}) => {
  test.skip(process.platform === 'darwin', 'macOS routes connections to its own private address through lo0; pinned-TLS LAN pairing is exercised by Linux CI.');
  const pairing = await ownerAPI(daemon, '/api/devices/pairing', {method:'POST', body:{}});
  const pairingURL = new URL(pairing.pairing_url);
  const secret = new URLSearchParams(pairingURL.hash.slice(1)).get('pair');
  expect(pairingURL.protocol).toBe('https:');
  expect(secret).toMatch(/^[A-Za-z0-9_-]{32,}$/);

  const paired = await pairThroughPinnedTLS(daemon, secret, 'Browser fixture phone');
  expect(paired.device.name).toBe('Browser fixture phone');
  const replay = await pairThroughPinnedTLS(daemon, secret, 'Replay fixture phone', 401);
  expect(replay.status).toBe(401);
  const devices = await ownerAPI(daemon, '/api/devices');
  const device = devices.find((item) => item.name === 'Browser fixture phone');
  expect(device?.id).toBeTruthy();
  await ownerAPI(daemon, `/api/devices/${encodeURIComponent(device.id)}`, {method:'DELETE'});
  const afterRevoke = await ownerAPI(daemon, '/api/devices');
  expect(afterRevoke.find((item) => item.id === device.id)?.revoked_at).toBeTruthy();
});

test('real MCP write remains pending until approval; approve dispatches once and denial never dispatches', async ({ownerPage, daemon}, testInfo) => {
  const page = ownerPage;
  const {agentID} = await setUpModelAgent(page, daemon);
  await savePrompt(daemon, agentID, 'Only create a note when the task asks for it.', 1);
  const server = await createMCPConnection(daemon);
  await configureWriteTool(daemon, agentID, server.id);
  await page.locator('#refresh-button').click();

  await runTask(page, 'approval-check: create a note');
  await expect(page.locator('#approvals-panel')).toBeVisible({timeout:30_000});
  await screenshot(page, testInfo, `approval-${testInfo.project.name}.png`);
  await expect(page.locator('#approvals-list')).toContainText('create_note');
  await expect(page.locator('.item-tag:visible, .count-label:visible, .state-pill:visible')).toHaveCount(0);
  const pendingStats = await fixtureStats();
  expect(pendingStats.mcpWrites, 'write provider must not receive a request while approval is pending').toBe(0);

  await page.locator('#approvals-list').getByRole('button', {name:'Approve and resume'}).click();
  await expect(page.locator('#page-message')).toContainText('Approval recorded. Run completed.', {timeout:30_000});
  await expect.poll(async () => (await fixtureStats()).mcpWrites).toBe(1);
  expect((await fixtureStats()).mcpCalls).toHaveLength(1);

  await runTask(page, 'denial-check: create a note');
  await expect(page.locator('#approvals-panel')).toBeVisible({timeout:30_000});
  const denied = await fixtureStats();
  expect(denied.mcpWrites, 'second write must remain undispatched while awaiting a decision').toBe(1);
  await page.locator('#approvals-list').getByRole('button', {name:'Deny'}).click();
  await expect(page.locator('#page-message')).toContainText('Denial recorded. The run was not resumed.');
  const finalStats = await fixtureStats();
  expect(finalStats.mcpWrites, 'denied run must never reach the external MCP provider').toBe(1);
  expect(finalStats.mcpCalls).toHaveLength(1);
});

test('expired owner session recovers through a new real one-time bootstrap link', async ({ownerPage, daemon}, testInfo) => {
  const page = ownerPage;
  const baseURL = daemon.baseURL;
  page.expectDaemonRestart(true);
  await stopProcess(daemon.child);
  await launch(daemon, testInfo);
  page.expectDaemonRestart(false);
  page.allowExpiredOwnerSession();
  await page.withExpectedNavigation(() => page.reload());
  await expect(page.locator('#token-panel')).toBeVisible();
  await expect(page.locator('#page-message')).toContainText('This browser no longer has access.');
  expect(await page.evaluate(() => sessionStorage.getItem('ownbot.api-token'))).toBeNull();

  const ticket = await freshBootstrap(daemon);
  await page.withExpectedNavigation(() => page.goto('about:blank'));
  await page.goto(`${baseURL}/dashboard#bootstrap=${encodeURIComponent(ticket)}`);
  await expect(page.locator('#token-panel')).toBeHidden();
  await expect(page.locator('#composer-empty')).toBeVisible();
  await expect(page.locator('#page-message')).toBeHidden();
  expect(await page.evaluate(() => sessionStorage.getItem('ownbot.api-token'))).toMatch(/^[A-Za-z0-9_-]{40,}$/);
});
