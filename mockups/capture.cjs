/* Run with Playwright installed in an environment that permits Chromium processes.
   CHROMIUM_PATH=/usr/bin/chromium node mockups/capture.cjs */
const { chromium } = require('playwright');
const path = require('path');
const assert = require('node:assert/strict');
(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? {executablePath: process.env.CHROMIUM_PATH} : {}) });
  const page = await browser.newPage({ viewport: {width:1440, height:1120}, deviceScaleFactor:1 });
  const errors=[]; page.on('pageerror',e=>errors.push(e.message));
  await page.goto('file://' + path.join(__dirname, 'index.html'));
  await page.screenshot({path:path.join(__dirname,'desktop.png'), fullPage:true});
  await page.locator('textarea').fill('任务一的草稿');
  await page.locator('.tab[data-task="2"]').click();
  assert.equal(await page.locator('textarea').inputValue(),'');
  await page.locator('textarea').fill('周六上午出发');
  await page.locator('.send').click();
  assert.match(await page.locator('#thread-content').innerText(),/周六上午出发/);
  await page.locator('.tab[data-task="0"]').click();
  assert.equal(await page.locator('textarea').inputValue(),'任务一的草稿');
  assert.doesNotMatch(await page.locator('#thread-content').innerText(),/周六上午出发/);
  await page.locator('textarea').fill('');
  await page.locator('.nav[data-view="plugins"]').click();
  await page.screenshot({path:path.join(__dirname,'plugins.png'),fullPage:true});
  await page.locator('.nav[data-view="settings"]').click();
  await page.screenshot({path:path.join(__dirname,'settings.png'),fullPage:true});
  await page.locator('.nav[data-view="workspace"]').click();
  await page.locator('.tab[data-task="2"]').click();
  await page.screenshot({path:path.join(__dirname,'question-card.png'),fullPage:true});
  await page.locator('.tab[data-task="0"]').click();
  await page.setViewportSize({width:390,height:844});
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),'Mobile horizontal overflow');
  await page.screenshot({path:path.join(__dirname,'mobile.png'),fullPage:true});
  assert.deepEqual(errors,[],'Browser JavaScript errors');
  await browser.close();
  console.log('Screenshots captured; task isolation and mobile overflow checks passed.');
})().catch(e=>{console.error(e);process.exit(1)});
