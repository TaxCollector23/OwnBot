#!/usr/bin/env node
// Dependency-light browser QA for the deployed site and its local static copy.
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { chromium } = require('@playwright/test');

const root = path.resolve(__dirname, '..');
const hosted = (process.env.OWNBOT_SITE_URL || 'https://ownbot-demo.vercel.app').replace(/\/$/, '');
const widths = [320, 375, 768, 1440];
const anchors = ['demos', 'install', 'setup', 'token', 'ssh', 'lan', 'service', 'hardware', 'homebrew', 'status'];
const videos = ['/videos/demo-terminal.mp4', '/videos/flyover.mp4'];
const failures = [];
let assertions = 0;

function check(condition, label, message) {
  assertions++;
  if (!condition) failures.push(label + ': ' + message);
}

function contentType(file) {
  const ext = path.extname(file).toLowerCase();
  return ({ '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.mp4': 'video/mp4', '.png': 'image/png', '.svg': 'image/svg+xml', '.txt': 'text/plain; charset=utf-8' })[ext] || 'application/octet-stream';
}

function startStaticServer() {
  const server = http.createServer((req, res) => {
    let pathname;
    try { pathname = decodeURIComponent(new URL(req.url, 'http://localhost').pathname); }
    catch { res.writeHead(400).end('Bad request'); return; }

    // Match the Vercel cleanUrls/trailingSlash routes while serving the checked-in files.
    if (pathname !== '/' && pathname.endsWith('/')) {
      res.writeHead(308, { Location: pathname.replace(/\/+$/, '') }).end();
      return;
    }
    let file = path.resolve(root, pathname === '/' ? 'index.html' : pathname.slice(1));
    if (!file.startsWith(root + path.sep)) { res.writeHead(403).end('Forbidden'); return; }
    if (!path.extname(file)) {
      const clean = file + '.html';
      file = fs.existsSync(clean) ? clean : path.join(file, 'index.html');
    }
    if (!fs.existsSync(file) || !fs.statSync(file).isFile()) {
      res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' }).end('Not found');
      return;
    }

    const size = fs.statSync(file).size;
    const headers = { 'Accept-Ranges': 'bytes', 'Content-Type': contentType(file) };
    const range = /^bytes=(\d+)-(\d*)$/.exec(req.headers.range || '');
    if (range) {
      const start = Number(range[1]);
      const end = range[2] ? Math.min(Number(range[2]), size - 1) : size - 1;
      if (start >= size || end < start) { res.writeHead(416, { 'Content-Range': 'bytes */' + size }).end(); return; }
      res.writeHead(206, Object.assign({}, headers, { 'Content-Length': end - start + 1, 'Content-Range': 'bytes ' + start + '-' + end + '/' + size }));
      if (req.method === 'HEAD') res.end(); else fs.createReadStream(file, { start, end }).pipe(res);
      return;
    }
    res.writeHead(200, Object.assign({}, headers, { 'Content-Length': size }));
    if (req.method === 'HEAD') res.end(); else fs.createReadStream(file).pipe(res);
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => resolve({ server, base: 'http://127.0.0.1:' + server.address().port }));
  });
}

async function routeChecks(base, label) {
  const canonical = await fetch(base + '/demos', { redirect: 'manual' });
  const html = await canonical.text();
  check(canonical.status === 200 && /demos\s*&amp;\s*docs/i.test(html), label, '/demos serves the docs page');
  const trailing = await fetch(base + '/demos/', { redirect: 'manual' });
  check(trailing.status === 308 && trailing.headers.get('location') === '/demos', label, '/demos/ redirects to /demos');
  const missing = await fetch(base + '/qa-missing-page', { redirect: 'manual' });
  check(missing.status === 404 && !(missing.headers.get('content-type') || '').includes('text/html'), label, 'missing page is a non-HTML 404');
  const missingAsset = await fetch(base + '/assets/qa-missing.svg', { redirect: 'manual' });
  check(missingAsset.status === 404 && !(missingAsset.headers.get('content-type') || '').includes('text/html'), label, 'missing asset is a non-HTML 404');
  const transcript = await fetch(base + '/videos/demo-terminal.txt');
  check(transcript.status === 200 && /ownbot/i.test(await transcript.text()), label, 'CLI transcript is available');
  for (const video of videos) {
    const media = await fetch(base + video, { headers: { Range: 'bytes=0-1023' } });
    const bytes = new Uint8Array(await media.arrayBuffer());
    check(media.status === 206 && media.headers.get('content-range')?.startsWith('bytes 0-1023/') && bytes.length === 1024, label, video + ' returns byte ranges');
  }
}

