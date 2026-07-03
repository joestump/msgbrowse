---
sidebar_position: 3
title: Security model
---

# Security model

msgbrowse handles your entire message history — some of the most sensitive data you own. It is designed local-first, least-privilege, and read-only with respect to the archive. This page distills the full [SECURITY.md](https://github.com/joestump/msgbrowse/blob/main/SECURITY.md); read that for the complete threat model.

## Local-only threat model

The adversaries msgbrowse defends against are:

- **Other software on the same machine** reading the derived database or the UI.
- **A malicious archive** — crafted message content trying to exploit the browser or the parser.
- **Accidental data exfiltration** to a hosted LLM.
- **Network attackers**, if the UI is ever exposed beyond loopback.

The assets are the plaintext message archive, the derived SQLite database and embeddings, and the encrypted `.snapshots` backups. Out of scope: the security of the upstream exporters, the macOS Keychain, and disk-at-rest encryption (FileVault is assumed for the plaintext export).

## Nothing leaves the machine — except one URL

Everything stays local except calls to the single configured `llm.base_url`. There is **no telemetry, no analytics, and no other outbound connection.** The default `llm.base_url` is a local LiteLLM proxy (`http://127.0.0.1:4000/v1`) routing to a local model, so out of the box message content never leaves the device.

What crosses that one boundary, and only if you deliberately point it at a hosted provider:

| Feature | What is sent to `llm.base_url` |
| --- | --- |
| `embed` | Message text (per message), to compute embeddings. |
| MCP `semantic_search` / hybrid `search_messages` | Your **query** text (to embed it). |
| Journal digests | A day's message text, to write the digest. |
| Journal image captions *(opt-in)* | **Image bytes** of received photos. |
| Journal audio transcripts *(opt-in)* | **Audio bytes** of voice messages. |

:::warning
Image and audio bytes are a much heavier and more sensitive egress than text. Enabling vision/audio while routing to a hosted model sends raw media off-device. The default local route keeps it on the machine.
:::

Privacy controls on this boundary:

- `journal.exclude_conversations` is a denylist of conversations whose content is **never** sent to any LLM, for any feature.
- Routing to a hosted provider must be a deliberate configuration edit; the default is local.
- The API key is read from `MSGBROWSE_LLM_API_KEY` (environment/secret) only — never baked into an image or expected in a committed file.

## Loopback bind, no auth

The web UI binds to `127.0.0.1:8787` by default. A non-loopback bind logs a warning; the UI has **no authentication**, so only expose it behind your own access control (VPN, authenticated reverse proxy). The same posture applies to `mcp --http`, which defaults to `127.0.0.1:8788`.

In the Docker setup the web port is published to host loopback only and LiteLLM is not published to the host at all; the container's internal `0.0.0.0` bind is confined to the isolated container network.

## Read-only archive guarantee

- The archive is treated as **strictly read-only**: msgbrowse only ever opens files inside it for reading, and in Docker it is mounted `:ro`. Imports write exclusively to `data_dir`, which must be outside the archive.
- The encrypted `.snapshots/*.tar` files (SQLCipher raw-database backups) are **inventoried by filename and size only**. msgbrowse never opens, decrypts, or reads their contents.
- msgbrowse **never touches the macOS Keychain**. The upstream exporters it can spawn (`sigexport`, `imessage-exporter`) access their own sources with the OS's consent; msgbrowse just runs them at your explicit request and passes nothing sensitive on the command line.

## Web UI hardening

- **Strict Content Security Policy**: `default-src 'none'` plus `script-src 'self'` and `img-src 'self' data:`, alongside `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, and `frame-ancestors 'none'`. There are no inline styles or scripts.
- Everything the UI loads is **same-origin and self-hosted** — the committed stylesheet, htmx, the theme-toggle script, and inline-SVG icons. No CDN, no external fonts or scripts, so nothing about your browsing is fetched off-device.
- All message content is untrusted and **HTML-escaped** via `html/template`. URLs are linkified with `rel="noopener noreferrer nofollow"`, and search snippets are escaped before highlight markers are applied.
- **Path traversal is contained**: media paths are cleaned, anchored, and verified to stay within the conversation directory before anything is served. Media gets a correct `Content-Type` and `Content-Disposition`; SVGs are forced to download, never inlined.

## MCP and container posture

- The MCP server is **read-only**, and its stdio transport keeps logs on stderr so they never corrupt the JSON-RPC stream.
- The app container runs as a **non-root** user with a **read-only root filesystem** (`/tmp` is tmpfs, `/data` is the only writable volume), all Linux capabilities dropped, and `no-new-privileges`.

## Reporting

If you find a vulnerability, open an issue (without sensitive data) at [github.com/joestump/msgbrowse/issues](https://github.com/joestump/msgbrowse/issues).
