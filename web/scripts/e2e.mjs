import { chromium } from 'playwright';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';

const baseURL = process.env.E2E_BASE_URL || 'http://127.0.0.1:18082';
const output = path.resolve(process.env.E2E_OUTPUT_DIR || '../.cache/ui-check');
if (process.env.E2E_ALLOW_DEMO_RESET !== '1') throw new Error('Use an isolated demo server and set E2E_ALLOW_DEMO_RESET=1.');
const readState = async () => {
  const response = await fetch(baseURL + '/api/state');
  assert.equal(response.status, 200);
  return response.json();
};
assert.equal((await readState()).settings.mode, 'demo', 'Never run these tests on a real cluster.');
await mkdir(output, { recursive: true });
const request = (endpoint, body = {}) => fetch(baseURL + '/api' + endpoint, {
  method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'KubePilot' }, body: JSON.stringify(body),
});
const prepare = async (endpoint, body = {}) => {
  const response = await request(endpoint, body);
  if (!response.ok) throw new Error('Fixture preparation failed: ' + endpoint + ' ' + await response.text());
};
for (const action of ['restore_image', 'restore_selector', 'restore_probe']) await prepare('/policies/' + action, { enabled: true, automatic: false });
await prepare('/demo/reset');
for (const incident of (await readState()).incidents.filter(i => i.status !== 'resolved')) await prepare('/incidents/' + incident.id + '/verify');
for (const fault of ['ImagePullBackOff', 'ProbeFailure', 'Pending']) await prepare('/demo/inject', { fault });
await prepare('/inspections');

const browser = await chromium.launch({ headless: true, ...(process.env.E2E_BROWSER === 'bundled' ? {} : { channel: process.env.E2E_BROWSER || 'chrome' }) });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce', permissions: ['clipboard-read', 'clipboard-write'] });
const page = await context.newPage();
const pageErrors = [];
const failedRequests = [];
page.on('pageerror', error => pageErrors.push(error.message));
page.on('response', response => { if (response.status() >= 500) failedRequests.push(response.status() + ' ' + response.url()); });
const checks = [];
const check = name => { checks.push(name); console.log('PASS ' + name); };
const pages = ['集群概览', '自动巡检', '故障事件', '处置策略', '操作审计', '实验工作台'];
async function navigate(name) {
  if (page.viewportSize().width <= 680) {
    if (!['集群概览', '实验工作台', '故障事件'].includes(name)) {
      await page.getByRole('button', { name: '更多功能', exact: true }).click();
      await page.getByRole('navigation', { name: '更多功能', exact: true }).getByRole('button', { name, exact: true }).click();
    } else await page.getByRole('navigation', { name: '移动主导航', exact: true }).getByRole('button', { name, exact: true }).click();
  } else await page.getByRole('navigation', { name: '主导航', exact: true }).getByRole('button', { name, exact: true }).click();
  await page.getByRole('heading', { name, exact: true, level: 1 }).waitFor();
}
async function waitState(predicate) {
  const start = Date.now();
  while (Date.now() - start < 15000) {
    const state = await readState();
    if (predicate(state)) return state;
    await new Promise(resolve => setTimeout(resolve, 150));
  }
  throw new Error('State assertion timed out');
}
async function noOverflow(label) {
  const sizes = await page.evaluate(() => ({ viewport: innerWidth, content: document.documentElement.scrollWidth }));
  assert.ok(sizes.content <= sizes.viewport, label + ' overflow: ' + JSON.stringify(sizes));
}
async function screenshot(name, fullPage = true) {
  await page.waitForFunction(() => !document.querySelector('.overlay-enter-active, .overlay-leave-active'));
  await page.screenshot({ path: path.join(output, name + '.png'), fullPage, animations: 'disabled' });
}

