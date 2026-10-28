#!/usr/bin/env node
// Dependency-free regression test for the dashboard's API token submit flow.
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const vm = require('node:vm');

const source = readFileSync(resolve(__dirname, '../web/dashboard.js'), 'utf8');

function createHarness(validToken, fragment = '', rejectBootstrap = false) {
  const elements = new Map();
  const documentListeners = new Map();
  const storage = new Map();
  const requests = [];
  const fakeWindow = {
    location: {hostname: '127.0.0.1', hash: fragment, pathname: '/dashboard', search: ''},
    history: {replaceState(_state, _title, url) { fakeWindow.location.hash = ''; fakeWindow.replacedURL = url; }},
  };

  function element(selector) {
    if (!elements.has(selector)) {
      const handlers = new Map();
      const classes = new Set();
      elements.set(selector, {
        value: '', textContent: '', innerHTML: '', hidden: false, className: '',
        disabled: false, dataset: {}, handlers,
        classList: {
          toggle(name, enabled) { if (enabled) classes.add(name); else classes.delete(name); },
          contains(name) { return classes.has(name); },
        },
        addEventListener(type, handler) { handlers.set(type, handler); },
        focus() {},
        reset() { this.value = ''; },
      });
    }
    return elements.get(selector);
  }

  const document = {
    querySelector: element,
    querySelectorAll: () => [],
    addEventListener(type, handler) { documentListeners.set(type, handler); },
  };
  const sessionStorage = {
    getItem(key) { return storage.get(key) || null; },
    setItem(key, value) { storage.set(key, String(value)); },
    removeItem(key) { storage.delete(key); },
  };
  const fetch = async (path, options = {}) => {
    requests.push({path, options});
    const status = path === '/api/status';
    const exchange = path === '/api/access/bootstrap/exchange';
    const auth = options.headers?.get?.('Authorization') || '';
    const authorized = auth === `Bearer ${validToken}`;
    if (exchange) {
      assert.equal(auth, '', 'bootstrap exchange never sends an existing token');
      assert.equal(JSON.parse(options.body).ticket, 'fixture-bootstrap-ticket');
      if (rejectBootstrap) {
        return response(401, JSON.stringify({error:'owner session link is invalid or expired'}));
      }
      return response(201, JSON.stringify({token: validToken, expires_at: new Date(Date.now() + 30 * 60 * 1000).toISOString()}));
    }
    if (path === '/api/access' && authorized) return response(200, JSON.stringify({enabled:true, urls:['https://10.0.0.2:8766/dashboard']}));
    const code = status ? 200 : authorized ? 200 : 401;
    const body = status
      ? JSON.stringify({ok:true, service:'ownbot', version:'test', api_token_required:true})
      : code === 200 ? '[]' : 'unauthorized';
    return response(code, body);
  };

  function response(status, body) {
    return {ok:status >= 200 && status < 300, status, async text() { return body; }, async json() { return JSON.parse(body); }};
  }

  vm.runInNewContext(source, { document, sessionStorage, fetch, Headers, URLSearchParams, window: fakeWindow, console });
  return {
    element,
    storage,
    requests,
    window: fakeWindow,
    async submitToken(value) {
      element('#api-token').value = value;
      const submit = element('#token-form').handlers.get('submit');
      assert.equal(typeof submit, 'function', 'token form submit handler is registered');
      await submit({ preventDefault() {} });
    },
  };
}

async function main() {
  const bad = createHarness('valid-token');
  await new Promise((resolve) => setImmediate(resolve));
  await bad.submitToken('wrong-token');
  assert.match(bad.element('#page-message').textContent, /expired or was rejected/i);
  assert.doesNotMatch(bad.element('#page-message').textContent, /connected to the local api/i);
  assert.equal(bad.element('#token-panel').hidden, false, 'token panel remains visible after rejection');
  assert.equal(bad.storage.has('ownbot.api-token'), false, 'rejected token is removed from session storage');

  const good = createHarness('valid-token');
  await new Promise((resolve) => setImmediate(resolve));
  await good.submitToken('valid-token');
  assert.match(good.element('#page-message').textContent, /connected to the local api/i);
  assert.equal(good.element('#token-panel').hidden, true, 'token panel closes after successful authorization');
  assert.equal(good.storage.get('ownbot.api-token'), 'valid-token', 'valid token remains scoped to the browser tab');

  const bootstrap = createHarness('short-lived-owner-session', '#bootstrap=fixture-bootstrap-ticket');
  await new Promise((resolve) => setImmediate(resolve));
  const exchange = bootstrap.requests.find((request) => request.path === '/api/access/bootstrap/exchange');
  assert.ok(exchange, 'fragment ticket is exchanged during page startup');
  assert.equal(bootstrap.window.replacedURL, '/dashboard', 'bootstrap fragment is removed before API calls');
  assert.equal(bootstrap.storage.get('ownbot.api-token'), 'short-lived-owner-session', 'only the returned local session is stored for this tab');
  assert.equal(bootstrap.element('#create-pairing-button').disabled, false, 'owner session unlocks local pairing controls');

  const expiredBootstrap = createHarness('unused-session', '#bootstrap=fixture-bootstrap-ticket', true);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(expiredBootstrap.window.replacedURL, '/dashboard', 'expired ticket is removed from the address before exchange');
  assert.match(expiredBootstrap.element('#page-message').textContent, /invalid or expired/i, 'expired link explains the failure');
  assert.match(expiredBootstrap.element('#page-message').textContent, /Run ownbot daemon start/i, 'expired link explains how to get a fresh session');
  assert.equal(expiredBootstrap.storage.has('ownbot.api-token'), false, 'expired ticket never stores an owner token');
  assert.equal(expiredBootstrap.element('#access-state').textContent, 'Locked', 'expired ticket leaves Access locked');
  assert.equal(expiredBootstrap.element('#create-pairing-button').disabled, true, 'expired ticket cannot unlock pairing controls');

  process.stdout.write('dashboard auth regression: PASS (wrong token rejected; valid token and one-time local bootstrap accepted)\n');
}

main().catch((error) => {
  process.stderr.write(`dashboard auth regression: FAIL: ${error.stack || error.message}\n`);
  process.exitCode = 1;
});
