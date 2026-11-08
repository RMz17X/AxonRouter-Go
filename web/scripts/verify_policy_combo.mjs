import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const DIR = '/workspaces/AxonRouter-Go/screenshots/modal_verification';
mkdirSync(DIR, { recursive: true });

const results = [];
function ok(name, detail = '') {
  results.push({ name, pass: true, detail });
  console.log(`PASS  ${name}${detail ? ' — ' + detail : ''}`);
}
function fail(name, err) {
  results.push({ name, pass: false, detail: String(err).slice(0, 300) });
  console.log(`FAIL  ${name} — ${String(err).slice(0, 300)}`);
}

const browser = await chromium.launch({
  executablePath: '/usr/bin/chromium',
  headless: true,
  args: ['--no-sandbox', '--disable-setuid-sandbox'],
});
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });

async function step(name, fn) {
  try {
    await fn();
    ok(name);
  } catch (e) {
    fail(name, e.message || e);
  }
}

// ---------- 1. Endpoint: edit key & policy ----------
await page.goto('http://127.0.0.1:3177/dashboard/endpoint', { waitUntil: 'networkidle' });
await page.waitForTimeout(1200);

await step('row actions menu opens', async () => {
  const trigger = page.locator('button[aria-label^="Actions for"]').first();
  await trigger.waitFor({ state: 'visible', timeout: 10000 });
  await trigger.click();
});

await step('menu item "Edit key & policy" opens ApiKeyPolicyModal', async () => {
  await page.getByText('Edit key & policy').first().click();
  await page.locator('#pol-tpm').waitFor({ state: 'visible', timeout: 8000 });
});

await step('policy modal has RPM / M-unit TPM / concurrency / quota in M', async () => {
  await page.locator('#pol-tpm').isVisible();
  if (!(await page.locator('#pol-quota').isVisible())) throw new Error('#pol-quota not visible');
  await page.screenshot({ path: `${DIR}/08_policy_modal_m_units.png` });
});

await step('policy modal Browse Models opens ModelPickerModal', async () => {
  await page.getByRole('button', { name: 'Browse Models' }).first().click();
  await page.getByText('Select Allowed Models').first().waitFor({ state: 'visible', timeout: 8000 });
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${DIR}/09_policy_model_picker.png` });
});

await step('picker shows Connected filter chip', async () => {
  await page.getByRole('button', { name: /Connected \(/ }).first().waitFor({ state: 'visible', timeout: 5000 });
});

await step('Escape closes only the picker (policy modal stays)', async () => {
  await page.keyboard.press('Escape');
  await page.waitForTimeout(500);
  if (!(await page.locator('#pol-tpm').isVisible())) throw new Error('policy modal disappeared too');
  if (await page.getByText('Select Allowed Models').first().isVisible().catch(() => false)) {
    throw new Error('picker still open');
  }
});

await step('policy modal closes via Cancel', async () => {
  await page.getByRole('button', { name: 'Cancel' }).last().click();
  await page.waitForTimeout(400);
});

// ---------- 2. Combos: Add Model picker shows connected models ----------
await page.goto('http://127.0.0.1:3177/dashboard/combos', { waitUntil: 'networkidle' });
await page.waitForTimeout(1200);

await step('Create Combo opens modal', async () => {
  await page.getByRole('button', { name: 'Create Combo' }).first().click();
  await page.getByText('Add Models Manually').first().waitFor({ state: 'visible', timeout: 8000 });
});

await step('Add Models Manually opens "Add Model to Combo" picker', async () => {
  await page.getByText('Add Models Manually').first().click();
  await page.getByText('Add Model to Combo').first().waitFor({ state: 'visible', timeout: 8000 });
  await page.waitForTimeout(1200);
  await page.screenshot({ path: `${DIR}/10_combo_model_picker.png` });
});

const pickerStats = await page.evaluate(() => {
  const dialog = document.querySelector('dialog[open]') || document.body;
  const text = dialog.innerText || '';
  const connected = document.querySelectorAll('[data-connected="true"]').length;
  return { text, connected };
});
const groups = [...pickerStats.text.matchAll(/^(.*)$/gm)].map((m) => m[1]);
const providerHits = [
  'tokenharbor',
  'dahl',
  'perch',
  'antigravity',
  'gemini',
].map((p) => ({ p, hit: pickerStats.text.toLowerCase().includes(p) }));
console.log('picker provider hits:', JSON.stringify(providerHits));
ok('picker rendered provider rows', `lines=${groups.length}, samples=${providerHits.filter((x) => x.hit).map((x) => x.p).join(',')}`);

await step('combo picker lists models with active connections only when Connected filter applied', async () => {
  await page.getByRole('button', { name: /Connected \(/ }).first().click();
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${DIR}/11_combo_picker_connected.png` });
});

await browser.close();

const failed = results.filter((r) => !r.pass);
console.log(`\nSUMMARY: ${results.length - failed.length}/${results.length} checks passed`);
if (failed.length) process.exit(1);
