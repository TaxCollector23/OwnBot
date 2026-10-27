#!/usr/bin/env node
// Dependency-free regression tests for LAN pairing and local device management.
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const vm = require('node:vm');

const source = readFileSync(resolve(__dirname, '../web/dashboard.js'), 'utf8');

function harness({hostname = '192.168.1.18', hash = '', storageSeed = {}, failPairing = false, failRevoke = false, rejectToken = false} = {}) {
  const elements = new Map();
  const requests = [];
  const storage = new Map(Object.entries(storageSeed));
  const location = {hostname, hash, pathname:'/dashboard', search:''};
  const history = {replaced:'', replaceState(_state, _title, url) { this.replaced = url; }};
  function element(selector) {
    if (!elements.has(selector)) {
      const handlers = new Map();
      const classes = new Set();
      elements.set(selector, {
        value:'', textContent:'', innerHTML:'', hidden:false, className:'', disabled:false, dataset:{}, handlers,
        classList:{toggle(name, enabled){if(enabled)classes.add(name);else classes.delete(name);}, contains(name){return classes.has(name);}},
        addEventListener(type, handler){handlers.set(type, handler);}, focus(){}, reset(){this.value='';}, querySelector(selector){return element(selector);},
        closest(){return null;},
      });
    }
    return elements.get(selector);
  }
  const document = {querySelector:element, querySelectorAll:()=>[], addEventListener(){}};
  ['#pair-panel', '#pairing-link-panel', '#token-panel', '#access-settings', '#pair-result'].forEach((selector) => { element(selector).hidden = true; });
  const sessionStorage = {
    getItem(key){return storage.get(key) || null;},
    setItem(key, value){storage.set(key, String(value));},
    removeItem(key){storage.delete(key);},
  };
  async function fetch(path, options = {}) {
    const method = options.method || 'GET';
    const body = options.body ? JSON.parse(options.body) : null;
    requests.push({path, method, body});
    let status = 200;
    let data = [];
    if (path === '/api/devices/pair' && method === 'POST') {
      if (body.secret !== 'once-secret' || body.name !== 'Kitchen tablet') { status = 400; data = {error:'invalid pairing secret'}; }
      else data = {token:'paired-token', device:{id:'d2', name:body.name}};
    } else if (path === '/api/access') data = {enabled:true, urls:['https://192.168.1.18:8766/dashboard'], certificate_fingerprint:'AA:BB'};
    else if (path === '/api/devices' && method === 'GET') data = [{id:'d1', name:'Desk', created_at:'2026-10-27T10:00:00Z', revoked_at:null}];
    else if (path === '/api/devices/pairing' && method === 'POST') {
      if (failPairing) {status = 500; data = {error:'pairing unavailable'};}
      else data = {pairing_url:'https://192.168.1.18:8766/dashboard#pair=new-secret', expires_at:'2026-10-27T12:00:00Z', certificate_fingerprint:'AA:BB'};
    } else if (path === '/api/devices/d1' && method === 'DELETE') {
      if (failRevoke) {status = 500; data = {error:'revocation unavailable'};}
      else data = null;
    }
    else if (path === '/api/status') data = {ok:true, api_token_required:true};
    if (rejectToken && path !== '/api/status') {status = 401; data = {error:'unauthorized'};}
    return {ok:status >= 200 && status < 300, status, async text(){return data === null ? '' : JSON.stringify(data);}};
  }
  const window = {location, history};
  vm.runInNewContext(source, {document, sessionStorage, fetch, Headers, URLSearchParams, window, navigator:{clipboard:{writeText:async()=>{}}}, console});
  return {element, storage, requests, location, history};
}

const pause = () => new Promise((resolve) => setImmediate(resolve));

