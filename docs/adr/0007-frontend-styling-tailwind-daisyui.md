# ADR-0007: Frontend styling — Tailwind CSS v4 + daisyUI via the standalone CLI

- **Status:** Accepted (visual choices superseded in part)
- **Date:** 2026-06-27
- **Builds on:** [ADR-0006](0006-web-stack-htmx.md) (server-rendered UI, no runtime npm, strict CSP)
- **Superseded in part by:** [ADR-0012](0012-slate-redesign-design-system.md) — the stock `dim`/`winter` themes and the chat-bubble transcript are replaced by the slate theme + dense-log transcript. The framework choice (Tailwind + daisyUI via the standalone CLI) stands.

## Context

The HTMX UI ([ADR-0006](0006-web-stack-htmx.md)) needs styling: a component
vocabulary (cards, menus, chat bubbles, drawers, stats), a light/dark theme
toggle, and icons — all under a `style-src 'self'` / `script-src 'self'` CSP with
**no CDN and no Node at runtime**. Tailwind + daisyUI is the desired look, but
the canonical way to run them is a PostCSS/npm pipeline, which we explicitly do
not want in the runtime container or the request path.

## Decision

**Use Tailwind CSS v4 + daisyUI, but build with the Tailwind standalone CLI
(a single binary, no Node), commit the built `app.css`, and serve it via
`go:embed`.**

1. **The built stylesheet is committed and embedded.** `make css` compiles
   `internal/web/tailwind/input.css` to `internal/web/static/app.css` (minified);
   that output is committed and served from the `//go:embed static/*` set. The
   runtime needs no toolchain — only the file.
2. **The toolchain is dev-time and gitignored.** `make css` downloads the
   Tailwind standalone CLI and the daisyUI npm tarball into `.tools/` (never
   committed; `make clean-tools` removes it). Versions are pinned in the Makefile:
   `TAILWIND_VERSION := v4.3.1`, `DAISYUI_VERSION := 5.6.3`. The Makefile maps
   `uname` → the correct Tailwind release asset (linux/macos × x64/arm64).
3. **daisyUI is loaded as a Tailwind v4 plugin from `.tools/`.** `input.css` has
   `@plugin "../../../.tools/daisyui/package/index.js" { themes: dim --default, winter; }`
   and `@source "../templates"` for v4 content detection.
4. **Theme toggle is self-hosted JS.** `static/theme.js` switches
   `data-theme` between `dim` (dark, default) and `winter` (light) and persists to
   `localStorage`; it is loaded non-deferred in `<head>` so the saved theme
   applies before first paint (no FOUC). It runs under `script-src 'self'`.
5. **Icons are inline Hero Icons SVG.** `templates/icons.html` vendors Hero Icons
   (MIT) as inline `{{define "icon-*"}}` SVG blocks — no icon font, no JS, no
   external fetch, so the CSP holds.
6. **Dynamic classes are safelisted.** Avatar background classes are chosen in Go
   (`avatarColor` in `render.go`), so Tailwind's content scan never sees them
   literally. `input.css` force-includes them with
   `@source inline("bg-rose-500 bg-orange-500 … bg-fuchsia-500")`, kept in sync
   with the `avatarPalette` slice.
7. **CI guards against stylesheet drift.** `.github/workflows/css.yml` runs
   `make css` when templates / `tailwind/**` / `app.css` / the Makefile change and
   fails if the committed `app.css` differs — so an edit to a template that adds a
   class can't ship without a rebuilt stylesheet. The path filter keeps the ~100 MB
   Tailwind binary download off unrelated PRs.

## Why these choices

- **Standalone CLI over PostCSS/npm:** it is a single pinned binary that produces
  the identical Tailwind v4 output without a `package.json`, `node_modules`, or a
  Node runtime — exactly the "no Node at runtime" property we want, while still
  using upstream Tailwind/daisyUI.
- **Commit the built CSS:** the runtime serves one static, embedded file; there is
  no build step between a `git clone` and a working UI, and the CSP needs only
  `style-src 'self'`.
- **`.tools/` gitignored, versions in the Makefile:** the heavy, platform-specific
  binaries don't belong in git, but the *versions* are pinned and visible so the
  build is reproducible.
- **`@source inline(...)` over disabling content-purge:** keeps the purge on (small
  CSS) while explicitly safelisting the one set of classes selected at runtime in
  Go — the comment in `render.go` and the directive in `input.css` document the
  coupling.
- **Inline SVG icons over an icon font/sprite CDN:** no extra request, no font
  egress, CSP-clean, and each icon scales with `size-[1.15em]`.

## Consequences

### Positive

- Runtime is toolchain-free: one committed, embedded, minified `app.css`.
- Full daisyUI component set and a real light/dark toggle, all same-origin under a
  strict CSP — no CDN, no external fonts.
- CI catches a stale `app.css`, so the committed stylesheet can't silently drift
  from the templates.

### Negative

- **A dev-time toolchain exists** — editing UI classes requires running `make css`
  (and committing the result) before CI passes. This is the deliberate trade:
  developer-time complexity in exchange for runtime simplicity and a strict CSP.
- The avatar safelist in `input.css` must be kept in sync with `avatarPalette`;
  adding a color in Go without updating the `@source inline(...)` list would purge
  it from the build.
- `make css` is platform-specific (downloads the matching Tailwind asset) and
  needs network access the first time.

### Operational

- The css workflow downloads the Tailwind binary, so it is scoped by path filter
  to UI-touching changes only.
- Bumping `TAILWIND_VERSION`/`DAISYUI_VERSION` is a Makefile edit followed by
  `make css` + commit; the CI guard will flag the regenerated `app.css`.

## Alternatives considered

- **Tailwind via PostCSS/npm in CI.** Rejected: pulls a Node toolchain and
  `node_modules` into the build for output a single pinned binary produces
  identically.
- **A CDN `<link>` to Tailwind/daisyUI.** Rejected: violates `style-src 'self'`
  and the no-off-device-fetch rule, and breaks offline/local-only use.
- **Hand-written CSS, no framework.** Rejected: reimplements daisyUI's components
  and theming by hand for no benefit; a small amount of bespoke CSS in `input.css`
  covers only what daisyUI doesn't (thumbnails, `:target` lightbox, flash
  animation).
- **An icon font or JS icon library.** Rejected: extra asset/egress and CSP
  friction; inline SVG is zero-dependency.

## References

- `Makefile` (`css` target, `TAILWIND_VERSION`, `DAISYUI_VERSION`, `.tools/`)
- `internal/web/tailwind/input.css` (`@plugin`, themes, `@source inline`)
- `internal/web/static/app.css` (committed build), `static/theme.js`
- `internal/web/templates/icons.html` (inline Hero Icons), `render.go` (`avatarPalette`)
- `.github/workflows/css.yml` (CSS-drift guard)
- [ADR-0006: Web stack — HTMX](0006-web-stack-htmx.md)
