#!/usr/bin/env node
// Dependency-free regression checks for the task, history, and approval surface.
const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const {resolve} = require('node:path');
const vm = require('node:vm');

const source = readFileSync(resolve(__dirname, '../web/dashboard.js'), 'utf8');

function harness() {
  const elements = new Map();
  const requests = [];
  let agents = [];
  let runs = [];
  let approvals = [{id:'ap1',run_id:'r1',agent_id:'a1',tool:'calendar.write',status:'pending',reason:'Create event',args:{title:'Review'}}];
  const storage = new Map([['ownbot.api-token','token']]);
  function element(selector) {
    if (!elements.has(selector)) {
      const handlers = new Map();
      elements.set(selector,{value:'',textContent:'',innerHTML:'',hidden:false,disabled:false,dataset:{},handlers,
        classList:{toggle(){}},addEventListener(type,handler){handlers.set(type,handler);},querySelector(child){return element(child);},showModal(){this.open=true;},close(){this.open=false;}});
    }
    return elements.get(selector);
  }
  const document={querySelector:element,querySelectorAll:()=>[],addEventListener(){}};
  const sessionStorage={getItem:(key)=>storage.get(key)||null,setItem:(key,value)=>storage.set(key,String(value)),removeItem:(key)=>storage.delete(key)};
  async function fetch(path,options={}) {
    const method=options.method||'GET';
    requests.push({path,method,body:options.body?JSON.parse(options.body):null});
    let data=[];
    if(path==='/api/status') data={ok:true,api_token_required:true};
    else if(path==='/api/agents'&&method==='GET') data=agents;
    else if(path==='/api/setup/recommendations'&&method==='GET') data={host_memory_bytes:16*1024**3,suggestions:[{model:'qwen3:1.7b',install_command:'ollama pull qwen3:1.7b',reason:'Tested candidate'}]};
    else if(path==='/api/runs'&&method==='GET') data=runs;
    else if(path==='/api/approvals'&&method==='GET') data=approvals;
    else if(path==='/api/agents/a1/runs'&&method==='POST') {data={id:'r2',agent_id:'a1',status:'completed',prompt_version:3};runs=[{id:'r2',agent_id:'a1',status:'completed',prompt_version:3}];}
    else if(path==='/api/runs/r2') data={id:'r2',status:'completed',prompt_version:3,output:'Fixture result.',steps:1,messages:[{role:'assistant',content:'Fixture result.'}]};
    else if(path==='/api/approvals/ap1/approve'&&method==='POST') {approvals=[];data={id:'ap1',status:'approved'};}
    else if(path==='/api/runs/r1/resume'&&method==='POST') data={id:'r1',status:'completed',prompt_version:2};
    const body=JSON.stringify(data);
    return {ok:true,status:200,async text(){return body;}};
  }
  vm.runInNewContext(source,{document,sessionStorage,fetch,Headers,URLSearchParams,window:{location:{hostname:'127.0.0.1',hash:'',pathname:'/dashboard',search:''},history:{replaceState(){}}},console});
  return {element,requests,setAgents(value){agents=value;},setApprovals(value){approvals=value;}};
}

const pause=()=>new Promise((resolve)=>setImmediate(resolve));
async function main(){
  const app=harness();
  await pause();
  const startup=app.requests.map(({path})=>path);
  assert.deepEqual(startup.sort(),['/api/agents','/api/approvals','/api/runs','/api/setup/recommendations'].sort(),'startup reads task data and host recommendation only while setup is empty');
  assert.equal(app.element('#composer-empty').hidden,false,'empty setup prompts for CLI setup');
  assert.match(app.element('#recommendations-list').innerHTML,/qwen3:1.7b/,'hardware recommendation is shown in the empty state');
  for(const forbidden of ['/api/models','/api/mcp/servers','/api/tools','/api/search','/api/audit','/api/automations']) assert.equal(startup.includes(forbidden),false,`${forbidden} is absent from startup`);

  app.setAgents([{id:'a1',name:'Assistant',enabled:true,prompt_version:3}]);
  await app.element('#refresh-button').handlers.get('click')();
  assert.equal(app.element('#task-form').hidden,false,'an enabled agent unlocks task entry');
  assert.equal(app.element('#task-agent').hidden,true,'a single enabled agent needs no selector');
  app.setAgents([{id:'a1',name:'Assistant',enabled:true},{id:'a2',name:'Writer',enabled:true}]);
  await app.element('#refresh-button').handlers.get('click')();
  assert.equal(app.element('#task-agent').hidden,false,'multiple agents reveal the selector');
  assert.match(app.element('#task-agent').innerHTML,/Assistant/);
  assert.match(app.element('#task-agent').innerHTML,/Writer/);

  const form=app.element('#task-form');
  form.dataset.agentId='a1';
  app.element('#task-input').value='Do one task';
  await form.handlers.get('submit')({preventDefault(){},currentTarget:form});
  assert.ok(app.requests.some(({path,method,body})=>path==='/api/agents/a1/runs'&&method==='POST'&&body.input==='Do one task'));
  assert.match(app.element('#run-detail').innerHTML,/Fixture result/);
  assert.match(app.element('#run-detail').innerHTML,/Prompt version 3/);

  const approve={dataset:{id:'ap1',runId:'r1',decision:'approve'},disabled:false};
  await app.element('#approvals-list').handlers.get('click')({target:{closest:(selector)=>selector==='[data-decision]'?approve:null}});
  const decide=app.requests.findIndex(({path})=>path==='/api/approvals/ap1/approve');
  const resume=app.requests.findIndex(({path})=>path==='/api/runs/r1/resume');
  assert.ok(decide>=0&&resume>decide,'approval is recorded before its run resumes');
  assert.match(app.element('#page-message').textContent,/Approval recorded\. Run completed/);
  process.stdout.write('dashboard runtime regression: PASS (minimal reads, CLI setup state, task selection, run history, prompt-version display, approval resume)\n');
}
main().catch((error)=>{process.stderr.write(`dashboard runtime regression: FAIL: ${error.stack||error.message}\n`);process.exitCode=1;});
