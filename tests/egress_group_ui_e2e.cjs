// Fixture APIs only; no live accounts or authentication material.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const http=require('node:http'),fs=require('node:fs'),path=require('node:path');
const root=path.resolve(__dirname,'../web/dist');
const server=http.createServer((req,res)=>{
 let name=new URL(req.url,'http://localhost').pathname;
 const pages={'/forward_rules':'forward_rules','/admin/node_groups':'admin_node_groups'};
 if(pages[name])name='/pages/'+pages[name]+'.html';
 const file=path.resolve(root,'.'+name);
 if(!file.startsWith(root+path.sep)||!fs.existsSync(file)||!fs.statSync(file).isFile()){res.writeHead(404);res.end();return}
 res.setHeader('Content-Type',({'.html':'text/html; charset=utf-8','.js':'text/javascript; charset=utf-8','.css':'text/css'})[path.extname(file)]||'application/octet-stream');fs.createReadStream(file).pipe(res);
});
const nodes=[
 {id:10,ingress_multiplier:'1.25',egress_multiplier:'0',name:'入口节点',ingress_enabled:true,allow_direct:true,tunnel_exit_enabled:false,tunnel_protocol:'plain_tcp',allowed_ingress_ids:[],public_address:'entry.example.test',port_min:1024,port_max:65535,group_ids:[20]},
 {id:20,ingress_multiplier:'1',egress_multiplier:'0.75',name:'单个出口',ingress_enabled:false,allow_direct:false,tunnel_exit_enabled:true,tunnel_protocol:'plain_tcp',allowed_ingress_ids:[10],public_address:'exit.example.test',group_ids:[20]},
 {id:30,ingress_multiplier:'1',egress_multiplier:'0',name:'备用出口',ingress_enabled:false,allow_direct:false,tunnel_exit_enabled:true,tunnel_protocol:'tls_h2',allowed_ingress_ids:[10],public_address:'backup.example.test',group_ids:[20]},
];
const group={id:20,name:'多出口节点组',description:'',enabled:true,sort_order:0,strategy:'least_connections',node_ids:[10,20,30],plan_count:1,health_interval_seconds:5,health_failures:3,health_successes:2,members:[{node_id:10,role:'disabled',weight:1},{node_id:20,role:'active',weight:2},{node_id:30,role:'standby',weight:1}]};
(async()=>{
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const browser=await chromium.launch({headless:true,...(process.env.NEKOPASS_BROWSER?{executablePath:process.env.NEKOPASS_BROWSER}:{})});
 try {
  const page=await browser.newPage({viewport:{width:1440,height:1100}});page.setDefaultTimeout(15000);
  let admin=false;const writes=[],errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/api/v1/**',async route=>{
   const req=route.request(),api=new URL(req.url()).pathname.slice('/api/v1/'.length);
   if(req.method()!=='GET'){writes.push({api,body:req.postDataJSON()});return route.fulfill({json:{id:1}})}
   let data=[];
   if(api==='me')data={id:2,username:'fixture@example.test',is_admin:admin};
   else if(api==='site')data={name:'Nekopass'};
   else if(api==='profile')data=[{id:2,node_ids:[10,20,30],quota_bytes:-1,max_rules:0,traffic_bytes:0,rule_count:0,plan_id:1,plan_enabled:true}];
   else if(api==='rule-nodes'||api==='admin/nodes')data=nodes;
   else if(api==='rule-egress-groups')data=[{...group,node_ids:[20,30]}];
   else if(api==='admin/node-groups')data=[group];
   return route.fulfill({json:data});
  });
  const base='http://127.0.0.1:'+server.address().port;
  const field=(dlg,label)=>dlg.getByText(label,{exact:true}).locator('..');
  async function select(dlg,label,value){await field(dlg,label).locator('.el-select').click();await page.getByRole('option',{name:value,exact:true}).click()}
  await page.goto(base+'/forward_rules');
  await page.getByRole('button',{name:'添加规则',exact:true}).click();
  let dlg=page.getByRole('dialog',{name:'添加规则',exact:true});
  await select(dlg,'入口','入口节点');
  await field(dlg,'出口').locator('.el-select').click();
  const single=page.getByRole('option',{name:'单个出口',exact:true});
  const grouped=page.getByRole('option',{name:'多出口节点组',exact:true});
  assert.equal(await single.locator('.is-node .ui-icon').evaluate(e=>getComputedStyle(e).color),'rgb(71, 200, 138)');
  assert.equal(await grouped.locator('.is-group .ui-icon').evaluate(e=>getComputedStyle(e).color),'rgb(102, 169, 255)');
  assert.match(await single.innerText(),/2 倍/);assert.match(await grouped.innerText(),/1.25–2 倍（按实际出口）/);
  await page.waitForTimeout(250);
  if(process.env.NEKOPASS_UI_SCREENSHOT_DIR){fs.mkdirSync(process.env.NEKOPASS_UI_SCREENSHOT_DIR,{recursive:true});await page.screenshot({path:path.join(process.env.NEKOPASS_UI_SCREENSHOT_DIR,'group-exit-options.png')})}
  await grouped.click();await field(dlg,'目标地址').locator('textarea').fill('target.example.test:443');
  await dlg.getByRole('button',{name:'确定',exact:true}).click();await dlg.waitFor({state:'hidden'});
  assert.equal(writes.at(-1).api,'rules');assert.equal(writes.at(-1).body.egress_node_id,0);assert.equal(writes.at(-1).body.egress_group_id,20);
  // A node and a group with the same numeric ID must remain distinguishable.
  await page.getByRole('button',{name:'添加规则',exact:true}).click();dlg=page.getByRole('dialog',{name:'添加规则',exact:true});
  await select(dlg,'入口','入口节点');await select(dlg,'出口','单个出口');
  await field(dlg,'目标地址').locator('textarea').fill('target.example.test:443');await dlg.getByRole('button',{name:'确定',exact:true}).click();await dlg.waitFor({state:'hidden'});
  assert.equal(writes.at(-1).body.egress_node_id,20);assert.equal(writes.at(-1).body.egress_group_id,0);
  admin=true;await page.goto(base+'/admin/node_groups');
  await page.getByRole('row').filter({hasText:'多出口节点组'}).getByRole('button',{name:'编辑',exact:true}).click();
  dlg=page.getByRole('dialog',{name:'编辑节点组',exact:true});
  await select(dlg,'出口负载策略','加权轮询');
  const backupRow=dlg.locator('.group-members-table .el-table__body-wrapper tbody tr').filter({hasText:'备用出口'});
  assert.match(await backupRow.innerText(),/容灾备用/);
  await backupRow.locator('.el-select').click();await page.getByRole('option',{name:'正常出口',exact:true}).click();
  await backupRow.locator('.el-input-number input').fill('3');
  await field(dlg,'连续失败次数').locator('input').fill('4');
  await field(dlg,'节点组名称').locator('input').click();await page.waitForTimeout(250);
  if(process.env.NEKOPASS_UI_SCREENSHOT_DIR)await page.screenshot({path:path.join(process.env.NEKOPASS_UI_SCREENSHOT_DIR,'group-member-settings.png')});
  await dlg.getByRole('button',{name:'保存节点组',exact:true}).click();await dlg.waitFor({state:'hidden'});
  const saved=writes.at(-1);assert.equal(saved.api,'admin/node-groups/20');assert.equal(saved.body.strategy,'round_robin');assert.equal(saved.body.health_failures,4);assert.deepEqual(saved.body.members.find(m=>m.node_id===30),{node_id:30,role:'active',weight:3});
  assert.deepEqual(errors,[]);console.log('PASS unified exits, green/blue icons, colliding IDs, single/group create, group strategy, role, weight and health settings');
 } finally {await browser.close();await new Promise(resolve=>server.close(resolve))}
})().catch(err=>{console.error(err);server.close();process.exitCode=1});
