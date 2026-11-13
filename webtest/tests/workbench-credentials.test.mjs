import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('credential wizard selects a provider, validates input, saves, and finishes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  app.router.get('/api/auth/gongfeng', { enabled: true });
  app.router.put('/api/users/1/credentials', {
    id: 1, name: 'Test User', username: 'tester', codeBuddyApiKeyConfigured: true,
  });
  await app.evaluate(`renderPersonalSettings()`);
  app.evaluate(`openCredentialWizard()`);
  const wizard = () => app.document.querySelector('#credentialWizard');
  wizard().querySelector('[data-wizard-kind="codeBuddyApiKey"]').click();
  wizard().querySelector('[data-wizard-next]').click();
  await settle(3);
  const secret = wizard().querySelector('input[name="secret"]');
  secret.value = 'placeholder-codebuddy-key';
  secret.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  wizard().querySelector('[data-wizard-next]').click();
  await settle(3);
  wizard().querySelector('[data-wizard-next]').click();
  await settle(6);
  assert.match(wizard().textContent, /已保存/);
  wizard().querySelector('[data-wizard-done]').click();
  assert.ok(app.router.calls.some((call) => call.path === '/api/users/1/credentials'));
});

test('credential wizard focuses the first field on its own but never pulls focus from a field the user picked', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  await app.evaluate(`renderPersonalSettings()`);
  app.evaluate(`openCredentialWizard()`);
  const wizard = app.document.querySelector('#credentialWizard');
  const pane = () => wizard.querySelector('.credential-pane:not(.is-leaving)');
  const afterFocusTimers = () => new Promise((resolve) => setTimeout(resolve, 300));
  wizard.querySelector('[data-wizard-kind="codexApiKey"]').click();
  wizard.querySelector('[data-wizard-next]').click();
  await afterFocusTimers();
  assert.equal(app.document.activeElement, pane().querySelector('input[name="secret"]'));

  wizard.querySelector('[data-wizard-back]').click();
  wizard.querySelector('[data-wizard-next]').click();
  const baseURL = pane().querySelector('input[name="extra"]');
  baseURL.focus();
  await afterFocusTimers();
  assert.equal(app.document.activeElement, baseURL);
  baseURL.value = 'not-a-url';
  baseURL.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.match(pane().querySelector('[data-credential-hint="extra"]').textContent, /http:\/\//);
  assert.equal(pane().querySelector('input[name="secret"]').value, '');
});

test('CodeBuddy wizard picks the network environment from a list and its key link follows the choice', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  app.router.put('/api/users/1/credentials', { id: 1, username: 'tester', codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'internal' });
  await app.evaluate(`renderPersonalSettings()`);
  app.evaluate(`openCredentialWizard('codeBuddyApiKey')`);
  const pane = () => app.document.querySelector('#credentialWizard .credential-pane:not(.is-leaving)');
  const select = pane().querySelector('select[name="extra"]');
  assert.ok(select, 'network environment is a list, not free text');
  assert.equal(pane().querySelector('input[name="extra"]'), null);
  assert.deepEqual([...select.options].map((option) => option.value), ['', 'internal', 'ioa']);
  const guideLink = () => pane().querySelector('[data-environment-link]');
  const hintLink = () => pane().querySelector('[data-credential-key-link]');
  assert.equal(guideLink().getAttribute('href'), 'https://www.codebuddy.ai/profile/keys');
  select.value = 'internal';
  select.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(guideLink().getAttribute('href'), 'https://copilot.tencent.com/profile/');
  assert.match(guideLink().textContent, /国内站/);
  assert.equal(hintLink().getAttribute('href'), 'https://copilot.tencent.com/profile/');

  const secret = pane().querySelector('input[name="secret"]');
  secret.value = 'sk-third-party';
  secret.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  const hint = pane().querySelector('[data-credential-hint="secret"]');
  assert.equal(hint.className, 'is-warn');
  assert.match(hint.textContent, /第三方/);
  const next = app.document.querySelector('#credentialWizard [data-wizard-next]');
  assert.equal(next.disabled, false, 'the warning never blocks saving');
  secret.value = 'ck_placeholder';
  secret.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(pane().querySelector('[data-credential-hint="secret"]').className, 'is-ok');
  next.click();
  await settle(3);
  assert.match(pane().textContent, /国内站 · copilot\.tencent\.com/);
  app.document.querySelector('#credentialWizard [data-wizard-next]').click();
  await settle(6);
  const saved = app.router.calls.find((call) => call.path === '/api/users/1/credentials');
  assert.deepEqual(saved.body, { codeBuddyApiKey: 'ck_placeholder', codeBuddyInternetEnvironment: 'internal' });
});

