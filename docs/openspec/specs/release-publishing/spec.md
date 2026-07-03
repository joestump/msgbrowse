---
status: draft
date: 2026-07-03
implements: [ADR-0019]
---

# SPEC-0012: Gitea-primary development and GitHub publishing

## Overview

The canonical msgbrowse repository moves to `gitea.stump.rocks/joestump/msgbrowse`
(ADR-0019). GitHub remains the public face and MUST continue to receive code,
docs, releases, and packages automatically: a push mirror keeps the GitHub repo
current (which keeps GitHub Pages and PR checks firing), while Gitea Actions on
self-hosted runners build and publish container images to GitHub Packages
(ghcr.io) and create GitHub Releases with artifacts. This is CI/infrastructure
work: no application endpoints or UI change.

## Requirements

### Requirement: Canonical repository on Gitea

The repository `gitea.stump.rocks/joestump/msgbrowse` MUST exist and hold the
full history of `github.com/joestump/msgbrowse`. Development pushes MUST land
on Gitea first; the GitHub repository MUST NOT be pushed to directly once the
mirror is live (GitHub-side merges MAY still occur for external contributor
PRs, and MUST be reconciled back to Gitea before the next Gitea-side push).

#### Scenario: History parity at cutover

- **WHEN** the Gitea repository is created from the GitHub repository
- **THEN** every branch, tag, and commit reachable on GitHub is present on Gitea with identical SHAs.

### Requirement: Push mirror to GitHub

Gitea MUST be configured with a push mirror to `github.com/joestump/msgbrowse`
using a GitHub PAT, with sync-on-commit enabled so mirroring completes within
minutes of a push. Because the mirror authenticates with a PAT (not
`GITHUB_TOKEN`), mirrored pushes MUST continue to trigger GitHub Actions
workflows — Pages deploys and CI checks included.

#### Scenario: Docs deploy still fires from a mirrored push

- **WHEN** a commit touching `docs-site/**` is pushed to Gitea `main` and the mirror syncs
- **THEN** the GitHub `docs` workflow runs on the mirrored push and GitHub Pages republishes.

#### Scenario: Mirror failure is visible

- **WHEN** the push mirror fails (expired PAT, network)
- **THEN** the failure is observable (Gitea mirror status and/or a failing scheduled check), not silent drift.

### Requirement: CI parity on Gitea

The three merge-gating checks (`gofmt + vet + tests`, `docker image builds`,
`app.css is up to date`) MUST be ported to `.gitea/workflows/` and MUST run on
Gitea pull requests and pushes to `main`. A Gitea PR MUST NOT be mergeable
with failing checks.

#### Scenario: Gitea PR gates like GitHub

- **WHEN** a Gitea PR introduces a gofmt violation
- **THEN** the Gitea Actions check fails and the PR shows as unmergeable until fixed.

### Requirement: Container images published to GitHub Packages

On every version tag (`v*`), Gitea Actions MUST build multi-arch
(linux/amd64 + linux/arm64) container images and push them to
`ghcr.io/joestump/msgbrowse` tagged with the version and `latest`. Pushes to
`main` SHOULD additionally publish an `edge` tag. Authentication MUST use a
GitHub PAT with `write:packages` stored as a Gitea Actions secret; the PAT
MUST NOT appear in workflow files or logs.

#### Scenario: Tag publishes a pullable image

- **WHEN** tag `v1.2.3` is pushed to Gitea
- **THEN** `docker pull ghcr.io/joestump/msgbrowse:v1.2.3` succeeds for both amd64 and arm64, and `latest` points at the same digest.

### Requirement: GitHub Releases with artifacts

On every version tag, Gitea Actions MUST create (or update) the corresponding
GitHub Release via the GitHub API, attaching built artifacts (at minimum the
`CGO_ENABLED=0` server/CLI binaries for darwin/linux, amd64/arm64) and
release notes. Desktop-shell artifacts (SPEC-0010) MAY attach when their CI
matrix produces them.

#### Scenario: Release appears on GitHub

- **WHEN** tag `v1.2.3` is pushed to Gitea and the release workflow completes
- **THEN** `github.com/joestump/msgbrowse/releases/tag/v1.2.3` exists with the binary artifacts attached.

### Requirement: Secondary publication to the Gitea registry

Images published to ghcr.io SHOULD also be pushed to the Gitea OCI registry
(`gitea.stump.rocks/joestump/msgbrowse`) with identical tags, so StumpCloud
deployments can pull without leaving the LAN.

#### Scenario: Local pull without GitHub

- **WHEN** a StumpCloud host pulls `gitea.stump.rocks/joestump/msgbrowse:latest`
- **THEN** it receives the same image digest as `ghcr.io/joestump/msgbrowse:latest`.

### Requirement: Tracker and workflow continuity

The SDD tracker (GitHub issues/PRs per CLAUDE.md) MUST remain functional and
unchanged by the cutover: in-flight epics and stories keep their numbers and
links. Any future migration of issues/reviews to Gitea is out of scope and
MUST be decided in a separate ADR.

#### Scenario: In-flight epic survives cutover

- **WHEN** the mirror goes live mid-epic
- **THEN** existing GitHub issues, PR references, and "Part of #N" links continue to resolve unchanged.

### Requirement: Documented operations

The docs site MUST document the topology (canonical Gitea, GitHub mirror), the
release process (tagging on Gitea), the secrets involved and their scopes, and
the external-contributor flow (GitHub PRs reconciled to Gitea).

#### Scenario: Operator can rotate the PAT from docs alone

- **WHEN** the GitHub PAT expires
- **THEN** the docs describe minting scopes (repo, write:packages) and where to update the Gitea mirror config and Actions secret.
