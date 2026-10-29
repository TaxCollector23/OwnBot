#!/usr/bin/env node
// Dependency-free regression checks for one-time owner sessions and rejected paired sessions.
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const {resolve} = require('node:path');
const vm = require('node:vm');

const source = readFileSync(resolve(__dirname, '../web/dashboard.js'), 'utf8');

function createHarness(validToken, fragment = '', rejectBootstrap = false, storageSeed = {}) {
  const elements = new Map();
  const requests = [];
  const storage = new Map(Object.entries(storageSeed));
  const fakeWindow = {
    location:{hostname:'127.0.0.1',hash:fragment,pathname:'/dashboard',search:''},
    history:{replaceState(_state,_title,url){fakeWindow.location.hash='';fakeWindow.replacedURL=url;}},
  };
  function element(selector) {
    if (!elements.has(selector)) {
      const handlers = new Map();
      elements.set(selector, {value:'',textContent:'',innerHTML:'',hidden:false,className:'',disabled:false,dataset:{},handlers,
        classList:{toggle(){}},addEventListener(type,handler){handlers.set(type,handler);},focus(){},querySelector(child){return element(child);}});
    }
    return elements.get(selector);
  }
  const document = {querySelector:element,querySelectorAll:()=>[],addEventListener(){}};
  const sessionStorage = {
    getItem(key){return storage.get(key)||null;},setItem(key,value){storage.set(key,String(value));},removeItem(key){storage.delete(key);},
  };
  function response(status, data) {
    const body = JSON.stringify(data);
    return {ok:status>=200&&status<300,status,async text(){return body;},async json(){return JSON.parse(body);}};
  }
  async function fetch(path, options = {}) {
    requests.push({path,options});
    const exchange = path === '/api/access/bootstrap/exchange';
    const auth = options.headers?.get?.('Authorization') || '';
    const authorized = auth === `Bearer ${validToken}`;
    if (exchange) {
      assert.equal(auth,'','bootstrap exchange never sends an existing token');
      assert.equal(JSON.parse(options.body).ticket,'fixture-bootstrap-ticket');
      return rejectBootstrap ? response(401,{error:'expired'}) : response(201,{token:validToken});
    }
    if (path === '/api/status') return response(200,{ok:true,api_token_required:true});
    return authorized ? response(200,[]) : response(401,{error:'unauthorized'});
  }
  vm.runInNewContext(source,{document,sessionStorage,fetch,Headers,URLSearchParams,window:fakeWindow,console});
  return {element,storage,requests,window:fakeWindow};
}

const pause = () => new Promise((resolve) => setImmediate(resolve));

async function main() {
  const bootstrap = createHarness('short-lived-owner-session','#bootstrap=fixture-bootstrap-ticket');
  await pause();
  assert.equal(bootstrap.window.replacedURL,'/dashboard','bootstrap fragment is removed before requests');
  assert.equal(bootstrap.storage.get('ownbot.api-token'),'short-lived-owner-session','only the returned browser session is stored');
  assert.deepEqual(bootstrap.requests.map((request)=>request.path).filter((path)=>path.startsWith('/api/')).sort(),[
    '/api/access/bootstrap/exchange','/api/agents','/api/approvals','/api/runs','/api/setup/recommendations',
  ].sort(),'startup loads only task-control data');
  assert.equal(bootstrap.element('#token-panel').hidden,true);
  assert.equal(bootstrap.element('#api-token').handlers.size,0,'manual token entry is absent');

  const expired = createHarness('unused-session','#bootstrap=fixture-bootstrap-ticket',true);
  await pause();
  assert.equal(expired.window.replacedURL,'/dashboard');
  assert.match(expired.element('#page-message').textContent,/one-time owner link expired/i);
  assert.equal(expired.storage.has('ownbot.api-token'),false);
  assert.equal(expired.element('#token-panel').hidden,false);

  const paired = createHarness('different-token','',false,{'ownbot.api-token':'old-device-token','ownbot.paired-device':'true'});
  await pause();
  assert.equal(paired.storage.has('ownbot.api-token'),false,'rejected paired session is removed');
  assert.equal(paired.element('#token-panel').hidden,false);
  assert.equal(paired.element('#paired-token-copy').hidden,false);
  assert.equal(paired.element('#forget-device-button').hidden,false,'paired browser can clear its stored identity');

  process.stdout.write('dashboard auth regression: PASS (one-time bootstrap, expired link, rejected paired session, no manual token form, minimal startup reads)\n');
}

main().catch((error)=>{process.stderr.write(`dashboard auth regression: FAIL: ${error.stack||error.message}\n`);process.exitCode=1;});
