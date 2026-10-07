// NODE_PATH must point to Playwright; BROWSER_PATH can select Chromium/Edge.
// node tests/ai-browser.cjs <executable> [--sqlite] [--disabled] [--update-screenshot]
const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const {spawn} = require('node:child_process');

(async () => {
    const sqlite = process.argv.includes('--sqlite'), disabled = process.argv.includes('--disabled');
    const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'minihub-ai-browser-'));
    const password = 'ai-browser-test-password';
    const calls = [];
    const ai = http.createServer(async (req, res) => {
        let body = '';
        for await (const chunk of req) body += chunk;
        assert.equal(req.url, '/v1/chat/completions');
        assert.equal(req.method, 'POST');
        assert.match(req.headers['x-minihub-request-id'], /^[0-9a-f]{32}$/);
        calls.push(JSON.parse(body));
        assert.equal(calls.at(-1).model, 'local-model');
        assert.equal(calls.at(-1).stream, false);
        res.writeHead(200, {'Content-Type': 'application/json'});
        res.end(JSON.stringify({choices: [{message: {role: 'assistant', content: '確認しました。受付手順を整理し、次の打ち合わせで改善案を共有しましょう。'}}]}));
    });
    await new Promise(resolve => ai.listen(0, '127.0.0.1', resolve));
    const reservation = http.createServer();
    await new Promise(resolve => reservation.listen(0, '127.0.0.1', resolve));
    const port = reservation.address().port;
    await new Promise(resolve => reservation.close(resolve));
    const basePath = sqlite ? '/hub' : '', base = `http://127.0.0.1:${port}${basePath}`;
    const config = path.join(temp, 'minihub.json');
    fs.writeFileSync(config, JSON.stringify({version: 1, storage: {type: sqlite ? 'sqlite' : 'file'},
        server: {basePath, dataDir: path.join(temp, 'data')},
        aiAccounts: disabled ? [] : [{id: 'helper', name: '社内アシスタント', url: `http://127.0.0.1:${ai.address().port}/v1/chat/completions`, model: 'local-model'}],
    }), {mode: 0o600});
    const app = spawn(path.resolve(process.argv[2]), ['-config', config, '-addr', `127.0.0.1:${port}`], {
        cwd: temp, env: {...process.env, MINIHUB_ADMIN_PASSWORD: password}, stdio: 'ignore', windowsHide: true,
    });
    let browser;
    try {
        for (let i = 0; i < 100; i++) {
            if (app.exitCode !== null) throw Error('Application exited');
            try { if ((await fetch(base + '/login')).ok) break; } catch {}
            await new Promise(resolve => setTimeout(resolve, 100));
        }
        const login = await fetch(base + '/api/auth/login', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({userId: 'admin', password})});
        assert.equal(login.status, 200);
        const cookie = login.headers.get('set-cookie').split(';')[0], csrf = (await login.json()).csrfToken;
        const api = async (route, method = 'GET', body) => {
            const response = await fetch(base + route, {method, headers: {Cookie: cookie, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json'}, ...(body ? {body: JSON.stringify(body)} : {})});
            assert.ok(response.ok, `${method} ${route}: ${response.status}`);
            return response.status === 204 ? null : response.json();
        };
        await api('/api/channels', 'POST', {id: 'general', name: '業務改善プロジェクト', type: 'public'});
        browser = await chromium.launch({executablePath: process.env.BROWSER_PATH || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe', headless: true});
        const context = await browser.newContext({viewport: {width: 1440, height: 1000}, locale: 'ja-JP', timezoneId: 'Asia/Tokyo'});
        const [name, ...value] = cookie.split('=');
        await context.addCookies([{name, value: value.join('='), url: base}]);
        const page = await context.newPage();
        const errors = [];
        page.on('pageerror', error => errors.push(error.stack));
        await page.goto(base + '/');
        await page.waitForFunction(() => S.me && S.channels.length && S.ws?.readyState === WebSocket.OPEN);
        await page.evaluate(() => select('general'));
        await page.waitForFunction(() => S.channel?.id === 'general' && !document.getElementById('input').disabled);
        if (disabled) {
            assert.equal(await page.locator('#aiButton').isVisible(), false);
            assert.equal(await page.locator('#threadAIButton').isVisible(), false);
            assert.deepEqual(await api('/api/ai-accounts'), []);
        } else {
            assert.equal(await page.locator('#aiButton').getAttribute('aria-label'), 'AIを呼び出す');
            await page.locator('#input').fill('新しい受付フローの改善点をまとめてください。 ');
            await page.locator('#aiButton').click();
            assert.equal(await page.locator('#mentionOptions').innerText(), '🤖 社内アシスタント\n@ai:helper');
            await page.locator('#mentionSearch').fill('アシスタント');
            await page.locator('#mentionSearch').press('ArrowDown');
            await page.keyboard.press('Enter');
            assert.match(await page.evaluate(() => editorValue()), /@ai:helper/);
            // Round-trip through the existing rich text serialization.
            await page.evaluate(() => { editor.value = editorValue(); });
            assert.match(await page.evaluate(() => editorValue()), /@ai:helper/);
            await page.locator('#send').click();
            await page.waitForFunction(() => S.msgs.length === 1);
            const root = await page.evaluate(() => S.msgs[0].seq);
            await page.evaluate(root => openThread('general', root), root);
            await page.waitForFunction(() => T.current?.messages.some(m => m.ai));
            assert.equal(calls.length, 1);
            assert.equal(calls[0].messages.length, 1);
            assert.match(await page.locator('#threadMessages').innerText(), /🤖 社内アシスタント/);
            // AI picker is separate from participant presence and supports IME guards.
            await page.locator('#threadInput').fill('続けて手順を整理してください。 ');
            await page.locator('#threadAIButton').click();
            await page.locator('#threadMentionOptions button').waitFor();
            assert.equal(await page.locator('#threadMentionOptions [data-presence-channel]').count(), 0);
            await page.locator('#threadMentionSearch').fill('helper');
            await page.locator('#threadMentionSearch').press('Enter');
            assert.match(await page.evaluate(() => document.getElementById('threadInput').value), /@ai:helper/);
            await page.evaluate(() => closeThread());
            await page.evaluate(root => openThread('general', root), root);
            await page.waitForFunction(() => document.getElementById('threadInput').value.includes('@ai:helper'));
            await page.locator('#threadSend').click();
            await page.waitForFunction(() => T.current?.messages.filter(m => m.ai).length === 2);
            assert.equal(calls.length, 2);
            assert.equal(calls[1].messages.length, 3);
            assert.deepEqual(calls[1].messages.map(m => m.role), ['user', 'assistant', 'user']);
            await page.locator('.toast .btn-close').evaluateAll(buttons => buttons.forEach(button => button.click()));
            await page.waitForTimeout(200);
            const screenshot = path.resolve('.tmp', `ai-browser-${sqlite ? 'sqlite' : 'file'}.png`);
            await page.screenshot({path: screenshot});
            if (!sqlite && process.argv.includes('--update-screenshot')) fs.copyFileSync(screenshot, path.resolve('docs/images/ai.png'));
            await page.setViewportSize({width: 390, height: 844});
            await page.locator('#threadAIButton').click();
            await page.locator('#threadMentionOptions button').waitFor();
            assert.ok(await page.locator('#threadMentionOptions button').isVisible());
            await page.locator('#threadMentionSearch').press('Escape');
        }
        assert.deepEqual(errors, []);
        console.log(`AI browser passed: ${sqlite ? 'sqlite /hub' : 'file /'} ${disabled ? 'disabled' : 'enabled'}`);
    } finally {
        await browser?.close();
        if (app.exitCode === null) {
            await new Promise(resolve => { app.once('exit', resolve); app.kill(); });
        }
        ai.closeAllConnections();
        await new Promise(resolve => ai.close(resolve));
        const resolved = path.resolve(temp);
        if (!resolved.startsWith(path.resolve(os.tmpdir()) + path.sep) || !path.basename(resolved).startsWith('minihub-ai-browser-')) throw Error('Unsafe cleanup target');
        fs.rmSync(resolved, {recursive: true, force: true});
    }
})().catch(error => { console.error(error); process.exitCode = 1; });