async function pageChecks(page, base, route, width, label) {
  await page.setViewportSize({ width, height: 900 });
  const errors = [];
  const onError = error => errors.push(error.message);
  const onResponse = response => {
    if (response.status() >= 400 && !/fonts\.(googleapis|gstatic)\.com/.test(response.url())) errors.push(response.status() + ' ' + response.url());
  };
  page.on('pageerror', onError);
  page.on('response', onResponse);
  const response = await page.goto(base + route, { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => document.fonts.ready);
  const info = await page.evaluate(() => ({
    width: innerWidth,
    doc: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
    h1Count: document.querySelectorAll('h1').length,
    h1: document.querySelector('h1')?.innerText.trim() || '',
    main: !!document.querySelector('main'),
    font: getComputedStyle(document.body).fontFamily,
  }));
  check(!!response && response.status() < 400, label + ' ' + route + ' ' + width, 'page loads');
  check(info.doc <= width && info.body <= width, label + ' ' + route + ' ' + width, 'no horizontal overflow');
  check(info.h1Count === 1 && info.main, label + ' ' + route + ' ' + width, 'one h1 and a main landmark');
  check(info.font.includes('Arial') || info.font.includes('sans-serif'), label + ' ' + route + ' ' + width, 'body font has a system fallback');

  if (route === '/') check(/local/i.test(info.h1), label + ' landing ' + width, 'local-agent headline appears');
  else {
    const ids = await page.locator('main [id]').evaluateAll(nodes => nodes.map(node => node.id));
    for (const id of anchors) check(ids.includes(id), label + ' docs', '#' + id + ' exists');
    const text = (await page.locator('main').innerText()).toLowerCase();
    for (const phrase of ['install from source', 'use over ssh', 'lan access', 'hardware and model checks', 'release status']) {
      check(text.includes(phrase), label + ' docs', 'contains ' + phrase);
    }
    check(await page.locator('video[controls]').count() === 2, label + ' demos ' + width, 'two videos expose native controls');
    check(await page.locator('.copy-button').count() > 0, label + ' demos ' + width, 'copy controls are added by the site script');
    check(await page.locator('a[href="/videos/demo-terminal.txt"]').count() === 1, label + ' demos ' + width, 'transcript link exists');
    check(await page.locator('a[href*="demo-session.sh"]').count() === 1, label + ' demos ' + width, 'recording source link exists');
  }
  page.off('pageerror', onError);
  page.off('response', onResponse);
  check(errors.length === 0, label + ' ' + route + ' ' + width, errors.join('; ') || 'no page errors or failed resources');
}

async function journeyChecks(page, base, width, label) {
  await page.setViewportSize({ width, height: 900 });
  await page.goto(base + '/', { waitUntil: 'domcontentloaded' });
  if (width <= 375) {
    const installNav = page.locator('.site-nav a[href="#install"]');
    const visible = await installNav.isVisible();
    check(visible, label + ' mobile nav ' + width, 'Install link remains visible');
    if (visible) {
      await installNav.click();
      await page.waitForFunction(() => location.hash === '#install');
      check(new URL(page.url()).hash === '#install', label + ' mobile nav ' + width, 'Install link navigates to its section');
    }
  }
  await page.locator('.hero .actions a[href="/demos/"]').click();
  await page.waitForURL(url => url.pathname === '/demos');
  check(new URL(page.url()).pathname === '/demos', label + ' journey ' + width, 'landing CTA reaches canonical demos route');
  const tocHardware = page.locator('.toc a[href="#hardware"]');
  check(await tocHardware.isVisible(), label + ' TOC ' + width, 'on-page contents remain available');
  await tocHardware.click();
  await page.waitForFunction(() => location.hash === '#hardware');
  check(new URL(page.url()).hash === '#hardware', label + ' TOC ' + width, 'TOC hardware link navigates to its section');
  for (const id of ['demos', 'install', 'ssh', 'lan', 'hardware', 'status']) {
    await page.locator('a[href="#' + id + '"]').first().click();
    await page.waitForFunction(id => location.hash === '#' + id && !!document.getElementById(id), id);
    check(new URL(page.url()).hash === '#' + id, label + ' journey ' + width, 'navigation reaches #' + id);
  }
}

async function keyboardChecks(page, base, label) {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(base + '/', { waitUntil: 'domcontentloaded' });
  await page.keyboard.press('Tab');
  const focus = await page.evaluate(() => ({
    text: document.activeElement?.textContent.trim(),
    href: document.activeElement?.getAttribute('href'),
    visible: document.activeElement?.getBoundingClientRect().top >= 0,
    outline: getComputedStyle(document.activeElement).outlineWidth,
  }));
  check(focus.href === '#main' && /skip to content/i.test(focus.text) && focus.visible, label, 'first Tab reaches visible skip link');
  check(focus.outline === '3px', label, 'keyboard focus outline is visible');
  await page.keyboard.press('Enter');
  check(new URL(page.url()).hash === '#main', label, 'skip link moves to main content');
  await page.goto(base + '/demos', { waitUntil: 'domcontentloaded' });
  const video = page.locator('video').first();
  await video.scrollIntoViewIfNeeded();
  await video.focus();
  await video.press('Space');
  await page.waitForFunction(el => !el.paused && el.currentTime > 0, await video.elementHandle(), { timeout: 8000 });
  await video.press('Space');
  await page.waitForFunction(el => el.paused, await video.elementHandle());
  check(true, label, 'video play/pause works from keyboard');
}

async function copyChecks(browser, base, label) {
  const context = await browser.newContext({ viewport: { width: 375, height: 900 } });
  try { await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: base }); } catch {}
  const page = await context.newPage();
  await page.goto(base + '/demos', { waitUntil: 'domcontentloaded' });
  const button = page.locator('.code-block + .copy-control .copy-button').first();
  if (await button.count() === 0) {
    check(false, label + ' copy', 'copy controls are present for documentation commands');
    await context.close();
    return;
  }
  const expected = (await page.locator('.code-block').first().textContent()).trim();
  await button.click();
  await page.waitForFunction(el => /copied|selected/i.test(el.textContent), await button.elementHandle());
  const feedback = await button.innerText();
  let clipboard = '';
  try { clipboard = await page.evaluate(() => navigator.clipboard.readText()); } catch {}
  const selected = await page.evaluate(() => window.getSelection()?.toString() || '');
  check((/copied/i.test(feedback) && clipboard === expected) || (/selected/i.test(feedback) && selected.includes('git clone')), label + ' copy', 'copy action writes the command or selects it with clear feedback');
  check(await button.getAttribute('aria-label') === 'Copy command or address', label + ' copy', 'copy button has an accessible name');
  await context.close();
}

