#!/usr/bin/env node
// Dependency-free regression test for the dashboard's API token submit flow.
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const vm = require('node:vm');

const source = readFileSync(resolve(__dirname, '../web/dashboard.js'), 'utf8');

function createHarness(validToken) {
  const elements = new Map();
  const documentListeners = new Map();
  const storage = new Map();

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
    const status = path === '/api/status';
    const authorized = options.headers.get('Authorization') === `Bearer ${validToken}`;
    const code = status ? 200 : authorized ? 200 : 401;
    const body = status
      ? JSON.stringify({ok:true, version:'test', api_token_required:true})
      : code === 200 ? '[]' : 'unauthorized';
    return {
      ok: code >= 200 && code < 300,
      status: code,
      async text() { return body; },
    };
  };

  vm.runInNewContext(source, { document, sessionStorage, fetch, Headers, console });
  return {
    element,
    storage,
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
  assert.match(bad.element('#page-message').textContent, /rejected this token/i);
  assert.doesNotMatch(bad.element('#page-message').textContent, /connected to the local api/i);
  assert.equal(bad.element('#token-panel').hidden, false, 'token panel remains visible after rejection');
  assert.equal(bad.storage.has('ownbot.api-token'), false, 'rejected token is removed from session storage');

  const good = createHarness('valid-token');
  await new Promise((resolve) => setImmediate(resolve));
  await good.submitToken('valid-token');
  assert.match(good.element('#page-message').textContent, /connected to the local api/i);
  assert.equal(good.element('#token-panel').hidden, true, 'token panel closes after successful authorization');
  assert.equal(good.storage.get('ownbot.api-token'), 'valid-token', 'valid token remains scoped to the browser tab');

  process.stdout.write('dashboard auth regression: PASS (wrong token rejected; valid token accepted)\n');
}

main().catch((error) => {
  process.stderr.write(`dashboard auth regression: FAIL: ${error.stack || error.message}\n`);
  process.exitCode = 1;
});
