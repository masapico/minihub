import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';
import {test} from 'node:test';

const script = readFileSync(new URL('../web/js/urls.js', import.meta.url), 'utf8');
for (const basePath of ['', '/hub', '/apps/team-chat']) {
    test(`deployment URLs and safe login return paths: ${basePath || '/'}`, () => {
        const context = {
            window: {}, URL,
            document: {documentElement: {dataset: {basePath}}},
            location: {origin: 'https://myhost.com', protocol: 'https:', host: 'myhost.com', pathname: basePath + '/schedules/example'},
        };
        runInNewContext(script, context);
        const urls = context.window.Minihub;
        assert.equal(urls.url('/api/me'), basePath + '/api/me');
        assert.equal(urls.url('/schedules/new?channelId=team'), basePath + '/schedules/new?channelId=team');
        assert.equal(urls.path(), '/schedules/example');
        assert.equal(urls.websocketURL(), `wss://myhost.com${basePath}/api/realtime`);
        context.location.protocol = 'http:';
        assert.equal(urls.websocketURL(), `ws://myhost.com${basePath}/api/realtime`);
        assert.equal(urls.storageKey('preference'), basePath ? `preference:base:${basePath}` : 'preference');
        for (const path of ['/schedules', '/schedules?period=past', '/schedules/example?tab=answers#results', '/schedules/new?edit=example']) {
            assert.equal(urls.scheduleReturnURL(basePath + path), basePath + path);
        }
        for (const next of [null, '', 'https://evil.test', '//evil.test/schedules', '//[', '/another/schedules', basePath + '/schedules-other', basePath + '/schedules/../../outside', basePath + '/schedules/%2e%2e/%2e%2e/outside', basePath + '/schedules/\\evil.test']) {
            assert.equal(urls.scheduleReturnURL(next), basePath + '/', next);
        }
    });
}
