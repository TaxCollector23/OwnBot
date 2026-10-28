const http = require('node:http');

const mode = process.argv[2];
const isModel = mode === 'model';
if (!isModel && mode !== 'mcp') throw new Error('usage: providers.cjs model|mcp');

const stats = {modelCalls: 0, mcpReads: 0, mcpWrites: 0, mcpCalls: [], failModel: false};
const send = (response, status, body) => {
  response.writeHead(status, {'Content-Type':'application/json; charset=utf-8', 'Cache-Control':'no-store'});
  response.end(body === undefined ? '' : JSON.stringify(body));
};

async function body(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : {};
}

function modelResponse(payload) {
  stats.modelCalls += 1;
  const messages = Array.isArray(payload.messages) ? payload.messages : [];
  const prompt = messages.map((message) => String(message.content || '')).join('\n');
  if (prompt.includes('Return only JSON')) return {content:'{"ok":true}'};
  if (prompt.includes('Call probe_one')) return {tool_calls:[{id:'probe-one', type:'function', function:{name:'probe_one', arguments:'{"value":"alpha"}'}}]};
  if (prompt.includes('Call probe_two')) return {tool_calls:[{id:'probe-two', type:'function', function:{name:'probe_two', arguments:'{"value":"beta"}'}}]};
  if (messages.some((message) => message.role === 'tool')) return {content:'Fixture task finished.'};
  if (/approval-check|denial-check/i.test(prompt) && Array.isArray(payload.tools) && payload.tools.length) {
    const tool = payload.tools[0]?.function;
    if (tool?.name) return {tool_calls:[{id:`mcp-call-${stats.modelCalls}`, type:'function', function:{name:tool.name, arguments:'{"title":"Browser fixture note"}'}}]};
  }
  return {content:'Fixture run result.'};
}

const server = http.createServer(async (request, response) => {
  const path = new URL(request.url, 'http://127.0.0.1').pathname;
  if (path === '/__fixture/health') return send(response, 200, {ok:true, mode});
  if (path === '/__fixture/reset' && request.method === 'POST') {
    Object.assign(stats, {modelCalls: 0, mcpReads: 0, mcpWrites: 0, mcpCalls: [], failModel:false});
    return send(response, 200, {ok:true});
  }
  if (isModel && path === '/__fixture/fail-model' && request.method === 'POST') {
    try { stats.failModel = Boolean((await body(request)).enabled); }
    catch { return send(response, 400, {error:'invalid JSON'}); }
    return send(response, 200, {failModel:stats.failModel});
  }
  if (path === '/__fixture/stats') return send(response, 200, stats);

  if (isModel && path === '/v1/chat/completions' && request.method === 'POST') {
    if (stats.failModel) return send(response, 503, {error:{message:'fixture model endpoint unavailable'}});
    let payload;
    try { payload = await body(request); }
    catch { return send(response, 400, {error:{message:'invalid JSON'}}); }
    const message = modelResponse(payload);
    return send(response, 200, {id:'ownbot-browser-fixture', model:'fixture-model', choices:[{index:0, message:{role:'assistant', ...message}, finish_reason:message.tool_calls ? 'tool_calls' : 'stop'}], usage:{prompt_tokens:8, completion_tokens:4, total_tokens:12}});
  }

  if (!isModel && request.method === 'POST') {
    let payload;
    try { payload = await body(request); }
    catch { return send(response, 400, {error:'invalid JSON'}); }
    const method = payload.method;
    const id = payload.id;
    if (method === 'notifications/initialized') {
      response.writeHead(202, {'Cache-Control':'no-store'});
      return response.end();
    }
    let result;
    if (method === 'initialize') {
      result = {protocolVersion:'2025-11-25', capabilities:{tools:{}}, serverInfo:{name:'OwnYourBot browser fixture', version:'1'}};
    } else if (method === 'tools/list') {
      result = {tools:[{name:'create_note', description:'Create a note after approval', inputSchema:{type:'object', properties:{title:{type:'string'}}, required:['title'], additionalProperties:false}}]};
    } else if (method === 'tools/call') {
      const toolName = payload.params?.name || '';
      stats.mcpCalls.push({name:toolName, arguments:payload.params?.arguments || {}});
      if (toolName === 'create_note') stats.mcpWrites += 1;
      else stats.mcpReads += 1;
      result = {content:[{type:'text', text:'Fixture note created.'}], isError:false};
    } else {
      return send(response, 404, {error:`unexpected MCP method: ${method}`});
    }
    return send(response, 200, {jsonrpc:'2.0', id, result});
  }
  return send(response, 404, {error:'not found'});
});

const port = isModel ? 11434 : 11435;
server.listen(port, '127.0.0.1');
