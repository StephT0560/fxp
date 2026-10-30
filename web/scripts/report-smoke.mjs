// SPDX-License-Identifier: MIT
// Reuse the browser already installed for console e2e; no server or network.
import { chromium } from '@playwright/test';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import assert from 'node:assert/strict';
const root = resolve(process.argv[2]);
const browser = await chromium.launch({ headless: true });
try {
  for (const name of ['partial-fill.html', 'hostile.html']) {
    const context = await browser.newContext({ offline: true });
    const page = await context.newPage();
    const errors = [], requests = [];
    page.on('pageerror', e => errors.push(e.message));
    page.on('request', r => { if (!r.url().startsWith('file:')) requests.push(r.url()); });
    page.on('dialog', async dialog => { errors.push('unexpected dialog'); await dialog.dismiss(); });
    await page.goto(pathToFileURL(resolve(root, name)).href);
    await page.waitForFunction(() => document.querySelector('#order-count').textContent.includes('chains'));
    await page.locator('#orders button').first().click();
    assert.ok((await page.locator('#explain').textContent()).length);
    await page.locator('#sequence').evaluate(e => { e.value = e.max; e.dispatchEvent(new Event('input')); });
    assert.ok((await page.locator('#book-caption').textContent()).includes('Sequence'));
    assert.equal(await page.locator('img').count(), 0);
    assert.equal(await page.evaluate(() => globalThis.__xss), undefined);
    assert.deepEqual(errors, []);
    assert.deepEqual(requests, []);
    if (name === 'partial-fill.html') {
      assert.ok((await page.locator('#bids').textContent()).includes('6'));
      assert.ok((await page.locator('#tape').textContent()).includes('100'));
      await page.locator('#owner').selectOption(JSON.stringify('alice'));
      assert.ok((await page.locator('#order-count').textContent()).startsWith('1 chains'));
    }
    await context.close();
  }
  console.log('PASS offline file report, filters/detail/scrubber, CSP, XSS and zero network');
} finally { await browser.close(); }
