import { chromium } from 'playwright';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';

const baseURL = process.env.E2E_BASE_URL || 'http://127.0.0.1:18082';
const output = path.resolve(process.env.E2E_OUTPUT_DIR || '../docs/console-review/workbench');
if (process.env.E2E_ALLOW_DEMO_RESET !== '1') throw new Error('Use an isolated demo server and set E2E_ALLOW_DEMO_RESET=1.');
const readState = async () => {
  const response = await fetch(baseURL + '/api/state');
  assert.equal(response.status, 200);
  return response.json();
};
const prepare = async (endpoint, body = {}) => {
  const response = await fetch(baseURL + '/api' + endpoint, {
    method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'KubePilot' }, body: JSON.stringify(body),
  });
  assert.equal(response.ok, true, endpoint + ': ' + await response.text());
};
assert.equal((await readState()).settings.mode, 'demo');
await prepare('/demo/reset');
for (const incident of (await readState()).incidents.filter(i => i.status !== 'resolved')) await prepare('/incidents/' + incident.id + '/verify');
for (const fault of ['ImagePullBackOff', 'ProbeFailure']) await prepare('/demo/inject', { fault });
await prepare('/inspections');
await mkdir(output, { recursive: true });

const browser = await chromium.launch({ headless: true, ...(process.env.E2E_BROWSER === 'bundled' ? {} : { channel: process.env.E2E_BROWSER || 'chrome' }) });
const context = await browser.newContext({ viewport: { width: 1917, height: 919 }, reducedMotion: 'reduce', permissions: ['clipboard-read', 'clipboard-write'] });
const page = await context.newPage();
page.setDefaultTimeout(10000);
const checks = [], pageErrors = [], writes = [];
context.on('page', next => next.on('pageerror', error => pageErrors.push(error.message)));
page.on('pageerror', error => pageErrors.push(error.message));
page.on('request', request => { if (request.method() === 'POST') writes.push(request.url()); });
const check = name => { checks.push(name); console.log('PASS ' + name); };
const stateEditor = page.getByRole('textbox', { name: '排障目标 JSON', exact: true });
const taskEditor = page.getByRole('textbox', { name: '执行任务 JSON', exact: true });
const runButton = page.getByRole('button', { name: /^运行请求/ });
const clear = () => page.getByRole('button', { name: 'Clear', exact: true }).click();
const choose = action => page.locator('.task-card').filter({ has: page.locator('.task-tag', { hasText: new RegExp('^' + action + '$') }) }).click();
const jsonState = value => stateEditor.fill(JSON.stringify(value));
const screenshot = async name => {
  await page.waitForFunction(() => !document.querySelector('.overlay-enter-active, .overlay-leave-active'));
  await page.screenshot({ path: path.join(output, name + '.png'), animations: 'disabled' });
};
const complete = async heading => {
  await page.locator('.console-result').getByRole('heading', { name: heading, exact: true }).waitFor();
  await page.waitForFunction(() => !document.querySelector('.operation-progress'));
};
try {
  await page.goto(baseURL);
  await page.waitForFunction(() => !document.querySelector('.run-request').disabled);
  const geometry = await page.evaluate(() => ({
    sidebar: document.querySelector('.sidebar').getBoundingClientRect().width,
    banner: document.querySelector('.announcement-bar').getBoundingClientRect().height,
    editorLeft: document.querySelector('.console-editor').getBoundingClientRect().left,
    editorRight: document.querySelector('.console-editor').getBoundingClientRect().right,
    stateBottom: document.querySelector('.state-section').getBoundingClientRect().bottom,
    color: getComputedStyle(document.querySelector('.console-editor')).backgroundColor,
    background: getComputedStyle(document.body).backgroundColor,
  }));
  assert.equal(geometry.sidebar, 254);
  assert.equal(geometry.banner, 32);
  assert.equal(geometry.editorLeft, 254);
  assert.ok(Math.abs(geometry.editorRight - 958) < 1);
  assert.ok(Math.abs(geometry.stateBottom - 455.5) < 1);
  assert.equal(geometry.color, 'rgb(29, 29, 29)');
  await screenshot('reference-layout-1917');
  check('default workbench matches reference sidebar, banner, editor ratio and dark surface');

  const separator = page.getByRole('separator', { name: '调整编辑区宽度' });
  await separator.focus();
  await page.keyboard.press('ArrowRight');
  assert.equal(await separator.getAttribute('aria-valuenow'), '44');
  await page.keyboard.press('Home');
  const divider = await separator.boundingBox();
  await page.mouse.move(divider.x + 4, divider.y + 100);
  await page.mouse.down();
  await page.mouse.move(divider.x + 104, divider.y + 100, { steps: 8 });
  await page.mouse.up();
  assert.ok(Number(await separator.getAttribute('aria-valuenow')) > 47);
  await separator.dblclick();
  assert.equal(await separator.getAttribute('aria-valuenow'), '42');
  await page.getByRole('button', { name: '收起导航', exact: true }).click();
  await page.waitForFunction(() => document.querySelector('.sidebar').getBoundingClientRect().width === 56);
  assert.equal(await page.locator('.sidebar').evaluate(node => node.getBoundingClientRect().width), 56);
  await page.reload();
  await page.getByRole('button', { name: '展开导航', exact: true }).click();
  await page.getByRole('button', { name: '关闭顶部提示' }).click();
  await page.reload();
  assert.equal(await page.locator('.announcement-bar').count(), 0);
  await page.evaluate(() => sessionStorage.removeItem('kubepilot-announcement'));
  await page.reload();
  await stateEditor.waitFor();
  check('pointer and keyboard resizing, collapse, notice dismissal and session persistence');

  const namespace = (await readState()).settings.namespace;
  const beforeInvalid = writes.length;
  for (const [value, message] of [
    ['{', 'State 必须是有效的 JSON 对象'],
    [JSON.stringify({ namespace: 'outside-scope', workload: '' }), '命名空间必须与当前连接范围一致'],
    [JSON.stringify({ namespace, workload: '', unknown: true }), 'State 仅接受'],
  ]) {
    await stateEditor.fill(value);
    await page.getByRole('alert').filter({ hasText: message }).waitFor();
    assert.equal(await runButton.isDisabled(), true);
    await page.keyboard.press('Control+Enter');
  }
  assert.equal(writes.length, beforeInvalid);
  await clear();
  await page.getByRole('button', { name: '编辑任务 JSON', exact: true }).click();
  await taskEditor.fill('{"action":"delete"}');
  await page.getByRole('alert').filter({ hasText: '请选择 inspect' }).waitFor();
  assert.equal(await runButton.isDisabled(), true);
  await taskEditor.fill('{"action":"inject","fault":"ImagePullBackOff"}');
  await jsonState({ namespace, workload: 'different-workload' });
  await page.getByRole('alert').filter({ hasText: '此演示场景的目标是 checkout-api' }).waitFor();
  assert.equal(await runButton.isDisabled(), true);
  await taskEditor.fill('{"action":"inspect"}');
  await jsonState({ namespace, workload: '' });
  await page.getByRole('button', { name: 'Format', exact: true }).click();
  assert.ok((await taskEditor.inputValue()).includes('\n  "action"'));
  check('invalid JSON, namespace, fields, action and injection target cannot issue writes; format works');

  await page.getByRole('button', { name: '表单目标编辑', exact: true }).click();
  await page.getByRole('textbox', { name: '目标工作负载', exact: true }).fill('inventory-api');
  await page.getByRole('button', { name: 'JSON 目标编辑', exact: true }).click();
  assert.equal(JSON.parse(await stateEditor.inputValue()).workload, 'inventory-api');
  const fileInput = page.getByLabel('导入排障目标文件', { exact: true });
  const upload = value => fileInput.setInputFiles({ name: 'target.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(value)) });
  await upload({ namespace, workload: 'checkout-api' });
  await page.waitForFunction(() => JSON.parse(document.querySelector('textarea[aria-label="排障目标 JSON"]').value).workload === 'checkout-api');
  assert.equal(JSON.parse(await stateEditor.inputValue()).workload, 'checkout-api');
  await upload({ namespace, workload: 'inventory-api', token: 'not-a-real-token' });
  await page.getByRole('alert').filter({ hasText: '目标文件仅支持' }).waitFor();
  assert.equal(JSON.parse(await stateEditor.inputValue()).workload, 'checkout-api');
  await page.getByRole('button', { name: '选择任务类型', exact: true }).click();
  await page.getByRole('button', { name: '选择已有故障目标', exact: true }).click();
  await page.getByRole('textbox', { name: '搜索排障目标', exact: true }).fill('inventory');
  await page.getByRole('dialog').getByRole('button', { name: /^inventory-api/ }).click();
  assert.equal(JSON.parse(await stateEditor.inputValue()).workload, 'inventory-api');
  check('form and JSON stay synchronized; import validates structure; target search selects actual incidents');

  await choose('diagnose');
  await runButton.click();
  await complete('故障诊断完成');
  assert.equal((await readState()).incidents.filter(i => i.target.name === 'inventory-api' && i.status !== 'resolved').at(-1).status, 'planned');
  await screenshot('diagnosis-result-1917');
  await page.locator('.response-topline').getByRole('button', { name: 'JSON', exact: true }).click();
  const diagnosed = JSON.parse(await page.locator('.result-json').innerText());
  assert.equal(diagnosed.action, 'diagnose');
  assert.equal(diagnosed.incident.target.name, 'inventory-api');
  await page.getByRole('button', { name: '复制运行结果', exact: true }).click();
  assert.equal(JSON.parse(await page.evaluate(() => navigator.clipboard.readText())).incident.id, diagnosed.incident.id);
  await page.locator('.response-topline').getByRole('button', { name: '结果', exact: true }).click();
  await page.getByRole('button', { name: '查看证据与处置计划' }).click();
  await page.getByRole('dialog').waitFor();
  await page.keyboard.press('Escape');
  await page.getByRole('dialog').waitFor({ state: 'hidden' });
  check('real diagnosis, structured response, clipboard export and incident drawer are connected');

  await choose('verify');
  await runButton.click();
  await complete('结果尚未确认');
  assert.ok((await page.locator('.result-message').innerText()).includes('尚未确认恢复'));
  assert.equal(await page.locator('.console-result .pill.success').count(), 0);
  check('verification of an unrepaired fault is explicitly unconfirmed, never reported as recovered');

  await page.getByRole('button', { name: '复制请求链接' }).click();
  const sharedURL = await page.evaluate(() => navigator.clipboard.readText());
  assert.equal(new URL(sharedURL).searchParams.get('action'), 'verify');
  const shared = await context.newPage();
  let sharedWrites = 0;
  shared.on('request', request => { if (request.method() === 'POST') sharedWrites++; });
  await shared.goto(sharedURL);
  await shared.getByRole('button', { name: /^运行请求/ }).waitFor();
  assert.equal(JSON.parse(await shared.getByRole('textbox', { name: '排障目标 JSON', exact: true }).inputValue()).workload, 'inventory-api');
  assert.equal(await shared.locator('.task-card[aria-pressed="true"] .task-tag').innerText(), 'verify');
  await shared.getByRole('button', { name: 'Clear', exact: true }).click();
  assert.equal(new URL(shared.url()).search, '');
  assert.equal(sharedWrites, 0);
  await shared.close();
  check('share restores target and task without executing; Clear also removes shared request parameters');

  await clear();
  await page.getByRole('button', { name: '载入服务端点异常场景', exact: true }).click();
  assert.equal(JSON.parse(await stateEditor.inputValue()).workload, 'payment-service');
  const experimentsBefore = (await readState()).experiments.length;
  await runButton.click();
  await complete('故障注入完成');
  assert.ok((await readState()).experiments.length > experimentsBefore);
  assert.ok((await readState()).incidents.some(i => i.type === 'ServiceSelector' && i.status !== 'resolved'));
  await page.getByRole('button', { name: '工作台设置' }).click();
  await page.getByRole('dialog').getByRole('button', { name: '实验记录', exact: true }).click();
  const records = page.getByRole('dialog', { name: '实验记录', exact: true });
  await records.waitFor();
  const download = page.waitForEvent('download');
  await records.getByRole('button', { name: '导出全部实验记录', exact: true }).click();
  await (await download).saveAs(path.join(output, 'experiments.csv'));
  await page.keyboard.press('Escape');
  check('reference-style examples inject a real demo fault; settings expose records and CSV export');

  await clear();
  const inspectionCount = (await readState()).inspections.length;
  await stateEditor.focus();
  await page.keyboard.press('Control+Enter');
  await complete('集群巡检完成');
  assert.ok((await readState()).inspections.length > inspectionCount);
  await screenshot('inspection-result-1917');
  await page.route('**/api/inspections', route => route.request().method() === 'POST' ? route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: '测试夹具：巡检请求被拒绝' }) }) : route.continue());
  await runButton.click();
  await complete('请求未完成');
  assert.ok((await page.locator('.result-message').innerText()).includes('巡检请求被拒绝'));
  await screenshot('request-error');
  await page.unroute('**/api/inspections');
  check('Ctrl+Enter runs an inspection; rejected requests render an explicit error result');

  for (const [width, height] of [[1440, 1000], [768, 1024], [390, 844], [360, 760]]) {
    await page.setViewportSize({ width, height });
    if (width <= 1100) await page.getByRole('tab', { name: '请求编辑', exact: true }).click();
    await clear();
    await screenshot('request-' + width);
    const sizes = await page.evaluate(() => ({ viewport: innerWidth, content: document.documentElement.scrollWidth, footer: document.querySelector('.request-footer').getBoundingClientRect().toJSON() }));
    assert.ok(sizes.content <= sizes.viewport);
    assert.ok(sizes.footer.y >= 0 && sizes.footer.bottom <= height);
    await page.getByRole('button', { name: 'Docs', exact: true }).click();
    await page.getByRole('button', { name: '关闭操作说明', exact: true }).click();
    await page.getByRole('button', { name: '重新打开操作说明', exact: true }).click();
    await screenshot('guide-' + width);
    if (width <= 1100) {
      const responseTab = page.getByRole('tab', { name: '操作说明', exact: true });
      await responseTab.focus();
      await page.keyboard.press('ArrowLeft');
      assert.equal(await page.getByRole('tab', { name: '请求编辑', exact: true }).getAttribute('aria-selected'), 'true');
      await page.keyboard.press('End');
      assert.equal(await responseTab.getAttribute('aria-selected'), 'true');
      await page.keyboard.press('Home');
    }
    await runButton.click();
    await complete('集群巡检完成');
    assert.equal(await page.locator('.console-result').isVisible(), true);
    await screenshot('result-' + width);
  }
  check('1440 / 768 / 390 / 360px: editor, guide and actual results fit; mobile tabs support keyboard and auto-switch');
  assert.deepEqual(pageErrors, []);
  check('no uncaught browser exceptions during workbench flows');
  await writeFile(path.join(output, 'result.json'), JSON.stringify({ baseURL, checks, geometry, pageErrors, completedAt: new Date().toISOString(), browser: 'installed Chrome, Playwright', viewports: [1917, 1440, 768, 390, 360] }, null, 2));
} catch (error) {
  await screenshot('failure').catch(() => {});
  await writeFile(path.join(output, 'failure.json'), JSON.stringify({ error: error.stack, checks, pageErrors }, null, 2));
  throw error;
} finally {
  await browser.close();
}
