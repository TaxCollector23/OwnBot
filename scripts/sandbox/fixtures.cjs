'use strict';

// Deterministic loopback-only stand-ins for a model provider and MCP server.
// No model weights, credentials, external services, or npm packages are used.
const http = require('node:http');

const MODEL_ID = 'ownbot-sandbox-fake-v1';
const HOST = '127.0.0.1';

function send(res, status, value) {
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store',
  });
  res.end(value === undefined ? '' : JSON.stringify(value));
}

async function readJSON(req) {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  if (!chunks.length) return {};
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

async function listen(handler, port = 0) {
  const server = http.createServer((req, res) => {
    Promise.resolve(handler(req, res)).catch(() => {
      if (!res.headersSent) send(res, 400, { error: 'invalid fixture request' });
      else res.destroy();
    });
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, HOST, resolve);
  });
  const address = server.address();
  return { server, host: HOST, port: address.port, url: `http://${HOST}:${address.port}` };
}

function completion(payload, message) {
  const hasCalls = Array.isArray(message.tool_calls);
  return {
    id: 'ownbot-sandbox-fake-response',
    model: payload.model || MODEL_ID,
    choices: [{ index: 0, message: { role: 'assistant', ...message }, finish_reason: hasCalls ? 'tool_calls' : 'stop' }],
    usage: { prompt_tokens: 8, completion_tokens: 4, total_tokens: 12 },
  };
}

function makeModelHandler(stats) {
  return async (req, res) => {
    const route = new URL(req.url, 'http://127.0.0.1').pathname;
    if (req.method === 'GET' && route === '/__fixture/health') return send(res, 200, { ok: true, kind: 'simulated-model', model: MODEL_ID });
    if (req.method === 'GET' && route === '/__fixture/stats') return send(res, 200, stats);
    if (req.method === 'POST' && route === '/__fixture/reset') {
      Object.assign(stats, { requests: 0, probeRequests: 0, taskRequests: 0, completedToolRequests: 0 });
      return send(res, 200, { ok: true });
    }
    if (req.method !== 'POST' || route !== '/v1/chat/completions') return send(res, 404, { error: 'not found' });
    const payload = await readJSON(req);
    stats.requests += 1;
    const messages = Array.isArray(payload.messages) ? payload.messages : [];
    const prompt = messages.map((message) => String(message.content || '')).join('\n');
    let answer;
    if (prompt.includes('Return only JSON')) {
      stats.probeRequests += 1;
      answer = { content: '{"ok":true}' };
    } else if (prompt.includes('Call probe_one')) {
      stats.probeRequests += 1;
      answer = { tool_calls: [{ id: 'sandbox-probe-one', type: 'function', function: { name: 'probe_one', arguments: '{"value":"alpha"}' } }] };
    } else if (prompt.includes('Call probe_two')) {
      stats.probeRequests += 1;
      answer = { tool_calls: [{ id: 'sandbox-probe-two', type: 'function', function: { name: 'probe_two', arguments: '{"value":"beta"}' } }] };
    } else if (messages.some((message) => message.role === 'tool')) {
      stats.completedToolRequests += 1;
      answer = { content: 'Sandbox task complete. The approved sample note was created by the local MCP fixture.' };
    } else if (/\b(create\s+(?:a\s+)?note|sandbox-note|approval-check|denial-check)\b/i.test(prompt) && Array.isArray(payload.tools)) {
      const tool = payload.tools.find((item) => item?.function?.name === 'create_note')?.function;
      if (tool) {
        stats.taskRequests += 1;
        answer = { tool_calls: [{
          id: `sandbox-note-${stats.taskRequests}`,
          type: 'function',
          function: { name: 'create_note', arguments: JSON.stringify({ title: 'Testing OwnBot locally' }) },
        }] };
      }
    }
    return send(res, 200, completion(payload, answer || { content: 'This is a deterministic sandbox response; no model inference was run.' }));
  };
}

function makeMcpHandler(stats) {
  return async (req, res) => {
    const route = new URL(req.url, 'http://127.0.0.1').pathname;
    if (req.method === 'GET' && route === '/__fixture/health') return send(res, 200, { ok: true, kind: 'simulated-mcp' });
    if (req.method === 'GET' && route === '/__fixture/stats') return send(res, 200, {
      calls: stats.calls.slice(),
      writes: stats.calls.length,
    });
    if (req.method === 'POST' && route === '/__fixture/reset') {
      stats.calls.length = 0;
      return send(res, 200, { ok: true });
    }
    if (req.method !== 'POST') return send(res, 404, { error: 'not found' });
    const payload = await readJSON(req);
    if (payload.method === 'notifications/initialized') {
      res.writeHead(202, { 'Cache-Control': 'no-store' }).end();
      return;
    }
    let result;
    if (payload.method === 'initialize') {
      result = { protocolVersion: '2025-11-25', capabilities: { tools: {} }, serverInfo: { name: 'ownbot sandbox fixture', version: '1' } };
    } else if (payload.method === 'tools/list') {
      result = { tools: [{
        name: 'create_note',
        description: 'Create a sample note in the isolated sandbox after approval.',
        inputSchema: { type: 'object', properties: { title: { type: 'string' } }, required: ['title'], additionalProperties: false },
      }] };
    } else if (payload.method === 'tools/call') {
      const name = payload.params?.name || '';
      if (name !== 'create_note') return send(res, 404, { error: 'unknown fixture tool' });
      const title = String(payload.params?.arguments?.title || 'Untitled sandbox note');
      stats.calls.push({ name, title });
      result = { content: [{ type: 'text', text: `Fixture created the sample note: ${title}` }], isError: false };
    } else {
      return send(res, 404, { error: 'unknown fixture method' });
    }
    return send(res, 200, { jsonrpc: '2.0', id: payload.id, result });
  };
}

async function closeServer(server) {
  if (!server?.listening) return;
  await new Promise((resolve) => server.close(resolve));
}

async function startFixtures({ modelPort = 0, mcpPort = 0 } = {}) {
  const modelStats = { requests: 0, probeRequests: 0, taskRequests: 0, completedToolRequests: 0 };
  const mcpStats = { calls: [] };
  let model;
  let mcp;
  try {
    model = await listen(makeModelHandler(modelStats), modelPort);
    mcp = await listen(makeMcpHandler(mcpStats), mcpPort);
  } catch (error) {
    await closeServer(model?.server);
    await closeServer(mcp?.server);
    throw error;
  }
  return {
    model: { ...model, model: MODEL_ID, baseURL: `${model.url}/v1`, statsURL: `${model.url}/__fixture/stats`, healthURL: `${model.url}/__fixture/health` },
    mcp: { ...mcp, endpoint: `${mcp.url}/mcp`, statsURL: `${mcp.url}/__fixture/stats`, healthURL: `${mcp.url}/__fixture/health` },
    close: async () => Promise.all([closeServer(model.server), closeServer(mcp.server)]),
  };
}

module.exports = { MODEL_ID, startFixtures };
