// Run with NODE_PATH pointing to a Playwright installation and BROWSER_PATH to Chromium/Edge.
// Usage: node tests/presence-browser.cjs <absolute path to minihub executable>
const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const net = require('node:net');
const {spawn} = require('node:child_process');

(async () => {
    const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'minihub-presence-'));
    const reservation = net.createServer();
    await new Promise(resolve => reservation.listen(0, '127.0.0.1', resolve));
    const port = reservation.address().port;
    await new Promise(resolve => reservation.close(resolve));
    const base = `http://127.0.0.1:${port}`;
    const password = 'presence-test-password';
    const app = spawn(process.argv[2], ['-addr', `127.0.0.1:${port}`, '-data', path.join(temp, 'data')], {
        cwd: temp, env: {...process.env, MINIHUB_ADMIN_PASSWORD: password}, stdio: 'ignore',
    });
    let browser;
    try {
        let ready = false;
        for (let i = 0; i < 100; i++) {
            try { ready = (await fetch(`${base}/login`)).ok; } catch {}
            if (ready) break;
            await new Promise(resolve => setTimeout(resolve, 50));
        }
        assert.ok(ready, 'server did not start');
        const login = async userId => {
            const response = await fetch(`${base}/api/auth/login`, {
                method: 'POST', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({userId, password}),
            });
            assert.equal(response.status, 200);
            return {cookie: response.headers.get('set-cookie').split(';')[0], csrf: (await response.json()).csrfToken};
        };
        const api = async (session, route, method = 'GET', body) => {
            const response = await fetch(base + route, {
                method, headers: {Cookie: session.cookie, 'X-CSRF-Token': session.csrf, 'Content-Type': 'application/json'},
                ...(body ? {body: JSON.stringify(body)} : {}),
            });
            if (!response.ok) throw Error(`${route}: ${response.status} ${await response.text()}`);
            return response.status === 204 ? null : response.json();
        };
        const admin = await login('admin');
        await api(admin, '/api/users', 'POST', {users: [{id: 'bob', name: '鈴木', password, role: 'user'}]});
        const bob = await login('bob');
        await api(admin, '/api/channels', 'POST', {id: 'general', name: 'General', type: 'public'});
        await api(bob, '/api/channels/general/join', 'POST');
        const root = await api(admin, '/api/channels/general/messages', 'POST', {text: 'Question'});
        browser = await chromium.launch({executablePath: process.env.BROWSER_PATH, headless: true});
        const context = async session => {
            const ctx = await browser.newContext();
            const [name, ...value] = session.cookie.split('=');
            await ctx.addCookies([{name, value: value.join('='), url: base}]);
            return ctx;
        };
        const adminContext = await context(admin);
        const adminPage = await adminContext.newPage();
        await adminPage.goto(base);
        await adminPage.waitForFunction(() => S.me && S.ws?.readyState === WebSocket.OPEN);
        await adminPage.evaluate(() => select('general'));
        await adminPage.waitForFunction(() => S.channel?.id === 'general');
        await adminPage.locator('#mentionButton').click();
        const mainOption = adminPage.locator('#mentionOptions [data-user-id="bob"]');
        await mainOption.locator('small').getByText('未接続', {exact: false}).waitFor();
        const bobContext = await context(bob);
        const bobPage1 = await bobContext.newPage();
        await bobPage1.goto(base);
        await bobPage1.waitForFunction(() => S.ws?.readyState === WebSocket.OPEN);
        await mainOption.locator('small').getByText('接続中', {exact: false}).waitFor();
        assert.ok(await mainOption.evaluate(option => option.classList.contains('presence-online')));
        const bobPage2 = await bobContext.newPage();
        await bobPage2.goto(base);
        await bobPage2.waitForFunction(() => S.ws?.readyState === WebSocket.OPEN);
        await bobPage1.close();
        await new Promise(resolve => setTimeout(resolve, 400));
        assert.ok(await mainOption.evaluate(option => option.classList.contains('presence-online')));
        await bobPage2.close();
        await mainOption.locator('small').getByText('未接続', {exact: false}).waitFor();
        await adminPage.evaluate(seq => openThread('general', seq), root.seq);
        await adminPage.waitForFunction(() => T.current?.channel === 'general');
        await adminPage.locator('#threadMentionButton').click();
        const threadOption = adminPage.locator('#threadMentionOptions [data-user-id="bob"]');
        await threadOption.locator('small').getByText('未接続', {exact: false}).waitFor();
        const bobPage3 = await bobContext.newPage();
        await bobPage3.goto(base);
        await bobPage3.waitForFunction(() => S.ws?.readyState === WebSocket.OPEN);
        await threadOption.locator('small').getByText('接続中', {exact: false}).waitFor();
        assert.ok(await threadOption.evaluate(option => option.classList.contains('presence-online')));
        await bobPage3.close();
        await threadOption.locator('small').getByText('未接続', {exact: false}).waitFor();
        console.log('PASS: main and thread mention presence, two tabs, last disconnect');
    } finally {
        await browser?.close();
        app.kill();
    }
})().catch(error => { console.error(error); process.exitCode = 1; });