test('CodeBuddy inline editor changes only the network environment and keeps the key link in step', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.users[0].id=1;
    state.currentUser={...state.data.users[0],id:1,isGuest:false,codeBuddyApiKeyConfigured:true,codeBuddyInternetEnvironment:'ioa'};
    state.data.users[0]=state.currentUser;
  `);
  app.router.put('/api/users/1/credentials', { id: 1, username: 'tester', codeBuddyApiKeyConfigured: true });
  await app.evaluate(`renderPersonalSettings()`);
  const row = app.document.querySelector('[data-credential-row="codeBuddyApiKey"]');
  assert.match(row.querySelector('.credential-row-who small').textContent, /网络环境iOA/);
  row.querySelector('.credential-row-main').click();
  const select = row.querySelector('select[name="extra"]');
  const save = row.querySelector('[data-credential-act="save"]');
  assert.equal(select.value, 'ioa');
  assert.equal(save.disabled, true);
  select.value = '';
  select.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  assert.equal(save.disabled, false);
  assert.equal(row.querySelector('[data-credential-key-link]').getAttribute('href'), 'https://www.codebuddy.ai/profile/keys');
  save.click();
  await settle(6);
  const saved = app.router.calls.find((call) => call.path === '/api/users/1/credentials');
  assert.deepEqual(saved.body, { codeBuddyApiKey: '', codeBuddyInternetEnvironment: '' });
});

test('CodeBuddy inline editor rewrites an old china value to internal without retyping the key', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.users[0].id=1;
    state.currentUser={...state.data.users[0],id:1,isGuest:false,codeBuddyApiKeyConfigured:true,codeBuddyInternetEnvironment:'china'};
    state.data.users[0]=state.currentUser;
  `);
  app.router.put('/api/users/1/credentials', { id: 1, username: 'tester', codeBuddyApiKeyConfigured: true, codeBuddyInternetEnvironment: 'internal' });
  await app.evaluate(`renderPersonalSettings()`);
  const row = () => app.document.querySelector('[data-credential-row="codeBuddyApiKey"]');
  assert.match(row().querySelector('.credential-row-who small').textContent, /「china」无法识别/);
  row().querySelector('.credential-row-main').click();
  const select = row().querySelector('select[name="extra"]');
  const save = row().querySelector('[data-credential-act="save"]');
  assert.equal(select.value, 'internal', 'the old value preselects what it meant');
  assert.equal(save.disabled, false, 'the stored text still needs rewriting, so saving is allowed as shown');
  assert.match(row().querySelector('[data-credential-env-hint]').textContent, /「china」无法识别/);
  save.click();
  await settle(6);
  const saved = app.router.calls.find((call) => call.path === '/api/users/1/credentials');
  assert.deepEqual(saved.body, { codeBuddyApiKey: '', codeBuddyInternetEnvironment: 'internal' });
  assert.match(row().querySelector('.credential-row-who small').textContent, /网络环境国内站/);
});

