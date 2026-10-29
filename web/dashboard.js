(() => {
  const TOKEN_KEY = 'ownbot.api-token';
  const PAIRED_KEY = 'ownbot.paired-device';
  const q = (selector, root = document) => root.querySelector(selector);
  const list = (data) => Array.isArray(data) ? data : Array.isArray(data?.items) ? data.items : [];
  const text = (value, fallback = '') => value === undefined || value === null || value === '' ? fallback : String(value);
  const escapeHTML = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
  const formatDate = (value) => {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? 'Time unavailable' : date.toLocaleString([], {dateStyle:'medium', timeStyle:'short'});
  };
  const tokenPanel = q('#token-panel');
  const pairPanel = q('#pair-panel');
  const pairForm = q('#pair-form');
  const pairNameInput = q('#pair-device-name');
  const settingsLessSetup = q('#setup-dialog');
  const pageMessage = q('#page-message');
  const loopback = ['localhost', '127.0.0.1', '::1', '[::1]'].includes(window.location.hostname);
  let token = sessionStorage.getItem(TOKEN_KEY) || '';
  let pairedDevice = sessionStorage.getItem(PAIRED_KEY) === 'true';
  let pairingSecret = '';
  let bootstrapTicket = '';
  const fragment = new URLSearchParams((window.location.hash || '').replace(/^#/, ''));
  pairingSecret = fragment.get('pair') || '';
  bootstrapTicket = fragment.get('bootstrap') || '';
  if (pairingSecret || bootstrapTicket) window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}`);

  const message = (value, error = false) => {
    pageMessage.textContent = value;
    pageMessage.hidden = !value;
    pageMessage.classList.toggle('message-info', !error);
  };
  const showConnection = (show) => {
    tokenPanel.hidden = !show;
    q('#owner-token-copy').hidden = !(loopback && !pairedDevice);
    q('#paired-token-copy').hidden = !pairedDevice;
    q('#remote-token-copy').hidden = loopback || pairedDevice;
    q('#forget-device-button').hidden = !pairedDevice;
    q('#forget-current-device').hidden = !pairedDevice || !token;
  };
  const api = async (path, options = {}) => {
    const headers = new Headers(options.headers || {});
    headers.set('Accept', 'application/json');
    if (options.body !== undefined) headers.set('Content-Type', 'application/json');
    if (token) headers.set('Authorization', `Bearer ${token}`);
    const response = await fetch(path, {...options, headers, cache:'no-store'});
    if (response.status === 401 || response.status === 403) {
      token = '';
      sessionStorage.removeItem(TOKEN_KEY);
      showConnection(true);
      const rejection = 'This browser no longer has access. Connect again from the host.';
      message(rejection, true);
      throw new Error(rejection);
    }
    if (!response.ok) {
      const body = await response.text().catch(() => '');
      let reason = body;
      try { reason = JSON.parse(body).error || JSON.parse(body).message || body; } catch {}
      throw new Error(reason || `Request failed (${response.status})`);
    }
    if (response.status === 204) return null;
    const body = await response.text();
    return body ? JSON.parse(body) : null;
  };
  let recommendationsLoaded = false;
  const redeemBootstrap = async () => {
    const ticket = bootstrapTicket;
    bootstrapTicket = '';
    const response = await fetch('/api/access/bootstrap/exchange', {
      method:'POST', credentials:'same-origin', cache:'no-store',
      headers:{'Accept':'application/json','Content-Type':'application/json'},
      body:JSON.stringify({ticket}),
    });
    if (!response.ok) throw new Error('The one-time owner link expired. Run ownbot daemon start on the host to open a fresh link.');
    const result = await response.json();
    if (!result?.token) throw new Error('The host did not return an owner session. Run ownbot daemon start again.');
    token = result.token;
    pairedDevice = false;
    sessionStorage.setItem(TOKEN_KEY, token);
    sessionStorage.removeItem(PAIRED_KEY);
    showConnection(false);
  };
  const forgetDevice = () => {
    token = '';
    pairedDevice = false;
    sessionStorage.removeItem(TOKEN_KEY);
    sessionStorage.removeItem(PAIRED_KEY);
    showConnection(true);
    message('This browser has forgotten its paired access.', false);
  };
  q('#forget-current-device').addEventListener('click', forgetDevice);
  q('#forget-device-button').addEventListener('click', forgetDevice);

  const updateComposer = (agents) => {
    const enabled = agents.filter((agent) => agent.enabled === true);
    const form = q('#task-form');
    const select = q('#task-agent');
    const label = q('#task-agent-label');
    const input = q('#task-input');
    const submit = q('#start-task-button');
    const empty = q('#composer-empty');
    if (!enabled.length) {
      form.hidden = true;
      empty.hidden = false;
      void loadRecommendations();
      return;
    }
    empty.hidden = true;
    form.hidden = false;
    if (enabled.length > 1) {
      const selected = select.value;
      select.innerHTML = enabled.map((agent) => `<option value="${escapeHTML(agent.id)}">${escapeHTML(text(agent.name, 'Agent'))}</option>`).join('');
      select.value = enabled.some((agent) => String(agent.id) === selected) ? selected : String(enabled[0].id);
      select.hidden = false;
      label.hidden = false;
    } else {
      select.innerHTML = `<option value="${escapeHTML(enabled[0].id)}">${escapeHTML(text(enabled[0].name, 'Agent'))}</option>`;
      select.value = String(enabled[0].id);
      select.hidden = true;
      label.hidden = true;
    }
    form.dataset.agentId = select.value;
    input.disabled = false;
    submit.disabled = false;
    q('#composer-state').textContent = enabled.length > 1 ? 'Choose an agent for this task.' : `Using ${text(enabled[0].name, 'your agent')}.`;
  };
  const loadAgents = async () => {
    const agents = list(await api('/api/agents'));
    updateComposer(agents);
    return agents;
  };
  const loadRuns = async () => {
    const runs = list(await api('/api/runs'));
    q('#runs-list').innerHTML = runs.length ? runs.map((run) => `<article class="history-item">
      <span class="history-agent">${escapeHTML(text(run.agent_name, run.agent_id || 'Agent'))}</span>
      <span class="history-meta">${escapeHTML(text(run.status, 'Unknown'))} · ${escapeHTML(formatDate(run.updated_at || run.created_at))}</span>
      <button class="text-button" type="button" data-run-id="${escapeHTML(run.id)}">View result</button>
    </article>`).join('') : '<p class="empty-inline">No runs yet.</p>';
  };
  const loadApprovals = async () => {
    const approvals = list(await api('/api/approvals')).filter((approval) => text(approval.status, 'pending') === 'pending');
    q('#approvals-panel').hidden = approvals.length === 0;
    q('#approvals-list').innerHTML = approvals.map((approval) => `<article class="approval-item">
      <h3>${escapeHTML(text(approval.tool, 'Requested action'))}</h3>
      <p>${escapeHTML(text(approval.agent_name, approval.agent_id || 'Agent'))} · ${escapeHTML(formatDate(approval.requested_at))}</p>
      ${approval.reason ? `<p>${escapeHTML(approval.reason)}</p>` : ''}
      ${approval.args !== undefined ? `<details><summary>Requested arguments</summary><pre>${escapeHTML(JSON.stringify(approval.args, null, 2))}</pre></details>` : ''}
      <div class="decision-actions"><button class="button primary" type="button" data-decision="approve" data-id="${escapeHTML(approval.id)}" data-run-id="${escapeHTML(approval.run_id || '')}">${approval.run_id ? 'Approve and resume' : 'Approve'}</button><button class="button outline" type="button" data-decision="deny" data-id="${escapeHTML(approval.id)}">Deny</button></div>
    </article>`).join('');
  };
  const runDetails = async (id) => {
    const target = q('#run-detail');
    q('#run-output-panel').hidden = false;
    target.hidden = false;
    target.innerHTML = '<p>Loading result…</p>';
    try {
      const run = await api(`/api/runs/${encodeURIComponent(id)}`);
      const messages = Array.isArray(run.messages) ? run.messages : [];
      const output = run.output ?? [...messages].reverse().find((entry) => entry?.role === 'assistant' && typeof entry.content === 'string' && entry.content.trim())?.content;
      const steps = Array.isArray(run.steps) ? run.steps : messages.filter((entry) => entry?.role === 'assistant' || entry?.role === 'tool').map((entry) => ({role:entry.role, name:entry.name || undefined, content:entry.content || ''}));
      const count = Array.isArray(run.steps) ? run.steps.length : Number(run.steps) || steps.length;
      target.innerHTML = `<div class="run-detail-head"><h2>Run result</h2><button class="text-button" type="button" data-run-detail-close>Close</button></div>
        <p class="run-meta">${escapeHTML(text(run.status, 'Status unavailable'))} · Prompt version ${escapeHTML(text(run.prompt_version, run.config?.prompt_version ?? '—'))}</p>
        ${output !== undefined && output !== null ? `<div class="run-output-text"><h3>Output</h3><pre>${escapeHTML(typeof output === 'string' ? output : JSON.stringify(output, null, 2))}</pre></div>` : ''}
        ${run.error ? `<p class="run-error">${escapeHTML(run.error)}</p>` : ''}
        <details><summary>Steps (${count})</summary><pre>${escapeHTML(JSON.stringify(steps, null, 2))}</pre></details>`;
    } catch (error) { target.innerHTML = `<p>Could not load this result: ${escapeHTML(error.message)}</p>`; }
  };
  const refresh = async () => {
    if (!token) {
      const status = await fetch('/api/status', {cache:'no-store'}).then((response) => response.ok ? response.json() : null).catch(() => null);
      q('#connection-label').textContent = status?.ok ? 'Connect to this host' : 'Unavailable';
      showConnection(true);
      return;
    }
    const results = await Promise.allSettled([loadAgents(), loadRuns(), loadApprovals()]);
    const error = results.find((result) => result.status === 'rejected');
    if (error && token) message(`Could not refresh the dashboard: ${error.reason?.message || 'request failed'}`, true);
    else if (!error) message('');
    q('#connection-label').textContent = token ? 'Connected' : 'Connect to this host';
  };

  const history = q('#history-rail');
  const setHistoryOpen = (open) => {
    history.classList.toggle('history-open', open);
    q('#history-toggle').setAttribute('aria-expanded', String(open));
    q('#history-backdrop').hidden = !open;
  };
  q('#history-toggle').addEventListener('click', () => setHistoryOpen(!history.classList.contains('history-open')));
  q('#history-close').addEventListener('click', () => setHistoryOpen(false));
  q('#history-backdrop').addEventListener('click', () => setHistoryOpen(false));
  q('#refresh-button')?.addEventListener('click', refresh);
  q('#history-rail').addEventListener('click', async (event) => {
    const button = event.target.closest('[data-run-id]');
    if (!button) return;
    await runDetails(button.dataset.runId);
    setHistoryOpen(false);
  });
  q('#run-detail').addEventListener('click', (event) => {
    if (event.target.closest('[data-run-detail-close]')) {
      q('#run-detail').hidden = true;
      q('#run-output-panel').hidden = true;
    }
  });
  q('#approvals-list').addEventListener('click', async (event) => {
    const button = event.target.closest('[data-decision]');
    if (!button) return;
    button.disabled = true;
    try {
      await api(`/api/approvals/${encodeURIComponent(button.dataset.id)}/${button.dataset.decision}`, {method:'POST'});
      if (button.dataset.decision === 'approve' && button.dataset.runId) {
        const run = await api(`/api/runs/${encodeURIComponent(button.dataset.runId)}/resume`, {method:'POST', body:'{}'});
        message(`Approval recorded. Run ${text(run?.status, 'resumed')}.`, false);
      } else message(button.dataset.decision === 'deny' ? 'Denial recorded. The run was not resumed.' : 'Approval recorded.', false);
      await Promise.all([loadRuns(), loadApprovals()]);
    } catch (error) { message(`Could not record this decision: ${error.message}`, true); button.disabled = false; }
  });
  q('#task-agent').addEventListener('change', () => { q('#task-form').dataset.agentId = q('#task-agent').value; });
  q('#task-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    const form = event.currentTarget;
    const input = q('#task-input').value.trim();
    const id = form.dataset.agentId || q('#task-agent').value;
    if (!input || !id) return;
    const submit = q('#start-task-button');
    submit.disabled = true;
    try {
      const run = await api(`/api/agents/${encodeURIComponent(id)}/runs`, {method:'POST', body:JSON.stringify({input})});
      q('#task-input').value = '';
      message(`Run ${text(run.id)} finished with status ${text(run.status, 'unknown')}.`, run.status === 'failed');
      await Promise.all([loadRuns(), loadApprovals()]);
      if (run.id) await runDetails(run.id);
    } catch (error) { message(`Could not start the task: ${error.message}`, true); }
    finally { submit.disabled = false; }
  });
  pairForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const submit = q('button[type="submit"]', pairForm);
    submit.disabled = true;
    try {
      const result = await api('/api/devices/pair', {method:'POST', body:JSON.stringify({secret:pairingSecret, name:pairNameInput.value.trim()})});
      if (!result?.token) throw new Error('The host did not return device access. Request a new pairing link.');
      token = result.token;
      pairedDevice = true;
      pairingSecret = '';
      sessionStorage.setItem(TOKEN_KEY, token);
      sessionStorage.setItem(PAIRED_KEY, 'true');
      pairPanel.hidden = true;
      q('#forget-current-device').hidden = false;
      await refresh();
      message(`This browser is paired${result.device?.name ? ` as ${result.device.name}` : ''}.`, false);
    } catch (error) { message(`Could not pair this browser: ${error.message}`, true); submit.disabled = false; }
  });
  async function loadRecommendations() {
    if (recommendationsLoaded) return;
    recommendationsLoaded = true;
    const status = q('#recommendations-status');
    if (!token) { status.textContent = 'Hardware recommendation unavailable until this browser is connected to the host.'; return; }
    try {
      const data = await api('/api/setup/recommendations');
      const bytes = Number(data?.host_memory_bytes ?? data?.memory_bytes);
      const gib = Number(data?.host_memory_gib ?? data?.memory_gib ?? (bytes > 0 ? bytes / (1024 ** 3) : 0));
      const suggestions = Array.isArray(data?.suggestions) ? data.suggestions : [];
      if (!(gib > 0) || suggestions.length === 0) {
        status.textContent = 'Hardware recommendation unavailable. Run ownbot inspect on the host for current system details.';
        return;
      }
      status.textContent = `Host memory reported: ${gib.toFixed(0)} GB.`;
      q('#recommendations-list').innerHTML = suggestions.map((item, index) => `<li>
        <button type="button" data-recommendation-index="${index}">${escapeHTML(text(item.model, 'Model suggestion'))}</button>
        <details><summary>Test details</summary>
          ${item.reason ? `<p>${escapeHTML(item.reason)}</p>` : ''}
          ${item.evidence ? `<a class="recommendation-evidence" href="${escapeHTML(item.evidence)}" target="_blank" rel="noopener noreferrer">View test</a>` : ''}
        </details>
      </li>`).join('');
      q('#recommendation-command').hidden = true;
      q('#recommendation-command').innerHTML = '';
      q('#recommendations-list').onclick = (event) => {
        const button = event.target.closest('[data-recommendation-index]');
        if (!button) return;
        const suggestion = suggestions[Number(button.dataset.recommendationIndex)];
        if (!suggestion?.install_command) return;
        const command = q('#recommendation-command');
        const origin = window.location.origin || 'http://127.0.0.1:8765';
        const defaultOrigin = origin === 'http://127.0.0.1:8765' || origin === 'http://localhost:8765';
        const setupCommand = `ownbot setup${loopback && !defaultOrigin ? ` --url ${origin}` : ''} --model ${text(suggestion.model)}`;
        command.innerHTML = `<p>Run this in the host terminal:</p><p><a href="https://ollama.com/download" target="_blank" rel="noopener noreferrer">Install Ollama</a> first if needed.</p><code class="recommendation-command">${escapeHTML(suggestion.install_command)}<br>${escapeHTML(setupCommand)}</code>`;
        command.hidden = false;
      };
    } catch {
      status.textContent = 'Hardware recommendation unavailable. Run ownbot inspect on the host for current system details.';
    }
  }
  const setup = () => {
    if (!settingsLessSetup.open) settingsLessSetup.showModal();
  };
  q('#setup-toggle').addEventListener('click', setup);
  q('#setup-close').addEventListener('click', () => settingsLessSetup.close());
  settingsLessSetup.addEventListener('click', (event) => { if (event.target === settingsLessSetup) settingsLessSetup.close(); });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && !settingsLessSetup.open && history.classList.contains('history-open')) setHistoryOpen(false);
  });

  q('#forget-current-device').hidden = !pairedDevice || !token;
  showConnection(false);
  if (pairingSecret) {
    pairPanel.hidden = false;
    pairNameInput.focus({preventScroll:true});
  }
  (async () => {
    if (bootstrapTicket) {
      try { await redeemBootstrap(); }
      catch (error) { showConnection(true); message(error.message, true); }
    }
    if (pairingSecret) return;
    await refresh();
  })();
})();
