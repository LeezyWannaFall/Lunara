const {chromium}=require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const fs=require('fs');
(async()=>{const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH || undefined,headless:true,args:['--no-sandbox']});
const data=JSON.parse(fs.readFileSync(process.env.LUNARA_WIDGET_FIXTURE || '/tmp/lunara-widget-preview.json','utf8'));const html=fs.readFileSync(require('path').join(__dirname,'../internal/telegram/widget.html'),'utf8');
data['w1|stress||0']={q:'w1|stress||0',t:'Длинная карточка',b:('W'.repeat(32)+'\n').repeat(7)};
const errors=[];
for(const width of [200,320,430])for(const dark of [false,true]){
const page=await browser.newPage({viewport:{width,height:700}});page.on('pageerror',e=>errors.push(e.message));

const palette=dark?'--tg-theme-bg-color:#17212b;--tg-theme-text-color:#edf2f7;--tg-theme-secondary-bg-color:#242f3d;--tg-theme-hint-color:#8c9bad;--tg-theme-button-color:#6d83cf;--tg-theme-accent-text-color:#9baeff;':'--tg-theme-bg-color:#fff;';
const content=`<!doctype html><style>:root{${palette}}body{background:var(--tg-theme-bg-color)}</style><script>window.fixture=${JSON.stringify(data)};window.calls=[];window.failNext=false;window.TelegramWidget={chatData:JSON.stringify(fixture['w1|week|2026-09-28|0']),ready(){},request(q){calls.push(q);if(failNext){failNext=false;return Promise.reject({code:'TIMEOUT'})}return Promise.resolve({result:JSON.stringify(fixture[q]||{error:'Нет данных'})})}};</script>`+html;
await page.route("http://widget.test/",route=>route.fulfill({contentType:"text/html; charset=utf-8",body:content}));await page.goto("http://widget.test/");
await page.waitForTimeout(50);
for(const key of Object.keys(data)){
await page.evaluate(q=>{TelegramWidget.chatData=JSON.stringify(fixture[q]);},key);
// Exercise the actual renderer through a request from Refresh.
await page.evaluate(q=>{window.override=q;const old=TelegramWidget.request;TelegramWidget.request=()=>{TelegramWidget.request=old;return Promise.resolve({result:JSON.stringify(fixture[override])})};},key);
await page.click('#refresh');await page.waitForTimeout(5);
const bounds=await page.evaluate(()=>({height:document.body.getBoundingClientRect().height,width:document.documentElement.scrollWidth}));
if(bounds.height>600||bounds.width>width)errors.push(`${width}/${dark} ${key}: ${JSON.stringify(bounds)}`);
if(key==='w1|stress||0'){let all=await page.locator('#body').innerText();for(let i=0;i<10&&await page.locator('#next').isEnabled();i++){await page.click('#next');all+=await page.locator('#body').innerText();const h=await page.evaluate(()=>document.body.getBoundingClientRect().height);if(h>600)errors.push('overflow on local page')}if(all!==data[key].b)errors.push('lost text in local pagination')}

}
await page.click('[data-mode="week"]');await page.waitForTimeout(10);
await page.locator("body").screenshot({path:`/tmp/lunara-widget-week-${width}-${dark?'dark':'light'}.png`});
await page.evaluate(()=>failNext=true);await page.click('#refresh');await page.waitForTimeout(10);if(await page.evaluate(()=>document.body.getBoundingClientRect().height)>600)errors.push('week error overflow');await page.click('#refresh');await page.waitForTimeout(10);
await page.click('#rows button:first-child');await page.waitForTimeout(10);
await page.locator("body").screenshot({path:`/tmp/lunara-widget-day-${width}-${dark?'dark':'light'}.png`});
const before=await page.locator('#body').innerText();await page.reload();await page.waitForTimeout(20);if(await page.locator('#body').innerText()!==before)errors.push('failed restoring last page');
await page.evaluate(()=>failNext=true);await page.click('#refresh');await page.waitForTimeout(10);
if(!(await page.locator('#status').innerText()).includes('Нет ответа'))errors.push('missing timeout feedback');
await page.click('#refresh');await page.waitForTimeout(10);
if((await page.locator('#status').innerText())!=='Обновлено')errors.push('failed retry');
await page.close();}
await browser.close();if(errors.length){console.error(errors.join('\n'));process.exit(1)}console.log('Widget UI: all fixture pages fit 200/320/430px, light/dark; navigation, long text, restoration, timeout and retry passed.');})();