async function fontFallbackChecks(browser, base, label) {
  const context = await browser.newContext({ viewport: { width: 320, height: 900 } });
  await context.route(/fonts\.googleapis\.com|fonts\.gstatic\.com/, route => route.abort());
  const page = await context.newPage();
  const response = await page.goto(base + '/demos', { waitUntil: 'domcontentloaded' });
  const info = await page.evaluate(() => ({
    h1: document.querySelector('h1')?.innerText || '',
    doc: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
    family: getComputedStyle(document.body).fontFamily,
    brandLoaded: [...document.fonts].some(font => /IBM Plex Sans/i.test(font.family) && font.status === 'loaded'),
  }));
  check(!!response && response.status() < 400, label + ' font fallback', 'page loads when remote fonts are blocked');
  check(!!info.h1 && info.doc <= 320 && info.body <= 320, label + ' font fallback', 'content stays visible without overflow at 320px');
  check(info.family.includes('Arial') && !info.brandLoaded, label + ' font fallback', 'system font is used when Google Fonts are unavailable');
  await context.close();
}

async function mediaChecks(browser, base, label, width) {
  const context = await browser.newContext({ viewport: { width, height: 900 } });
  const page = await context.newPage();
  const mediaResponses = [];
  page.on('response', response => { if (response.url().includes('/videos/')) mediaResponses.push(response.status()); });
  await page.goto(base + '/demos', { waitUntil: 'domcontentloaded' });
  for (let i = 0; i < videos.length; i++) {
    const video = page.locator('video').nth(i);
    await video.scrollIntoViewIfNeeded();
    const handle = await video.elementHandle();
    await page.waitForFunction(el => el.readyState >= 1 && Number.isFinite(el.duration) && el.duration > 1, handle, { timeout: 15000 });
    const meta = await video.evaluate(el => ({ duration: el.duration, src: el.currentSrc }));
    check(meta.src.endsWith(videos[i]) && meta.duration > 1, label + ' media ' + width, videos[i] + ' metadata loads');

    await video.evaluate(el => { el.pause(); el.currentTime = 0; });
    const box = await video.boundingBox();
    const x = box.x + box.width * 0.72;
    const y = box.y + box.height - 22;
    if (width >= 768) {
      await page.mouse.move(x, y);
      await page.waitForTimeout(100);
      await page.mouse.click(x, y);
      try {
        await page.waitForFunction(el => el.currentTime > el.duration * 0.45, handle, { timeout: 4000 });
        check(true, label + ' media ' + width, videos[i] + ' seeks using the native timeline');
      } catch { check(false, label + ' media ' + width, videos[i] + ' timeline click did not seek'); }
    }

    await video.evaluate(el => { el.pause(); el.currentTime = 0; });
    await video.evaluate(el => el.play());
    await page.waitForFunction(el => el.currentTime > 0.25 && !el.paused, handle, { timeout: 8000 });
    await video.evaluate(el => el.pause());
    check(true, label + ' media ' + width, videos[i] + ' plays and pauses');
  }
  check(mediaResponses.includes(206), label + ' media ' + width, 'browser uses media byte-range responses');
  await context.close();
}

