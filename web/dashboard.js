(() => {
  const TOKEN_KEY = 'ownbot.api-token';
  const tokenPanel = document.querySelector('#token-panel');
  const tokenForm = document.querySelector('#token-form');
  const tokenInput = document.querySelector('#api-token');
  const pageMessage = document.querySelector('#page-message');
  const connectionLabel = document.querySelector('#connection-label');
  let token = sessionStorage.getItem(TOKEN_KEY) || '';
  let protectedApiResponded = false;
  let accessRejected = false;

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
    if (show) tokenInput.focus();
  };
  const api = async (path, options = {}) => {
    const headers = new Headers(options.headers || {});
    headers.set('Accept', 'application/json');
    if (options.body !== undefined) headers.set('Content-Type', 'application/json');
    if (token) headers.set('Authorization', `Bearer ${token}`);
    const response = await fetch(path, {...options, headers, cache:'no-store'});
    if (response.status === 401 || response.status === 403) {
      accessRejected = true;
      token = '';
      sessionStorage.removeItem(TOKEN_KEY);
      showTokenPanel(true);
      throw new Error('Access token was rejected. Check the token from ~/.ownbot/api.token and try again.');
    }
    if (!response.ok) {
      const body = await response.text().catch(() => '');
      let reason = body;
      try { reason = JSON.parse(body).error || JSON.parse(body).message || body; } catch {}
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
      ['Service', connected ? 'Responding' : 'Not confirmed'],
      ['Version', value(data?.version, 'Not reported')],
      ['Agents', value(data?.agents, 'Not reported')],
      ['Pending approvals', value(data?.pending_approvals, 'Not reported')],
      ['Active automations', value(data?.active_automations, 'Not reported')],
      ['Queued jobs', value(data?.queued_jobs, 'Not reported')],
      ['API token', data?.api_token_required ? 'Required for account data' : 'Not required'],
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
      <div class="item-meta">${probedAt ? `Probed ${esc(date(probedAt))}. ` : ''}Diagnostic result only; no agent runtime uses this profile yet.</div>
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
  const loadMCPServers = async () => {
    setLoading('#mcp-list', 'Loading saved MCP configuration…');
    try {
      const servers = list(await api('/api/mcp/servers'));
      q('#mcp-count').textContent = `${servers.length} saved`;
      if (!servers.length) {
        setLoading('#mcp-list', 'No MCP server configurations are saved. Provider connections and tools are not active.');
        return;
      }
      q('#mcp-list').innerHTML = servers.map((server) => {
        const transport = value(server.transport, 'stdio');
        const address = transport === 'stdio'
          ? [server.command, ...(Array.isArray(server.args) ? server.args : [])].filter(Boolean).join(' ')
          : value(server.endpoint, 'Endpoint not configured');
        const connection = server.runtime_available ? 'Runtime available' : 'Not connected';
        const configState = server.enabled ? 'Enabled configuration' : 'Disabled configuration';
        const tools = server.allowed_tools;
        return `<article class="mcp-card">
          <div class="mcp-card-head"><div><div class="model-card-title">${esc(value(server.name, 'Unnamed server'))}</div><div class="model-card-meta">${esc(transport)} · ${esc(address || 'No command or endpoint saved')}</div></div><span class="item-tag">${esc(configState)}</span></div>
          <div class="mcp-status-row"><span>Connection</span><strong>${esc(connection)}</strong><span>Tools</span><strong>Not active</strong></div>
          <div class="mcp-inactive-tools">Configured tool allowlist (inactive): ${esc(metadataList(tools))}</div>
          <div class="mcp-inactive-tools">Read tools (inactive): ${esc(metadataList(server.read_tools))} · Write tools (inactive): ${esc(metadataList(server.write_tools))}</div>
          <div class="mcp-inactive-tools">Environment names allowed: ${esc(metadataList(server.environment_allowlist))} · Timeout: ${esc(value(server.request_timeout_seconds, 'Not reported'))} sec</div>
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
        return `<article class="item-row" data-agent-id="${id}">
          <div class="item-top"><div><div class="item-title">${esc(value(agent.name, 'Unnamed agent'))}</div><div class="item-meta">${esc(value(agent.model, 'Model not configured'))} · prompt v${esc(value(agent.prompt_version, '—'))}</div></div>${stateTag(agent)}</div>
          <div class="item-description">Tools: ${esc(tools)}<br>Run limits: ${esc(value(agent.max_steps, '—'))} steps · ${esc(value(agent.max_tokens, '—'))} tokens · ${esc(value(agent.timeout_seconds, '—'))} sec<br>Web search: ${agent.web_search ? 'on' : 'off'} · MCP: ${agent.mcp_enabled ? 'on' : 'off'}</div>
          <div class="row-actions"><button type="button" data-prompt-edit="${id}">Edit system prompt</button></div>
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
        <div class="row-actions"><button class="approve-action" type="button" data-decision="approve" data-id="${esc(item.id)}">Record approval</button><button type="button" data-decision="deny" data-id="${esc(item.id)}">Deny</button></div>
        <div class="item-meta decision-note">Recording a decision does not execute the requested action.</div>
      </article>`).join('');
    } catch (error) { q('#approval-count').textContent = 'Unavailable'; sectionError('#approvals-list', 'Approvals', error); }
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
    await loadStatus();
    if (!token) {
      ['#models-list','#mcp-list','#agents-list','#approvals-list','#automations-list','#audit-list'].forEach((selector) => setLoading(selector, 'Enter the local API token to load host data.'));
      q('#approval-count').textContent = 'Locked';
      q('#mcp-count').textContent = 'Locked';
      return false;
    }
    await Promise.all([loadModels(), loadMCPServers(), loadAgents(), loadApprovals(), loadAutomations(), loadAudit()]);
    return protectedApiResponded && !accessRejected;
  };

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
  q('#new-automation-button').addEventListener('click', () => { q('#automation-form').hidden = false; q('#automation-name').focus(); });
  document.querySelectorAll('.cancel-button').forEach((button) => button.addEventListener('click', () => { button.closest('form').hidden = true; }));

  q('#agent-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const payload = {name:String(form.get('name')).trim(), model:String(form.get('model') || '').trim(), enabled:false, tools:[], prompt:''};
    try { await api('/api/agents', {method:'POST', body:JSON.stringify(payload)}); event.currentTarget.reset(); event.currentTarget.hidden = true; showMessage('Agent created in disabled state.', false); await Promise.all([loadAgents(), loadStatus()]); }
    catch (error) { showMessage(`Could not create agent: ${error.message}`); }
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
      const {id} = decision.dataset;
      try {
        await api(`/api/approvals/${encodeURIComponent(id)}/${decision.dataset.decision}`, {method:'POST'});
        showMessage(`Decision recorded: ${decision.dataset.decision}. No external action was executed.`, false);
        await Promise.all([loadApprovals(), loadStatus()]);
      } catch (error) { decision.disabled = false; showMessage(`Could not record decision: ${error.message}`); }
    }
  });
  document.addEventListener('submit', async (event) => {
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
  loadAll();
})();
