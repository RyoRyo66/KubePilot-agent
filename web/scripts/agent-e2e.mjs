import { chromium } from 'playwright';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';

const baseURL = process.env.E2E_BASE_URL || 'http://127.0.0.1:18082';
const output = path.resolve(process.env.E2E_OUTPUT_DIR || '../docs/agent-review');
if (process.env.E2E_ALLOW_DEMO_RESET !== '1') throw new Error('Use an isolated demo server and set E2E_ALLOW_DEMO_RESET=1.');
const state = async () => { const response = await fetch(baseURL + '/api/state'); assert.equal(response.status, 200); return response.json(); };
assert.equal((await state()).settings.mode, 'demo');
assert.equal((await state()).settings.agentModel, 'compatible:fixture-model', 'Start the explicitly labelled local protocol fixture.');
const post = async (endpoint, body = {}) => {
  const response = await fetch(baseURL + '/api' + endpoint, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'KubePilot' }, body: JSON.stringify(body) });
  assert.equal(response.ok, true, endpoint + ': ' + await response.text());
};
await post('/demo/reset');
for (const incident of (await state()).incidents.filter(i => i.status !== 'resolved')) await post('/incidents/' + incident.id + '/verify');
for (const fault of ['ImagePullBackOff', 'ProbeFailure', 'Pending']) await post('/demo/inject', { fault });
await post('/inspections');
for (const action of ['restore_image', 'restore_selector', 'restore_probe']) await post('/policies/' + action, { enabled: true, automatic: false });
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ channel: process.env.E2E_BROWSER || 'chrome', headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' });
page.setDefaultTimeout(15000);
const errors = [], checks = [];
page.on('pageerror', error => errors.push(error.message));
const check = name => { checks.push(name); console.log('PASS ' + name); };
const screenshot = async name => {
  await page.waitForFunction(() => !document.querySelector('.overlay-enter-active, .overlay-leave-active'));
  await page.screenshot({ path: path.join(output, name + '.png'), animations: 'disabled' });
};
const waitRun = async predicate => {
  const end = Date.now() + 15000;
  while (Date.now() < end) {
    const run = (await state()).agentRuns.at(-1);
    if (run && predicate(run)) return run;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error('Agent run did not reach expected state');
};
const start = async (workload, mode) => {
  await page.getByLabel('目标工作负载', { exact: true }).fill(workload);
  await page.getByLabel('执行方式', { exact: true }).selectOption(mode);
  await page.getByRole('button', { name: '启动 Agent', exact: true }).click();
};
try {
  await page.goto(baseURL + '/#agent');
  await page.getByRole('heading', { name: 'Agent 任务', exact: true, level: 1 }).waitFor();
  await page.getByText('compatible:fixture-model', { exact: true }).first().waitFor();
  assert.equal(await page.getByRole('button', { name: '启动 Agent', exact: true }).isDisabled(), true);
  await screenshot('empty-1440');
  check('Agent page and empty state expose environment, configured model and validated task form');
  await start('checkout-api', 'approval');
  let run = await waitRun(r => r.status === 'awaiting_approval');
  await page.getByRole('button', { name: '核对并执行计划', exact: true }).waitFor();
  assert.equal(run.modelCalls, 3);
  assert.equal(run.fallback, false);
  assert.equal(run.diagnosis.engine, 'compatible:fixture-model');
  assert.equal((await state()).incidents.find(i => i.id === run.incidentId).executionCount, 0);
  assert.ok(run.steps.some(s => s.tool === 'search_runbooks' && s.output[0].version));
  await page.locator('.agent-trace summary').filter({ hasText: '读取目标证据' }).click();
  await screenshot('approval-1440');
  check('real HTTP tool-call loop reads evidence, retrieves versioned knowledge, submits diagnosis and waits before any write');
  await page.getByRole('button', { name: '核对并执行计划', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '确认本次处置计划', exact: true });
  await dialog.waitFor();
  assert.ok((await dialog.innerText()).includes(run.plan.before));
  await screenshot('confirm-plan');
  await page.keyboard.press('Escape');
  assert.equal((await state()).incidents.find(i => i.id === run.incidentId).executionCount, 0);
  await page.getByRole('button', { name: '核对并执行计划', exact: true }).click();
  await dialog.getByRole('button', { name: '确认执行此计划', exact: true }).click();
  run = await waitRun(r => r.status === 'recovered');
  await page.locator('.agent-run-heading .pill').filter({ hasText: '已验证恢复' }).waitFor();
  assert.equal((await state()).incidents.find(i => i.id === run.incidentId).executionCount, 1);
  await screenshot('recovered-1440');
  check('exact plan confirmation drives one demo write and observed recovery; cancelling the dialog causes no write');
  await page.reload();
  await page.locator('.agent-run-heading .pill').filter({ hasText: '已验证恢复' }).waitFor();
  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('button', { name: '导出运行记录', exact: true }).click();
  await (await downloadPromise).saveAs(path.join(output, 'agent-run.json'));
  assert.equal(JSON.parse(await readFile(path.join(output, 'agent-run.json'), 'utf8')).id, run.id);
  await page.getByRole('button', { name: '查看关联事件', exact: true }).click();
  await page.getByRole('dialog').waitFor();
  await page.keyboard.press('Escape');
  check('run history survives reload; JSON export and the existing incident drawer use the same recorded task');
  for (const [width, height] of [[768, 1024], [390, 844]]) {
    await page.setViewportSize({ width, height });
    await screenshot('agent-' + width);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.locator('.agent-detail-panel').scrollIntoViewIfNeeded();
    await screenshot('trace-' + width);
  }
  check('768px and 390px task forms, history and trace stay within the viewport');
  await page.setViewportSize({ width: 1440, height: 1000 });
  await start('inventory-api', 'observe');
  run = await waitRun(r => r.request.workload === 'inventory-api' && r.status === 'completed');
  assert.equal((await state()).incidents.find(i => i.id === run.incidentId).executionCount, 0);
  await start('report-worker', 'automatic');
  run = await waitRun(r => r.request.workload === 'report-worker' && r.status === 'needs_attention');
  assert.equal((await state()).incidents.find(i => i.id === run.incidentId).executionCount, 0);
  await page.locator('.agent-run-heading .pill').filter({ hasText: '需人工核查' }).waitFor();
  await screenshot('manual-handoff');
  check('observe mode performs no repair; a fault without a trusted action ends with human handoff');
  await start('inventory-api', 'approval');
  run = await waitRun(r => r.request.workload === 'inventory-api' && r.status === 'awaiting_approval');
  await page.getByRole('button', { name: '停止任务', exact: true }).click();
  await waitRun(r => r.id === run.id && r.status === 'cancelled');
  assert.equal((await state()).incidents.find(i => i.id === run.incidentId).executionCount, 0);
  check('waiting tasks can be cancelled without submitting the proposed change');
  await post('/policies/restore_probe', { enabled: true, automatic: true });
  await start('inventory-api', 'automatic');
  run = await waitRun(r => r.request.workload === 'inventory-api' && r.status === 'recovered');
  assert.equal((await state()).incidents.find(i => i.id === run.incidentId).executionCount, 1);
  check('explicitly enabled automatic policy completes the same model/evidence/plan/verification pipeline');
  assert.deepEqual(errors, []);
  check('no uncaught browser errors in tested Agent flows');
  await writeFile(path.join(output, 'result.json'), JSON.stringify({ baseURL, checks, errors, model: 'local Chat Completions protocol fixture, not a real LLM', environment: 'isolated simulated Kubernetes provider', completedAt: new Date().toISOString() }, null, 2));
} catch (error) {
  await screenshot('failure').catch(() => {});
  await writeFile(path.join(output, 'failure.json'), JSON.stringify({ error: error.stack, checks, errors }, null, 2));
  throw error;
} finally { await browser.close(); }
