(() => {
  const TOKEN_KEY = 'ownbot.api-token';
  const PAIRED_KEY = 'ownbot.paired-device';
  const tokenPanel = document.querySelector('#token-panel');
  const tokenForm = document.querySelector('#token-form');
  const tokenInput = document.querySelector('#api-token');
  const pairPanel = document.querySelector('#pair-panel');
  const pairForm = document.querySelector('#pair-form');
  const pairNameInput = document.querySelector('#pair-device-name');
  const isLoopbackHost = typeof window !== 'undefined' && ['localhost', '127.0.0.1', '::1', '[::1]'].includes(window.location.hostname);
  let pairingSecret = '';
  if (typeof window !== 'undefined') {
    pairingSecret = new URLSearchParams((window.location.hash || '').replace(/^#/, '')).get('pair') || '';
    if (pairingSecret) window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}`);
  }
  const pageMessage = document.querySelector('#page-message');
  const connectionLabel = document.querySelector('#connection-label');
  let token = sessionStorage.getItem(TOKEN_KEY) || '';
  let pairedDevice = sessionStorage.getItem(PAIRED_KEY) === 'true';
  let protectedApiResponded = false;
  let accessRejected = false;
  let mcpTools = [];
  let mcpServers = [];
  let toolsUnavailable = false;
  document.querySelector('#forget-current-device').hidden = !pairedDevice || !token;

  const q = (selector, root = document) => root.querySelector(selector);
  const list = (value) => Array.isArray(value) ? value : Array.isArray(value?.items) ? value.items : Array.isArray(value?.data) ? value.data : [];
  const value = (...items) => items.find((item) => item !== undefined && item !== null && item !== '');
  const esc = (input) => String(input ?? '').replace(/[&<>"']/g, (char) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
  const pretty = (input) => {
    if (input === undefined || input === null || input === '') return 'Not configured';
    if (typeof input === 'object') return JSON.stringify(input);
    return String(input);
  };
  const date = (input) => {
    if (!input) return 'Time unavailable';
    const parsed = new Date(input);
    return Number.isNaN(parsed.getTime()) ? String(input) : parsed.toLocaleString([], {dateStyle:'medium', timeStyle:'short'});
  };
  const showMessage = (message, isError = true) => {
    pageMessage.textContent = message;
    pageMessage.hidden = !message;
    pageMessage.classList.toggle('message-info', !isError);
  };
  const showTokenPanel = (show) => {
    tokenPanel.hidden = !show;
    const ownerLocal = isLoopbackHost && !pairedDevice;
    q('#owner-token-copy').hidden = !ownerLocal;
    q('#paired-token-copy').hidden = !pairedDevice;
    q('#remote-token-copy').hidden = ownerLocal || pairedDevice;
    q('#forget-device-button').hidden = !pairedDevice;
    q('#forget-current-device').hidden = !pairedDevice || !token;
    tokenForm.hidden = pairedDevice || !ownerLocal;
    if (show && ownerLocal) tokenInput.focus();
  };
  showTokenPanel(false);
  const forgetDevice = () => {
    token = '';
    pairedDevice = false;
    sessionStorage.removeItem(TOKEN_KEY);
    sessionStorage.removeItem(PAIRED_KEY);
    tokenForm.hidden = false;
    showTokenPanel(false);
    showMessage('This browser has forgotten its paired access.', false);
    loadAll();
  };
  const api = async (path, options = {}) => {
    const remoteAuthExpected = options.remoteAuthExpected === true;
    const requestOptions = {...options};
    delete requestOptions.remoteAuthExpected;
    const headers = new Headers(options.headers || {});
    headers.set('Accept', 'application/json');
    if (options.body !== undefined) headers.set('Content-Type', 'application/json');
    if (token) headers.set('Authorization', `Bearer ${token}`);
    const response = await fetch(path, {...requestOptions, headers, cache:'no-store'});
    if (response.status === 401 || response.status === 403) {
      if (remoteAuthExpected) {
        const body = await response.text().catch(() => '');
        let reason = body;
        try { reason = JSON.parse(body).error || JSON.parse(body).message || body; } catch {}
        if (/auth[_ -]?required|oauth|sign.?in/i.test(reason)) throw new Error('auth_required: this MCP server requires OAuth or sign-in, which is not available yet.');
      }
      accessRejected = true;
      token = '';
      sessionStorage.removeItem(TOKEN_KEY);
      showTokenPanel(true);
      const rejection = pairedDevice
        ? 'This paired device was rejected. Pair it again from the host.'
        : isLoopbackHost
          ? 'Access token was rejected. Check the token from ~/.ownbot/api.token and try again.'
          : 'Device access was rejected. Ask the owner to create a new pairing link from the host.';
      showMessage(rejection);
      throw new Error(rejection);
    }
    if (!response.ok) {
      const body = await response.text().catch(() => '');
      let reason = body;
      try {
        const payload = JSON.parse(body);
        reason = payload.error || payload.message || body;
        if (remoteAuthExpected && payload.code === 'auth_required') reason = 'auth_required: this MCP server requires OAuth or sign-in, which is not available yet.';
      } catch {}
      throw new Error(reason || `Request failed (${response.status})`);
    }
    if (path !== '/api/status') protectedApiResponded = true;
    if (response.status === 204) return null;
    const body = await response.text();
    return body ? JSON.parse(body) : null;
  };

  const setLoading = (selector, message) => { q(selector).innerHTML = `<div class="empty-inline">${esc(message)}</div>`; };
  const sectionError = (selector, label, error) => { q(selector).innerHTML = `<div class="empty-inline">${esc(label)} could not be loaded. ${esc(error.message)}</div>`; };
  const stateTag = (input) => {
    const status = value(input?.status, input?.state, input?.enabled ? 'enabled' : 'disabled', 'unknown');
    return `<span class="item-tag">${esc(status)}</span>`;
  };
  const renderStatus = (data) => {
    const connected = data?.ok === true;
    q('#status-pill').textContent = connected ? 'Connected' : value(data?.status, 'Unavailable');
    q('#status-pill').className = `state-pill ${connected ? 'state-ok' : 'state-warn'}`;
    q('#status-facts').innerHTML = [
      ['Version', value(data?.version, 'Not reported')],
      ['Agents', value(data?.agents, 'Not reported')],
      ['Pending approvals', value(data?.pending_approvals, 'Not reported')],
      ['Queued jobs', value(data?.queued_jobs, 'Not reported')],
    ].map(([label, content]) => `<div><span class="fact-label">${esc(label)}</span><span class="fact-value">${esc(content)}</span></div>`).join('');
    connectionLabel.textContent = connected ? 'Local host responding' : 'Local host status';
    if (data?.api_token_required && !token) showTokenPanel(true);
  };
  const loadStatus = async () => {
    try { renderStatus(await api('/api/status')); }
    catch (error) {
      q('#status-pill').textContent = 'Unavailable';
      q('#status-pill').className = 'state-pill state-warn';
      q('#status-facts').innerHTML = `<div class="empty-inline">The local API did not respond: ${esc(error.message)}</div>`;
      connectionLabel.textContent = 'Local host not responding';
    }
  };
  const loadSearch = async () => {
    try {
      const config = await api('/api/search');
      const configured = Boolean(config?.configured && config?.endpoint);
      q('#search-state').textContent = configured ? 'Configured' : 'Not configured';
      q('#search-state').className = `item-tag ${configured ? 'search-configured' : ''}`;
      q('#search-status').innerHTML = configured
        ? `Endpoint: <code>${esc(config.endpoint)}</code><br>Requests leave this device: ${config.requests_leave_device === true ? 'Yes' : 'Not reported'}. Configuration is already set.`
        : 'No endpoint is configured. Search will remain unavailable until one is saved.';
      q('#search-form').hidden = configured;
    } catch (error) {
      q('#search-state').textContent = 'Unavailable';
      q('#search-form').hidden = true;
      q('#search-status').textContent = `Search configuration could not be loaded: ${error.message}`;
    }
  };
  const renderProbe = (probe, probedAt) => {
    if (!probe) return '<div class="item-meta">No capability probe has been run.</div>';
    const checks = [
      ['JSON output', probe.json_output],
      ['Single tool call', probe.single_tool],
      ['Tool choice', probe.tool_choice],
      ['Argument accuracy', probe.argument_accuracy],
    ];
    const restricted = Boolean(probe.restricted);
    return `<div class="probe-result">
      <div class="probe-score"><strong>Observed score: ${esc(value(probe.score, '—'))}/100</strong><span class="probe-restriction ${restricted ? '' : 'passed'}">${restricted ? 'Below threshold; restricted profile indicated' : 'At threshold; unrestricted profile indicated'}</span></div>
      <ul class="probe-checks">${checks.map(([label, passed]) => `<li class="${passed ? 'check-pass' : 'check-fail'}">${passed ? '✓' : '×'} ${esc(label)}</li>`).join('')}</ul>
      <div class="item-meta">${probedAt ? `Probed ${esc(date(probedAt))}. ` : ''}The runtime uses this result to decide whether an agent can run; it does not predict task success.</div>
    </div>`;
  };
  const loadModels = async () => {
    setLoading('#models-list', 'Loading model configurations…');
    try {
      const models = list(await api('/api/models'));
      if (!models.length) { setLoading('#models-list', 'No model configurations yet. Add an Ollama or LM Studio model on this machine.'); return; }
      q('#models-list').innerHTML = models.map((model) => `<article class="model-card" data-model-id="${esc(model.id)}">
        <div class="model-card-head"><div><div class="model-card-title">${esc(value(model.name, model.model, 'Model'))}</div><div class="model-card-meta">${esc(model.provider === 'lm_studio' ? 'LM Studio' : model.provider === 'ollama' ? 'Ollama' : value(model.provider, 'Provider'))} · ${esc(value(model.model, 'Model identifier unavailable'))}</div></div><span class="item-tag">${model.last_probe ? 'probed' : 'not probed'}</span></div>
        <code class="model-endpoint-value">${esc(model.base_url || 'Endpoint not reported')}</code>
        ${renderProbe(model.last_probe, model.probed_at)}
        <div class="row-actions"><button class="probe-action" type="button" data-model-probe="${esc(model.id)}">Run capability probe</button></div>
      </article>`).join('');
    } catch (error) { sectionError('#models-list', 'Model configurations', error); }
  };
  const metadataList = (items) => Array.isArray(items) && items.length ? items.join(', ') : 'None configured';
  const loadTools = async () => {
    try {
      mcpTools = list(await api('/api/tools'));
      toolsUnavailable = false;
    } catch {
      mcpTools = [];
      toolsUnavailable = true;
    }
  };
  const loadMCPServers = async () => {
    setLoading('#mcp-list', 'Loading saved MCP configuration…');
    try {
      const servers = list(await api('/api/mcp/servers'));
      mcpServers = servers;
      await loadTools();
      q('#mcp-count').textContent = `${servers.length} saved`;
      if (!servers.length) {
        setLoading('#mcp-list', 'No MCP server configurations are saved. Add an HTTP endpoint to connect and discover tools.');
        return;
      }
      q('#mcp-list').innerHTML = servers.map((server) => {
        const transport = value(server.transport, 'stdio');
        const address = transport === 'stdio'
          ? [server.command, ...(Array.isArray(server.args) ? server.args : [])].filter(Boolean).join(' ')
          : value(server.endpoint, 'Endpoint not configured');
        const connected = server.runtime_available === true || (server.runtime_available === undefined && Boolean(server.enabled));
        const connection = connected ? 'Connected in this daemon session' : transport === 'stdio' ? 'Unavailable: stdio is not enabled in this daemon' : 'Not connected';
        const configState = connected ? 'Connected' : 'Saved · disabled';
        const tools = server.allowed_tools;
        const actualTools = mcpTools.filter((tool) => String(tool.server) === String(server.id) || String(tool.server) === String(server.name));
        const connectControls = transport === 'streamable_http'
          ? `<button type="button" data-mcp-action="${connected ? 'disconnect' : 'connect'}" data-mcp-id="${esc(server.id)}">${connected ? 'Disconnect' : 'Connect'}</button>`
          : `<span class="item-meta">Stdio connections are unavailable in this daemon.</span>`;
        const toolMarkup = connected
          ? (toolsUnavailable ? '<div class="empty-inline">Tool discovery is unavailable from this daemon.</div>'
            : actualTools.length ? `<div class="mcp-discovered-tools"><strong>Discovered tools</strong>${actualTools.map((tool) => `<div class="mcp-tool-row"><span><code>${esc(tool.name)}</code> · ${esc(value(tool.risk, 'risk not reported'))}</span><span>${esc(value(tool.description, 'No description reported'))}</span></div>`).join('')}</div>`
              : '<div class="item-meta">Connected; no tools were reported for this server.</div>')
          : '<div class="item-meta">Connect explicitly to discover tools. No calls happen automatically.</div>';
        return `<article class="mcp-card">
          <div class="mcp-card-head"><div><div class="model-card-title">${esc(value(server.name, 'Unnamed server'))}</div><div class="model-card-meta">${esc(transport)} · ${esc(address || 'No command or endpoint saved')}</div></div><span class="item-tag">${esc(configState)}</span></div>
          <div class="mcp-status-row"><span>Connection</span><strong>${esc(connection)}</strong><span>Tools</span><strong>${connected ? actualTools.length : 'Not discovered'}</strong></div>
          <div class="mcp-inactive-tools">Allowed tool names: ${esc(metadataList(tools))}</div>
          <div class="mcp-inactive-tools">Read: ${esc(metadataList(server.read_tools))} · Write: ${esc(metadataList(server.write_tools))} · Timeout: ${esc(value(server.request_timeout_seconds, 'Not reported'))} sec</div>
          ${toolMarkup}
          <div class="row-actions">${connectControls}<button type="button" data-mcp-action="remove" data-mcp-id="${esc(server.id)}">Remove configuration</button></div>
        </article>`;
      }).join('');
    } catch (error) {
      q('#mcp-count').textContent = 'Unavailable';
      sectionError('#mcp-list', 'MCP configuration', error);
    }
  };
  const loadAgents = async () => {
    setLoading('#agents-list', 'Loading agents…');
    try {
      const agents = list(await api('/api/agents'));
      if (!agents.length) { setLoading('#agents-list', 'No agents yet. Create one to configure its name and model.'); return; }
      q('#agents-list').innerHTML = agents.map((agent) => {
        const id = esc(agent.id);
        const tools = Array.isArray(agent.tools) ? agent.tools.join(', ') : pretty(agent.tools);
        const enabled = Boolean(agent.enabled);
        const configuredTools = Array.isArray(agent.tools) ? agent.tools : [];
        const selectedServers = Array.isArray(agent.mcp_servers) ? agent.mcp_servers : [];
        const toolChoices = mcpTools.filter((tool) => {
          if (tool.name === 'web_search' && tool.source === 'web_search') return true;
          const server = mcpServers.find((item) => String(item.id) === String(tool.server) || String(item.name) === String(tool.server));
          return server && (server.runtime_available === true || (server.runtime_available === undefined && Boolean(server.enabled))) && Array.isArray(server.allowed_tools) && server.allowed_tools.includes(tool.name);
        });
        const serverChoices = mcpServers.filter((server) => server.runtime_available === true || (server.runtime_available === undefined && Boolean(server.enabled)));
        const agentSelectedServers = selectedServers.length ? selectedServers : toolChoices.filter((tool) => configuredTools.includes(tool.name)).map((tool) => String(mcpServers.find((server) => String(server.id) === String(tool.server) || String(server.name) === String(tool.server))?.id || ''));
        return `<article class="item-row" data-agent-id="${id}">
          <div class="item-top"><div><div class="item-title">${esc(value(agent.name, 'Unnamed agent'))}</div><div class="item-meta">${esc(value(agent.model, 'Model not configured'))} · prompt v${esc(value(agent.prompt_version, '—'))}</div></div>${stateTag(agent)}</div>
          <div class="item-description">Tools: ${esc(tools)}<br>Run limits: ${esc(value(agent.max_steps, '—'))} steps · ${esc(value(agent.max_tokens, '—'))} tokens · ${esc(value(agent.timeout_seconds, '—'))} sec<br>Web search: ${agent.web_search ? 'on' : 'off'} · MCP: ${agent.mcp_enabled ? 'on' : 'off'}</div>
          <div class="row-actions"><button type="button" data-prompt-edit="${id}">Edit system prompt</button><button type="button" data-agent-tools-edit="${id}">Configure capabilities</button><button type="button" data-agent-enabled="${id}" data-enabled="${enabled}">${enabled ? 'Disable agent' : 'Enable agent'}</button></div>
          <form class="agent-capability-form inline-form" data-agent-tools-form="${id}" hidden>
            <label class="check-row"><input type="checkbox" name="web_search" ${agent.web_search ? 'checked' : ''}> Allow web search</label>
            <label class="check-row"><input type="checkbox" name="mcp_enabled" ${agent.mcp_enabled ? 'checked' : ''}> Allow connected MCP tools</label>
            <fieldset><legend>Connected servers</legend>${serverChoices.length ? serverChoices.map((server) => `<label class="check-row"><input type="checkbox" name="mcp_server" value="${esc(server.id)}" ${agentSelectedServers.includes(String(server.id)) ? 'checked' : ''}> ${esc(server.name)}</label>`).join('') : '<div class="item-meta">No connected HTTP servers.</div>'}</fieldset>
            <fieldset><legend>Tools allowed for this agent</legend>${toolChoices.length ? toolChoices.map((tool) => `<label class="tool-choice"><input type="checkbox" name="tool" value="${esc(tool.name)}" ${configuredTools.includes(tool.name) ? 'checked' : ''}><span><code>${esc(tool.name)}</code> · ${esc(value(tool.risk, 'risk not reported'))}<small>${esc(value(tool.description, 'No description reported'))}</small></span></label>`).join('') : '<div class="item-meta">Connect a server and configure actual tool names in its allowlist to discover tools here.</div>'}</fieldset>
            <div class="form-actions"><button class="button primary small-button" type="submit">Save capabilities</button><button class="text-button cancel-button" type="button">Cancel</button></div>
          </form>
          ${enabled ? `<form class="run-form inline-form" data-run-form="${id}"><label for="run-input-${id}">Message</label><textarea id="run-input-${id}" name="input" rows="3" maxlength="64000" required placeholder="What should this agent do?"></textarea><div class="form-actions"><button class="button primary small-button" type="submit">Start run</button><span class="item-meta">Requires a saved prompt and a model that passes all capability checks.</span></div></form>` : `<div class="item-meta">Disabled agents cannot run. Enable only after reviewing the prompt and configuration.</div>`}
          <form class="prompt-form inline-form" data-prompt-form="${id}" hidden><label for="prompt-${id}">System prompt</label><textarea id="prompt-${id}" name="prompt" rows="7" maxlength="20000">${esc(agent.prompt || '')}</textarea><div class="form-actions"><button class="button primary small-button" type="submit">Save prompt</button><button class="text-button cancel-button" type="button">Cancel</button></div></form>
        </article>`;
      }).join('');
    } catch (error) { sectionError('#agents-list', 'Agents', error); }
  };
  const loadApprovals = async () => {
    setLoading('#approvals-list', 'Loading approvals…');
    try {
      const approvals = list(await api('/api/approvals'));
      const pending = approvals.filter((item) => value(item.status, 'pending') === 'pending');
      q('#approval-count').textContent = `${pending.length} pending`;
      if (!pending.length) { setLoading('#approvals-list', 'No pending approvals.'); return; }
      q('#approvals-list').innerHTML = pending.map((item) => `<article class="item-row">
        <div class="item-top"><div><div class="item-title">${esc(value(item.tool, 'Requested action'))}</div><div class="item-meta">${esc(value(item.agent_name, item.agent_id, 'Agent unavailable'))} · ${esc(date(item.requested_at))}</div></div>${stateTag(item)}</div>
        ${item.reason ? `<div class="item-description">${esc(item.reason)}</div>` : ''}
        ${item.args !== undefined ? `<details class="detail-box"><summary>View requested arguments</summary><pre>${esc(pretty(item.args))}</pre></details>` : ''}
        <div class="row-actions"><button class="approve-action" type="button" data-decision="approve" data-id="${esc(item.id)}" data-run-id="${esc(item.run_id || '')}">${item.run_id ? 'Approve and resume' : 'Record approval'}</button><button type="button" data-decision="deny" data-id="${esc(item.id)}">Deny</button></div>
        <div class="item-meta decision-note">${item.run_id ? 'Approval is recorded first, then the paused run resumes.' : 'This approval has no linked run, so the decision is recorded without execution.'}</div>
      </article>`).join('');
    } catch (error) { q('#approval-count').textContent = 'Unavailable'; sectionError('#approvals-list', 'Approvals', error); }
  };
  const loadRuns = async () => {
    setLoading('#runs-list', 'Loading runs…');
    try {
      const runs = list(await api('/api/runs'));
      q('#runs-count').textContent = `${runs.length} recent`;
      if (!runs.length) { setLoading('#runs-list', 'No runs recorded yet. Enable a configured agent to start one.'); return; }
      q('#runs-list').innerHTML = runs.map((run) => `<article class="item-row run-row">
        <div class="item-top"><div><div class="item-title">${esc(value(run.agent_name, run.agent_id, 'Agent'))}</div><div class="item-meta">${esc(run.id)} · prompt v${esc(value(run.prompt_version, '—'))} · ${esc(date(run.updated_at || run.created_at))}</div></div>${stateTag(run)}</div>
        <div class="row-actions"><button type="button" data-run-detail="${esc(run.id)}">View run details</button></div>
      </article>`).join('');
    } catch (error) { q('#runs-count').textContent = 'Unavailable'; sectionError('#runs-list', 'Runs', error); }
  };
  const showRunDetails = async (id) => {
    const target = q('#run-detail');
    target.hidden = false;
    target.innerHTML = '<div class="empty-inline">Loading run details…</div>';
    try {
      const run = await api(`/api/runs/${encodeURIComponent(id)}`);
      const steps = Array.isArray(run.steps) ? run.steps : [];
      target.innerHTML = `<div class="run-detail-head"><h3>Run ${esc(value(run.id, id))}</h3><button type="button" data-run-detail-close>Close</button></div>
        <p class="item-meta">Status: ${esc(value(run.status, 'unknown'))} · Prompt version: ${esc(value(run.prompt_version, '—'))} · Tainted input: ${run.tainted ? 'yes' : 'no'}</p>
        ${run.output !== undefined && run.output !== null ? `<div class="item-description"><strong>Output</strong><pre>${esc(pretty(run.output))}</pre></div>` : ''}
        ${run.error ? `<div class="item-description"><strong>Run error</strong><pre>${esc(run.error)}</pre></div>` : ''}
        <details class="detail-box"><summary>Run steps (${steps.length})</summary><pre>${esc(pretty(steps))}</pre></details>`;
    } catch (error) { target.innerHTML = `<div class="empty-inline">Could not load run details. ${esc(error.message)}</div>`; }
  };
  const loadAutomations = async () => {
    setLoading('#automations-list', 'Loading automations…');
    try {
      const automations = list(await api('/api/automations'));
      if (!automations.length) { setLoading('#automations-list', 'No automations configured.'); return; }
      q('#automations-list').innerHTML = automations.map((item) => `<article class="item-row">
        <div class="item-top"><div><div class="item-title">${esc(value(item.name, 'Unnamed automation'))}</div><div class="item-meta">${esc(value(item.trigger_type, 'Trigger not configured'))}${item.schedule ? ` · ${esc(item.schedule)}` : ''}</div></div>${stateTag(item)}</div>
        ${item.pipeline ? `<details class="detail-box"><summary>View pipeline</summary><pre>${esc(pretty(item.pipeline))}</pre></details>` : ''}
      </article>`).join('');
    } catch (error) { sectionError('#automations-list', 'Automations', error); }
  };
  const loadAudit = async () => {
    setLoading('#audit-list', 'Loading audit events…');
    try {
      const events = list(await api('/api/audit'));
      if (!events.length) { setLoading('#audit-list', 'No audit events recorded yet.'); return; }
      q('#audit-list').innerHTML = events.slice(0, 8).map((event) => `<article class="item-row">
        <div class="item-top"><div><div class="item-title">${esc(value(event.event_type, 'Activity'))}</div><div class="item-meta">${esc(date(event.created_at))} · ${esc(value(event.actor, event.agent_id, 'System'))}</div></div></div>
        ${event.details !== undefined ? `<div class="item-description">${esc(pretty(event.details))}</div>` : ''}
      </article>`).join('');
    } catch (error) { sectionError('#audit-list', 'Audit log', error); }
  };
  const loadAll = async () => {
    showMessage('');
    protectedApiResponded = false;
    accessRejected = false;
    if (isLoopbackHost) {
      q('#access-settings').hidden = false;
      if (token) await loadAccess();
      else {
        q('#access-state').textContent = 'Locked';
        q('#access-status').textContent = 'Connect with the local API token to manage LAN access and paired devices.';
        q('#create-pairing-button').disabled = true;
        setLoading('#devices-list', 'Connect with the local API token to view paired devices.');
      }
    }
    await loadStatus();
    if (!token) {
      ['#models-list','#mcp-list','#agents-list','#approvals-list','#runs-list','#automations-list','#audit-list'].forEach((selector) => setLoading(selector, 'Enter the local API token to load host data.'));
      q('#approval-count').textContent = 'Locked';
      q('#mcp-count').textContent = 'Locked';
      q('#search-state').textContent = 'Locked';
      q('#search-status').textContent = 'Enter the local API token to read search configuration.';
      q('#search-form').hidden = true;
      return false;
    }
    await Promise.all([loadModels(), loadMCPServers()]);
    await Promise.all([loadAgents(), loadApprovals(), loadRuns(), loadAutomations(), loadAudit(), loadSearch()]);
    return protectedApiResponded && !accessRejected;
  };

  const loadDevices = async () => {
    setLoading('#devices-list', 'Loading paired devices…');
    try {
      const devices = list(await api('/api/devices'));
      if (!devices.length) { setLoading('#devices-list', 'No devices have been paired.'); return; }
      q('#devices-list').innerHTML = devices.map((device) => `<article class="item-row device-row">
        <div class="item-top"><div><div class="item-title">${esc(value(device.name, 'Unnamed device'))}</div><div class="item-meta">Paired ${esc(date(device.created_at))}${device.revoked_at ? ` · Revoked ${esc(date(device.revoked_at))}` : ''}</div></div><span class="item-tag">${device.revoked_at ? 'Revoked' : 'Active'}</span></div>
        ${device.revoked_at ? '' : `<div class="row-actions"><button type="button" data-revoke-device="${esc(device.id)}">Revoke access</button></div>`}
      </article>`).join('');
    } catch (error) { sectionError('#devices-list', 'Paired devices', error); }
  };
  const loadAccess = async () => {
    if (!isLoopbackHost) return;
    q('#access-settings').hidden = false;
    try {
      const access = await api('/api/access');
      q('#access-state').textContent = access?.enabled ? 'Enabled' : 'Disabled';
      q('#access-status').innerHTML = access?.enabled
        ? `<div>Available on your local network:</div>${list(access.urls).map((url) => `<code class="access-url">${esc(url)}</code>`).join('') || '<span>Address not reported.</span>'}${access.certificate_fingerprint ? `<div class="item-meta">Certificate fingerprint: <code>${esc(access.certificate_fingerprint)}</code></div>` : ''}`
        : 'LAN access is off. Start the daemon with the command below to pair a device.';
      q('#create-pairing-button').disabled = !access?.enabled;
      await loadDevices();
    } catch (error) {
      q('#access-state').textContent = 'Unavailable';
      q('#access-status').textContent = `LAN access could not be loaded: ${error.message}`;
      q('#create-pairing-button').disabled = true;
      sectionError('#devices-list', 'Paired devices', error);
    }
  };

  if (pairingSecret) {
    pairPanel.hidden = false;
    pairNameInput.focus();
  }
  pairForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const name = pairNameInput.value.trim();
    if (!pairingSecret || !name) return;
    const submit = q('button[type="submit"]', pairForm);
    submit.disabled = true;
    try {
      const result = await api('/api/devices/pair', {method:'POST', body:JSON.stringify({secret:pairingSecret, name})});
      if (!result?.token) throw new Error('The host did not return a device token. Request a new pairing link.');
      token = result.token;
      pairedDevice = true;
      sessionStorage.setItem(TOKEN_KEY, token);
      sessionStorage.setItem(PAIRED_KEY, 'true');
      showTokenPanel(false);
      pairingSecret = '';
      pairPanel.hidden = true;
      const connected = await loadAll();
      if (connected) showMessage(`This browser is paired${result.device?.name ? ` as ${result.device.name}` : ''}.`, false);
      else showMessage('This browser was paired, but the host did not confirm access. Pair again from the host if needed.');
    } catch (error) {
      showMessage(`Could not pair this device: ${error.message}`);
      submit.disabled = false;
    }
  });
  q('#forget-device-button').addEventListener('click', forgetDevice);
  q('#forget-current-device').addEventListener('click', forgetDevice);
  q('#create-pairing-button').addEventListener('click', async () => {
    const button = q('#create-pairing-button');
    button.disabled = true;
    try {
      const pairing = await api('/api/devices/pairing', {method:'POST', body:JSON.stringify({})});
      if (!pairing?.pairing_url) throw new Error('The host did not return a pairing link.');
      q('#pairing-link').value = pairing.pairing_url;
      q('#pairing-link-panel').hidden = false;
      q('#pairing-link-meta').textContent = `Expires ${date(pairing.expires_at)}${pairing.certificate_fingerprint ? ` · Certificate fingerprint: ${pairing.certificate_fingerprint}` : ''}`;
      showMessage('Pairing link created. Anyone with this link can pair one device before it expires.', false);
    } catch (error) { showMessage(`Could not create pairing link: ${error.message}`); }
    finally { button.disabled = false; }
  });
  q('#copy-pairing-link').addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(q('#pairing-link').value);
      q('#pairing-link-meta').textContent += ' · Copied to clipboard.';
    } catch { showMessage('Copy failed. Select the pairing link and copy it manually.'); }
  });
  q('#refresh-devices-button').addEventListener('click', loadDevices);
  q('#devices-list').addEventListener('click', async (event) => {
    const button = event.target.closest('[data-revoke-device]');
    if (!button) return;
    const id = button.dataset.revokeDevice;
    button.disabled = true;
    try {
      await api(`/api/devices/${encodeURIComponent(id)}`, {method:'DELETE'});
      showMessage('Device access revoked.', false);
      await loadDevices();
    } catch (error) { button.disabled = false; showMessage(`Could not revoke device: ${error.message}`); }
  });

  tokenForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    token = tokenInput.value.trim();
    if (!token) return;
    try {
      sessionStorage.setItem(TOKEN_KEY, token);
      tokenInput.value = '';
      showTokenPanel(false);
      const connected = await loadAll();
      if (!connected) {
        showMessage(accessRejected
          ? 'The local API rejected this token. Check the token and try again.'
          : 'Could not confirm access to the local API. Check the host and try again.');
        return;
      }
      showMessage('Connected to the local API for this browser tab.', false);
    } catch (error) { showMessage(error.message); }
  });
  q('#refresh-button').addEventListener('click', loadAll);
  q('#new-model-button').addEventListener('click', () => { q('#model-form').hidden = false; q('#model-id').focus(); });
  q('#model-provider').addEventListener('change', (event) => {
    const baseURL = event.target.value === 'lm_studio' ? 'http://127.0.0.1:1234/v1' : 'http://127.0.0.1:11434/v1';
    q('#model-endpoint').textContent = baseURL;
    q('#model-base-url').value = baseURL;
  });
  q('#new-agent-button').addEventListener('click', () => { q('#agent-form').hidden = false; q('#agent-name').focus(); });
  q('#new-mcp-button').addEventListener('click', () => { q('#mcp-form').hidden = false; q('#mcp-name').focus(); });
  q('#new-automation-button').addEventListener('click', () => { q('#automation-form').hidden = false; q('#automation-name').focus(); });
  document.querySelectorAll('.cancel-button').forEach((button) => button.addEventListener('click', () => { button.closest('form').hidden = true; }));

  q('#agent-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const payload = {name:String(form.get('name')).trim(), model:String(form.get('model') || '').trim(), enabled:false, tools:[], prompt:''};
    try { await api('/api/agents', {method:'POST', body:JSON.stringify(payload)}); event.currentTarget.reset(); event.currentTarget.hidden = true; showMessage('Agent created in disabled state.', false); await Promise.all([loadAgents(), loadStatus()]); }
    catch (error) { showMessage(`Could not create agent: ${error.message}`); }
  });
  q('#mcp-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const names = (field) => String(form.get(field) || '').split(',').map((name) => name.trim()).filter(Boolean);
    const allowed = names('allowed_tools');
    const read = names('read_tools');
    const write = names('write_tools');
    const allowedSet = new Set(allowed);
    if (new Set(allowed).size !== allowed.length || new Set(read).size !== read.length || new Set(write).size !== write.length) {
      showMessage('Tool names must be unique within each list.');
      return;
    }
    if ([...read, ...write].some((name) => !allowedSet.has(name))) {
      showMessage('Read and write tool names must also appear in the allowed tool list.');
      return;
    }
    if (!allowed.length || allowed.some((name) => !read.includes(name) && !write.includes(name)) || read.some((name) => write.includes(name))) {
      showMessage('Every allowed tool needs exactly one access class: read or write.');
      return;
    }
    const payload = {
      name:String(form.get('name') || '').trim(), transport:'streamable_http',
      endpoint:String(form.get('endpoint') || '').trim(), enabled:false,
      allowed_tools:allowed, read_tools:read, write_tools:write, request_timeout_seconds:30,
    };
    try {
      await api('/api/mcp/servers', {method:'POST', body:JSON.stringify(payload)});
      event.currentTarget.reset();
      event.currentTarget.hidden = true;
      showMessage('HTTP server configuration saved disabled. Connect it explicitly to discover its tools.', false);
      await loadMCPServers();
    } catch (error) { showMessage(`Could not save MCP configuration: ${error.message}`); }
  });
  q('#search-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const endpoint = String(new FormData(event.currentTarget).get('endpoint') || '').trim();
    const button = q('button[type="submit"]', event.currentTarget);
    if (!endpoint) return;
    button.disabled = true;
    try {
      await api('/api/search', {method:'POST', body:JSON.stringify({endpoint})});
      showMessage('SearXNG configured. Search queries and results leave this device when an enabled agent uses web search.', false);
      await loadSearch();
      await loadTools();
      await loadAgents();
    } catch (error) {
      showMessage(`Could not configure SearXNG: ${error.message}`);
      if (/already configured/i.test(error.message)) await loadSearch();
    } finally { button.disabled = false; }
  });
  q('#model-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const button = q('button[type="submit"]', event.currentTarget);
    button.disabled = true;
    const payload = {
      name: String(form.get('name') || '').trim(),
      provider: String(form.get('provider')),
      model: String(form.get('model') || '').trim(),
      base_url: String(form.get('base_url')),
    };
    try {
      await api('/api/models', {method:'POST', body:JSON.stringify(payload)});
      event.currentTarget.reset();
      event.currentTarget.hidden = true;
      q('#model-provider').dispatchEvent(new Event('change'));
      showMessage('Local model configuration saved. Run a probe to record its observed capabilities.', false);
      await Promise.all([loadModels(), loadStatus()]);
    } catch (error) { showMessage(`Could not save model configuration: ${error.message}`); }
    finally { button.disabled = false; }
  });
  q('#automation-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const schedule = String(form.get('schedule') || '').trim();
    const payload = {name:String(form.get('name')).trim(), enabled:false, trigger_type:schedule ? 'cron' : 'manual', schedule, pipeline:[]};
    try { await api('/api/automations', {method:'POST', body:JSON.stringify(payload)}); event.currentTarget.reset(); event.currentTarget.hidden = true; showMessage('Automation created disabled. No job has been run.', false); await Promise.all([loadAutomations(), loadStatus()]); }
    catch (error) { showMessage(`Could not create automation: ${error.message}`); }
  });
  document.addEventListener('click', async (event) => {
    const cancel = event.target.closest('.cancel-button');
    if (cancel && cancel.closest('form')?.matches('.agent-capability-form')) {
      cancel.closest('form').hidden = true;
      return;
    }
    if (cancel && cancel.closest('form')?.matches('#mcp-form')) {
      cancel.closest('form').hidden = true;
      return;
    }
    const capabilityButton = event.target.closest('[data-agent-tools-edit]');
    if (capabilityButton) {
      const form = q(`[data-agent-tools-form="${CSS.escape(capabilityButton.dataset.agentToolsEdit)}"]`);
      form.hidden = !form.hidden;
      return;
    }
    const mcpAction = event.target.closest('[data-mcp-action]');
    if (mcpAction) {
      const id = mcpAction.dataset.mcpId;
      const action = mcpAction.dataset.mcpAction;
      mcpAction.disabled = true;
      try {
        if (action === 'remove') {
          await api(`/api/mcp/servers/${encodeURIComponent(id)}`, {method:'DELETE'});
          showMessage('Saved server configuration removed.', false);
        } else {
          const result = await api(`/api/mcp/servers/${encodeURIComponent(id)}/${action}`, {method:'POST', body:'{}', remoteAuthExpected:action === 'connect'});
          if (result?.code === 'auth_required' || result?.status === 'auth_required') throw new Error('auth_required: this MCP server requires OAuth or sign-in, which is not available yet.');
          showMessage(action === 'connect' ? 'MCP server connected. Discovered tools are shown below; no tool calls were made.' : 'MCP server disconnected. Its tools are unavailable to agent runs.', false);
        }
        await loadMCPServers();
        await loadAgents();
      } catch (error) {
        const prefix = /auth[_ -]?required/i.test(error.message) ? 'Sign-in required: ' : `Could not ${action} MCP server: `;
        showMessage(`${prefix}${error.message.replace(/^auth_required:\s*/i, '')}`);
      } finally { mcpAction.disabled = false; }
      return;
    }
    const enabledButton = event.target.closest('[data-agent-enabled]');
    if (enabledButton) {
      const id = enabledButton.dataset.agentEnabled;
      const enabled = enabledButton.dataset.enabled !== 'true';
      enabledButton.disabled = true;
      try {
        await api(`/api/agents/${encodeURIComponent(id)}`, {method:'PATCH', body:JSON.stringify({enabled})});
        showMessage(enabled ? 'Agent enabled. A run still requires a saved prompt and a model that passes all capability checks.' : 'Agent disabled for new runs.', false);
        await loadAgents();
      } catch (error) { showMessage(`Could not update agent state: ${error.message}`); }
      finally { enabledButton.disabled = false; }
      return;
    }
    const runButton = event.target.closest('[data-run-detail]');
    if (runButton) { await showRunDetails(runButton.dataset.runDetail); return; }
    if (event.target.closest('[data-run-detail-close]')) { q('#run-detail').hidden = true; return; }
    const promptButton = event.target.closest('[data-prompt-edit]');
    if (promptButton) {
      const id = promptButton.dataset.promptEdit;
      const form = q(`[data-prompt-form="${CSS.escape(id)}"]`);
      if (form.hidden) {
        try {
          const promptVersions = list(await api(`/api/agents/${encodeURIComponent(id)}/prompts`));
          const current = promptVersions.reduce((latest, entry) => Number(entry?.version) > Number(latest?.version) ? entry : latest, promptVersions[0]);
          q('textarea', form).value = current?.prompt || '';
          form.hidden = false;
          q('textarea', form).focus();
        } catch (error) { showMessage(`Could not load prompt: ${error.message}`); }
      } else form.hidden = true;
      return;
    }
    const probeButton = event.target.closest('[data-model-probe]');
    if (probeButton) {
      const id = probeButton.dataset.modelProbe;
      probeButton.disabled = true;
      probeButton.setAttribute('aria-busy', 'true');
      const originalText = probeButton.textContent;
      probeButton.textContent = 'Probing…';
      showMessage('Running a capability probe against the selected local model. This can take up to four minutes.', false);
      try {
        await api(`/api/models/${encodeURIComponent(id)}/probe`, {method:'POST', body:'{}'});
        showMessage('Probe completed. The result is recorded as diagnostic data only.', false);
        await loadModels();
      } catch (error) { showMessage(`Capability probe failed: ${error.message}`); }
      finally {
        probeButton.removeAttribute('aria-busy');
        probeButton.disabled = false;
        probeButton.textContent = originalText;
      }
      return;
    }
    const decision = event.target.closest('[data-decision]');
    if (decision) {
      decision.disabled = true;
      const {id, runId} = decision.dataset;
      try {
        await api(`/api/approvals/${encodeURIComponent(id)}/${decision.dataset.decision}`, {method:'POST'});
        if (decision.dataset.decision === 'approve' && runId) {
          try {
            const resumed = await api(`/api/runs/${encodeURIComponent(runId)}/resume`, {method:'POST', body:'{}'});
            showMessage(`Approval recorded. Run ${value(resumed?.status, 'resumed')} with its pinned prompt and model configuration.`, false);
          } catch (error) {
            showMessage(`Approval was recorded, but run ${runId} could not resume: ${error.message}`);
          }
          await Promise.all([loadApprovals(), loadRuns(), loadStatus()]);
        } else {
          showMessage(decision.dataset.decision === 'approve'
            ? 'Approval decision recorded. No linked run was available to resume.'
            : 'Denial recorded. The run was not resumed.', false);
          await Promise.all([loadApprovals(), loadRuns(), loadStatus()]);
        }
      } catch (error) { decision.disabled = false; showMessage(`Could not record decision: ${error.message}`); }
    }
  });
  document.addEventListener('submit', async (event) => {
    const capabilityForm = event.target.closest('.agent-capability-form');
    if (capabilityForm) {
      event.preventDefault();
      const id = capabilityForm.dataset.agentToolsForm;
      const data = new FormData(capabilityForm);
      const tools = data.getAll('tool').map(String);
      const serverIDs = new Set(data.getAll('mcp_server').map(String));
      for (const toolName of tools) {
        const tool = mcpTools.find((item) => item.name === toolName);
        const server = tool && mcpServers.find((item) => String(item.id) === String(tool.server) || String(item.name) === String(tool.server));
        if (server) serverIDs.add(String(server.id));
      }
      const payload = {
        web_search:data.get('web_search') === 'on',
        mcp_enabled:data.get('mcp_enabled') === 'on',
        tools,
        mcp_servers:[...serverIDs],
      };
      try {
        await api(`/api/agents/${encodeURIComponent(id)}`, {method:'PATCH', body:JSON.stringify(payload)});
        showMessage('Agent capabilities saved for future runs. Connected servers and explicitly selected tools define MCP access.', false);
        await loadAgents();
      } catch (error) { showMessage(`Could not save agent capabilities: ${error.message}`); }
      return;
    }
    const runForm = event.target.closest('.run-form');
    if (runForm) {
      event.preventDefault();
      const id = runForm.dataset.runForm;
      const input = String(new FormData(runForm).get('input') || '').trim();
      const button = q('button[type="submit"]', runForm);
      if (!input) return;
      button.disabled = true;
      try {
        const run = await api(`/api/agents/${encodeURIComponent(id)}/runs`, {method:'POST', body:JSON.stringify({input})});
        showMessage(`Run ${value(run?.id, '')} finished with status ${value(run?.status, 'unknown')}.`, run?.status === 'failed');
        runForm.reset();
        await Promise.all([loadRuns(), loadApprovals(), loadStatus()]);
        if (run?.id) await showRunDetails(run.id);
      } catch (error) { showMessage(`Could not start run: ${error.message}`); }
      finally { button.disabled = false; }
      return;
    }
    const form = event.target.closest('.prompt-form');
    if (!form) return;
    event.preventDefault();
    const id = form.dataset.promptForm;
    try {
      const response = await api(`/api/agents/${encodeURIComponent(id)}/prompts`, {method:'POST', body:JSON.stringify({prompt:q('textarea', form).value})});
      showMessage(`Prompt version ${value(response?.version, 'saved')} saved.`, false);
      form.hidden = true;
      await loadAgents();
    } catch (error) { showMessage(`Could not save prompt: ${error.message}`); }
  });
  if (pairedDevice && !token && !pairingSecret) showTokenPanel(true);
  if (!pairingSecret) {
    loadAll();
  }
})();
