// Placeholder Scalar UI bundle.
//
// This file is a stub committed to keep the binary buildable in environments
// without network access. To install the real Scalar bundle, run:
//
//   make refresh-docs-ui
//
// which downloads the version pinned in VERSION, validates its SHA-256
// against SHA256.expected, and overwrites this file. The contract test
// `TestDocsOfflineInvariant` ensures the served HTML never references any
// https:// origin even with the placeholder in place.
(function () {
  var el = document.getElementById('api-reference');
  if (!el) return;
  var url = el.getAttribute('data-url') || '/openapi.yaml';
  el.innerHTML =
    '<div style="font-family:system-ui,sans-serif;padding:2rem;line-height:1.5;">' +
    '<h1>kube-state-graph API reference</h1>' +
    '<p><strong>Scalar UI bundle is a placeholder.</strong> Run <code>make refresh-docs-ui</code> ' +
    'on a workstation with internet access to install the pinned bundle, then rebuild and redeploy.</p>' +
    '<p>The OpenAPI 3.0 spec is available at <a href="' + url + '">' + url + '</a> ' +
    'and <a href="/openapi.json">/openapi.json</a>.</p>' +
    '</div>';
})();