try {
  await page.goto(baseURL + '/#overview');
  await page.getByText('kind-graduation-demo', { exact: true }).waitFor();
  await page.getByText('数据来自独立模拟环境，操作仅作用于演示资源。', { exact: false }).waitFor();
  check('overview shows explicit demo provenance and real API data');
  for (const name of pages) { await navigate(name); await noOverflow('1440px ' + name); }
  check('all six workspaces are reachable without page overflow');

  await navigate('集群概览');
  await page.getByRole('button', { name: '运行巡检', exact: true }).click();
  await page.getByRole('status').filter({ hasText: '巡检完成' }).waitFor();
  await navigate('故障事件');
  await page.getByRole('textbox', { name: '搜索事件', exact: true }).fill('不存在的工作负载');
  await page.getByRole('heading', { name: '没有匹配的事件', exact: true }).waitFor();
  await page.getByRole('button', { name: '清除筛选', exact: true }).click();
  await page.getByRole('button', { name: '待诊断', exact: true }).click();
  await page.getByRole('row').filter({ hasText: 'checkout-api' }).first().click();
  assert.equal(await page.locator('.app-shell').evaluate(el => el.inert), true);
  await page.getByRole('button', { name: '关闭详情', exact: true }).focus();
  await page.keyboard.press('Shift+Tab');
  assert.equal(await page.evaluate(() => document.activeElement.textContent.trim()), '诊断故障');
  check('drawer makes the background inert and traps keyboard focus');

  await page.getByRole('button', { name: '复制', exact: true }).click();
  await page.getByRole('status').filter({ hasText: '故障证据已复制' }).waitFor();
  assert.ok((await page.evaluate(() => navigator.clipboard.readText())).length > 30);
  await page.getByRole('tab', { name: '故障证据', exact: true }).focus();
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.getByRole('tab', { name: '诊断与处置', exact: true }).getAttribute('aria-selected'), 'true');
  check('evidence copying and keyboard-operated detail tabs work');

  await page.getByRole('button', { name: '诊断故障', exact: true }).click();
  await page.getByText('规则引擎', { exact: true }).waitFor();
  const toastBounds = await page.locator('.toast-message').boundingBox();
  const footerBounds = await page.locator('.drawer-footer').boundingBox();
  assert.ok(toastBounds.y + toastBounds.height < footerBounds.y, 'toast must not cover drawer actions');
  await screenshot('drawer-diagnosis');
  await page.getByRole('button', { name: '执行演示修复', exact: true }).click();
  await page.getByRole('heading', { name: '确认执行处置计划', exact: true }).waitFor();
  assert.equal(await page.evaluate(() => document.activeElement.textContent.trim()), '取消');
  await screenshot('confirm-execution');
  await page.keyboard.press('Escape');
  await page.getByRole('dialog', { name: '镜像拉取失败', exact: true }).waitFor();
  assert.equal(await page.evaluate(() => document.activeElement.textContent.trim()), '执行演示修复');
  assert.equal(await page.locator('.app-shell').evaluate(el => el.inert), true);
  check('Escape closes only the nested confirmation and restores its trigger');

  await page.getByRole('button', { name: '执行演示修复', exact: true }).click();
  await page.getByRole('button', { name: '确认执行', exact: true }).click();
  await page.getByText('恢复验证通过', { exact: true }).waitFor({ timeout: 15000 });
  await waitState(s => s.incidents.some(i => i.type === 'ImagePullBackOff' && i.status === 'resolved'));
  await screenshot('drawer-recovered');
  check('inspection → evidence → diagnosis → confirmation → execution → verified recovery');
  await page.getByRole('button', { name: '关闭详情', exact: true }).click();
  assert.equal(await page.locator('.app-shell').evaluate(el => el.inert), false);
  await page.reload();
  await page.getByRole('heading', { name: '故障事件', exact: true, level: 1 }).waitFor();
  await page.getByRole('button', { name: '已恢复', exact: true }).click();
  await page.getByRole('row').filter({ hasText: 'checkout-api' }).first().waitFor();
  check('route and recovered events survive browser reload');
  await navigate('自动巡检'); await navigate('处置策略'); await page.goBack();
  await page.getByRole('heading', { name: '自动巡检', exact: true, level: 1 }).waitFor();
  check('browser Back restores the previous workspace');

  await navigate('处置策略');
  const imagePolicy = page.locator('.policy-card').filter({ has: page.getByRole('heading', { name: '恢复镜像版本', exact: true }) });
  await imagePolicy.getByRole('switch').click();
  await waitState(s => !s.policies.find(p => p.action === 'restore_image').enabled);
  await navigate('实验工作台');
  await page.getByRole('button', { name: '载入镜像拉取失败场景', exact: true }).click();
  await page.getByRole('button', { name: /^运行请求/ }).click();
  await page.getByRole('status').filter({ hasText: '故障已注入并完成巡检' }).waitFor();
  await navigate('故障事件');
  await page.getByRole('button', { name: '待诊断', exact: true }).click();
  await page.getByRole('row').filter({ hasText: 'checkout-api' }).first().click();
  await page.getByRole('button', { name: '诊断故障', exact: true }).click();
  await page.getByText('对应处置策略已关闭，请先启用策略。', { exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: '执行演示修复', exact: true }).isDisabled(), true);
  const planned = (await readState()).incidents.find(i => i.type === 'ImagePullBackOff' && i.status === 'planned');
  assert.ok(planned);
  const refused = await request('/incidents/' + planned.id + '/execute');
  assert.equal(refused.status, 409);
  assert.match((await refused.json()).error, /处置策略未允许/);
  assert.equal((await readState()).incidents.find(i => i.id === planned.id).executionCount, 0);
  check('disabled policy is explained in the UI and the API independently refuses a write');
  await page.getByRole('button', { name: '关闭详情', exact: true }).click();

  await navigate('操作审计');
  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('button', { name: '导出记录', exact: true }).click();
  const download = await downloadPromise;
  const csvPath = path.join(output, 'audit-export.csv');
  await download.saveAs(csvPath);
  assert.match(await readFile(csvPath, 'utf8'), /"actor".*"action"/);
  check('audit CSV downloads with actual records');

  await page.keyboard.press('Control+k');
  const command = page.getByRole('combobox', { name: '搜索页面或故障', exact: true });
  await command.fill('实验'); await command.press('Enter');
  await page.getByRole('heading', { name: '实验工作台', exact: true, level: 1 }).waitFor();
  await page.keyboard.press('Control+k'); await command.fill('checkout-api'); await command.press('Enter');
  await page.getByRole('dialog', { name: '镜像拉取失败', exact: true }).waitFor();
  await page.keyboard.press('Escape');
  check('Ctrl+K searches and opens both pages and incident details');

  // Controlled browser-only error fixtures verify UI feedback without breaking the service.
  await navigate('集群概览');
  await page.route('**/api/inspections', route => route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: '测试：巡检暂不可用' }) }));
  await page.getByRole('button', { name: '运行巡检', exact: true }).click();
  await page.getByRole('alert').filter({ hasText: '测试：巡检暂不可用' }).waitFor();
  assert.equal(await page.getByRole('button', { name: '运行巡检', exact: true }).isDisabled(), false);
  await page.unroute('**/api/inspections');
  await page.getByRole('button', { name: '关闭提示', exact: true }).click();
  await page.route('**/api/state', route => route.abort());
  await page.getByRole('button', { name: '刷新状态', exact: true }).click();
  await page.getByText('连接暂时中断', { exact: true }).waitFor();
  await page.getByText('kind-graduation-demo', { exact: true }).waitFor();
  await screenshot('connection-error');
  await page.unroute('**/api/state');
  await page.getByRole('button', { name: '重新连接', exact: true }).click();
  await page.getByRole('status').filter({ hasText: '已同步最新状态' }).waitFor();
  if (await page.getByRole('button', { name: '关闭提示', exact: true }).isVisible()) await page.getByRole('button', { name: '关闭提示', exact: true }).click();
  check('mutation errors stay visible; offline state preserves data and reconnects');

  for (const [width, height] of [[1440, 1000], [768, 1024], [390, 844]]) {
    await page.setViewportSize({ width, height });
    for (const name of pages) {
      await navigate(name);
      await noOverflow(width + 'px ' + name);
      const slug = ['overview', 'inspections', 'incidents', 'policies', 'audit', 'experiments'][pages.indexOf(name)];
      await screenshot(slug + '-' + width);
      if (name === '集群概览') await screenshot('viewport-' + width, false);
    }
    if (width === 390) {
      await page.getByRole('button', { name: '更多功能', exact: true }).click();
      await page.getByRole('dialog', { name: '更多功能', exact: true }).waitFor();
      await screenshot('mobile-more', false);
      await page.keyboard.press('Escape');
      assert.equal(await page.evaluate(() => document.activeElement.getAttribute('aria-label')), '更多功能');
      await page.getByRole('button', { name: '更多功能', exact: true }).click();
      await page.getByRole('navigation', { name: '更多功能', exact: true }).getByRole('button', { name: '环境与连接', exact: true }).click();
      await page.getByRole('dialog', { name: '环境与连接', exact: true }).waitFor();
      await screenshot('environment-mobile', false);
      await page.getByRole('button', { name: '知道了', exact: true }).click();
      assert.equal(await page.locator('.app-shell').evaluate(el => el.inert), false);
      await navigate('故障事件');
      await page.getByRole('row').filter({ hasText: 'inventory-api' }).first().click();
      await noOverflow('390px drawer'); await screenshot('drawer-mobile', false);
      await page.getByRole('button', { name: '关闭详情', exact: true }).click();
    }
  }
  check('1440px / 768px / 390px: all six pages, phone navigation and drawer verified with screenshots');
  for (const width of [360, 680, 681, 860, 1024, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    for (const name of ['集群概览', '故障事件', '处置策略', '实验工作台']) { await navigate(name); await noOverflow(width + 'px ' + name); }
  }
  check('intermediate widths 360 / 680 / 681 / 860 / 1024 / 1280 have no page overflow');
  await navigate('集群概览');
  const reduced = await page.locator('.page-view').evaluate(el => getComputedStyle(el).animationDuration);
  assert.equal(reduced, '1e-05s');
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await navigate('集群概览');
  const animated = await page.locator('.page-view').evaluate(el => getComputedStyle(el).animationDuration);
  assert.equal(animated, '0.18s');
  check('motion preference is respected; normal page transitions remain enabled');

  const loading = await context.newPage();
  let releaseState;
  const held = new Promise(resolve => { releaseState = resolve; });
  await loading.route('**/api/state', async route => { await held; await route.continue(); });
  await loading.goto(baseURL + '/#overview', { waitUntil: 'domcontentloaded' });
  await loading.getByRole('status', { name: '正在加载工作空间', exact: true }).waitFor();
  await loading.screenshot({ path: path.join(output, 'loading.png'), fullPage: true });
  releaseState();
  await loading.getByText('kind-graduation-demo', { exact: true }).waitFor();
  await loading.close();
  check('initial loading skeleton transitions to actual state');

  const empty = await context.newPage();
  const emptyState = { ...(await readState()), incidents: [], inspections: [], audits: [], experiments: [], snapshot: undefined };
  await empty.route('**/api/state', route => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(emptyState) }));
  await empty.goto(baseURL + '/#incidents');
  await empty.getByRole('heading', { name: '还没有故障事件', exact: true }).waitFor();
  await empty.screenshot({ path: path.join(output, 'empty-events.png'), fullPage: true, animations: 'disabled' });
  await empty.close();
  check('empty event state provides a clear next action');

  const login = await context.newPage();
  await login.route('**/api/state', route => {
    if (route.request().headers().authorization === 'Bearer ui-check-local-token') return route.continue();
    return route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: '请输入有效的访问令牌' }) });
  });
  await login.goto(baseURL + '/#overview');
  await login.getByRole('dialog', { name: '连接你的工作空间', exact: true }).waitFor();
  await login.keyboard.press('Escape');
  assert.equal(await login.getByRole('dialog', { name: '连接你的工作空间', exact: true }).count(), 1);
  assert.equal(await login.getByRole('button', { name: '连接工作空间', exact: true }).isDisabled(), true);
  await login.getByLabel('访问令牌', { exact: true }).fill('ui-check-local-token');
  await login.getByRole('button', { name: '连接工作空间', exact: true }).click();
  await login.getByText('kind-graduation-demo', { exact: true }).waitFor();
  assert.equal(await login.evaluate(() => sessionStorage.getItem('kubepilot-token')), 'ui-check-local-token');
  await login.close();
  check('token form handles 401, disabled submission, Escape protection and session-only storage');

  await navigate('实验工作台');
  const previousCount = (await readState()).incidents.length;
  await page.getByRole('button', { name: '工作台设置', exact: true }).click();
  await page.getByRole('button', { name: '复位演示环境', exact: true }).click();
  await page.getByRole('button', { name: '确认复位', exact: true }).click();
  await page.getByRole('status').filter({ hasText: '演示资源已复位，历史记录已保留' }).waitFor();
  const resetState = await readState();
  assert.equal(resetState.incidents.length, previousCount);
  assert.equal(resetState.snapshot.readyPods, resetState.snapshot.pods);
  check('reset confirmation restores demo resources and preserves event history');
  assert.deepEqual(pageErrors, []);
  assert.deepEqual(failedRequests, []);
  check('no uncaught browser exceptions or HTTP 5xx during the tested flows');
  await writeFile(path.join(output, 'result.json'), JSON.stringify({ baseURL, checks, pageErrors, failedRequests, completedAt: new Date().toISOString(), browser: 'installed Chrome, Playwright', viewports: [1440, 768, 390], intermediateWidths: [360, 680, 681, 860, 1024, 1280] }, null, 2));
} catch (error) {
  await screenshot('failure');
  throw error;
} finally { await browser.close(); }
