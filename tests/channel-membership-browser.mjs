// Real-server regression test for login, channel search, membership and unread UI.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import {spawn} from 'node:child_process';
import assert from 'node:assert/strict';

const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'minihub-membership-'));
const reservation = net.createServer();
await new Promise(resolve => reservation.listen(0, '127.0.0.1', resolve));
const port = reservation.address().port;
await new Promise(resolve => reservation.close(resolve));
const base = `http://127.0.0.1:${port}`;
const password = 'membership-test-password';
const app = spawn(process.argv[2] || '/private/tmp/minihub-membership',
    ['-addr', `127.0.0.1:${port}`, '-data', path.join(temp, 'data')],
    {cwd: temp, env: {...process.env, MINIHUB_ADMIN_PASSWORD: password}, stdio: ['ignore', 'pipe', 'pipe']});
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
let chrome, cdp;

try {
    for (let i = 0; i < 100; i++) {
        try { if ((await fetch(base + '/login')).ok) break; } catch {}
        await pause(50);
    }
    async function login(userId) {
        const response = await fetch(base + '/api/auth/login', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({userId, password}),
        });
        assert.equal(response.status, 200);
        return {cookie: response.headers.get('set-cookie').split(';')[0], csrf: (await response.json()).csrfToken};
    }
    async function api(session, url, method = 'GET', body) {
        const response = await fetch(base + url, {
            method, headers: {Cookie: session.cookie, 'X-CSRF-Token': session.csrf, 'Content-Type': 'application/json'},
            ...(body ? {body: JSON.stringify(body)} : {}),
        });
        const text = await response.text();
        assert.equal(response.ok, true, `${method} ${url}: ${text}`);
        return text ? JSON.parse(text) : null;
    }
    const admin = await login('admin');
    await api(admin, '/api/users', 'POST', {users: [{id: 'bob', name: 'Bob', password, role: 'user'}]});
    await api(admin, '/api/channels', 'POST', {id: 'general', name: '全体連絡', type: 'public'});
    await api(admin, '/api/channels/general/messages', 'POST', {text: '参加前の投稿'});
    const bob = await login('bob');

    chrome = spawn(process.env.BROWSER_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
        ['--headless=new', '--no-sandbox', '--remote-debugging-port=0', '--user-data-dir=' + path.join(temp, 'chrome'), 'about:blank'],
        {stdio: ['ignore', 'ignore', 'pipe']});
    const browserURL = await new Promise((resolve, reject) => {
        let log = '';
        const timer = setTimeout(() => reject(Error('Chrome startup timeout')), 15000);
        chrome.stderr.on('data', chunk => {
            log += chunk;
            const match = log.match(/DevTools listening on (ws:\/\/\S+)/);
            if (match) { clearTimeout(timer); resolve(match[1]); }
        });
    });
    const pages = await (await fetch('http://' + new URL(browserURL).host + '/json/list')).json();
    cdp = new WebSocket(pages.find(page => page.type === 'page').webSocketDebuggerUrl);
    await new Promise(resolve => cdp.addEventListener('open', resolve, {once: true}));
    let sequence = 0;
    const pending = new Map(), errors = [];
    cdp.addEventListener('message', event => {
        const message = JSON.parse(event.data);
        if (pending.has(message.id)) { pending.get(message.id)(message); pending.delete(message.id); }
        if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails);
    });
    const send = (method, params = {}) => new Promise(resolve => {
        const id = ++sequence;
        pending.set(id, resolve);
        cdp.send(JSON.stringify({id, method, params}));
    });
    const run = async expression => {
        const response = await send('Runtime.evaluate', {expression, awaitPromise: true, returnByValue: true});
        if (response.result.exceptionDetails) throw Error(JSON.stringify(response.result.exceptionDetails));
        return response.result.result.value;
    };
    const wait = async expression => {
        for (let i = 0; i < 150; i++) { if (await run(expression)) return; await pause(50); }
        throw Error('timeout: ' + expression);
    };
    await send('Runtime.enable');
    await send('Page.navigate', {url: base + '/login'});
    await wait("document.getElementById('loginForm') !== null");
    await run("$('uid').value = 'bob'; $('pw').value = 'incorrect-password'; $('showPassword').click()");
    assert.equal(await run("$('pw').type === 'text'"), true);
    await run("$('loginForm').requestSubmit(); $('loginForm').requestSubmit()");
    await wait("!$('loginButton').disabled && !$('loginError').classList.contains('hidden')");
    assert.equal(await run("$('uid').value === 'bob' && $('pw').value === '' && $('pw').type === 'password' && !$('showPassword').checked && document.activeElement.id === 'pw'"), true);
    await pause(7100);
    assert.equal(await run("getComputedStyle($('loginError')).display !== 'none'"), true);
    await run(`$('pw').value = ${JSON.stringify(password)}; $('loginForm').requestSubmit()`);
    await wait("typeof S !== 'undefined' && S.me?.id === 'bob'");
    await send('Network.setCookie', {name: bob.cookie.split('=')[0], value: bob.cookie.split('=').slice(1).join('='), url: base});
    await send('Page.navigate', {url: base});
    await wait("typeof S !== 'undefined' && S.me?.id === 'bob' && S.channels.length === 1");
    await run("select('general')");
    await wait("S.channel?.id === 'general' && S.msgs.length === 1 && !S.channelActionBusy && !S.initialLoading && $('join').textContent === 'チャンネルに参加' && getComputedStyle($('join')).display !== 'none'");
    assert.equal(await run("document.querySelector('.channel.unjoined') !== null"), true);
    assert.equal(await run("$('join').textContent === 'チャンネルに参加' && $('join').getAttribute('aria-label') === 'チャンネルに参加' && $('join').title === 'チャンネルに参加' && getComputedStyle($('join')).display !== 'none' && !$('join').disabled"), true);
    assert.equal(await run("renderChannelAction(); renderChannelAction(); getComputedStyle($('join')).display !== 'none'"), true);
    assert.equal(await run("!document.querySelector('.channel .tc-badge') && $('publicUnread').classList.contains('hidden') && !document.querySelector('#messages .unread-line')"), true);
    assert.equal(await run("$('send').disabled"), true);

    await run("$('join').click()");
    await wait("S.channel?.members?.includes('bob') && !S.channelActionBusy");
    assert.equal((await api(bob, '/api/channels/general/read')).unreadCount, 0);
    assert.equal(await run("$('join').textContent === 'チャンネルを退出' && $('join').getAttribute('aria-label') === 'チャンネルを退出' && $('join').title === 'チャンネルを退出' && getComputedStyle($('join')).display !== 'none'"), true);
    await api(admin, '/api/channels/general/messages', 'POST', {text: '参加後の投稿'});
    await wait("S.unread.get('general') === 1");
    assert.equal(await run("document.querySelector('.channel .tc-badge')?.textContent === '1'"), true);
    await api(admin, '/api/channels', 'POST', {id: 'team-dev', name: '開発チーム', type: 'public'});
    await api(admin, '/api/channels', 'POST', {id: 'team-private', name: '限定チーム', type: 'private', members: ['bob']});
    await api(admin, '/api/channels', 'POST', {id: 'secret', name: '機密の会話', type: 'private'});
    await run('loadChannels()');
    await wait('S.channels.length === 3');
    const search = value => run(`$('channelSearch').value = ${JSON.stringify(value)}; $('channelSearch').dispatchEvent(new Event('input', {bubbles:true}))`);
    const visibleIDs = () => run("Array.from(document.querySelectorAll('.channel:not(.hidden)')).map(row => row.dataset.channelId).sort()");
    await run("setChannelSectionCollapsed('public', true); setChannelSectionCollapsed('private', true)");
    await search('チーム');
    assert.deepEqual(await visibleIDs(), ['team-dev', 'team-private']);
    assert.equal(await run("$('publicToggle').getAttribute('aria-expanded') === 'true' && $('privateToggle').getAttribute('aria-expanded') === 'true' && $('publicCount').textContent === '1/2' && $('channelSearchStatus').textContent === '検索結果: 2件'"), true);
    assert.equal(await run("$('publicUnread').textContent === '1' && !$('publicUnread').classList.contains('hidden')"), true);
    await run('loadChannels()');
    assert.deepEqual(await visibleIDs(), ['team-dev', 'team-private']);
    await search('ＴＥＡＭ－ＤＥＶ');
    assert.deepEqual(await visibleIDs(), ['team-dev']);
    await search('機密');
    assert.deepEqual(await visibleIDs(), []);
    assert.equal(await run("$('channelSearchStatus').textContent === '検索結果: 0件' && $('public').textContent.includes('一致するチャンネルはありません') && !S.channels.some(c => c.id === 'secret')"), true);
    await run("$('channelSearch').dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true}))");
    assert.equal(await run("$('channelSearch').value === '' && $('publicToggle').getAttribute('aria-expanded') === 'false' && $('privateToggle').getAttribute('aria-expanded') === 'false' && $('publicCount').textContent === '2'"), true);
    await run("setChannelSectionCollapsed('public', false); setChannelSectionCollapsed('private', false)");
    for (const [width, height] of [[1440, 900], [390, 740]]) {
        await send('Emulation.setDeviceMetricsOverride', {width, height, deviceScaleFactor:1, mobile:false});
        // Let the viewport change handler finish before opening the mobile dialog.
        await run("new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
        if (width < 720) {
            await run("$('channelNavOpen').click()");
            await wait("$('channelNav').getAttribute('aria-hidden') === 'false' && !$('channelNav').inert");
        }
        await search('チーム');
        assert.equal(await run("(() => {const nav=$('channelNav').getBoundingClientRect(), input=$('channelSearch').getBoundingClientRect(); return input.width > 100 && input.left >= nav.left && input.right <= nav.right && input.bottom <= nav.bottom && document.documentElement.scrollWidth <= innerWidth})()"), true);
        assert.equal(await run("document.querySelector('.sidebar-links a').getAttribute('href') === '/schedules' && document.querySelector('.sidebar-links a').rel.includes('noopener')"), true);
        await pause(250);
        const screenshot = await send('Page.captureScreenshot', {format:'png'});
        fs.writeFileSync(path.join(temp, `channels-${width}.png`), Buffer.from(screenshot.result.data, 'base64'));
        if (width < 720) {
            await send('Page.bringToFront');
            await run("$('channelSearch').focus()");
            assert.equal(await run("document.activeElement.id === 'channelSearch'"), true);
            await send('Input.dispatchKeyEvent', {type:'keyDown', key:'Escape', code:'Escape', windowsVirtualKeyCode:27});
            assert.equal(await run("$('channelSearch').value === '' && document.querySelector('.app').classList.contains('channel-nav-open')"), true);
        }
    }
    assert.equal(errors.length, 0, JSON.stringify(errors));
    console.log('PASS: login errors and retry, channel search and access, collapse restoration, unread, desktop/mobile layout, membership');
    console.log('Screenshots: ' + temp);
} finally {
    cdp?.close();
    chrome?.kill();
    app.kill();
}
