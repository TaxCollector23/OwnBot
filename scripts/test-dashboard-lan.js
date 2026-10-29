#!/usr/bin/env node
// Dependency-free regression checks for pairing fragments and paired-session handling.
const assert=require('node:assert/strict');
const {readFileSync}=require('node:fs');
const {resolve}=require('node:path');
const vm=require('node:vm');

const source=readFileSync(resolve(__dirname,'../web/dashboard.js'),'utf8');
function harness({hostname='192.168.1.18',hash='',storageSeed={},pairStatus=201,rejectToken=false}={}){
  const elements=new Map();
  const requests=[];
  const storage=new Map(Object.entries(storageSeed));
  const location={hostname,hash,pathname:'/dashboard',search:''};
  const history={replaced:'',replaceState(_state,_title,url){this.replaced=url;location.hash='';}};
  function element(selector){
    if(!elements.has(selector)){
      const handlers=new Map();
      elements.set(selector,{value:'',textContent:'',innerHTML:'',hidden:false,disabled:false,dataset:{},handlers,classList:{toggle(){}},
        addEventListener(type,handler){handlers.set(type,handler);},focus(){},querySelector(child){return element(child);},showModal(){this.open=true;},close(){this.open=false;}});
    }
    return elements.get(selector);
  }
  const document={querySelector:element,querySelectorAll:()=>[],addEventListener(){}};
  const sessionStorage={getItem:key=>storage.get(key)||null,setItem:(key,value)=>storage.set(key,String(value)),removeItem:key=>storage.delete(key)};
  function response(status,data){return {ok:status>=200&&status<300,status,async text(){return JSON.stringify(data);}};}
  async function fetch(path,options={}){
    const method=options.method||'GET';
    const body=options.body?JSON.parse(options.body):null;
    requests.push({path,method,body});
    if(path==='/api/devices/pair'&&method==='POST') return pairStatus===201?response(201,{token:'paired-token',device:{id:'phone',name:body.name}}):response(pairStatus,{error:'invalid pairing link'});
    if(path==='/api/status') return response(200,{ok:true,api_token_required:true});
    if(rejectToken) return response(401,{error:'unauthorized'});
    if(method==='GET'&&['/api/agents','/api/runs','/api/approvals'].includes(path)) return response(200,[]);
    return response(200,{});
  }
  vm.runInNewContext(source,{document,sessionStorage,fetch,Headers,URLSearchParams,window:{location,history},console});
  return {element,storage,requests,location,history};
}
const pause=()=>new Promise((resolve)=>setImmediate(resolve));
async function main(){
  const pairing=harness({hash:'#pair=once-secret'});
  assert.equal(pairing.history.replaced,'/dashboard','pair secret is removed from the address before use');
  assert.equal(pairing.element('#pair-panel').hidden,false,'pair name form opens for a pairing link');
  assert.equal(pairing.requests.length,0,'unpaired browser makes no protected reads');
  pairing.element('#pair-device-name').value='Kitchen tablet';
  await pairing.element('#pair-form').handlers.get('submit')({preventDefault(){}});
  assert.deepEqual(pairing.requests.find(({path})=>path==='/api/devices/pair').body,{secret:'once-secret',name:'Kitchen tablet'});
  assert.equal(pairing.storage.get('ownbot.api-token'),'paired-token');
  assert.equal(pairing.storage.get('ownbot.paired-device'),'true');
  assert.equal(pairing.element('#pair-panel').hidden,true);
  assert.equal(pairing.element('#forget-current-device').hidden,false);

  const expiredPair=harness({hash:'#pair=expired-secret',pairStatus:401});
  expiredPair.element('#pair-device-name').value='Kitchen tablet';
  await expiredPair.element('#pair-form').handlers.get('submit')({preventDefault(){}});
  assert.match(expiredPair.element('#page-message').textContent,/Could not pair this browser/i);
  assert.equal(expiredPair.storage.has('ownbot.api-token'),false);

  const rejected=harness({storageSeed:{'ownbot.api-token':'old-pair-token','ownbot.paired-device':'true'},rejectToken:true});
  await pause();
  assert.equal(rejected.storage.has('ownbot.api-token'),false,'rejected paired token is removed');
  assert.equal(rejected.element('#token-panel').hidden,false);
  assert.equal(rejected.element('#paired-token-copy').hidden,false);
  assert.equal(rejected.element('#forget-device-button').hidden,false);
  assert.equal(rejected.element('#api-token').handlers.size,0,'no manual token form is present');

  const remote=harness();
  await pause();
  assert.equal(remote.element('#token-panel').hidden,false);
  assert.equal(remote.element('#remote-token-copy').hidden,false,'remote browser is directed to host pairing');
  assert.equal(remote.element('#api-token').handlers.size,0);
  const owner=harness({hostname:'localhost'});
  await pause();
  assert.equal(owner.element('#owner-token-copy').hidden,false,'local host guidance uses the daemon command');
  process.stdout.write('dashboard LAN regression: PASS (pair fragment, one-time exchange, rejection, paired recovery, no settings or manual token form)\n');
}
main().catch((error)=>{process.stderr.write(`dashboard LAN regression: FAIL: ${error.stack||error.message}\n`);process.exitCode=1;});