async function run() {
  for (const file of ['index.html', 'demos/index.html', 'scripts/demo-session.sh', 'videos/demo-terminal.txt']) {
    check(fs.existsSync(path.join(root, file)), 'local source', file + ' exists');
  }
  const local = await startStaticServer();
  const exe = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE;
  const browser = await chromium.launch({ headless: true, ...(exe ? { executablePath: exe } : {}) });
  const selectedTarget = process.env.OWNBOT_QA_TARGET;
  try {
    const targets = [{ base: hosted, label: 'hosted' }, { base: local.base, label: 'local' }];
    for (const target of targets.filter(item => !selectedTarget || item.label === selectedTarget)) {
      await routeChecks(target.base, target.label);
      const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
      const page = await context.newPage();
      for (const width of widths) {
        await pageChecks(page, target.base, '/', width, target.label);
        await pageChecks(page, target.base, '/demos', width, target.label);
        if (width === 320 || width === 1440) await journeyChecks(page, target.base, width, target.label);
      }
      await keyboardChecks(page, target.base, target.label);
      await context.close();
      await copyChecks(browser, target.base, target.label);
      await fontFallbackChecks(browser, target.base, target.label);
      for (const width of [320, 1440]) await mediaChecks(browser, target.base, target.label, width);
    }
    if (selectedTarget !== 'local') {
      const source = await fetch(hosted + '/demos').then(response => response.text());
      const link = source.match(/href="(https:\/\/github\.com\/TaxCollector23\/OwnBot\/blob\/main\/demo-site\/scripts\/demo-session\.sh)"/);
      if (link) check((await fetch(link[1])).status === 200, 'hosted source', 'recording source resolves on GitHub');
      else check(false, 'hosted source', 'recording source link is present');
    }
  } finally {
    await browser.close();
    await new Promise(resolve => local.server.close(resolve));
  }
  console.log('Website QA assertions: ' + assertions + ', failures: ' + failures.length);
  for (const failure of failures) console.error('FAIL ' + failure);
  if (failures.length) process.exitCode = 1;
  else console.log('PASS ' + (selectedTarget || 'hosted and local') + ' routes, breakpoints, journey, keyboard, font fallback, copy actions, transcript, source, and media controls');
}

run().catch(error => { console.error(error); process.exitCode = 1; });
