// Capture real application screens with disposable data and Node's built-in CDP client.
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const executable = process.argv[2] && path.resolve(process.argv[2]);
if (!executable) throw new Error('Usage: node scripts/capture-readme.mjs <minihub executable>');
await fs.access(executable);
const browserPath = process.env.BROWSER_PATH || (process.platform === 'win32'
    ? 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
    : process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : 'chromium');
const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'minihub-readme-'));
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const children = [];
let socket;
const pending = new Map();
function start(command, args, options = {}) {
    const child = spawn(command, args, {stdio: ['ignore', 'ignore', 'pipe'], ...options});
    child.failure = null;
    child.on('error', error => { child.failure = error; });
    children.push(child);
    return child;
}
async function stop(child) {
    if (child.exitCode !== null || child.signalCode !== null || !child.pid) return;
    const exited = new Promise(resolve => child.once('exit', resolve));
    child.kill();
    let timer;
    await Promise.race([exited, new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error('Child process did not stop')), 10000);
    })]).finally(() => clearTimeout(timer));
}

try {
    const reservation = net.createServer();
    await new Promise((resolve, reject) => {
        reservation.once('error', reject);
        reservation.listen(0, '127.0.0.1', resolve);
    });
    const port = reservation.address().port;
    await new Promise(resolve => reservation.close(resolve));
    const base = `http://127.0.0.1:${port}`;
    const password = 'readme-demo-password';
    const app = start(executable, ['-addr', `127.0.0.1:${port}`, '-data', path.join(temp, 'data')], {
        cwd: temp, env: {...process.env, MINIHUB_ADMIN_PASSWORD: password},
    });
    // Drain stderr, but never include credentials or message bodies in diagnostics.
    app.stderr.resume();
    let ready = false;
    for (let i = 0; i < 200; i++) {
        if (app.failure) throw app.failure;
        if (app.exitCode !== null) throw new Error('Application exited before becoming ready');
        try { ready = (await fetch(base + '/login', {signal: AbortSignal.timeout(1000)})).ok; } catch {}
        if (ready) break;
        await pause(100);
    }
    if (!ready) throw new Error('Application startup timeout');
    async function login(userId) {
        const response = await fetch(base + '/api/auth/login', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({userId, password}), signal: AbortSignal.timeout(10000),
        });
        if (!response.ok) throw new Error(`Login failed: ${response.status}`);
        return {cookie: response.headers.get('set-cookie').split(';')[0], csrf: (await response.json()).csrfToken};
    }
    async function api(session, url, method = 'GET', body) {
        const response = await fetch(base + url, {
            method, headers: {Cookie: session.cookie, 'X-CSRF-Token': session.csrf, 'Content-Type': 'application/json'},
            ...(body === undefined ? {} : {body: JSON.stringify(body)}), signal: AbortSignal.timeout(10000),
        });
        if (!response.ok) throw new Error(`${method} ${url}: ${response.status}`);
        const text = await response.text();
        return text ? JSON.parse(text) : null;
    }
    const admin = await login('admin');
    await api(admin, '/api/users', 'POST', {users: [
        {id: 'sato', name: '佐藤 花子', password, role: 'user'},
        {id: 'suzuki', name: '鈴木 太郎', password, role: 'user'},
        {id: 'tanaka', name: '田中 美咲', password, role: 'user'},
    ]});
    const users = await Promise.all(['sato', 'suzuki', 'tanaka'].map(login));
    for (const [id, name] of [['general', '全社連絡'], ['project', '業務改善プロジェクト'], ['lounge', '雑談・相談']]) {
        await api(admin, '/api/channels', 'POST', {id, name, type: 'public'});
        for (const user of users) await api(user, `/api/channels/${id}/join`, 'POST');
    }
    const post = (user, text) => api(user, '/api/channels/project/messages', 'POST', {text});
    await post(users[1], '今週の業務改善プロジェクトの進捗を共有します。');
    const topic = await post(users[2], '新しい受付フローの案をまとめました。\nまずは営業チームで試して、気づいた点をこのスレッドで共有しませんか？');
    await api(users[0], `/api/channels/project/messages/${topic.seq}/reactions/ack`, 'PUT');
    await api(users[1], `/api/channels/project/messages/${topic.seq}/reactions/thanks`, 'PUT');
    for (const [user, text] of [
        [users[1], 'いいですね！入力項目を減らせそうです。'],
        [users[0], '確認しました。来週から試行できるように準備します。'],
        [users[2], 'ありがとうございます。試行後に改善点をまとめましょう。'],
    ]) await api(user, `/api/channels/project/threads/${topic.seq}/messages`, 'POST', {text});
    await post(users[0], '打ち合わせの日程と、次に取り組むテーマも相談しましょう。');
    const pollDetail = await api(users[2], '/api/polls', 'POST', {
        requestId: 'readme-demo-poll', channelId: 'project', question: '次に改善したい業務は？',
        description: '日々の業務で、特に時間がかかっているものを教えてください。',
        options: ['問い合わせの受付', '会議の準備・議事録', '社内情報の共有'], multiple: false,
    });
    const poll = pollDetail.poll;
    for (let i = 0; i < users.length; i++) await api(users[i], `/api/polls/${poll.id}/vote`, 'PUT', {
        revision: 0, optionIds: [poll.options[i === 1 ? 1 : 0].id],
    });
    const dateParts = new Intl.DateTimeFormat('en', {
        timeZone: 'Asia/Tokyo', year: 'numeric', month: '2-digit', day: '2-digit',
    }).formatToParts(new Date());
    const datePart = type => dateParts.find(part => part.type === type).value;
    const today = new Date(`${datePart('year')}-${datePart('month')}-${datePart('day')}T00:00:00Z`);
    const candidates = [7, 8, 9].map(days => ({
        date: new Date(today.getTime() + days * 86400000).toISOString().slice(0, 10),
        startTime: '14:00', endTime: '15:00',
    }));
    let schedule = await api(users[2], '/api/schedules', 'POST', {
        channelId: 'project', title: '業務改善ミーティング',
        description: '受付フローの試行結果を共有します。参加できる日程を回答してください。', candidates,
    });
    schedule = await api(users[2], `/api/schedules/${schedule.id}/publish`, 'POST', {revision: schedule.revision});
    const choices = [['yes', 'maybe', 'no'], ['yes', 'yes', 'no'], ['yes', 'no', 'maybe']];
    for (let i = 0; i < users.length; i++) await api(users[i], `/api/schedules/${schedule.id}/responses/me`, 'PUT', {
        revision: schedule.revision,
        choices: Object.fromEntries(schedule.candidates.map((candidate, j) => [candidate.id, choices[i][j]])),
        comment: ['初日が参加しやすいです。', '資料を準備します。', 'よろしくお願いします。'][i],
    });

    const chrome = start(browserPath, ['--headless=new', '--remote-debugging-port=0',
        '--lang=ja-JP', '--user-data-dir=' + path.join(temp, 'browser'), 'about:blank']);
    const browserURL = await new Promise((resolve, reject) => {
        let log = '';
        const timer = setTimeout(() => reject(new Error('Browser startup timeout')), 15000);
        const fail = error => { clearTimeout(timer); reject(error); };
        chrome.once('error', fail);
        chrome.once('exit', () => fail(new Error('Browser exited before becoming ready')));
        chrome.stderr.on('data', bytes => {
            log = (log + bytes).slice(-8192);
            const match = log.match(/DevTools listening on (ws:\/\/\S+)/);
            if (match) { clearTimeout(timer); resolve(match[1]); }
        });
    });
    const pages = await (await fetch('http://' + new URL(browserURL).host + '/json/list')).json();
    socket = new WebSocket(pages.find(page => page.type === 'page').webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
        socket.addEventListener('open', resolve, {once: true});
        socket.addEventListener('error', reject, {once: true});
    });
    let sequence = 0;
    const browserErrors = [];
    socket.addEventListener('message', event => {
        const message = JSON.parse(event.data);
        const request = pending.get(message.id);
        if (request) {
            pending.delete(message.id);
            clearTimeout(request.timer);
            if (message.error) request.reject(new Error(message.error.message));
            else request.resolve(message.result);
        }
        if (message.method === 'Runtime.exceptionThrown') browserErrors.push(message.params.exceptionDetails);
    });
    const send = (method, params = {}) => new Promise((resolve, reject) => {
        const id = ++sequence;
        const timer = setTimeout(() => { pending.delete(id); reject(new Error(`CDP timeout: ${method}`)); }, 15000);
        pending.set(id, {resolve, reject, timer});
        socket.send(JSON.stringify({id, method, params}));
    });
    async function run(expression) {
        const result = await send('Runtime.evaluate', {expression, awaitPromise: true, returnByValue: true});
        if (result.exceptionDetails) throw new Error('Browser evaluation failed: ' + expression);
        return result.result.value;
    }
    async function wait(expression) {
        for (let i = 0; i < 200; i++) {
            if (await run(expression)) return;
            await pause(50);
        }
        throw new Error('Screen readiness timeout: ' + expression);
    }
    await send('Runtime.enable');
    await send('Page.enable');
    await send('Emulation.setDeviceMetricsOverride', {width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false});
    await send('Emulation.setTimezoneOverride', {timezoneId: 'Asia/Tokyo'});
    await send('Emulation.setLocaleOverride', {locale: 'ja-JP'});
    const [cookieName, ...cookieValue] = users[0].cookie.split('=');
    await send('Network.setCookie', {name: cookieName, value: cookieValue.join('='), url: base});
    const images = new Map();
    async function capture(name, fullPage = false) {
        await run('document.fonts.ready');
        await run('new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))');
        if (browserErrors.length) throw new Error('Uncaught browser error during capture');
        const metrics = fullPage ? await send('Page.getLayoutMetrics') : null;
        const shot = await send('Page.captureScreenshot', {
            format: 'png', captureBeyondViewport: fullPage,
            ...(fullPage ? {clip: {x: 0, y: 0, width: 1440, height: Math.ceil(metrics.cssContentSize.height), scale: 1}} : {}),
        });
        images.set(name, Buffer.from(shot.data, 'base64'));
        console.log(`Captured ${name}`);
    }
    await send('Page.navigate', {url: base});
    await wait("typeof S !== 'undefined' && S.me && S.channels.length === 3");
    await run("select('project')");
    await wait("S.channel?.id === 'project' && document.querySelector('#messages .poll-card')");
    await run(`openThread('project', ${topic.seq})`);
    await wait("typeof T !== 'undefined' && T.current?.messages.length === 3 && document.querySelectorAll('#threadMessages .message').length === 4");
    await capture('chat.png');
    await run('closeThread()');
    await run("document.querySelector('#messages .poll-card').scrollIntoView({block:'center'})");
    await wait("document.querySelector('#messages .poll-card').textContent.includes('回答者 3人')");
    await capture('poll.png');
    await send('Page.navigate', {url: base + '/schedules/' + schedule.id});
    await wait("document.querySelector('#results tbody')?.rows.length === 4 && document.querySelectorAll('#responseChoices input:checked').length === 3");
    await capture('schedule.png', true);

    // Stage every image first; a failed capture leaves the published set untouched.
    const output = path.join(root, 'docs', 'images');
    await fs.mkdir(output, {recursive: true});
    for (const [name, bytes] of images) await fs.writeFile(path.join(temp, name), bytes);
    for (const name of images.keys()) await fs.copyFile(path.join(temp, name), path.join(output, name));
    console.log('Updated docs/images/{chat,poll,schedule}.png');
} finally {
    for (const request of pending.values()) clearTimeout(request.timer);
    socket?.close();
    const stopped = await Promise.allSettled(children.reverse().map(stop));
    const failure = stopped.find(result => result.status === 'rejected');
    if (failure) throw failure.reason;
    // temp is an absolute directory created by mkdtemp above, never a user-supplied path.
    await fs.rm(temp, {recursive: true, force: true, maxRetries: 10, retryDelay: 200});
}
