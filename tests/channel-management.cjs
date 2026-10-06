// Run with Playwright available on NODE_PATH; optionally set BROWSER_PATH.
// Uses intercepted HTTP fixtures only; no running service or real user data.
const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

(async () => {
    const browser = await chromium.launch({ headless: true, ...(process.env.BROWSER_PATH ? { executablePath: process.env.BROWSER_PATH } : {}) });
    try {
        const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        const users = Array.from({ length: 500 }, (_, i) => ({ id: `u${String(i).padStart(3, '0')}`, name: `ユーザー${String(499 - i).padStart(3, '0')}`, enabled: i !== 499 }));
        users[1].name = users[2].name = '同名';
        let detail = { id: 'general', name: '全社連絡', type: 'public', members: ['u000', 'u499'], managers: ['u000'], groups: ['sales'], users, effectiveMembers: [], capabilities: { manageSettings: true, manageMembers: true, archive: true, restore: false } };
        let failSave = false, posted;
        await page.route('http://chat.test/**', async route => {
            const request = route.request(), p = new URL(request.url()).pathname;
            if (p === '/api/me') return route.fulfill({ status: 503, json: { error: 'fixture bootstrap' } });
            if (p === '/api/channels/general/members') {
                posted = request.postDataJSON();
                if (failSave) return route.fulfill({ status: 500, json: { error: '保存失敗テスト' } });
                for (const [kind, suffix] of [['members', 'Users'], ['managers', 'Managers'], ['groups', 'Groups']]) {
                    detail[kind] = [...new Set([...detail[kind], ...posted['add' + suffix]])].filter(id => !posted['remove' + suffix].includes(id));
                }
                return route.fulfill({ json: detail });
            }
            if (p === '/api/channels/general/management') return route.fulfill({ json: detail });
            if (p === '/api/channels') return route.fulfill({ json: detail.managers.includes('u000') ? [detail] : [] });
            if (p.startsWith('/api/')) return route.fulfill({ json: {} });
            const file = path.join(__dirname, '..', 'web', p === '/' ? 'index.html' : p);
            let body = fs.readFileSync(file);
            if (p === '/') body = Buffer.from(body.toString().replaceAll('{{.BasePath}}', '').replaceAll('{{.WorkspaceTitle}}', 'minihub').replaceAll('{{.NetworkPathMode}}', 'copy'));
            await route.fulfill({ body, contentType: p.endsWith('.css') ? 'text/css' : p.endsWith('.js') ? 'text/javascript' : p.endsWith('.svg') ? 'image/svg+xml' : 'text/html' });
        });
        await page.goto('http://chat.test/');
        await page.evaluate(async () => {
            S.me = { id: 'u000', name: '管理者', role: 'admin' };
            S.directory = new Map([['u000', '管理者']]);
            S.groups = new Map([['sales', '営業'], ['dev', '開発']]);
            await openChannelManagement();
        });
        const rows = page.locator('.channel-membership-row');
        await page.locator('#channelManagementModal').waitFor({ state: 'visible' });
        assert.equal(await rows.count(), 2);
        assert(await rows.filter({ hasText: 'u000' }).getByRole('button').isDisabled());
        assert(await rows.filter({ hasText: '無効' }).getByRole('button').isEnabled());
        await page.locator('#addChannelMembers').click();
        assert.equal(await rows.count(), 498);
        await page.locator('#channelMembershipSearch').fill('同名');
        assert.equal(await rows.count(), 2);
        assert((await rows.first().innerText()).includes('u001'));
        await rows.first().getByRole('checkbox').check();
        await page.locator('#channelMembershipSearch').fill('u003');
        await rows.first().getByRole('checkbox').check();
        await page.locator('#channelMembershipSort').selectOption('id');
        assert.equal(await page.locator('#applyChannelCandidates').innerText(), '選択した2件を追加');
        await page.locator('#applyChannelCandidates').click();
        assert.equal(await rows.count(), 4);
        await page.locator('#channelTabGroups').click();
        assert.equal(await rows.count(), 1);
        await page.locator('#channelTabManagers').click();
        assert(await rows.first().getByRole('button').isDisabled());
        await page.locator('#addChannelMembers').click();
        await page.locator('#channelMembershipSearch').fill('u004');
        await rows.first().getByRole('checkbox').check();
        await page.locator('#applyChannelCandidates').click();
        assert.equal(await rows.count(), 2);
        await page.locator('#channelTabMembers').click();
        assert.equal(await rows.count(), 5);
        assert(await rows.filter({ hasText: 'u004' }).getByRole('button').isDisabled());
        // Closing with edits can be cancelled without losing the draft.
        page.once('dialog', dialog => dialog.dismiss());
        await page.locator('#channelManagementModal .btn-close').click();
        assert(await page.locator('#channelManagementModal').isVisible());
        // A failed save preserves all edits and leaves retry enabled.
        failSave = true;
        await page.locator('#saveChannelMembership').click();
        await page.waitForFunction(() => !membershipBusy);
        assert.equal(await rows.count(), 5);
        assert(await page.locator('#saveChannelMembership').isEnabled());
        assert.deepEqual(posted.addUsers.sort(), ['u001', 'u003', 'u004']);
        assert.deepEqual(posted.addManagers, ['u004']);
        failSave = false;
        await page.locator('#saveChannelMembership').click();
        await page.waitForFunction(() => !membershipBusy && !membershipDirty());
        assert(await page.locator('#saveChannelMembership').isDisabled());
        // Candidate cancellation and keyboard tab navigation.
        await page.locator('#addChannelMembers').click();
        await page.locator('#channelMembershipSearch').fill('u005');
        await rows.first().getByRole('checkbox').check();
        await page.locator('#cancelChannelCandidates').click();
        assert.equal(await rows.count(), 5);
        await page.locator('#channelTabMembers').focus();
        await page.keyboard.press('ArrowRight');
        assert.equal(await page.locator('#channelTabGroups').getAttribute('aria-selected'), 'true');
        await page.locator('#channelTabMembers').click();
        await rows.filter({ hasText: 'u001' }).getByRole('button').click();
        await page.locator('#resetChannelMembership').click();
        assert.equal(await rows.count(), 5);
        // Render a full roster and verify list scrolling with visible toolbar/footer.
        detail.members = users.map(user => user.id);
        await page.evaluate(() => loadChannelManagement('general'));
        for (const [width, height] of [[1440, 900], [1024, 900], [900, 900], [768, 900], [360, 740]]) {
            await page.setViewportSize({ width, height });
            await page.locator('#channelMembershipList').evaluate(el => { el.scrollTop = el.scrollHeight; });
            assert(await page.evaluate(() => {
                const list = document.getElementById('channelMembershipList');
                const save = document.getElementById('saveChannelMembership').getBoundingClientRect();
                const search = document.getElementById('channelMembershipSearch').getBoundingClientRect();
                const dialog = document.querySelector('#channelManagementModal .modal-dialog').getBoundingClientRect();
                const content = document.querySelector('#channelManagementModal .user-modal').getBoundingClientRect();
                return list.scrollHeight > list.clientHeight && save.bottom <= innerHeight && search.top > 0 && document.documentElement.scrollWidth <= innerWidth && content.left >= dialog.left - 1 && content.right <= dialog.right + 1;
            }), `layout at ${width}`);
            if (process.env.SCREENSHOT_DIR) await page.screenshot({ path: path.join(process.env.SCREENSHOT_DIR, `channel-management-${width}.png`) });
        }
        // Archived channels remain searchable but cannot be modified.
        detail.archivedAt = '2026-09-16T00:00:00Z';
        await page.evaluate(() => loadChannelManagement('general'));
        assert(await page.locator('#addChannelMembers').isHidden());
        assert.equal(await page.locator('.channel-membership-row button:enabled').count(), 0);
        detail.archivedAt = null;
        detail.type = 'private';
        await page.evaluate(() => loadChannelManagement('general'));
        assert(!(await page.locator('#channelMembershipHint').innerText()).includes('解除後も再参加'));
        await page.locator('#channelMembershipSearch').fill('not-a-user');
        assert.equal(await rows.count(), 0);
        // Giving up management must return to an empty manageable-channel list.
        await page.evaluate(() => { S.me.role = 'user'; });
        await page.locator('#channelTabManagers').click();
        await rows.filter({ hasText: 'u000' }).getByRole('button').click();
        await page.locator('#saveChannelMembership').click();
        await page.waitForFunction(() => !membershipBusy && S.managedChannelDetail === null);
        assert(await page.locator('#channelManagementEmpty').isVisible());
        await page.evaluate(() => bootstrap.Modal.getOrCreateInstance($('channelManagementModal')).hide());
        await page.locator('#channelManagementModal').waitFor({ state: 'hidden' });
        await page.evaluate(() => {
            const longID = 'a'.repeat(64), longName = '長い表示名'.repeat(20);
            const user = document.createElement('button');
            user.className = 'user';
            user.innerHTML = '<span class="user-avatar">長</span><span class="user-copy"><b></b><small></small></span><span class="user-role">管理者</span>';
            user.querySelector('b').textContent = longName;
            user.querySelector('small').textContent = longID;
            $('userList').append(user);
            $('one').classList.add('hidden');
            $('editUser').classList.remove('hidden');
            const chip = document.createElement('span');
            chip.className = 'readonly-group';
            chip.textContent = longID;
            $('editgroups').replaceChildren(chip);
            $('groupEmpty').classList.add('hidden');
            $('groupDetail').classList.remove('hidden');
            $('groupDetailTitle').textContent = longName;
            $('groupDetailID').textContent = longID;
            $('channelManagementEmpty').classList.add('hidden');
            $('channelManagementDetail').classList.remove('hidden');
            $('managedChannelTitle').textContent = longName;
            $('managedChannelID').textContent = longID;
        });
        for (const modalID of ['userModal', 'groupModal', 'channelManagementModal']) {
            await page.evaluate(id => new Promise(resolve => {
                const modal = $(id);
                modal.addEventListener('shown.bs.modal', resolve, { once: true });
                bootstrap.Modal.getOrCreateInstance(modal).show();
            }), modalID);
            await page.locator(`#${modalID}`).waitFor({ state: 'visible' });
            for (const [width, zoom] of [[1440, 1], [1024, 1], [900, 1], [768, 1], [1024, 1.25]]) {
                await page.setViewportSize({ width, height: 900 });
                await page.evaluate(value => { document.documentElement.style.zoom = String(value); }, zoom);
                const layout = await page.locator(`#${modalID}`).evaluate(modal => {
                    const dialog = modal.querySelector('.modal-dialog').getBoundingClientRect();
                    const content = modal.querySelector('.user-modal').getBoundingClientRect();
                    const body = modal.querySelector('.modal-body');
                    return {
                        insideDialog: content.left >= dialog.left - 1 && content.right <= dialog.right + 1,
                        insideViewport: content.left >= 0 && content.right <= innerWidth,
                        bodyOverflow: body.scrollWidth > body.clientWidth + 1,
                    };
                });
                assert(layout.insideDialog && layout.insideViewport && !layout.bodyOverflow, `${modalID} at ${width}px, ${zoom * 100}%: ${JSON.stringify(layout)}`);
            }
            await page.evaluate(() => { document.documentElement.style.zoom = ''; });
            await page.evaluate(id => bootstrap.Modal.getOrCreateInstance($(id)).hide(), modalID);
            await page.locator(`#${modalID}`).waitFor({ state: 'hidden' });
        }
        await page.locator('#changePassword').evaluate(button => button.classList.remove('hidden'));
        const profileButtons = await page.locator('.profile-main').evaluate(profile => {
            const password = profile.querySelector('#changePassword').getBoundingClientRect();
            const logout = profile.querySelector('#logout').getBoundingClientRect();
            return { password: [password.width, password.height], logout: [logout.width, logout.height], inside: logout.right <= profile.getBoundingClientRect().right };
        });
        assert.deepEqual(profileButtons.password, profileButtons.logout);
        assert(profileButtons.inside);
        assert.deepEqual(errors, []);
        console.log('Channel management browser scenarios passed (500 users; desktop/mobile; draft/save/delegation).');
    } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
