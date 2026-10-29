// Build web first. Fixture APIs only; no live account or wallet is modified.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const http=require('node:http'),fs=require('node:fs'),path=require('node:path');
const root=path.resolve(__dirname,'../web/dist');
const server=http.createServer((req,res)=>{
 let name=new URL(req.url,'http://localhost').pathname;
 if(name==='/profile')name='/pages/profile.html';
 const file=path.resolve(root,'.'+name);
 if(!file.startsWith(root+path.sep)||!fs.existsSync(file)||!fs.statSync(file).isFile()){res.writeHead(404);res.end();return}
 res.setHeader('Content-Type',({'.html':'text/html; charset=utf-8','.js':'text/javascript; charset=utf-8','.css':'text/css','.svg':'image/svg+xml'})[path.extname(file)]||'application/octet-stream');fs.createReadStream(file).pipe(res);
});
(async()=>{
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const browser=await chromium.launch({headless:true,...(process.env.NEKOPASS_BROWSER?{executablePath:process.env.NEKOPASS_BROWSER}:{})});
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1100}});page.setDefaultTimeout(10000);
  const writes=[],errors=[];page.on('pageerror',e=>errors.push(e.message));
  let setting={enabled:false,cycle:'',price_limit_cents:0,status:'off',error:'',plan_id:1,plan_name:'fixture',prices:{monthly:1000,quarterly:2500,onetime:9999},expires_at:'2030-01-01T00:00:00Z',last_success_at:null};
  await page.route('**/api/v1/**',async route=>{
   const req=route.request(),endpoint=new URL(req.url()).pathname.slice('/api/v1/'.length);
   if(endpoint==='profile/auto-renew'){
    if(req.method()==='GET')return route.fulfill({json:[setting]});
    const body=req.postDataJSON();writes.push(body);
    setting={...setting,...body,price_limit_cents:body.expected_price_cents,status:body.enabled?'waiting':'off'};
    return route.fulfill({json:{ok:true}});
   }
   if(endpoint==='me')return route.fulfill({json:{id:2,username:'fixture@example.test',is_admin:false}});
   if(endpoint==='profile')return route.fulfill({json:[{id:2,username:'fixture@example.test',plan_id:1,quota_bytes:100,traffic_bytes:50,actual_traffic_bytes:200,expires_at:setting.expires_at}]});
   return route.fulfill({json:{name:'Nekopass'}});
  });
  const base='http://127.0.0.1:'+server.address().port;
  await page.goto(base+'/profile');
  const toggle=page.getByTestId('auto-renew-enabled');
  await toggle.waitFor();
  assert.match(await page.locator('.el-descriptions').innerText(),/已消耗额度 \/ 总额度\s*50 B \/ 100 B/);
  assert.match(await page.locator('.el-descriptions').innerText(),/实际传输流量\s*200 B/);
  await toggle.click();
  await page.getByTestId('auto-renew-cycle').click();
  assert.equal(await page.getByRole('option',{name:/一次性/}).count(),0);
  await page.getByRole('option',{name:'季付 · 25.00 元',exact:true}).click();
  const save=page.getByTestId('auto-renew-save');
  await save.click();
  let confirm=page.getByRole('dialog',{name:'确认自动续费',exact:true});
  await confirm.getByRole('button',{name:'取消',exact:true}).click();
  assert.equal(writes.length,0,'cancel must not save opt-in');
  await save.click();await confirm.getByRole('button',{name:'确认开启',exact:true}).click();
  await page.getByText('自动续费设置已保存',{exact:true}).waitFor();
  assert.deepEqual(writes[0],{enabled:true,cycle:'quarterly',expected_price_cents:2500});
  assert.equal(await toggle.getByRole('switch').getAttribute('aria-checked'),'true');
  let saved=page.waitForResponse(r=>r.request().method()==='PUT'&&r.url().endsWith('/profile/auto-renew'));
  await toggle.click();await save.click();await saved;
  assert.equal(writes.at(-1).enabled,false);
  // A retired/permanent plan must not prevent turning a previously enabled setting off.
  setting={...setting,enabled:true,expires_at:null,prices:{},status:'unavailable'};
  await page.reload();await toggle.waitFor();
  assert.equal(await toggle.getByRole('switch').getAttribute('aria-checked'),'true');
  saved=page.waitForResponse(r=>r.request().method()==='PUT'&&r.url().endsWith('/profile/auto-renew'));
  await toggle.click();await save.click();await saved;
  await page.getByText('当前套餐永不到期或尚未分配，无需自动续费。',{exact:true}).waitFor();
  assert.equal(writes.at(-1).enabled,false);
  assert.equal(await page.getByRole('link',{name:'管理后台',exact:true}).count(),0);
  assert.deepEqual(errors,[]);
  console.log('PASS auto renewal opt-in, cycle/price confirmation, cancel, disable retired plan, quota/raw distinction and user-only UI');
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve))}
})().catch(err=>{console.error(err);server.close();process.exitCode=1});
