// Real-server smoke test for poll creation, voting, results, and responsive layout.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import {spawn} from 'node:child_process';
import assert from 'node:assert/strict';
const temp=fs.mkdtempSync(path.join(os.tmpdir(),'minihub-poll-smoke-'));
const reservation=net.createServer();await new Promise(r=>reservation.listen(0,'127.0.0.1',r));const port=reservation.address().port;await new Promise(r=>reservation.close(r));
const base=`http://127.0.0.1:${port}`,password='poll-test-password';
const app=spawn(process.argv[2]||'/private/tmp/minihub-polls-browser',['-addr',`127.0.0.1:${port}`,'-data',path.join(temp,'data')],{cwd:temp,env:{...process.env,MINIHUB_ADMIN_PASSWORD:password},stdio:['ignore','pipe','pipe']});
let chrome,cdp;const pause=ms=>new Promise(r=>setTimeout(r,ms));
try{
 for(let i=0;i<100;i++){try{if((await fetch(base+'/login')).ok)break}catch{}await pause(50)}
 async function login(user){const r=await fetch(base+'/api/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({userId:user,password})});assert.equal(r.status,200);return{cookie:r.headers.get('set-cookie').split(';')[0],csrf:(await r.json()).csrfToken}}
 const admin=await login('admin');async function api(url,method='GET',body){const r=await fetch(base+url,{method,headers:{Cookie:admin.cookie,'X-CSRF-Token':admin.csrf,'Content-Type':'application/json'},...(body?{body:JSON.stringify(body)}:{})});const text=await r.text();assert.equal(r.ok,true,text);return text?JSON.parse(text):null}
 await api('/api/channels','POST',{id:'general',name:'全社',type:'public'});
 await api('/api/users','POST',{users:[{id:'bob',name:'Bob',password,role:'user'}]});const bob=await login('bob');
 await fetch(base+'/api/channels/general/join',{method:'POST',headers:{Cookie:bob.cookie,'X-CSRF-Token':bob.csrf}}).then(r=>assert.equal(r.ok,true));
 chrome=spawn(process.env.BROWSER_PATH||'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',['--headless=new','--no-sandbox','--remote-debugging-port=0','--user-data-dir='+path.join(temp,'chrome'),'about:blank'],{stdio:['ignore','ignore','pipe']});
 const browserURL=await new Promise((resolve,reject)=>{let log='';const timer=setTimeout(()=>reject(Error('Chrome startup timeout')),15000);chrome.stderr.on('data',b=>{log+=b;const match=log.match(/DevTools listening on (ws:\/\/\S+)/);if(match){clearTimeout(timer);resolve(match[1])}})});
 const pages=await(await fetch('http://'+new URL(browserURL).host+'/json/list')).json();cdp=new WebSocket(pages.find(p=>p.type==='page').webSocketDebuggerUrl);await new Promise(r=>cdp.addEventListener('open',r,{once:true}));
 let seq=0;const pending=new Map(),errors=[];cdp.addEventListener('message',e=>{const m=JSON.parse(e.data);if(pending.has(m.id)){pending.get(m.id)(m);pending.delete(m.id)}if(m.method==='Runtime.exceptionThrown')errors.push(m.params.exceptionDetails)});
 const send=(method,params={})=>new Promise(r=>{const id=++seq;pending.set(id,r);cdp.send(JSON.stringify({id,method,params}))});
 const run=async expression=>{const r=await send('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(r.result.exceptionDetails)throw Error(JSON.stringify(r.result.exceptionDetails));return r.result.result.value};
 const wait=async expression=>{for(let i=0;i<150;i++){if(await run(expression))return;await pause(50)}throw Error('timeout: '+expression)};
 await send('Runtime.enable');await send('Emulation.setDeviceMetricsOverride',{width:1440,height:1000,deviceScaleFactor:1,mobile:false});await send('Network.setCookie',{name:admin.cookie.split('=')[0],value:admin.cookie.split('=').slice(1).join('='),url:base});await send('Page.navigate',{url:base});await wait("typeof S !== 'undefined' && S.me && S.channels.length===1 && typeof pollRenderCard==='function'");
 await run("select('general')");await wait("S.channel?.id==='general' && !$('createMenuButton').disabled");
 assert.equal(await run("$('participatingThreads')===null && $('threadTab')!==null && $('createSchedule')!==null"),true);
 assert.equal(await run("!$('pollScheduleNav') && !$('allPollSchedules') && !$('pollListModal') && !!$('pollModal')"),true);
 assert.equal(await run("$('createPoll').textContent==='アンケート' && $('pollModalTitle').textContent==='アンケートを作成' && document.querySelector('#channelInfoPanel h3').textContent.includes('アンケート')"),true);
 await run("$('createPoll').click();$('pollQuestion').value='昼食は？';document.querySelectorAll('#pollOptions input')[0].value='和食';document.querySelectorAll('#pollOptions input')[1].value='洋食';$('pollMultiple').checked=true;$('pollForm').requestSubmit()");
 await wait("S.msgs.some(m=>m.pollRef) && document.querySelector('.poll-card .poll-choice')");
 assert.equal(await run("S.msgs.find(m=>m.pollRef).text.startsWith('アンケートを開始しました: ') && document.querySelector('#messages .poll-card').getAttribute('aria-label').startsWith('アンケート: ') && document.querySelector('#messages .poll-vote-form button').textContent==='回答する'"),true);
 await run("S.msgs.find(m=>m.pollRef).text='投票を開始しました: 昼食は？';render()");
 await wait("document.querySelector('#messages .message .text')?.textContent.includes('アンケートを開始しました: 昼食は？') && !document.querySelector('#messages .message .text')?.textContent.includes('投票を開始しました')");
 await wait("document.querySelector('.poll-card')?.textContent.includes('回答者 0人')");
 await run("document.querySelectorAll('.poll-card .poll-choice input')[0].click();document.querySelectorAll('.poll-card .poll-choice input')[1].click();document.querySelector('.poll-vote-form button').click()");
 await wait("document.querySelector('.poll-card').textContent.includes('回答者 1人')");
 assert.equal(await run("document.querySelector('.poll-card').textContent.includes('総選択数 2票')"),true);
 await run("document.querySelectorAll('.poll-card .poll-choice input')[0].click();document.querySelector('.poll-vote-form button').click()");await wait("document.querySelector('.poll-card').textContent.includes('総選択数 1票')");
 await run("document.querySelectorAll('.poll-card .poll-choice input')[0].click();document.querySelectorAll('.poll-card .poll-choice input')[0].focus()");const pollID=await run("S.msgs.find(m=>m.pollRef).pollRef.id");const vote=await fetch(base+'/api/polls/'+pollID+'/vote',{method:'PUT',headers:{Cookie:bob.cookie,'X-CSRF-Token':bob.csrf,'Content-Type':'application/json'},body:JSON.stringify({revision:0,optionIds:['o1']})});assert.equal(vote.ok,true,await vote.text());await wait("document.querySelector('.poll-card')?.textContent.includes('回答者 2人')");assert.equal(await run("document.querySelectorAll('.poll-card .poll-choice input')[0].checked && document.activeElement===document.querySelectorAll('.poll-card .poll-choice input')[0]"),true);
 await wait("document.querySelector('#activePollSchedules .poll-side-item')");
 await run("document.querySelector('#activePollSchedules .poll-side-item').click()");
 await wait("document.querySelector('#activePollSchedules .poll-card') && document.querySelector('#activePollSchedules .poll-side-item').getAttribute('aria-expanded')==='true'");
 await run("document.querySelectorAll('#activePollSchedules .poll-vote-form input')[0].click();document.querySelector('#activePollSchedules .poll-vote-form button').click()");
 await wait("document.querySelector('#activePollSchedules .poll-vote-form')?.dataset.saved==='[\"o1\",\"o2\"]' && !document.querySelector('#activePollSchedules .poll-vote-form button')?.disabled");
 await run("document.querySelectorAll('#activePollSchedules .poll-vote-form input')[0].click()");
 await run("window.pollChannelSelected()");
 await wait("document.querySelector('#activePollSchedules .poll-side-item')?.getAttribute('aria-expanded')==='true' && !document.querySelectorAll('#activePollSchedules .poll-vote-form input')[0].checked");
 const schedule=await api('/api/schedules','POST',{channelId:'general',title:'来週の予定',candidates:[{date:'2027-01-10'},{date:'2027-01-11'}]});
 await api('/api/schedules/'+schedule.id+'/publish','POST',{revision:schedule.revision});
 await run("window.pollChannelSelected()");
 await wait("[...document.querySelectorAll('#activePollSchedules .poll-side-item')].some(b=>b.textContent.includes('予定 · 来週の予定'))");
 assert.equal(await run(`(()=>{let opened;window.open=(url,target,features)=>{opened={url,target,features}};[...document.querySelectorAll('#activePollSchedules .poll-side-item')].find(b=>b.textContent.includes('予定 · 来週の予定')).click();return opened?.url==='/schedules/${schedule.id}'&&opened.target==='_blank'&&opened.features==='noopener,noreferrer'})()`),true);
 await run("document.dispatchEvent(new Event('poll-reconnect'))");
 await wait("document.querySelector('#activePollSchedules .poll-side-item')?.getAttribute('aria-expanded')==='true' && document.querySelector('#activePollSchedules .poll-card')");
 await api('/api/channels','POST',{id:'other',name:'別チャンネル',type:'public'});await wait("S.channels.length===2");
 await run("select('other')");await wait("S.channel?.id==='other' && $('activePollSchedules').textContent.includes('受付中のアンケート・予定はありません')");
 await run("select('general')");await wait("S.channel?.id==='general' && document.querySelector('#activePollSchedules .poll-side-item')?.getAttribute('aria-expanded')==='false'");
 await wait("document.querySelector('.poll-card')");
 await send('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true});assert.equal(await run("document.querySelector('.poll-card').getBoundingClientRect().width <= window.innerWidth"),true);
 await run("$('channelInfoToggle').click()");await wait("$('channelInfoPanel').classList.contains('show')");
 await run("document.querySelector('#activePollSchedules .poll-side-item').click()");await wait("document.querySelector('#activePollSchedules .poll-card')");
 assert.equal(await run("document.querySelector('#activePollSchedules .poll-card').getBoundingClientRect().width <= window.innerWidth"),true);
 assert.equal(errors.length,0,JSON.stringify(errors));await wait("!$('pollModal').classList.contains('show') && !document.querySelector('.modal-backdrop')");const shot=await send('Page.captureScreenshot',{format:'png'});fs.writeFileSync(path.join(temp,'poll-mobile.png'),Buffer.from(shot.result.data,'base64'));console.log('PASS: poll create, answer, change, anonymous result, navigation, mobile layout');console.log('Screenshot: '+path.join(temp,'poll-mobile.png'));
}finally{cdp?.close();chrome?.kill();app.kill();}
