const {test,expect, freshBootstrap}=require('../../../../scripts/browser-lab/daemon-fixture.cjs');
const {startFixtures}=require('../../../../scripts/sandbox/fixtures.cjs');
const fs=require('node:fs');const path=require('node:path');const {execFileSync}=require('node:child_process');
const out=path.join(__dirname,'output');
test('record whiteboard and real sandbox approval',async({browser,daemon})=>{
 const fixture=await startFixtures();
 const api=async(route,body,method=body?'POST':'GET')=>{const r=await fetch(daemon.baseURL+route,{method,headers:{Authorization:`Bearer ${daemon.ownerToken}`,'Content-Type':'application/json'},...(body?{body:JSON.stringify(body)}:{})});if(!r.ok)throw Error(route+' '+r.status);return r.status===204?null:r.json()};
 const context=await browser.newContext({viewport:{width:1280,height:720},recordVideo:{dir:out,size:{width:1280,height:720}}});const page=await context.newPage();const video=page.video();const begin=Date.now();const timeline=[];
 try{
 const model=await api('/api/models',{provider:'ollama',model:fixture.model.model,base_url:fixture.model.baseURL});
 const probe=await api(`/api/models/${model.id}/probe`,{});expect(probe.probe.score).toBe(100);
 const agent=await api('/api/agents',{name:'Sandbox test agent · simulated model',model:model.id,enabled:true,max_steps:8,max_tokens:4096,timeout_seconds:120});
 const server=await api('/api/mcp/servers',{name:'Sandbox notes only',transport:'streamable_http',endpoint:fixture.mcp.endpoint,allowed_tools:['create_note'],read_tools:[],write_tools:['create_note']});await api(`/api/mcp/servers/${server.id}/connect`,{});
 const tool=(await api('/api/tools')).find(t=>t.provider_tool==='create_note');expect(tool).toBeTruthy();await api(`/api/agents/${agent.id}`,{mcp_enabled:true,tools:[tool.name],mcp_servers:[server.id]},'PATCH');
 const story=JSON.parse(fs.readFileSync(path.join(out,'timing.json')));
 for(let i=0;i<story.length;i++){
 const scene=story[i];const start=Date.now();timeline.push({index:i,start:(start-begin)/1000,duration:scene.duration});
 if(i===4){
  const ticket=await freshBootstrap(daemon);await page.goto(`${daemon.baseURL}/dashboard#bootstrap=${encodeURIComponent(ticket)}`);
  await expect(page.locator('#task-input')).toBeVisible();
  await page.locator('#task-input').fill('Create a note about testing OwnBot locally.');await page.locator('#start-task-button').click();
  await expect(page.locator('#approvals-panel')).toBeVisible({timeout:30000});
  expect((await fetch(fixture.mcp.statsURL).then(r=>r.json())).writes).toBe(0);
  await page.evaluate(()=>{let e=document.createElement('div');e.id='demo-caption';e.textContent='SIMULATED MODEL · The write is waiting for approval';Object.assign(e.style,{position:'fixed',top:'12px',left:'20px',right:'20px',background:'#fff',border:'2px solid #2340f0',padding:'12px',font:'20px Arial',zIndex:9999});document.body.append(e)});
  await page.waitForTimeout(3000);const approve=page.locator('#approvals-list').getByRole('button',{name:'Approve and resume'});const box=await approve.boundingBox();await page.mouse.move(box.x+box.width/2,box.y+box.height/2,{steps:30});await page.waitForTimeout(600);await approve.click();await expect(page.locator('#page-message')).toContainText('Approval recorded. Run completed.',{timeout:30000});
  expect((await fetch(fixture.mcp.statsURL).then(r=>r.json())).writes).toBe(1);
  await page.evaluate(()=>document.querySelector('#demo-caption').textContent='Approved → executed once → recorded in the audit trail');
 }else if(i===0){
  await page.goto('https://ownbot-demo.vercel.app/',{waitUntil:'domcontentloaded'});
  await page.waitForTimeout(3000);await page.locator('.doc-rail a[href="#install"]').click();
 }else{
  await page.goto('about:blank');await page.setContent(`<html><body style="margin:0;background:#fffdf8;color:#171714;font:24px Arial"><main style="padding:60px 70px"><div style="font-size:20px;color:#2340f0">ownbot / ${i+1} of 6</div><h1 style="font-size:49px;font-weight:500;margin:24px 0 50px">${scene.title}</h1><div style="display:flex;align-items:center;gap:20px">${scene.board.map((x,j)=>`${j?'<span style="font-size:48px;color:#2340f0">⟶</span>':''}<div class="node" style="border:2px solid #222;border-radius:3px;padding:30px 22px;width:280px;min-height:140px;display:grid;place-items:center;text-align:center;opacity:0;transform:rotate(${j%2?1:-1}deg);transition:opacity .6s">${x}</div>`).join('')}</div><p style="font-size:28px;color:#2340f0;margin-top:64px">${scene.caption}</p></main><div id="captions" style="position:fixed;bottom:20px;left:70px;right:70px;font-size:22px;background:#f3f2ee;padding:14px 20px"></div></body></html>`);
  if(i===2){const help=execFileSync(process.env.OWNBOT_BINARY,['help'],{encoding:'utf8'});await page.locator('main').evaluate((el,help)=>{const pre=document.createElement('pre');pre.textContent=help;Object.assign(pre.style,{font:'17px/1.5 monospace',background:'#171714',color:'#fffdf8',padding:'22px',maxHeight:'310px',overflow:'hidden'});el.querySelector('div+ h1').after(pre);el.querySelector('h1+pre+div').style.display='none';},help);}
  for(let j=0;j<3;j++){await page.locator('.node').nth(j).evaluate(e=>e.style.opacity=1);await page.waitForTimeout(350)}

 }
 const remaining=scene.duration*1000-(Date.now()-start);if(remaining>0)await page.waitForTimeout(remaining);
 }
 fs.writeFileSync(path.join(out,'timeline.json'),JSON.stringify(timeline));
 }finally{await context.close();await video.saveAs(path.join(out,'capture.webm'));await fixture.close()}
});
