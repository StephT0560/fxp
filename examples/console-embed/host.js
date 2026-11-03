// SPDX-License-Identifier: MIT
// Trusted host example: configuration stays in JS memory, never in markup/URLs.
const frame = document.querySelector('#console');
const status = document.querySelector('#host-status');
let session;
let notified = false;
window.addEventListener('message', event => {
  if (event.source !== frame.contentWindow || event.origin !== location.origin || !session) return;
  const data = event.data;
  if (data?.type === 'fxp-console:ready' && data.version === 1 && typeof data.nonce === 'string' && session.delivery === 'message') {
    frame.contentWindow.postMessage({ type: 'fxp-console:init', version: 1, nonce: data.nonce, config: session.config }, location.origin);
  }
  if (data?.type === 'fxp-console:token-expired' && data.version === 1 && data.runId === session.config.runId && !notified) {
    notified = true;
    status.textContent = `Run ${data.runId} ended or token expired`;
    // Replace this with the host's own onTokenExpired handler. No token is sent.
  }
});
// Called by our fixed bootstrap script before the console's module entrypoint.
window.__FXP_EMBED_SETUP__ = child => {
  if (!session || child !== frame.contentWindow) return;
  const { config, delivery } = session;
  if (delivery === 'message' || config.notifyTokenExpired) child.__FXP_CONSOLE_ALLOWED_ORIGINS__ = [location.origin];
  if (delivery === 'injected') child.__FXP_CONSOLE__ = config;
};
document.querySelector('#connect').addEventListener('submit', event => {
  event.preventDefault();
  const config = {
    gatewayUrl: document.querySelector('#gateway').value,
    token: document.querySelector('#token').value,
    runId: document.querySelector('#run-id').value,
    readOnly: document.querySelector('#read-only').checked,
  };
  const delivery = document.querySelector('#delivery').value;
  if (document.querySelector('#notify').checked) {
    config.notifyTokenExpired = true;
    config.parentOrigin = location.origin;
  }
  document.querySelector('#token').value = '';
  session = { config, delivery }; notified = false;
  status.textContent = 'Loading run console';
  frame.onload = () => { status.textContent = `Run ${config.runId}`; };
  frame.src = '/console.html';
});
