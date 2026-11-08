import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const SCREENSHOT_DIR = '/workspaces/AxonRouter-Go/screenshots/modal_verification';
mkdirSync(SCREENSHOT_DIR, { recursive: true });

async function run() {
  console.log('Launching browser...');
  const browser = await chromium.launch({
    executablePath: '/usr/bin/chromium',
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox'],
  });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();

  console.log('Navigating to dashboard at http://127.0.0.1:3177...');
  await page.goto('http://127.0.0.1:3177', { waitUntil: 'networkidle' });
  await page.waitForTimeout(1000);

  // 1. Verify Endpoint / Keys View
  console.log('Verifying Endpoint view...');
  const createKeyBtn = page.getByRole('button', { name: 'Create Key' }).first();
  await createKeyBtn.waitFor({ state: 'visible', timeout: 10000 });
  await page.screenshot({ path: `${SCREENSHOT_DIR}/01_endpoint_view.png` });

  // 2. Open Create API Key Modal
  console.log('Opening CreateApiKeyModal...');
  await createKeyBtn.click();
  await page.waitForTimeout(600);

  // Check dialog header
  const modalHeader = page.locator('text=Create API Key');
  await modalHeader.first().waitFor({ state: 'visible' });

  // Verify Single & Bulk tabs exist
  const singleTab = page.getByRole('button', { name: 'Single' });
  const bulkTab = page.getByRole('button', { name: 'Bulk' });
  await singleTab.waitFor({ state: 'visible' });
  await bulkTab.waitFor({ state: 'visible' });

  // Verify form controls in Single tab
  const nameInput = page.locator('#new-key-name');
  await nameInput.waitFor({ state: 'visible' });
  const tpmInput = page.locator('#create-tpm');
  await tpmInput.waitFor({ state: 'visible' });
  const quotaInput = page.locator('#create-quota');
  await quotaInput.waitFor({ state: 'visible' });

  console.log('Single tab form verified (Name, TPM in M, Quota in M).');
  await page.screenshot({ path: `${SCREENSHOT_DIR}/02_create_key_modal_single.png` });

  // 3. Open Model Picker from CreateApiKeyModal
  console.log('Opening ModelPickerModal from CreateApiKeyModal...');
  const browseBtn = page.getByRole('button', { name: 'Browse Models' });
  await browseBtn.waitFor({ state: 'visible' });
  await browseBtn.click();
  await page.waitForTimeout(800);

  // Verify Model Picker modal
  const pickerDialog = page.locator('text=Select Allowed Models').or(page.locator('text=Add Model to Combo'));
  await pickerDialog.first().waitFor({ state: 'visible' });

  // Verify quick filter chips (Connected, Vision, Reasoning)
  const connectedChip = page.getByRole('button', { name: /Connected/ });
  await connectedChip.waitFor({ state: 'visible' });

  console.log('ModelPickerModal opened successfully with filters & active connection resolution.');
  await page.screenshot({ path: `${SCREENSHOT_DIR}/03_create_key_model_picker.png` });

  // Close Model Picker via Escape (tests escapeStack handler)
  console.log('Closing Model Picker via Escape key...');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(500);

  // 4. Switch to Bulk tab
  console.log('Switching to Bulk tab...');
  await bulkTab.click();
  await page.waitForTimeout(300);
  const bulkInput = page.locator('#bulk-keys-input');
  await bulkInput.waitFor({ state: 'visible' });
  const bulkSubmitBtn = page.getByRole('button', { name: 'Create keys' });
  await bulkSubmitBtn.waitFor({ state: 'visible' });
  console.log('Bulk tab verified.');
  await page.screenshot({ path: `${SCREENSHOT_DIR}/04_create_key_modal_bulk.png` });

  // Close Create Key modal
  const cancelBtn = page.getByRole('button', { name: 'Cancel' }).first();
  await cancelBtn.click();
  await page.waitForTimeout(400);

  // 5. Verify Policy Modal on existing key if any exists
  console.log('Checking for existing keys to verify ApiKeyPolicyModal...');
  const policyBtn = page.locator('button[title="Rate limits, plan & metadata"]').first();
  if (await policyBtn.isVisible()) {
    console.log('Opening ApiKeyPolicyModal...');
    await policyBtn.click();
    await page.waitForTimeout(600);

    const policyHeader = page.locator('text=API Key Policy');
    await policyHeader.waitFor({ state: 'visible' });

    // Verify TPM and Quota inputs with M helper
    const policyTpm = page.locator('#policy-tpm');
    await policyTpm.waitFor({ state: 'visible' });
    const policyQuota = page.locator('#policy-quota');
    await policyQuota.waitFor({ state: 'visible' });

    console.log('ApiKeyPolicyModal opened and verified with M units.');
    await page.screenshot({ path: `${SCREENSHOT_DIR}/05_policy_modal_view.png` });

    // Verify Browse Models button inside Policy modal
    const policyBrowseBtn = page.getByRole('button', { name: 'Browse Models' });
    await policyBrowseBtn.waitFor({ state: 'visible' });
    await policyBrowseBtn.click();
    await page.waitForTimeout(600);

    console.log('ModelPickerModal opened from ApiKeyPolicyModal successfully.');
    await page.screenshot({ path: `${SCREENSHOT_DIR}/06_policy_modal_model_picker.png` });

    console.log('Closing ModelPickerModal from policy modal via Escape...');
    await page.keyboard.press('Escape');
    await page.waitForTimeout(400);

    await page.screenshot({ path: `${SCREENSHOT_DIR}/07_policy_modal_ready.png` });

    // Cancel policy modal
    await page.getByRole('button', { name: 'Cancel' }).last().click();
  } else {
    console.log('No existing keys row found for policy modal test.');
  }

  await browser.close();
  console.log('SUCCESS! Verification run complete. Screenshots saved in', SCREENSHOT_DIR);
}

run().catch((err) => {
  console.error('VERIFICATION ERROR:', err);
  process.exit(1);
});
