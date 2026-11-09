const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const source = fs.readFileSync(path.join(__dirname, 'State.js'), 'utf8').replace(/^\.pragma library\s*/, '');
const context = {};
vm.runInNewContext(source, context, { filename: 'State.js' });
const reduce = context.reduce;
const json = value => JSON.parse(JSON.stringify(value));
const base = () => ({
  requestActions: { pair1: 'pair', devices1: 'devices', inbox1: 'inbox' },
  activeRequest: '', pairRequest: 'pair1', pairCancelling: false, devices: [], inbox: [], selectedAlias: '', receiving: false,
  notice: '', pairing: { qr: 'invite', png: 'cG5n' }, approval: null
});
const result = (id, extra = {}) => ({ version: 1, id, type: 'result', ...extra });

// Quiet refresh results cannot settle or replace the pending pair action.
{
  let state = base();
  state = reduce(state, result('devices1', { devices: [{ alias: 'Desk' }] }));
  assert.equal(state.selectedAlias, 'Desk');
  assert.equal(state.pairing.qr, 'invite');
  assert.equal(state.notice, '');
  state = reduce(state, result('inbox1', { items: [] }));
  assert.equal(state.pairing.qr, 'invite');
  assert.deepEqual(json(state.effects), []);
  assert.equal(state.requestActions.pair1, 'pair');
}

// Once pairing is cancelled, a late QR event cannot bring the expired invite back.
{
  const state = reduce({ ...base(), pairCancelling: true, pairing: null },
    { version: 1, id: 'pair1', type: 'pairing', qr: 'late', png: 'cG5n' });
  assert.equal(state.pairing, null);
  assert.equal(state.pairRequest, 'pair1');
}

// Approval can finish before the pair command; only the pair result closes QR.
{
  let state = base();
  state = reduce(state, { version: 1, id: 'pair1', type: 'pairing', qr: 'invite', png: 'cG5n' });
  state = reduce(state, { version: 1, id: 'approve1', type: 'approval', requestId: 'approval1', name: 'Phone' });
  state.requestActions.approve1 = 'approve';
  state = reduce(state, result('approve1'));
  assert.equal(state.pairing.qr, 'invite');
  assert.equal(state.approval, null);
  state = reduce(state, result('pair1', { alias: 'Phone' }));
  assert.equal(state.pairing, null);
  assert.equal(state.pairRequest, '');
  assert.deepEqual(json(state.effects), ['devices', 'inbox']);
}

// The receive result endpoint is surfaced only after the explicit enable action.
{
  const state = reduce({ ...base(), requestActions: { receive1: 'receive' } },
    result('receive1', { enabled: true, endpoint: '192.0.2.4:53318' }));
  assert.equal(state.receiving, true);
  assert.equal(state.receiveEndpoint, '192.0.2.4:53318');
  const restored = reduce(state, { version: 1, type: 'status', state: 'receive_restored', endpoint: 'https://192.0.2.4:53318' });
  assert.equal(restored.receiving, true);
  assert.equal(restored.receiveEndpoint, 'https://192.0.2.4:53318');
}

// Retry state preserves the original payload type; cancelled sends are not offered as retries.
{
  const failedFiles = reduce({ ...base(), requestActions: { send1: 'send' }, activeRequest: 'send1',
    activePayload: { alias: 'Desk', files: ['/tmp/photo.png'], source: 'files' } },
  { version: 1, id: 'send1', type: 'error', code: 'expired' });
  assert.equal(failedFiles.failedSend.source, 'files');
  assert.deepEqual(json(failedFiles.failedSend.files), ['/tmp/photo.png']);
  assert.equal(failedFiles.activeRequest, '');
  const cancelled = reduce({ ...base(), requestActions: { send1: 'send' }, activeRequest: 'send1',
    activePayload: { alias: 'Desk', files: [], source: 'clipboard' } },
  { version: 1, id: 'send1', type: 'error', code: 'cancelled' });
  assert.equal(cancelled.failedSend, null);
}

