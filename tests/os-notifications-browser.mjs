// Real Go server and two Chrome tabs; the Notification constructor is replaced to inspect UI behavior.
// Run: node tests/os-notifications-browser.mjs /private/tmp/minihub-os-notification-build
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import {spawn} from 'node:child_process';
import assert from 'node:assert/strict';

const temp=fs.mkdtempSync(path.join(os.tmpdir(),'minihub-os-notifications-'));
const reservation=net.createServer();
await new Promise(resolve=>reservation.listen(0,'127.0.0.1',resolve));
const port=reservation.address().port;
await new Promise(resolve=>reservation.close(resolve));
const base=`http://127.0.0.1:${port}`;
const password='notification-test-password';
const disabled=process.argv.includes('--disabled');
fs.writeFileSync(path.join(temp,'minihub.json'),JSON.stringify({version:1,notifications:{osNotificationsEnabled:!disabled}}));
const app=spawn(process.argv[2]||'/private/tmp/minihub-os-notification-build',
    ['-addr',`127.0.0.1:${port}`,'-data',path.join(temp,'data')],
    {cwd:temp,env:{...process.env,MINIHUB_ADMIN_PASSWORD:password},stdio:['ignore','pipe','pipe']});
let chrome;
const pages=[];
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));

async function connect(url) {
    const socket=new WebSocket(url);
    await new Promise(resolve=>socket.addEventListener('open',resolve,{once:true}));
    let seq=0;
    const pending=new Map(),errors=[];
    socket.addEventListener('message',event=>{
        const message=JSON.parse(event.data);
        if(pending.has(message.id)){pending.get(message.id)(message);pending.delete(message.id);}
        if(message.method==='Runtime.exceptionThrown')errors.push(message.params.exceptionDetails);
    });
    const send=(method,params={})=>new Promise(resolve=>{
        const id=++seq;pending.set(id,resolve);socket.send(JSON.stringify({id,method,params}));
    });
    const run=async expression=>{
        const result=await send('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});
        if(result.result.exceptionDetails)throw Error(JSON.stringify(result.result.exceptionDetails));
        return result.result.result.value;
    };
    const wait=async expression=>{
        for(let i=0;i<200;i++){if(await run(expression))return;await pause(50);}
        throw Error('timeout: '+expression);
    };
    await send('Runtime.enable');await send('Network.enable');
    const page={socket,send,run,wait,errors};pages.push(page);return page;
}

try {
    for(let i=0;i<100;i++){
        try{if((await fetch(base+'/login')).ok)break;}catch{}
        await pause(50);
    }
    async function login(user){
        const response=await fetch(base+'/api/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({userId:user,password})});
        assert.equal(response.status,200);
        return {cookie:response.headers.get('set-cookie').split(';')[0],csrf:(await response.json()).csrfToken};
    }
    const admin=await login('admin');
    async function api(session,url,method='GET',body){
        const response=await fetch(base+url,{method,headers:{Cookie:session.cookie,'X-CSRF-Token':session.csrf,'Content-Type':'application/json'},...(body?{body:JSON.stringify(body)}:{})});
        const text=await response.text();
        assert.equal(response.ok,true,`${method} ${url}: ${response.status} ${text}`);
        return text?JSON.parse(text):null;
    }
    await api(admin,'/api/users','POST',{users:[{id:'bob',name:'鈴木',password,role:'user'}]});
    const bob=await login('bob');
    await api(admin,'/api/channels','POST',{id:'general',name:'全社連絡',type:'public'});
    await api(bob,'/api/channels/general/join','POST');
    const root=await api(admin,'/api/channels/general/messages','POST',{text:'相談の起点'});

    chrome=spawn(process.env.BROWSER_PATH||'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
        ['--headless=new','--no-sandbox','--remote-debugging-port=0','--user-data-dir='+path.join(temp,'chrome'),'about:blank'],
        {stdio:['ignore','ignore','pipe']});
    const browserURL=await new Promise((resolve,reject)=>{
        let output='';const timer=setTimeout(()=>reject(Error('Chrome startup timeout')),15000);
        chrome.stderr.on('data',data=>{output+=data;const match=output.match(/DevTools listening on (ws:\/\/\S+)/);if(match){clearTimeout(timer);resolve(match[1]);}});
    });
    const devtools=`http://${new URL(browserURL).host}`;
    const targets=await(await fetch(devtools+'/json/list')).json();
    const first=await connect(targets.find(target=>target.type==='page').webSocketDebuggerUrl);
    const secondTarget=await first.send('Target.createTarget',{url:'about:blank'});
    const secondID=secondTarget.result.targetId;
    let secondURL;
    for(let i=0;i<100;i++){
        secondURL=(await(await fetch(devtools+'/json/list')).json()).find(target=>target.id===secondID)?.webSocketDebuggerUrl;
        if(secondURL)break;
        await pause(50);
    }
    assert.ok(secondURL);
    const second=await connect(secondURL);
    for(const page of [first,second]){
        await page.send('Network.setCookie',{name:admin.cookie.split('=')[0],value:admin.cookie.split('=').slice(1).join('='),url:base});
        await page.send('Page.navigate',{url:base});
        await page.wait("typeof showOSNotification==='function' && S.me?.id==='admin' && S.channels.length===1");
        await page.run(`globalThis.osTestNotices=[];
            window.Notification=class {
                static permission='default';
                static async requestPermission(){this.permission='granted';return 'granted';}
                constructor(title,options){this.title=title;this.options=options;osTestNotices.push(this);}
                close(){this.closed=true;}
            };
            document.hasFocus=()=>false;updateOSFocusLock();renderOSNotificationSetting()`);
    }
    if(disabled){
        for(const page of [first,second]){
            assert.equal(await page.run('document.body.dataset.osNotificationsEnabled'),'false');
            await page.run(`saveUIPreference('os-notifications',true);
                globalThis.osTestPermissionRequests=0;
                Notification.requestPermission=async()=>{osTestPermissionRequests++;return 'granted'};
                renderOSNotificationSetting();$('osNotificationToggle').onclick();
                document.hasFocus=()=>true;updateOSFocusLock()`);
            assert.equal(await page.run("$('osNotificationToggle').disabled"),true);
            assert.equal(await page.run("$('osNotificationStatus').textContent"),'管理者の設定によりOS通知は利用できません');
            assert.equal(await page.run('osTestPermissionRequests'),0);
            await page.run("Notification.permission='granted';showOSNotification({channelId:'general',channelName:'全社連絡',seq:999999,body:'新しい投稿があります'})");
            assert.equal(await page.run('osNotificationEnabled()'),false);
            assert.equal(await page.run('osTestNotices.length'),0);
            assert.equal(await page.run("readUIPreference('os-notifications')"),true,'saved preference was erased');
            assert.equal(await page.run("navigator.locks.query().then(state=>state.held.some(lock=>lock.name.startsWith('minihub:os-notifications:')) )"),false);
            await page.run("document.hasFocus=()=>false;select('general')");
        }
        const post=await api(bob,'/api/channels/general/messages','POST',{text:'@admin 管理者設定でOS通知は停止中'});
        for(const page of [first,second]){
            await page.wait(`S.msgs.some(item=>item.seq===${post.seq})`);
            await page.run("bootstrap.Offcanvas.getOrCreateInstance($('notificationCenter')).show()");
            await page.wait('S.mentionUnread>0');
            assert.equal(await page.run('osTestNotices.length'),0);
            assert.equal(await page.run("$('notificationBadge').classList.contains('hidden')"),false,'in-app badge stopped');
        }
        for(const page of [first,second])await page.run('clearSelectedChannel()');
        await api(bob,'/api/channels/general/messages','POST',{text:'OS通知禁止中も未読と画面内通知を継続'});
        for(const page of [first,second]){
            await page.wait("S.unread.get('general')>0");
            await page.wait('S.localNotifications.some(item=>item.message.includes("新しいメッセージがあります"))');
            assert.equal(await page.run('osTestNotices.length'),0);
        }
        await first.run("window.Notification=undefined;renderOSNotificationSetting()");
        assert.equal(await first.run("$('osNotificationStatus').textContent"),'管理者の設定によりOS通知は利用できません');
        console.log('PASS: administrator disable, reason, no permission request/notification/locks, saved opt-in preserved, in-app mention and unread continue');
    }else{
    assert.equal(await first.run("$('osNotificationToggle').disabled"),false);
    assert.equal(await first.run("osNotificationEnabled()"),false,'OS notification should default to off');
    await first.run("$('osNotificationToggle').click()");
    await first.wait('osNotificationEnabled()');
    await second.run("Notification.permission='granted';renderOSNotificationSetting()");
    await second.wait('osNotificationEnabled()');

    await first.run("Object.defineProperty(document,'visibilityState',{configurable:true,get:()=> 'visible'});document.hasFocus=()=>true;updateOSFocusLock()");
    await first.wait("navigator.locks.query().then(state=>state.held.some(lock=>lock.name===osFocusLockName()))");
    await second.run("showOSNotification({channelId:'general',channelName:'全社連絡',seq:999999,body:'新しい投稿があります'})");
    assert.equal(await second.run('osTestNotices.length'),0,'focused tab did not suppress another tab');
    await first.run("document.hasFocus=()=>false;updateOSFocusLock()");
    await first.wait("navigator.locks.query().then(state=>!state.held.some(lock=>lock.name===osFocusLockName()))");
    const duplicate="showOSNotification({channelId:'general',channelName:'全社連絡',seq:999999,body:'新しい投稿があります'})";
    await Promise.all([first.run(duplicate),second.run(duplicate)]);
    assert.equal(await first.run('osTestNotices.length')+await second.run('osTestNotices.length'),1,'duplicate notification across tabs');

    await first.run("select('general')");
    await first.wait("S.channel?.id==='general' && S.msgs.length===1");
    const post=await api(bob,'/api/channels/general/messages','POST',{text:'通常の投稿本文は表示しない'});
    const tag=`minihub:admin:general:${post.seq}`;
    const tagged=key=>`osTestNotices.find(item=>item.options.tag===${JSON.stringify(key)})`;
    for(let i=0;i<200;i++){
        if(await first.run(`!!${tagged(tag)}`)||await second.run(`!!${tagged(tag)}`))break;
        await pause(50);
    }
    const owner=await first.run(`!!${tagged(tag)}`)?first:second;
    assert.equal(await owner.run(`${tagged(tag)}?.title`),'# 全社連絡');
    assert.equal(await owner.run(`${tagged(tag)}?.options.body`),'新しい投稿があります');
    await owner.run(`${tagged(tag)}.onclick()`);
    await owner.wait(`S.highlightedMessage?.seq===${post.seq}`);

    const mention=await api(bob,'/api/channels/general/messages','POST',{text:'@admin 確認をお願いします'});
    const mentionTag=`minihub:admin:general:${mention.seq}`;
    await first.wait(`!!${tagged(mentionTag)} || S.msgs.some(item=>item.seq===${mention.seq})`);
    await pause(300);
    const mentionOwner=await first.run(`!!${tagged(mentionTag)}`)?first:second;
    assert.equal(await mentionOwner.run(`${tagged(mentionTag)}?.options.body`),'あなた宛てのメンションがあります');

    const reply=await api(bob,`/api/channels/general/threads/${root.seq}/messages`,'POST',{text:'@admin スレッドの返信'});
    const replyTag=`minihub:admin:general:${reply.seq}`;
    for(let i=0;i<200;i++){
        if(await first.run(`!!${tagged(replyTag)}`)||await second.run(`!!${tagged(replyTag)}`))break;
        await pause(50);
    }
    const replyOwner=await first.run(`!!${tagged(replyTag)}`)?first:second;
    assert.equal(await replyOwner.run(`${tagged(replyTag)}?.options.body`),'スレッドであなた宛てのメンションがあります');
    await replyOwner.run(`${tagged(replyTag)}.onclick()`);
    await replyOwner.wait(`T.current?.messages.some(item=>item.seq===${reply.seq})`);

    await first.run('closeThread()');
    await second.run('closeThread()');
    const plainReply=await api(bob,`/api/channels/general/threads/${root.seq}/messages`,'POST',{text:'参加中スレッドの通常返信'});
    const plainReplyTag=`minihub:admin:general:${plainReply.seq}`;
    for(let i=0;i<200;i++){
        if(await first.run(`!!${tagged(plainReplyTag)}`)||await second.run(`!!${tagged(plainReplyTag)}`))break;
        await pause(50);
    }
    const plainOwner=await first.run(`!!${tagged(plainReplyTag)}`)?first:second;
    assert.equal(await plainOwner.run(`${tagged(plainReplyTag)}?.options.body`),'参加中スレッドに新しい返信があります');

    const beforeOwn=await first.run('osTestNotices.length')+await second.run('osTestNotices.length');
    const own=await api(admin,'/api/channels/general/messages','POST',{text:'自分の投稿'});
    await first.wait(`S.msgs.some(item=>item.seq===${own.seq})`);
    await pause(300);
    assert.equal(await first.run('osTestNotices.length')+await second.run('osTestNotices.length'),beforeOwn,'own post raised OS notification');

    await first.run('S.ws.onclose=null;S.ws.close()');
    await second.run('S.ws.onclose=null;S.ws.close()');
    await first.wait('S.ws.readyState===WebSocket.CLOSED');
    await second.wait('S.ws.readyState===WebSocket.CLOSED');
    const beforeReconnect=await first.run('osTestNotices.length')+await second.run('osTestNotices.length');
    const missed=await api(bob,'/api/channels/general/messages','POST',{text:'切断中の投稿'});
    await first.run('realtime()');
    await second.run('realtime()');
    await first.wait(`S.msgs.some(item=>item.seq===${missed.seq})`);
    await pause(300);
    assert.equal(await first.run('osTestNotices.length')+await second.run('osTestNotices.length'),beforeReconnect,'reconnect replay raised OS notification');

    await first.run("Notification.permission='denied';renderOSNotificationSetting()");
    assert.equal(await first.run("$('osNotificationToggle').disabled && !osNotificationEnabled()"),true);
    await first.run("window.Notification=undefined;renderOSNotificationSetting()");
    assert.equal(await first.run("$('osNotificationToggle').disabled && !osNotificationEnabled()"),true);
    assert.equal(await first.run('S.ws?.readyState===WebSocket.OPEN'),true,'unsupported notification API interrupted chat');
    await second.run("$('osNotificationToggle').click()");
    assert.equal(await second.run('osNotificationEnabled()'),false,'OS notification did not stop');
    console.log('PASS: opt-in, focus suppression, two-tab deduplication, normal/mention/thread text and click, own post and replay suppression, unsupported API fallback');
    }
    for(const page of pages)assert.equal(page.errors.length,0,JSON.stringify(page.errors));
} finally {
    for(const page of pages)page.socket.close();
    chrome?.kill();app.kill();
}