test('credential panel filters, expands, validates, reveals secrets, and confirms deletion', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.users[0].id=1;
    state.currentUser={...state.data.users[0],id:1,isGuest:false,gitTokenConfigured:true,codeBuddyApiKeyConfigured:true};
    state.data.users[0]=state.currentUser;
  `);
  app.router.get('/api/auth/gongfeng', { enabled: true });
  app.router.get('/api/users/1/git-token/verify', { valid: true, username: 'tester', scopes: ['repo'] });
  app.router.put('/api/users/1/credentials', {
    id: 1, name: 'Test User', username: 'tester', gitTokenConfigured: false, codeBuddyApiKeyConfigured: true,
  });
  await app.evaluate(`renderPersonalSettings()`);
  const panel = app.document.querySelector('#credentialPanel');
  panel.querySelector('[data-credential-filter="configured"]')?.click();
  panel.querySelector('[data-credential-filter="missing"]')?.click();
  panel.querySelector('[data-credential-filter="all"]')?.click();
  panel.querySelector('.credential-group-head')?.click();
  panel.querySelector('.credential-group-head')?.click();
  const row = panel.querySelector('[data-credential-row="gitToken"]');
  row.querySelector('.credential-row-main')?.click();
  row.querySelector('[data-credential-eye]')?.click();
  row.querySelector('[data-credential-act="delete"]')?.click();
  row.querySelector('[data-credential-act="undelete"]')?.click();
  await app.evaluate(`refreshGitTokenStatus({announce:true})`);
  app.evaluate(`revealCredentialRow('gitToken');repaintCredentialRow('gitToken');flashCredentialRow('gitToken')`);
  assert.match(panel.textContent, /工蜂|校验/);
});

test('Git token verification rejects invalid tokens before save and accepts valid ones', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  app.router.post('/api/users/1/git-token/verify', (call) =>
    call.body.token === 'valid-token'
      ? { valid: true, username: 'tester', scopes: ['repo'] }
      : { valid: false, error: 'invalid token' });
  app.router.put('/api/users/1/credentials', {
    id: 1, username: 'tester', gitTokenConfigured: true,
  });
  const invalid = await app.evaluate(`verifyAndSaveGitToken('bad')`);
  const valid = await app.evaluate(`verifyAndSaveGitToken('valid-token')`);
  assert.equal(invalid.saved, false);
  assert.equal(valid.saved, true);
});

test('Qoder editor rides the official endpoint: catalog without Base URL or protocol', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const user = {
    id: 1, username: 'tester',
    agentCliCredentials: {
      qoder: { apiKeyConfigured: true, models: [{ id: 'auto' }, { id: 'qoder3-max', name: 'Qoder3 Max' }] },
    },
  };
  const kind = await app.evaluate(`credentialKind('agentCli.qoder')`);
  assert.ok(kind, 'qoder credential kind exists');
  assert.ok(kind.models, 'qoder exposes the model catalog');
  assert.equal(kind.extra, undefined, 'qoder has no Base URL field');
  assert.equal(kind.protocol, undefined, 'qoder has no protocol selector');

  const html = await app.evaluate(`credentialEditorHTML(credentialKind('agentCli.qoder'), ${JSON.stringify(user)})`);
  assert.match(html, /data-credential-catalog/);
  assert.match(html, /data-catalog-mode="official"/);
  assert.doesNotMatch(html, /name="extra"/);
  assert.doesNotMatch(html, /name="protocol"/);
  assert.match(html, /value="qoder3-max"/);

  const subline = await app.evaluate(`credentialSubline(credentialKind('agentCli.qoder'), ${JSON.stringify(user)})`);
  assert.match(subline, /2 个自定义模型/);

  const payload = await app.evaluate(`credentialSavePayload(credentialKind('agentCli.qoder'), {secret:'qdr_pat', models:[{id:'auto',name:''},{id:'qoder3-max',name:'Qoder3 Max'}]})`);
  // The payload crosses the vm realm, so compare by JSON structure.
  assert.deepEqual(JSON.parse(JSON.stringify(payload.agentCliCredentials.qoder)), {
    apiKey: 'qdr_pat',
    models: [{ id: 'auto' }, { id: 'qoder3-max', name: 'Qoder3 Max' }],
  });
});

test('个人配置 offers a server install for a CLI the host lacks', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  app.router.get('/api/assistant/cli-install/status', {
    engines: { kimi: { status: 'missing' }, dsh: { status: 'installed' }, qoder: { status: 'failed', error: 'boom' } },
  });
  await app.evaluate(`renderPersonalSettings()`);
  await settle(4);

  const kimi = app.document.querySelector('[data-credential-row="agentCli.kimi"]');
  assert.match(kimi.textContent, /服务器未安装/);
  const install = kimi.querySelector('[data-credential-act="cli-install"]');
  assert.ok(install, 'a missing CLI gets the install shortcut');
  assert.equal(install.dataset.cliEngine, 'kimi');

  const installed = app.document.querySelector('[data-credential-row="agentCli.dsh"]');
  assert.equal(installed.querySelector('[data-credential-act="cli-install"]'), null);
  assert.doesNotMatch(installed.textContent, /服务器未安装/);

  const failed = app.document.querySelector('[data-credential-row="agentCli.qoder"]');
  assert.match(failed.querySelector('[data-credential-act="cli-install"]').textContent, /重试安装/);

  // An engine outside the server's install allowlist (no status entry) stays quiet.
  const zcode = app.document.querySelector('[data-credential-row="agentCli.zcode"]');
  assert.equal(zcode.querySelector('[data-credential-act="cli-install"]'), null);
  assert.doesNotMatch(zcode.textContent, /服务器未安装/);
});

test('每个模型行在自己删除按钮旁提供测试按钮', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`state.data.users[0].id=1;state.currentUser={...state.data.users[0],id:1,isGuest:false};state.data.users[0]=state.currentUser`);
  await app.evaluate(`renderPersonalSettings()`);
  await settle(3);
  const kind = await app.evaluate(`credentialKind('agentCli.zcode')`);
  const html = await app.evaluate(`credentialCatalogHTML(credentialKind('agentCli.zcode'), {agentCliCredentials:{zcode:{models:[{id:'glm-5.3'}]}}})`);
  // 模型行里测试按钮紧跟删除按钮
  assert.match(html, /data-catalog-act="test"[^>]*>[\s\S]*?data-catalog-act="remove"/);
  assert.match(html, /data-catalog-test/);
  // 整张卡的删除按钮边上不再有测试按钮
  const row = app.evaluate(`(() => {
    const kind = credentialKind('agentCli.zcode');
    const user = {agentCliCredentials:{zcode:{apiKeyConfigured:true,models:[{id:'glm-5.3'}]}}};
    return credentialEditorHTML(kind, user);
  })()`);
  const foot = row.slice(row.indexOf('credential-editor-foot'));
  assert.doesNotMatch(foot.slice(0, foot.indexOf('credential-confirm')), /data-credential-act="cli-test"/);
  assert.ok(kind);
});
