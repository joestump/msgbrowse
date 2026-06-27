# Security

msgbrowse handles sensitive personal data (your entire message history). It is
designed local-first, least-privilege, and read-only with respect to the
archive. This document describes the threat model and the mitigations.

## Threat model

- **Adversary:** other software on the same machine, a malicious archive (crafted
  message content), accidental data exfiltration to a hosted LLM, and network
  attackers if the UI is ever exposed beyond loopback.
- **Assets:** the plaintext message archive, the derived SQLite database +
  embeddings, and the encrypted `.snapshots` backups.
- **Out of scope:** the security of the upstream exporters, the macOS Keychain,
  and disk-at-rest encryption (FileVault is assumed for the plaintext export).

## What stays on the machine

Everything, except calls to the single configured `llm.base_url`. There is **no
telemetry, no analytics, and no other outbound connection.** The default
`llm.base_url` is a local LiteLLM proxy routing to a local model (Ollama), so out
of the box message content never leaves the device.

## The data-sent-to-the-LLM boundary

This is the one place data leaves the box, and only if you point LiteLLM at a
hosted provider. What is sent, and when:

| Feature | What is sent to `llm.base_url` |
| --- | --- |
| `embed` | Message text (per message), to compute embeddings. |
| MCP `semantic_search` / hybrid `search_messages` | Your **query** text (to embed it). |
| Journal digests *(Slice 6)* | A day's message text, to write the digest. |
| Journal image captions *(Slice 6, opt-in)* | **Image bytes** of received photos. |
| Journal audio transcripts *(Slice 6, opt-in)* | **Audio bytes** of voice messages. |

Image and audio bytes are a **much heavier and more sensitive egress** than text.
If — and only if — you route LiteLLM to a hosted model, enabling vision/audio
sends raw media off-device. The default local route keeps it on the machine.

**Privacy controls:**
- `journal.exclude_conversations` is a denylist of conversations whose content is
  **never** sent to any LLM, for any feature.
- Keep the default local LiteLLM route. Routing to a hosted provider must be a
  deliberate edit to `litellm.config.yaml` and is documented as off-device.
- The API key is read from `MSGBROWSE_LLM_API_KEY` (env/secret) only and is never
  baked into the image or expected in a committed file.

## Archive integrity

- The archive is mounted **read-only** in Docker (`:ro`) and msgbrowse only ever
  opens files for reading. Imports write exclusively to `data_dir`, which must be
  outside the archive.
- The encrypted `.snapshots/*.tar` (SQLCipher raw-DB backups) are **inventoried by
  filename and size only** — msgbrowse never opens, decrypts, or reads their
  contents, and never touches the macOS Keychain.

## Web UI hardening

- Binds to **loopback by default**. A non-loopback bind logs a warning; the UI has
  no authentication, so only expose it behind your own access control.
- Strict `Content-Security-Policy: default-src 'none'` (plus `script-src 'self'`,
  `img-src 'self' data:`), `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, `frame-ancestors 'none'`.
- Everything the UI loads is **same-origin and self-hosted** — the stylesheet
  (Tailwind + daisyUI, built at dev time and committed), htmx, the theme-toggle
  script, and Hero Icons (inline SVG). No CDN, no external fonts/scripts, so the
  strict CSP holds and nothing about your browsing is fetched off-device.
- All message content is untrusted and **HTML-escaped** via `html/template`;
  attachment markdown is stripped, URLs are linkified with
  `rel="noopener noreferrer nofollow"`, and search snippets are escaped before
  highlight markers are applied.
- Media is served with correct `Content-Type` and `Content-Disposition`; SVGs are
  forced to download (never inlined); **path traversal is contained** (cleaned,
  leading-slash-anchored, and verified to stay within the conversation directory).
- The MCP server is read-only and its stdio transport keeps logs on stderr so they
  never corrupt the JSON-RPC stream.

## Container hardening

The app container runs as a **non-root** user with a **read-only root
filesystem** (`/tmp` is tmpfs, `/data` is the only writable volume), **all Linux
capabilities dropped**, and **no-new-privileges**. The web port is published to
host **loopback only**; LiteLLM is not published to the host at all. The
in-container `0.0.0.0` bind is safe because the container network is isolated and
the host mapping is loopback-only — this is the standard Docker pattern, not a
public exposure.

## Reporting

This is a personal project. If you find a vulnerability, open an issue (without
sensitive data) at <https://github.com/joestump/msgbrowse/issues>.
