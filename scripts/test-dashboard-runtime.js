#!/usr/bin/env node
// Dependency-free regression checks for MCP connections, agent capabilities, and runs.
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const vm = require('node:vm');

const source = readFileSync(resolve(__dirname, '../web/dashboard.js'), 'utf8');

function harness(connectRequiresAuth = false, rejectSearch = false) {
  const elements = new Map();
  const listeners = new Map();
  const requests = [];
  let agents = [{id:'a1', name:'Assistant', model:'m1', enabled:false, prompt_version:1, prompt:'Be safe.', tools:[], max_steps:8, max_tokens:4096, timeout_seconds:120}];
  let mcpServers = [{id:'s1', name:'Notes', transport:'streamable_http', endpoint:'https://notes.example/mcp', enabled:false, runtime_available:false, allowed_tools:['Lookup'], read_tools:['Lookup'], write_tools:[]}];
  let mcpConnected = false;
  let searchEndpoint = '';
  const tools = [{name:'Lookup', description:'Find notes', risk:'read', source:'mcp', server:'s1'}];
  const approvals = [{id:'ap1', run_id:'r1', agent_id:'a1', tool:'calendar.write', status:'pending', reason:'Create event'}];
  function element(selector) {
    if (!elements.has(selector)) {
      const handlers = new Map();
      elements.set(selector, {
        value:'', textContent:'', innerHTML:'', hidden:false, className:'', disabled:false, dataset:{}, handlers,
        classList:{toggle(){}}, addEventListener(type, handler){handlers.set(type, handler);}, focus(){}, reset(){this.value='';},
      });
    }
    return elements.get(selector);
  }
  const document = {
    querySelector: element,
    querySelectorAll: () => [],
    addEventListener(type, handler) { listeners.set(type, handler); },
  };
  element('#token-panel').hidden = true;
  const sessionStorage = {getItem: ()=>'token', setItem(){}, removeItem(){}};
  async function fetch(path, options = {}) {
    const method = options.method || 'GET';
    requests.push({path, method, body:options.body});
    let status = 200;
    let data = [];
    if (path === '/api/status') data = {ok:true, api_token_required:true};
    else if (path === '/api/search' && method === 'GET') data = {provider:'searxng', endpoint:searchEndpoint, configured:Boolean(searchEndpoint), requests_leave_device:true};
    else if (path === '/api/search' && method === 'POST' && rejectSearch) { status = 400; data = {error:'search endpoint must use HTTPS or loopback HTTP without credentials'}; }
    else if (path === '/api/search' && method === 'POST') { searchEndpoint = JSON.parse(options.body).endpoint; data = {provider:'searxng', endpoint:searchEndpoint, configured:true, requests_leave_device:true}; }
    else if (path === '/api/agents' && method === 'GET') data = agents;
    else if (path === '/api/mcp/servers' && method === 'GET') data = mcpServers;
    else if (path === '/api/tools' && method === 'GET') data = [...(mcpConnected ? tools : []), ...(searchEndpoint ? [{name:'web_search', description:'Search the configured SearXNG instance', risk:'read', source:'web_search', server:''}] : [])];
    else if (path === '/api/mcp/servers/s1/connect' && method === 'POST' && connectRequiresAuth) { data = {code:'auth_required', error:'provider sign-in required'}; }
    else if (path === '/api/mcp/servers/s1/connect' && method === 'POST') { mcpConnected = true; mcpServers = [{...mcpServers[0], enabled:true, runtime_available:true}]; data = mcpServers[0]; }
    else if (path === '/api/agents/a1' && method === 'PATCH') { agents = [{...agents[0], ...JSON.parse(options.body)}]; data = agents[0]; }
    else if (path === '/api/agents/a1/runs' && method === 'POST') data = {id:'r2', agent_id:'a1', status:'succeeded', prompt_version:1, output:'Done'};
    else if (path === '/api/runs') data = [{id:'r1', agent_id:'a1', status:'awaiting_approval', prompt_version:1}];
    else if (path === '/api/runs/r1' || path === '/api/runs/r2') data = {id:path.endsWith('r1') ? 'r1' : 'r2', status:'succeeded', prompt_version:1, steps:[], output:'Done'};
    else if (path === '/api/approvals') data = approvals;
    else if (path === '/api/approvals/ap1/approve' && method === 'POST') data = {id:'ap1', status:'approved'};
    else if (path === '/api/runs/r1/resume' && method === 'POST') data = {id:'r1', status:'succeeded', prompt_version:1, output:'Continued'};
    if (connectRequiresAuth && path === '/api/mcp/servers/s1/connect' && method === 'POST') status = 409;
    const body = JSON.stringify(data);
    return {ok:status >= 200 && status < 300, status, async text(){return body;}};
  }
  class FakeFormData { constructor(form){this.form=form;} get(name){return this.form.values?.[name] || '';} getAll(name){const value=this.form.values?.[name]; return Array.isArray(value) ? value : value ? [value] : [];} }
  vm.runInNewContext(source, {document, sessionStorage, fetch, Headers, FormData:FakeFormData, CSS:{escape:(v)=>v}, console});
  return {element, listeners, requests, get agents(){return agents;}};
}