async function main() {
  const pairing = harness({hash:'#pair=once-secret'});
  assert.equal(pairing.history.replaced, '/dashboard', 'pair secret is removed from the address immediately');
  assert.equal(pairing.element('#pair-panel').hidden, false, 'pair form opens for a pairing link');
  assert.equal(pairing.requests.length, 0, 'pairing page makes no API requests before name submission');
  pairing.element('#pair-device-name').value = 'Kitchen tablet';
  await pairing.element('#pair-form').handlers.get('submit')({preventDefault(){}});
  const exchange = pairing.requests.find(({path, method}) => path === '/api/devices/pair' && method === 'POST');
  assert.deepEqual(exchange.body, {secret:'once-secret', name:'Kitchen tablet'});
  assert.equal(pairing.storage.get('ownbot.api-token'), 'paired-token', 'device token uses the existing tab-scoped token key');
  assert.equal(pairing.storage.get('ownbot.paired-device'), 'true', 'browser remembers that this token came from pairing');
  assert.equal(pairing.element('#pair-panel').hidden, true, 'pair form closes after token exchange');
  assert.equal(pairing.element('#forget-current-device').hidden, false, 'paired browser can forget its token from the dashboard header');

  const invalidPair = harness({hash:'#pair=expired-secret'});
  invalidPair.element('#pair-device-name').value = 'Kitchen tablet';
  await invalidPair.element('#pair-form').handlers.get('submit')({preventDefault(){}});
  assert.match(invalidPair.element('#page-message').textContent, /Could not pair this device/i);
  assert.equal(invalidPair.storage.has('ownbot.api-token'), false, 'failed pairing does not store a device token');

  const rejected = harness({storageSeed:{'ownbot.api-token':'old-pair-token','ownbot.paired-device':'true'}, rejectToken:true});
  await pause();
  assert.equal(rejected.element('#paired-token-copy').hidden, false, 'rejected paired device sees the re-pair message');
  assert.equal(rejected.element('#owner-token-copy').hidden, true, 'paired browser is never asked for the owner API token');
  assert.equal(rejected.element('#token-form').hidden, true, 'owner token form is not shown to a paired browser');
  assert.match(rejected.element('#page-message').textContent, /Pair it again from the host/i);

  const remote = harness();
  await pause();
  assert.equal(remote.element('#token-form').hidden, true, 'remote browser is never offered the owner API token form');
  assert.equal(remote.element('#remote-token-copy').hidden, false, 'unpaired remote browser is told to use a pairing link');
  assert.equal(remote.element('#access-settings').hidden, true, 'remote paired device cannot manage access or devices');

  const owner = harness({hostname:'localhost', storageSeed:{'ownbot.api-token':'owner-token'}});
  await pause();
  assert.equal(owner.element('#access-settings').hidden, false, 'device management is visible on loopback');
  assert.match(owner.element('#devices-list').innerHTML, /Desk/);
  await owner.element('#create-pairing-button').handlers.get('click')();
  assert.equal(owner.element('#pairing-link').value, 'https://192.168.1.18:8766/dashboard#pair=new-secret');
  const revokeButton = {dataset:{revokeDevice:'d1'}, disabled:false};
  owner.element('#devices-list').handlers.get('click')({target:{closest:(selector)=>selector === '[data-revoke-device]' ? revokeButton : null}});
  await pause();
  assert.ok(owner.requests.some(({path, method}) => path === '/api/devices/d1' && method === 'DELETE'), 'revoke calls the device delete endpoint');

  const revokeError = harness({hostname:'localhost', storageSeed:{'ownbot.api-token':'owner-token'}, failRevoke:true});
  await pause();
  const failedRevokeButton = {dataset:{revokeDevice:'d1'}, disabled:false};
  await revokeError.element('#devices-list').handlers.get('click')({target:{closest:(selector)=>selector === '[data-revoke-device]' ? failedRevokeButton : null}});
  assert.match(revokeError.element('#page-message').textContent, /Could not revoke device/i);

  const failed = harness({hostname:'localhost', storageSeed:{'ownbot.api-token':'owner-token'}, failPairing:true});
  await pause();
  await failed.element('#create-pairing-button').handlers.get('click')();
  assert.match(failed.element('#page-message').textContent, /Could not create pairing link/i);
  assert.equal(failed.element('#pairing-link-panel').hidden, true, 'failed generation does not display an old or empty credential');

  process.stdout.write('dashboard LAN regression: PASS (fragment clearing, one-time exchange, rejected paired token, local pairing and revoke, pairing errors)\n');
}

main().catch((error) => {
  process.stderr.write(`dashboard LAN regression: FAIL: ${error.stack || error.message}\n`);
  process.exitCode = 1;
});
