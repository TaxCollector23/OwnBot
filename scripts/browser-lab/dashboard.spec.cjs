const {request: httpsRequest} = require('node:https');
const {readFile} = require('node:fs/promises');
const path = require('node:path');
const {test, expect, freshBootstrap, launch, stopProcess} = require('./daemon-fixture.cjs');

const screenshot = async (page, testInfo, name) => page.screenshot({
  path:testInfo.outputPath(name),
  fullPage:true,
  animations:'disabled',
  mask:[page.locator('.access-url'), page.locator('#pairing-link'), page.locator('#pairing-link-meta'), page.locator('#pairing-fingerprint-meta')],
  maskColor:'#6e6e68',
});

async function setUpModelAgent(page) {
  const settings = page.locator('#settings-toggle');
  if (!(await page.locator('#settings-drawer').evaluate((node) => node.open))) await settings.click();
  await page.locator('#add-model-empty').click();
  await page.locator('#model-id').fill('browser-fixture-model');
  // Exercise real daemon validation and the visible error path before the valid save.
  await page.locator('#model-base-url').evaluate((input) => { input.value = 'http://example.com/v1'; });
  const rejectedModelResponse = page.waitForResponse((response) => {
    const request = response.request();
    return new URL(response.url()).pathname === '/api/models' && request.method() === 'POST';
  });
  await page.getByRole('button', {name:'Save model'}).click();
  const rejected = await rejectedModelResponse;
  let rejection = `status=${rejected.status()}`;
  try { rejection += ` error=${(await rejected.json()).error || 'unknown'}`; } catch {}
  expect(rejected.status(), `check=real_model_configuration_rejection ${rejection}`).toBe(400);
  await expect(page.locator('#page-message')).toContainText('Could not save model configuration: HTTP model endpoints must use localhost or a loopback IP');
  await expect(page.locator('#models-list')).toContainText('No local models configured');
  await page.locator('#model-base-url').evaluate((input) => { input.value = 'http://127.0.0.1:11434/v1'; });

  const createModelResponse = page.waitForResponse((response) => {
    const request = response.request();
    return new URL(response.url()).pathname === '/api/models' && request.method() === 'POST';
  });
  await page.getByRole('button', {name:'Save model'}).click();
  const modelResponse = await createModelResponse;
  if (!modelResponse.ok()) {
    let reason = `status=${modelResponse.status()}`;
    try { reason += ` error=${(await modelResponse.json()).error || 'unknown'}`; } catch {}
    throw new Error(`check=real_model_configuration ${reason}`);
  }
  await expect(page.locator('#models-list')).toContainText('browser-fixture-model');
  const modelCard = page.locator('[data-model-id]').first();
  const modelID = await modelCard.getAttribute('data-model-id');
  await modelCard.locator('[data-model-probe]').click();
  await expect(modelCard.locator('.probe-result')).toContainText('Observed score: 100/100', {timeout:30_000});

  await page.locator('#new-agent-button').click();
  await page.locator('#agent-name').fill('Browser fixture agent');
  await page.locator('#agent-model').fill(modelID);
  await page.getByRole('button', {name:'Create disabled agent'}).click();
  const agent = page.locator('#agents-list [data-agent-id]').first();
  await expect(agent).toContainText('Browser fixture agent');
  await agent.locator('[data-agent-enabled]').click();
  await expect(page.locator('#task-form')).toBeVisible();
  const agentID = await agent.getAttribute('data-agent-id');
  return {agent, agentID, modelID};
}

async function savePrompt(page, agent, prompt) {
  await agent.locator('[data-prompt-edit]').click();
  const editor = agent.locator('.prompt-form textarea');
  await expect(editor).toBeVisible();
  await editor.fill(prompt);
  await agent.locator('.prompt-form button[type="submit"]').click();
  await expect(page.locator('#agents-list')).toContainText('Prompt version');
}

async function createMCPConnection(page) {
  await page.locator('#new-mcp-button').click();
  await page.locator('#mcp-name').fill('Browser fixture notes');
  await page.locator('#mcp-endpoint').fill('http://127.0.0.1:11435/mcp');
  await page.locator('#mcp-allowed-tools').fill('create_note');
  await page.locator('#mcp-read-tools').fill('');
  await page.locator('#mcp-write-tools').fill('create_note');
  await page.getByRole('button', {name:'Save disabled config'}).click();
  const server = page.locator('#mcp-list .mcp-card').first();
  await expect(server).toContainText('Browser fixture notes');
  await server.locator('[data-mcp-action="connect"]').click();
  await expect(server).toContainText('Connected in this daemon session', {timeout:15_000});
  await expect(server).toContainText('create_note');
  return server;
}