async function main() {
  const h = harness();
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(h.element('#agents-list').innerHTML, /Disabled agents cannot run/);
  const connect = {dataset:{mcpId:'s1', mcpAction:'connect'}, disabled:false};
  await h.listeners.get('click')({target:{closest:(selector)=>selector === '[data-mcp-action]' ? connect : null}});
  assert.ok(h.requests.some(({path, method}) => path === '/api/mcp/servers/s1/connect' && method === 'POST'), 'connect must be an explicit action');
  assert.match(h.element('#mcp-list').innerHTML, /Lookup/);
  const enable = {dataset:{agentEnabled:'a1', enabled:'false'}, disabled:false};
  await h.listeners.get('click')({target:{closest:(selector)=>selector === '[data-agent-enabled]' ? enable : null}});
  assert.equal(h.agents[0].enabled, true, 'enable action persists enabled state');
  assert.match(h.element('#agents-list').innerHTML, /data-run-form="a1"/, 'enabled agent receives run form');

  const capabilityForm = {dataset:{agentToolsForm:'a1'}, values:{web_search:'on', mcp_enabled:'on', tool:['Lookup'], mcp_server:['s1']}};
  await h.listeners.get('submit')({target:{closest:(selector)=>selector === '.agent-capability-form' ? capabilityForm : null}, preventDefault(){}});
  const capabilityPatch = h.requests.find(({path, method, body}) => path === '/api/agents/a1' && method === 'PATCH' && JSON.parse(body).tools);
  assert.ok(capabilityPatch, 'agent capability configuration is saved');
  assert.deepEqual(JSON.parse(capabilityPatch.body).tools, ['Lookup']);
  assert.deepEqual(JSON.parse(capabilityPatch.body).mcp_servers, ['s1']);

  const form = {dataset:{runForm:'a1'}, values:{input:'hello'}, reset(){}, querySelector(){return {disabled:false};}};
  await h.listeners.get('submit')({target:{closest:(selector)=>selector === '.run-form' ? form : null}, preventDefault(){}});
  assert.ok(h.requests.some(({path, method, body}) => path === '/api/agents/a1/runs' && method === 'POST' && JSON.parse(body).input === 'hello'));

  const approval = {dataset:{id:'ap1', runId:'r1', decision:'approve'}, disabled:false};
  await h.listeners.get('click')({target:{closest:(selector)=>selector === '[data-decision]' ? approval : null}});
  const decisionIndex = h.requests.findIndex(({path}) => path === '/api/approvals/ap1/approve');
  const resumeIndex = h.requests.findIndex(({path}) => path === '/api/runs/r1/resume');
  assert.ok(decisionIndex >= 0 && resumeIndex > decisionIndex, 'linked approval is recorded before the paused run resumes');
  assert.match(h.element('#page-message').textContent, /Approval recorded\. Run succeeded/);

  const auth = harness(true);
  await new Promise((resolve) => setImmediate(resolve));
  const authConnect = {dataset:{mcpId:'s1', mcpAction:'connect'}, disabled:false};
  await auth.listeners.get('click')({target:{closest:(selector)=>selector === '[data-mcp-action]' ? authConnect : null}});
  assert.match(auth.element('#page-message').textContent, /sign-in required.*OAuth or sign-in/i);
  assert.equal(auth.element('#token-panel').hidden, true, 'provider auth requirement does not invalidate the local API token');

  const search = harness();
  await new Promise((resolve) => setImmediate(resolve));
  const searchForm = {values:{endpoint:'https://search.example/'}, querySelector(){return {disabled:false};}};
  await search.element('#search-form').handlers.get('submit')({currentTarget:searchForm, preventDefault(){}});
  assert.ok(search.requests.some(({path, method, body}) => path === '/api/search' && method === 'POST' && JSON.parse(body).endpoint === 'https://search.example/'));
  assert.match(search.element('#search-status').innerHTML, /Requests leave this device: Yes/);
  assert.match(search.element('#agents-list').innerHTML, /<code>web_search<\/code>/, 'configured search tool can be explicitly allowlisted on an agent');
  const rejectedSearch = harness(false, true);
  await new Promise((resolve) => setImmediate(resolve));
  await rejectedSearch.element('#search-form').handlers.get('submit')({currentTarget:searchForm, preventDefault(){}});
  assert.match(rejectedSearch.element('#page-message').textContent, /Could not configure SearXNG/i);
  assert.doesNotMatch(rejectedSearch.element('#page-message').textContent, /configured\. Search queries/i);
  process.stdout.write('dashboard runtime regression: PASS (search boundary, MCP connect and auth state, agent capabilities, runs and approval resume)\n');
}

main().catch((error) => {
  process.stderr.write(`dashboard runtime regression: FAIL: ${error.stack || error.message}\n`);
  process.exitCode = 1;
});
