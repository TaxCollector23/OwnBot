'use strict';

const assert = require('node:assert/strict');
const { MODEL_ID, startFixtures } = require('./fixtures.cjs');

async function requestJSON(url, method = 'GET', body) {
  const response = await fetch(url, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(3000),
  });
  assert.equal(response.ok, true, `${method} ${url} returned ${response.status}`);
  return response.status === 202 ? null : response.json();
}

async function modelCall(baseURL, messages, tools) {
  const response = await requestJSON(`${baseURL}/chat/completions`, 'POST', { model: MODEL_ID, messages, tools });
  assert.equal(response.model, MODEL_ID);
  return response.choices[0].message;
}

async function main() {
  const fixture = await startFixtures();
  try {
    assert.equal(fixture.model.host, '127.0.0.1');
    assert.equal(fixture.mcp.host, '127.0.0.1');
    assert.equal((await requestJSON(fixture.model.healthURL)).model, MODEL_ID);
    assert.equal((await requestJSON(fixture.mcp.healthURL)).kind, 'simulated-mcp');

    const jsonProbe = await modelCall(fixture.model.baseURL, [{ role: 'user', content: 'Return only JSON.' }]);
    assert.equal(jsonProbe.content, '{"ok":true}');
    const toolProbe = await modelCall(fixture.model.baseURL, [{ role: 'user', content: 'Call probe_one.' }]);
    assert.equal(toolProbe.tool_calls[0].function.name, 'probe_one');
    const choiceProbe = await modelCall(fixture.model.baseURL, [{ role: 'user', content: 'Call probe_two.' }]);
    assert.equal(choiceProbe.tool_calls[0].function.name, 'probe_two');

    const tools = [{ type: 'function', function: { name: 'create_note', parameters: { type: 'object' } } }];
    const toolRequest = await modelCall(fixture.model.baseURL, [{ role: 'user', content: 'Create a note about testing OwnBot locally.' }], tools);
    assert.equal(toolRequest.tool_calls[0].function.name, 'create_note');
    assert.deepEqual(JSON.parse(toolRequest.tool_calls[0].function.arguments), { title: 'Testing OwnBot locally' });
    const namespaced='mcp_ab12_create_note_cd34';
    const actualTool=await modelCall(fixture.model.baseURL,[{role:'user',content:'Create a note about testing OwnBot locally.'}],[{type:'function',function:{name:namespaced,parameters:{type:'object'}}}]);
    assert.equal(actualTool.tool_calls[0].function.name,namespaced);
    const summary = await modelCall(fixture.model.baseURL, [
      { role: 'user', content: 'Create a note about testing OwnBot locally.' },
      { role: 'tool', content: 'Fixture created the sample note: Testing OwnBot locally' },
    ], tools);
    assert.match(summary.content, /Sandbox task complete/);

    const initialize = await requestJSON(fixture.mcp.endpoint, 'POST', { jsonrpc: '2.0', id: 1, method: 'initialize', params: {} });
    assert.equal(initialize.result.serverInfo.name, 'ownbot sandbox fixture');
    await requestJSON(fixture.mcp.endpoint, 'POST', { jsonrpc: '2.0', method: 'notifications/initialized' });
    const list = await requestJSON(fixture.mcp.endpoint, 'POST', { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} });
    assert.equal(list.result.tools[0].name, 'create_note');
    const write = await requestJSON(fixture.mcp.endpoint, 'POST', {
      jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'create_note', arguments: { title: 'Testing OwnBot locally' } },
    });
    assert.match(write.result.content[0].text, /Testing OwnBot locally/);
    const stats = await requestJSON(fixture.mcp.statsURL);
    assert.equal(stats.writes, 1);
    assert.equal(stats.calls[0].title, 'Testing OwnBot locally');

    console.log('PASS: deterministic model probe, task tool call, MCP write, and completion summary');
  } finally {
    await fixture.close();
  }
}

main().catch((error) => {
  console.error(`FAIL: ${error?.message || 'sandbox fixture check failed'}`);
  process.exitCode = 1;
});