async function configureWriteTool(page, agent) {
  await agent.locator('[data-agent-tools-edit]').click();
  const form = agent.locator('.agent-capability-form');
  await expect(form).toBeVisible();
  await form.locator('input[name="mcp_enabled"]').check();
  await form.locator('input[name="tool"]').first().check();
  await form.getByRole('button', {name:'Save capabilities'}).click();
  await expect(agent).toContainText('MCP: on');
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

async function pairThroughPinnedTLS(runtime, secret, name) {
  const cert = await readFile(path.join(runtime.stateDir, 'lan.cert.pem'));
  const address = `${runtime.lanIP}:${runtime.lanPort}`;
  const body = JSON.stringify({secret, name});
  const response = await new Promise((resolve, reject) => {
    const request = httpsRequest({
      host:runtime.lanIP,
      port:runtime.lanPort,
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
  if (response.status !== 201) throw new Error(`check=pinned_lan_pairing status=${response.status}`);
  const result = JSON.parse(response.body);
  if (!result.device?.id || !result.token) throw new Error('check=paired_device_response');
  return result;
}

test('real owner bootstrap, empty workspace, screenshots, and responsive controls', async ({ownerPage}, testInfo) => {
  const page = ownerPage;
  await screenshot(page, testInfo, `home-${testInfo.project.name}.png`);
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.locator('#composer-empty')).toContainText('Add a local model before creating an agent run.');
  await expect(page.locator('#token-panel')).toBeHidden();
  await expect(page.locator('#token-form')).toBeHidden();
  await expect(page.locator('#api-token')).toBeHidden();
  await expect(page.locator('#agents-list')).toContainText('No agents yet');
  await expect(page.locator('#models-list')).toContainText('No local models configured');
  await expect(page.locator('.eyebrow:visible')).toHaveCount(0);
  await expect(page.locator('.live-dot:visible')).toHaveCount(0);
  await expect(page.getByText(/localhost responding|local workspace/i)).toHaveCount(0);
  await expect(page.locator('.status-facts:visible')).toHaveCount(0);
  await expect(page.locator('.item-tag:visible, .count-label:visible, .state-pill:visible')).toHaveCount(0);
  await expect(page.locator('#task-heading')).toHaveText('What should your agent do?');
  await expect(page.locator('#task-form')).toBeHidden();
  await page.keyboard.press('Tab');
  await expect(page.locator('.skip-link')).toBeFocused();
  await page.evaluate(() => document.activeElement.blur());
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport.width);
  await page.locator('#settings-toggle').click();
  await expect(page.locator('#settings-toggle')).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('#settings-drawer')).toHaveJSProperty('open', true);
  await expect(page.getByRole('heading', {name:'Local models'})).toBeVisible();
  await expect(page.getByRole('heading', {name:'Agents'})).toBeVisible();
  await screenshot(page, testInfo, `settings-${testInfo.project.name}.png`);
  await expect(page.locator('.eyebrow:visible')).toHaveCount(0);
  await expect(page.locator('.live-dot:visible')).toHaveCount(0);
  await expect(page.getByText(/localhost responding|local workspace/i)).toHaveCount(0);
  await expect(page.locator('.status-facts:visible')).toHaveCount(0);
  await expect(page.locator('.item-tag:visible, .count-label:visible, .state-pill:visible')).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport.width);
  if (testInfo.project.name !== 'desktop-1440') {
    const smallControls = await page.locator('button:not([hidden]), input:not([type="hidden"]):not([type="checkbox"]):not([type="radio"]), select, textarea, summary').evaluateAll((items) => items.flatMap((item) => {
      const rect = item.getBoundingClientRect();
      const visible = rect.width > 0 && rect.height > 0 && getComputedStyle(item).visibility !== 'hidden';
      return visible && rect.height < 44 ? [{text:(item.innerText || item.getAttribute('aria-label') || item.type || item.tagName).trim(), height:Math.round(rect.height)}] : [];
    }));
    expect(smallControls, 'visible phone controls must have a 44px touch target').toEqual([]);
  }

  const modal = page.locator('dialog[open], [role="dialog"][aria-modal="true"]');
  if (await modal.count()) {
    await page.keyboard.press('Escape');
    await expect(modal).toBeHidden();
  }
});

test('real daemon model probe, agent setup, prompt editing and rollback, capability toggle, run result and history', async ({ownerPage}, testInfo) => {
  const page = ownerPage;
  const {agent, agentID} = await setUpModelAgent(page);
  await savePrompt(page, agent, 'Use the available tools carefully.');
  await savePrompt(page, agent, 'Give a short, direct answer.');

  await agent.locator('[data-prompt-edit]').click();
  const restore = agent.locator('[data-prompt-rollback][data-version="1"]');
  await expect(restore).toBeVisible();
  let confirmDialog;
  page.once('dialog', (dialog) => { confirmDialog = dialog; });
  await restore.click();
  await expect.poll(() => confirmDialog?.type()).toBe('confirm');
  await page.keyboard.press('Escape');
  await expect(page.locator('#page-message')).not.toContainText('restored from version 1');
  await expect(agent.locator('[data-prompt-rollback][data-version="1"]')).toBeVisible();
  await expect(agent.locator('.prompt-form')).toBeVisible();
  const restoreAgain = agent.locator('[data-prompt-rollback][data-version="1"]');
  page.once('dialog', (dialog) => dialog.accept());
  await restoreAgain.click();
  await expect(page.locator('#page-message')).toContainText('restored from version 1');

  await agent.locator('[data-agent-tools-edit]').click();
  const capability = agent.locator('.agent-capability-form');
  await capability.locator('input[name="web_search"]').check();
  await capability.getByRole('button', {name:'Save capabilities'}).click();
  await expect(agent).toContainText('Web search: on');

  await runTask(page, 'Answer this with one short sentence.');
  await expect(page.locator('#page-message')).toContainText('finished with status succeeded', {timeout:30_000});
  await expect(page.locator('#run-detail')).toContainText('Fixture run result.');
  await page.locator('#history-toggle').click();
  await expect(page.locator('#history-toggle')).toHaveAttribute('aria-expanded', 'true');
  const historyItem = page.locator('#runs-list [data-run-detail]').first();
  await expect(historyItem).toBeVisible();
  await historyItem.click();
  await expect(page.locator('#run-detail')).toContainText('Fixture run result.');
  expect(agentID).toMatch(/^[a-zA-Z0-9_-]+$/);
});

test('real LAN pairing enabled, create link, pair with pinned test TLS, and revoke from owner UI', async ({ownerPage, daemon}) => {
  const page = ownerPage;
  await page.locator('#settings-toggle').click();
  await expect(page.locator('#create-pairing-button')).toBeEnabled();
  await expect(page.locator('#access-state')).toHaveText('Enabled');
  await page.locator('#create-pairing-button').click();
  const link = page.locator('#pairing-link');
  await expect(link).toBeVisible();
  const pairingURL = new URL(await link.inputValue());
  const secret = new URLSearchParams(pairingURL.hash.slice(1)).get('pair');
  expect(pairingURL.protocol).toBe('https:');
  expect(secret).toMatch(/^[A-Za-z0-9_-]{32,}$/);

  const paired = await pairThroughPinnedTLS(daemon, secret, 'Browser fixture phone');
  expect(paired.device.name).toBe('Browser fixture phone');
  await page.locator('#refresh-devices-button').click();
  const device = page.locator('#devices-list .device-row').filter({hasText:'Browser fixture phone'});
  await expect(device).toBeVisible();
  await device.getByRole('button', {name:'Revoke access'}).click();
  await expect(page.locator('#page-message')).toContainText('Device access revoked.');
  await expect(device).toContainText('Revoked');
  await expect(device.getByRole('button', {name:'Revoke access'})).toHaveCount(0);
});

test('real MCP write remains pending until approval; approve dispatches once and denial never dispatches', async ({ownerPage}, testInfo) => {
  const page = ownerPage;
  const {agent} = await setUpModelAgent(page);
  await savePrompt(page, agent, 'Only create a note when the task asks for it.');
  await createMCPConnection(page);
  await configureWriteTool(page, agent);

  await runTask(page, 'approval-check: create a note');
  await expect(page.locator('#approvals-panel')).toBeVisible({timeout:30_000});
  await screenshot(page, testInfo, `approval-${testInfo.project.name}.png`);
  await expect(page.locator('#approvals-list')).toContainText('create_note');
  await expect(page.locator('.item-tag:visible, .count-label:visible, .state-pill:visible')).toHaveCount(0);
  const pendingStats = await fixtureStats();
  expect(pendingStats.mcpWrites, 'write provider must not receive a request while approval is pending').toBe(0);

  await page.locator('#approvals-list').getByRole('button', {name:'Approve and resume'}).click();
  await expect(page.locator('#page-message')).toContainText('Run succeeded', {timeout:30_000});
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
  await stopProcess(daemon.child);
  await launch(daemon, testInfo);
  await page.locator('#history-toggle').click();
  await page.locator('#refresh-button').click();
  await expect(page.locator('#token-panel')).toBeVisible();
  await expect(page.locator('#page-message')).toContainText('owner session expired or was rejected');
  expect(await page.evaluate(() => sessionStorage.getItem('ownbot.api-token'))).toBeNull();

  const ticket = await freshBootstrap(daemon);
  await page.goto(`${baseURL}/dashboard#bootstrap=${encodeURIComponent(ticket)}`);
  await expect(page.locator('#token-panel')).toBeHidden();
  await expect(page.locator('#composer-empty')).toBeVisible();
  await expect(page.locator('#page-message')).toBeHidden();
  expect(await page.evaluate(() => sessionStorage.getItem('ownbot.api-token'))).toMatch(/^[A-Za-z0-9_-]{40,}$/);
});
