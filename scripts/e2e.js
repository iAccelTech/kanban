const { chromium } = require('playwright');
// Usage: node scripts/e2e.js OUT_DIR  (server running on a fresh data dir)
const OUT = process.argv[2] || '.';
const BASE = 'http://127.0.0.1:6220';
(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  const errors = [];
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', e => errors.push(String(e)));
  const step = (s) => console.log('·', s);

  await page.goto(BASE + '/');
  await page.getByText('Create new board').click();
  await page.locator('.popover input[name=title]').fill('Lab roadmap');
  await page.locator('.popover .swatch.bg-green').click();
  await page.screenshot({ path: OUT + '/1-create-board.png' });
  await page.locator('.popover button[type=submit]').click();
  await page.waitForURL(/board\.html\?id=\d+/);
  step('board created');

  for (const t of ['To do', 'Doing', 'Done']) {
    if (await page.locator('.add-list').isVisible()) await page.locator('.add-list').click();
    await page.locator('.add-list-col input').fill(t);
    await page.locator('.add-list-col input').press('Enter');
    await page.locator('.list-title', { hasText: t }).waitFor();
  }
  await page.keyboard.press('Escape');
  step('lists added');

  const addCards = async (listIdx, titles) => {
    const list = page.locator('.list').nth(listIdx);
    await list.locator('.add-card').click();
    for (const t of titles) {
      await list.locator('.composer-input').fill(t);
      await list.locator('.composer-input').press('Enter');
      await list.locator('.card-title', { hasText: t }).waitFor();
    }
    await list.locator('.composer-input').press('Escape');
  };
  await addCards(0, ['Order reagents', 'Calibrate LC-MS', 'Write SOP for extraction', 'Review BLAST results']);
  await addCards(1, ['Migrate GeneAnnot DB']);
  step('cards added');

  // Second tab to watch live updates.
  const page2 = await ctx.newPage();
  await page2.goto(page.url());
  await page2.locator('.live-dot.on').waitFor({ timeout: 5000 });
  step('second tab live');

  // Drag "Calibrate LC-MS" to Doing (below Migrate).
  const src = page.locator('.card', { hasText: 'Calibrate LC-MS' });
  const dst = page.locator('.list').nth(1).locator('.card', { hasText: 'Migrate GeneAnnot DB' });
  await src.dragTo(dst, { targetPosition: { x: 50, y: 30 } });
  await page.waitForTimeout(800);
  const doing = await page.locator('.list').nth(1).locator('.card-title').allTextContents();
  console.log('  Doing after drag:', doing);
  await page.reload();
  const doingAfter = await page.locator('.list').nth(1).locator('.card-title').allTextContents();
  console.log('  Doing after reload:', doingAfter);
  const doing2 = await page2.locator('.list').nth(1).locator('.card-title').allTextContents();
  console.log('  Doing in tab 2:', doing2);

  // Open a card and fill details.
  await page.locator('.card', { hasText: 'Order reagents' }).click();
  await page.locator('.modal .m-title').waitFor();
  await page.getByRole('button', { name: 'Add a more detailed description…' }).click();
  await page.locator('[data-key=desc]').fill('Need **acetonitrile** and formic acid.\n\n- [ ] get quote\n- [x] check stock\n\nSee <script>alert(1)</script> [vendor](https://example.com)');
  await page.getByRole('button', { name: 'Save', exact: true }).first().click();
  await page.locator('.modal .md strong').waitFor();
  step('description saved');

  await page.locator('.side-btn', { hasText: 'Labels' }).click();
  await page.locator('.popover .label-chip.pick').nth(0).click();
  await page.locator('.popover .label-chip.pick').nth(3).click();
  await page.getByRole('button', { name: 'Create a new label' }).click();
  await page.locator('.popover input').first().fill('Urgent');
  await page.locator('.popover .swatch.lc-red').click();
  await page.locator('.popover button[type=submit]').click();
  await page.locator('.popover .label-chip.pick', { hasText: 'Urgent' }).click();
  await page.keyboard.press('Escape');
  step('labels');

  await page.locator('.side-btn', { hasText: 'Checklist' }).click();
  await page.locator('.popover input').fill('Before ordering');
  await page.locator('.popover button[type=submit]').click();
  await page.getByRole('button', { name: 'Add an item' }).click();
  for (const t of ['Get 3 quotes', 'Check budget code', 'Submit PO']) {
    await page.locator('[data-key^=item-]').fill(t);
    await page.locator('[data-key^=item-]').press('Enter');
    await page.locator('.ci-text', { hasText: t }).waitFor();
  }
  await page.locator('[data-key^=item-]').press('Escape');
  await page.locator('.ci input[type=checkbox]').first().check();
  await page.waitForTimeout(300);
  step('checklist');

  await page.locator('.side-btn', { hasText: 'Dates' }).click();
  await page.locator('.popover input[type=datetime-local]').fill('2026-10-28T15:00');
  await page.locator('.popover button[type=submit]').click();
  await page.waitForTimeout(300);
  step('due');

  await page.locator('[data-key=comment]').fill('Asked **Dana** for the vendor list.');
  await page.locator('.comment-composer .btn.primary').click();
  await page.locator('.comment-body strong').waitFor();
  step('comment');
  await page.screenshot({ path: OUT + '/3-card-modal.png', fullPage: false });

  const scriptCount = await page.locator('.modal script').count();
  console.log('  <script> in modal:', scriptCount);

  await page.locator('.modal-close').click();
  await page.waitForTimeout(500);

  // Activity sidebar
  await page.locator('.hdr-btn', { hasText: 'Activity' }).click();
  await page.locator('.act-item').first().waitFor();
  await page.screenshot({ path: OUT + '/2-board.png' });
  await page.locator('.hdr-btn', { hasText: 'Activity' }).click();

  // Tab 2 should show badges for the card without reload
  const badges = await page2.locator('.card', { hasText: 'Order reagents' }).locator('.badge').count();
  const labels2 = await page2.locator('.card', { hasText: 'Order reagents' }).locator('.label-chip').count();
  console.log('  tab2 badges:', badges, 'labels:', labels2);

  // Rename list inline
  await page.locator('.list-title', { hasText: 'Done' }).click();
  await page.locator('.list-header .inline-input').fill('Done ✅');
  await page.locator('.list-header .inline-input').press('Enter');
  await page2.locator('.list-title', { hasText: 'Done ✅' }).waitFor({ timeout: 3000 });
  step('rename list propagated');

  // Home page
  await page.goto(BASE + '/');
  await page.locator('.tile', { hasText: 'Lab roadmap' }).hover();
  await page.locator('.tile', { hasText: 'Lab roadmap' }).locator('.tile-star').click();
  await page.locator('.board-section h2', { hasText: 'Starred' }).waitFor();
  await page.screenshot({ path: OUT + '/0-home.png' });

  // Mobile board view
  const m = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await m.goto(page2.url());
  await m.waitForTimeout(500);
  await m.screenshot({ path: OUT + '/4-mobile-board.png' });

  console.log('console errors:', errors.length ? errors : 'none');
  await browser.close();
})().catch(e => { console.error(e); process.exit(1); });
