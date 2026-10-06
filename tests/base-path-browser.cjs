// Requires Playwright on NODE_PATH and a built minihub executable as argv[2].
// Runs against disposable servers behind a proxy that preserves paths and Host.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

const executable = path.resolve(process.argv[2] || '.tmp/minihub-base-path.exe');
const password = 'base-path-browser-password';
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const listen = server => new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
async function freePort() {
    const server = net.createServer();
    await listen(server);
    const port = server.address().port;
    await new Promise(resolve => server.close(resolve));
    return port;
}
async function waitForService(url, child) {
    for (let i = 0; i < 100; i++) {
        if (child.exitCode !== null) throw Error(`server exited: ${child.exitCode}`);
        try { if ((await fetch(url)).ok) return; } catch {}
        await pause(100);
    }
    throw Error('server startup timed out');
}

(async () => {
    const browserPath = process.env.BROWSER_PATH || (process.platform === 'win32' ? 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe' : undefined);
    const browser = await chromium.launch({headless: true, ...(browserPath ? {executablePath: browserPath} : {})});
    try {
        for (const [basePath, storage, override] of [['', 'file', false], ['/hub', 'file', false], ['/apps/chat', 'sqlite', true]]) {
            const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'minihub-base-path-'));
            const upstreamPort = await freePort();
            fs.writeFileSync(path.join(temp, 'minihub.json'), JSON.stringify({
                server: {listenAddress: `127.0.0.1:${upstreamPort}`, basePath: override ? '/configured' : basePath},
                storage: {type: storage}, notifications: {osNotificationsEnabled: false},
                initialAdmin: {id: 'admin', name: '管理者', password},
            }), {mode: 0o600});
            const child = spawn(executable, ['-config', 'minihub.json', ...(override ? ['-base-path', basePath] : [])], {cwd: temp, stdio: 'ignore'});
            const paths = [], upgrades = [], failures = [], sockets = new Set();
            const proxy = http.createServer((req, res) => {
                paths.push(req.url);
                if (req.url === '/other/') { res.end('existing application'); return; }
                const forwarded = http.request({hostname: '127.0.0.1', port: upstreamPort, path: req.url, method: req.method, headers: req.headers}, response => {
                    res.writeHead(response.statusCode, response.headers);
                    response.pipe(res);
                });
                forwarded.on('error', error => { failures.push(error.message); res.destroy(); });
                req.pipe(forwarded);
            });
            proxy.on('connection', socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)); });
            proxy.on('upgrade', (req, socket, head) => {
                upgrades.push(req.url);
                const upstream = net.connect(upstreamPort, '127.0.0.1', () => {
                    const headers = [];
                    for (let i = 0; i < req.rawHeaders.length; i += 2) headers.push(`${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}`);
                    upstream.write(`${req.method} ${req.url} HTTP/1.1\r\n${headers.join('\r\n')}\r\n\r\n`);
                    if (head.length) upstream.write(head);
                    socket.pipe(upstream).pipe(socket);
                });
                upstream.on('error', error => { failures.push(error.message); socket.destroy(); });
                socket.on('close', () => upstream.destroy());
                socket.on('error', () => upstream.destroy());
            });
            const context = await browser.newContext();
            try {
                await waitForService(`http://127.0.0.1:${upstreamPort}${basePath}/login`, child);
                await listen(proxy);
                const origin = `http://127.0.0.1:${proxy.address().port}`;
                const base = origin + basePath;
                assert.equal(await (await fetch(origin + '/other/')).text(), 'existing application');
                const login = await fetch(base + '/api/auth/login', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({userId: 'admin', password})});
                assert.equal(login.status, 200);
                const cookie = login.headers.get('set-cookie').split(';')[0];
                assert(login.headers.get('set-cookie').includes(`Path=${basePath}/;`));
                const csrf = (await login.json()).csrfToken;
                async function api(url, method = 'GET', body) {
                    const response = await fetch(base + url, {method, headers: {Cookie: cookie, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json'}, ...(body ? {body: JSON.stringify(body)} : {})});
                    const text = await response.text();
                    assert(response.ok, `${method} ${url}: ${response.status} ${text}`);
                    return text ? JSON.parse(text) : null;
                }
                await api('/api/channels', 'POST', {id: 'general', name: '全体連絡', type: 'public'});
                const schedule = await api('/api/schedules', 'POST', {channelId: 'general', title: '運用日程', candidates: [{date: '2099-01-10'}, {date: '2099-01-11'}]});
                await api(`/api/schedules/${schedule.id}/publish`, 'POST', {revision: schedule.revision});

                const page = await context.newPage();
                const errors = [];
                page.on('pageerror', error => errors.push(error.message));
                page.on('response', response => { if (response.status() >= 400) failures.push(`${response.status()} ${response.url()}`); });
                await page.goto(`${base}/schedules/${schedule.id}?tab=answers`);
                assert.equal(new URL(page.url()).pathname, basePath + '/login');
                assert.equal(new URL(page.url()).searchParams.get('next'), `${basePath}/schedules/${schedule.id}?tab=answers`);
                await page.locator('#uid').fill('admin');
                await page.locator('#pw').fill(password);
                await page.locator('#loginButton').click();
                await page.waitForFunction(() => document.querySelector('#detailTitle')?.textContent === '運用日程');
                assert.equal(new URL(page.url()).pathname, `${basePath}/schedules/${schedule.id}`);
                await page.waitForFunction(() => state.ws?.readyState === WebSocket.OPEN);
                assert.equal((await context.cookies()).find(c => c.name === 'minihub_session').path, basePath + '/');
                await page.locator('#edit').click();
                await page.waitForFunction(() => document.querySelector('#title')?.value === '運用日程');
                assert.equal(new URL(page.url()).pathname, basePath + '/schedules/new');
                await page.locator('#title').fill('変更済み日程');
                await page.locator('#saveDraft').click();
                await page.waitForFunction(() => document.querySelector('#detailTitle')?.textContent === '変更済み日程');

                await page.goto(base + '/schedules');
                await page.locator('.schedule-card').waitFor();
                assert.equal(new URL(await page.locator('.schedule-card').getAttribute('href'), origin).pathname, `${basePath}/schedules/${schedule.id}`);
                await page.locator('[data-period="past"]').click();
                assert.equal(new URL(page.url()).searchParams.get('period'), 'past');
                await page.goBack();
                await page.locator('.schedule-card').waitFor();
                await page.locator('#newSchedule').click();
                await page.waitForFunction(() => document.querySelector('#candidates')?.children.length === 2);
                await page.locator('#title').fill('ブラウザから作成');
                await page.locator('#saveDraft').click();
                await page.waitForFunction(() => document.querySelector('#detailTitle')?.textContent === 'ブラウザから作成');
                await page.locator('#publishDetail').click();
                await page.waitForFunction(() => state.detail?.schedule.status === 'open');

                await page.goto(base + '/');
                await page.waitForFunction(() => typeof S !== 'undefined' && S.me?.id === 'admin' && S.ws?.readyState === WebSocket.OPEN);
                await page.evaluate(() => select('general'));
                await page.locator('.schedule-message-link').first().waitFor();
                assert.equal(new URL(await page.locator('.schedule-message-link').first().getAttribute('href'), origin).pathname, `${basePath}/schedules/${schedule.id}`);
                const popupReady = page.waitForEvent('popup');
                await page.locator('#activePollSchedules .poll-side-item').filter({hasText: '変更済み日程'}).click();
                const popup = await popupReady;
                await popup.waitForLoadState();
                assert.equal(new URL(popup.url()).pathname, `${basePath}/schedules/${schedule.id}`);
                await popup.close();
                await page.locator('#createMenuButton').click();
                const editorReady = page.waitForEvent('popup');
                await page.locator('#createSchedule').click();
                const editor = await editorReady;
                await editor.waitForFunction(() => document.querySelector('#candidates')?.children.length === 2);
                assert.equal(new URL(editor.url()).pathname, basePath + '/schedules/new');
                assert.equal(new URL(editor.url()).searchParams.get('channelId'), 'general');
                await editor.close();
                await page.evaluate(() => document.body.append(svgIcon('folder')));
                await api('/api/channels/general/messages', 'POST', {text: 'プロキシ経由のリアルタイム投稿'});
                await page.waitForFunction(() => S.msgs.some(m => m.text === 'プロキシ経由のリアルタイム投稿'));
                await page.evaluate(() => { window.previousSocket = S.ws; S.ws.close(); });
                await api('/api/channels/general/messages', 'POST', {text: '再接続で取得する投稿'});
                await page.waitForFunction(() => S.ws !== window.previousSocket && S.ws?.readyState === WebSocket.OPEN && S.msgs.some(m => m.text === '再接続で取得する投稿'));
                await page.locator('#logout').click();
                await page.waitForURL(base + '/login');
                assert.equal((await context.cookies()).some(c => c.name === 'minihub_session'), false);
                assert(upgrades.length >= 3 && upgrades.every(url => url === basePath + '/api/realtime'));
                assert(paths.filter(url => url !== '/other/').every(url => url.startsWith(basePath + '/')), `unprefixed request: ${paths}`);
                assert.deepEqual(errors, []);
                assert.deepEqual(failures, []);
                console.log(`PASS ${basePath || '/'} (${storage}${override ? ', CLI override' : ''}): proxy, login return, schedules, cookies, WebSocket and reconnect`);
            } finally {
                await context.close();
                for (const socket of sockets) socket.destroy();
                if (proxy.listening) await new Promise(resolve => proxy.close(resolve));
                const exited = new Promise(resolve => child.once('exit', resolve));
                child.kill();
                if (child.exitCode === null) await exited;
                const resolvedTemp = fs.realpathSync(temp);
                const tempRoot = fs.realpathSync(os.tmpdir());
                assert(path.dirname(resolvedTemp) === tempRoot && path.basename(resolvedTemp).startsWith('minihub-base-path-'), 'unsafe cleanup path');
                fs.rmSync(resolvedTemp, {recursive: true, force: true});
            }
        }
    } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
