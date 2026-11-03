// SPDX-License-Identifier: MIT
// This example is same-origin. A production cross-origin console instead sets
// its own trusted allowlist before its module entrypoint, without reading parent.
if (window.parent !== window) {
  try { window.parent.__FXP_EMBED_SETUP__?.(window); }
  catch { /* An unrelated parent is not a source of trusted configuration. */ }
}
