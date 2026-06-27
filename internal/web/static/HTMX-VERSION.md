# Vendored htmx

This directory ships a vendored copy of htmx so the web UI works without any
build step or network access at runtime. The CSP locks `script-src 'self'`, so
the file embedded here is the only script that can ever execute.

## Current pin

- **Version:** `2.0.4`
- **File:** `htmx.min.js`
- **Size:** 50917 bytes
- **SHA-256:** `e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447`
- **Upstream:** <https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js>

## Updating

1. Download the new release from the upstream URL above with the desired version.
2. Verify its SHA-256 matches what the htmx release announcement publishes
   (or, at minimum, compare it to a second independent source — jsdelivr,
   GitHub release, npm tarball).
3. Replace `htmx.min.js` and update this file's version, size, and SHA-256.
4. Run `make check` and exercise the transcript page locally (infinite scroll
   uses `hx-trigger="revealed"`, which is the surface most likely to regress).