// A cancelled or failed pair removes a stale QR and never schedules refreshes.
{
  const state = reduce(base(), { version: 1, id: 'pair1', type: 'error', code: 'cancelled' });
  assert.equal(state.pairing, null);
  assert.equal(state.notice, 'Pairing cancelled');
  assert.equal(state.noticeKind, 'info');
  assert.deepEqual(json(state.effects), []);
}

// Pair completion schedules one refresh; both refresh results are terminal and quiet.
{
  let state = reduce(base(), result('pair1'));
  assert.deepEqual(json(state.effects), ['devices', 'inbox']);
  state.requestActions = { devices2: 'devices', inbox2: 'inbox' };
  state = reduce(state, result('devices2', { devices: [{ alias: 'Desk' }] }));
  assert.deepEqual(json(state.effects), []);
  state = reduce(state, result('inbox2', { items: [] }));
  assert.deepEqual(json(state.effects), []);
}

// Unpair's device event updates shared state without starting a duplicate refresh chain.
{
  let state = reduce({ ...base(), requestActions: { unpair1: 'unpair' } }, result('unpair1'));
  assert.deepEqual(json(state.effects), []);
  state = reduce(state, { version: 1, id: 'unpair1', type: 'devices', devices: [] });
  assert.deepEqual(json(state.devices), []);
  assert.deepEqual(json(state.effects), []);
}

