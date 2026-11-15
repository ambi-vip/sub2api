// Offline DOM checks. No installers or paid API calls are executed.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('../../frontend/node_modules/jsdom');

async function main() {
  const publicPath = path.resolve(__dirname, '../../frontend/public/help');
  const html = fs.readFileSync(path.join(publicPath, 'index.html'), 'utf8');
  const dom = new JSDOM(html, { url: 'https://sub.ambi.vip/help/', runScripts: 'outside-only' });
  const { window } = dom;
  const { document } = window;
  const blobs = [];
  const links = [];
  const copied = [];
  window.setTimeout = () => 1;
  window.clearTimeout = () => {};
  window.URL.createObjectURL = blob => { blobs.push(blob); return 'blob:test-download'; };
  window.URL.revokeObjectURL = () => {};
  window.HTMLAnchorElement.prototype.click = function () { links.push(this.download); };
  Object.defineProperty(window, 'isSecureContext', { value: true });
  Object.defineProperty(window.navigator, 'clipboard', { value: { writeText: async text => { copied.push(text); } } });
  window.fetch = () => { throw new Error('The help page must not send credentials over the network'); };
  window.eval(fs.readFileSync(path.join(publicPath, 'setup.js'), 'utf8'));

  const fakeKey = 'sk-offline-browser-test-not-a-real-key';
  document.getElementById('apiKey').value = fakeKey;
  document.getElementById('apiKey').dispatchEvent(new window.Event('input'));
  assert(!document.getElementById('preview').textContent.includes(fakeKey));
  assert.equal(document.getElementById('baseUrl').value, 'https://sub.ambi.vip/v1');
  assert.equal(document.getElementById('baseUrl').readOnly, true);

  for (const [platform, filename] of [['windows', 'ambi-setup-windows.cmd'], ['macos', 'ambi-setup-macos.zip']]) {
    document.querySelector(`[data-installer="${platform}"]`).click();
    assert.equal(links.at(-1), filename);
    const bytes = await new Promise((resolve, reject) => {
      const reader = new window.FileReader();
      reader.onload = () => resolve(Buffer.from(reader.result));
      reader.onerror = reject;
      reader.readAsArrayBuffer(blobs.at(-1));
    });
    assert.deepEqual(bytes, fs.readFileSync(path.join(publicPath, 'downloads', filename)));
    assert(!bytes.includes(Buffer.from(fakeKey)), 'Downloaded script must not embed the page API key');
  }
  document.getElementById('copyConfig').click();
  await Promise.resolve();
  assert.deepEqual(JSON.parse(copied.at(-1)), { base_url: 'https://sub.ambi.vip/v1', api_key: fakeKey });
  dom.window.close();
  process.stdout.write('Passed: both downloads, payload integrity, hidden key preview, manual copy, and zero page API requests.\n');
}
main().catch(error => { console.error(error); process.exitCode = 1; });
