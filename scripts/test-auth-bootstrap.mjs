import fs from 'node:fs';
import vm from 'node:vm';
import assert from 'node:assert/strict';
const file = fs.readFileSync(new URL('../internal/auth/webview.go', import.meta.url), 'utf8');
const bootstrap = file.match(/const authBootstrapJS = `([\s\S]*?)`/)[1]
  .replaceAll('__IDRIVE_GATEWAY_NONCE__', JSON.stringify('window-capability'));
for (const [origin, path, frame, expected] of [
  ['https://outside.invalid', '/idrive/home', false, 0],
  ['https://www.idrive.com', '/idrive/home', true, 0],
  ['https://www.idrive.com', '/idrive/login', false, 0],
  ['https://www.idrive.com', '/idrive/home', false, 1],
]) {
  const calls = [];
  const window = { location: { origin, pathname: path }, addEventListener() {},
    idriveGatewayTokenReady(raw) { calls.push(JSON.parse(raw)); },
    idriveGatewayAuthError() { throw new Error('unexpected auth error'); } };
  window.top = frame ? {} : window;
  let fetches = 0;
  vm.runInNewContext(bootstrap, {window, document: {addEventListener() {}, readyState: 'complete'},
    setInterval() {}, console: {error() {}},
    async fetch() { fetches++; return { async text() { return JSON.stringify({token:'synthetic',sid:'synthetic',url:'https://evs.idrive.com/login'}); } }; }
  });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(calls.length, expected);
  assert.equal(fetches, expected);
  if (expected) assert.equal(calls[0].callback_nonce, 'window-capability');
  assert.equal(Object.values(window).includes('window-capability'), false);
}
console.log('Native login bootstrap origin, frame and capability checks passed');