// Markup shaped network labels survive state handling and QML renders them as plain text.
{
  const label = '<img src="https://invalid.example/pixel">';
  const state = reduce(base(), { version: 1, type: 'devices', devices: [{ alias: label }] });
  assert.equal(state.selectedAlias, label);
  const qml = fs.readFileSync(path.join(__dirname, 'PanelContent.qml'), 'utf8');
  const labelComponent = fs.readFileSync(path.join(__dirname, 'Label.qml'), 'utf8');
  assert.match(labelComponent, /textFormat:\s*Text\.PlainText/);
  assert.doesNotMatch(qml, /(?:^|\n)\s*Text\s*\{/);
  assert.match(qml, /text: String\(transfer\.modelData\.text/);

}

// Service owns one backend per shell; bar views contain no process and expose open/close methods for shell hotkeys.
{
  const manifest = JSON.parse(fs.readFileSync(path.join(__dirname, 'manifest.json'), 'utf8'));
  const qml = fs.readFileSync(path.join(__dirname, 'Panel.qml'), 'utf8');
  const service = fs.readFileSync(path.join(__dirname, 'Service.qml'), 'utf8');
  assert.deepEqual(manifest.kinds, ['service', 'bar-widget']);
  assert.equal(manifest.keepLoaded, true);
  assert.doesNotMatch(qml, /\bProcess\s*\{/);
  assert.match(service, /Process\s*\{/);
  assert.match(service, /function start\(candidate\)/);
  assert.match(qml, /function open\(\)/);
  assert.match(qml, /function close\(\)/);
  assert.match(service, /property bool receiving: false/);
  assert.match(service, /if \(activeRequest\).*A transfer is already in progress/);
  const content = fs.readFileSync(path.join(__dirname, "PanelContent.qml"), "utf8");
  assert.match(content, /drag\.accepted = drag\.hasUrls && !!root\.selectedAlias && !root\.sending/);
  assert.match(content, /paths\.length && root\.selectedAlias && !root\.sending/);
  assert.match(service, /Component\.onDestruction: uiProcess\.running = false/);
}

// Failed actions carry context without routine refresh overwriting the error.
{
  let state = reduce({ ...base(), requestActions: { copy1: 'copy', devices1: 'devices' } },
    { version: 1, id: 'copy1', type: 'error', message: 'Delivery expired' });
  assert.equal(state.noticeKind, 'error');
  assert.equal(state.noticeAction, 'copy');
  state = reduce(state, result('devices1', { devices: [] }));
  assert.equal(state.noticeKind, 'error');
  assert.equal(state.notice, 'Delivery expired');
  state.requestActions = { save1: 'save' };
  state = reduce(state, result('save1'));
  assert.equal(state.noticeKind, 'success');
  assert.equal(state.notice, 'Saved');
}

// A successful clear/approval after a send error replaces the whole notice.
for (const action of ['clear', 'approve']) {
  let state = reduce({ ...base(), requestActions: { send1: 'send', next1: action }, activeRequest: 'send1',
    activePayload: { alias: 'iPhone', files: [], source: 'clipboard' } },
  { version: 1, id: 'send1', type: 'error', message: 'iPhone did not respond' });
  assert.equal(state.notice, 'iPhone did not respond');
  assert.equal(state.noticeKind, 'error');
  assert.equal(state.noticeAction, 'send');
  state = reduce(state, result('next1'));
  assert.equal(state.notice, action === 'clear' ? 'Cleared' : '');
  assert.equal(state.noticeKind, 'success');
  assert.equal(state.noticeAction, action);
  assert.equal(state.failedSend.alias, 'iPhone');
}

// Local status changes replace stale error metadata, including cancellation after Save fails.
{
  const qml = fs.readFileSync(path.join(__dirname, 'Service.qml'), 'utf8');
  const service = {
    notice: 'Save failed', noticeKind: 'error', noticeAction: 'save',
    activeRequest: 'send1', pairRequest: 'pair1', pairCancelling: false,
    requestSerial: 0, requestActions: {}, started: true, executable: 'clipsend',
    uiProcess: { running: true, write() {} },
    Quickshell: { env() { return ''; } }
  };
  vm.createContext(service);
  for (const name of ['setNotice', 'request', 'start', 'send', 'cancelActive', 'cancelPairing', 'restart']) {
    const method = qml.match(new RegExp('  function ' + name + '\\([^]*?\\n  }'));
    assert.ok(method, name + ' is available for transport-isolated testing');
    vm.runInContext(method[0], service);
  }
  function afterError(action, message) {
    service.notice = 'Delivery expired';
    service.noticeKind = 'error';
    service.noticeAction = 'save';
    action();
    assert.equal(service.notice, message);
    assert.equal(service.noticeKind, 'info');
    assert.equal(service.noticeAction, '');
  }
  afterError(() => service.cancelActive(), 'Cancelling transfer…');
  afterError(() => service.cancelPairing(), 'Cancelling pairing…');
  afterError(() => service.send('Desk', []), 'A transfer is already in progress');
  service.activeRequest = '';
  afterError(() => service.send('', []), 'Pair a device to send');
  afterError(() => service.send('Desk', []), 'Preparing clipboard…');
  service.activeRequest = '';
  afterError(() => service.send('Desk', ['/tmp/report.pdf']), 'Preparing files…');
  afterError(() => service.start('/tmp/candidate'), 'Executable changed; restart the connection to apply it');
  service.noticeKind = 'error'; service.noticeAction = 'save';
  service.start('clipsend');
  assert.equal(service.notice, 'Connected');
  assert.equal(service.noticeKind, 'info');
  assert.equal(service.noticeAction, '');
  afterError(() => service.restart(), 'Restarting ClipSend…');
  service.started = false;
  afterError(() => service.start('clipsend'), 'Starting ClipSend…');
  // A failed cancellation must preserve its own failure, not claim cancellation.
  service.uiProcess.running = false;
  service.activeRequest = 'send2';
  service.cancelActive();
  assert.equal(service.notice, 'ClipSend UI process is unavailable');
  assert.equal(service.noticeKind, 'error');
  assert.equal(service.noticeAction, 'cancel');
}

// Deferred clear failures must restore the actual backend inbox; Open has its own result.
{
  const state = { ...base(), requestActions: { c: 'clear', o: 'open' } };
  const failed = reduce(state, { version: 1, id: 'c', type: 'error', message: 'Unavailable' });
  assert.deepEqual(json(failed.effects), ['inbox']);
  const opened = reduce(state, result('o'));
  assert.equal(opened.notice, 'Opened');
  assert.equal(opened.noticeAction, 'open');
}
console.log('Omarchy ClipSend UI state checks passed');
