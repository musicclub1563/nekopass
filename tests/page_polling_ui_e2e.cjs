// Fixture requests are delayed deliberately; no live panel is contacted.
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
  const page=await browser.newPage();await page.clock.install();
  let calls=0,held;
  const profile=[{id:1,username:'fixture@example.test',plan_id:1,plan_name:'fixture',traffic_bytes:0,quota_bytes:100}];
  await page.route('**/api/v1/**',async route=>{
   const endpoint=new URL(route.request().url()).pathname.slice('/api/v1/'.length);
   if(endpoint==='me')return route.fulfill({json:{id:1,username:'fixture@example.test',is_admin:false}});
   if(endpoint==='profile'){
    calls++;
    if(calls===2){held=route;return}
    return route.fulfill({json:profile});
   }
   if(endpoint==='profile/auto-renew')return route.fulfill({json:[{enabled:false,prices:{},expires_at:null,status:'off'}]});
   return route.fulfill({json:{name:'Nekopass'}});
  });
  await page.goto('http://127.0.0.1:'+server.address().port+'/profile');
  await page.locator('.el-descriptions').waitFor();assert.equal(calls,1);
  const nextPoll=page.waitForRequest(r=>new URL(r.url()).pathname==='/api/v1/profile');
  await page.clock.runFor(5100);await nextPoll;await new Promise(resolve=>setTimeout(resolve,30));
  assert.equal(calls,2);
  await page.clock.runFor(20000);await new Promise(resolve=>setTimeout(resolve,30));
  assert.equal(calls,2,'slow poll must not accumulate overlapping requests');
  const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/profile');
  await held.fulfill({json:profile});await response;await new Promise(resolve=>setTimeout(resolve,50));
  const resumed=page.waitForRequest(r=>new URL(r.url()).pathname==='/api/v1/profile');
  await page.clock.runFor(5100);await resumed;await new Promise(resolve=>setTimeout(resolve,30));
  assert.equal(calls,3,'polling must resume after the previous request completes');
  console.log('PASS slow polling never overlaps and resumes when the delayed request completes');
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve))}
})().catch(err=>{console.error(err);server.close();process.exitCode=1});
