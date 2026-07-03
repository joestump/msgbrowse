/**
 * msgbrowse docs homepage.
 *
 * Hero + screenshot + six feature tiles + a slim "Private by design" band.
 * Styling lives entirely in index.module.css (CSS modules, no shared CSS
 * touched). The hero and privacy band are always dark slate — the app's own
 * palette — while the feature grid adapts to the active Docusaurus theme.
 */

import type {ReactNode} from 'react';
import Link from '@docusaurus/Link';
import useBaseUrl from '@docusaurus/useBaseUrl';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Heading from '@theme/Heading';
import Layout from '@theme/Layout';

import styles from './index.module.css';

const GITHUB_URL = 'https://github.com/joestump/msgbrowse';

type Feature = {
  emoji: string;
  title: string;
  body: string;
  to: string;
};

const FEATURES: Feature[] = [
  {
    emoji: '🗂️',
    title: 'Unified archive',
    body:
      'Signal and iMessage in one reading room — a single local database ' +
      'with incremental, idempotent imports and a pinnable, filterable sidebar.',
    to: '/docs/features/browsing',
  },
  {
    emoji: '🔍',
    title: 'Full-text search',
    body:
      'SQLite FTS5 keyword search over every message, with live results as ' +
      'you type. Instant, offline, no LLM required.',
    to: '/docs/features/search',
  },
  {
    emoji: '🖼️',
    title: 'Media gallery',
    body:
      'Every image, file, and link across your archive in tabbed galleries ' +
      'with a lightbox — served straight from your read-only export.',
    to: '/docs/features/media-gallery',
  },
  {
    emoji: '💬',
    title: 'Reactions & rich transcripts',
    body:
      'Dense-log transcripts with day separators, sender rails, and reaction ' +
      'badges — built for reading years of history, not scrolling bubbles.',
    to: '/docs/features/browsing#the-dense-log-transcript',
  },
  {
    emoji: '🧠',
    title: 'AI facts & semantic search',
    body:
      'Embeddings and cited contact facts, computed by the one ' +
      'OpenAI-compatible LLM endpoint you configure — local by default.',
    to: '/docs/features/ai-features',
  },
  {
    emoji: '🤖',
    title: 'MCP server for AI assistants',
    body:
      'Claude and any other MCP client can search and read your history over ' +
      'stdio — read-only, on your machine.',
    to: '/docs/features/mcp-server',
  },
];

const PRIVACY_POINTS = [
  'Binds to loopback by default',
  'Your archive is treated strictly read-only',
  'One configurable LLM endpoint — nothing else leaves',
  'Strict Content-Security-Policy',
];

function Hero(): ReactNode {
  return (
    <header className={styles.hero}>
      <div className={styles.heroInner}>
        <Heading as="h1" className={styles.title}>
          msgbrowse
        </Heading>
        <p className={styles.tagline}>
          A calm, private reading room for everything you&rsquo;ve ever said.
        </p>
        <p className={styles.pill}>
          <span className={styles.pillDot} aria-hidden="true" />
          Local-only. Nothing leaves your machine.
        </p>
        <div className={styles.ctaRow}>
          <Link
            className={styles.ctaPrimary}
            to="/docs/getting-started/what-is-msgbrowse">
            Get Started
          </Link>
          <Link className={styles.ctaGhost} href={GITHUB_URL}>
            GitHub
          </Link>
        </div>
      </div>
      <Screenshot />
    </header>
  );
}

function Screenshot(): ReactNode {
  const screenshotUrl = useBaseUrl('/img/hero-screenshot.png');
  return (
    <div className={styles.frame}>
      <div className={styles.frameBar}>
        <div className={styles.frameDots} aria-hidden="true">
          <span />
          <span />
          <span />
        </div>
        <span className={styles.frameUrl}>127.0.0.1:8787</span>
        <span aria-hidden="true" />
      </div>
      <img
        className={styles.screenshot}
        src={screenshotUrl}
        alt="The msgbrowse reading room: a sidebar of conversations next to a dense transcript with day separators and reaction badges"
        width={2000}
        height={1238}
        loading="eager"
        decoding="async"
      />
    </div>
  );
}

function FeatureTile({emoji, title, body, to}: Feature): ReactNode {
  return (
    <Link className={styles.tile} to={to}>
      <span className={styles.tileEmoji} aria-hidden="true">
        {emoji}
      </span>
      <Heading as="h3" className={styles.tileTitle}>
        {title}
      </Heading>
      <p className={styles.tileBody}>{body}</p>
      <span className={styles.tileCta}>Learn more →</span>
    </Link>
  );
}

function FeatureGrid(): ReactNode {
  return (
    <section className={styles.features}>
      <div className={styles.featuresInner}>
        <span className={styles.kicker}>Features</span>
        <Heading as="h2" className={styles.sectionTitle}>
          Everything in the reading room
        </Heading>
        <div className={styles.grid}>
          {FEATURES.map((feature) => (
            <FeatureTile key={feature.title} {...feature} />
          ))}
        </div>
      </div>
    </section>
  );
}

function PrivacyBand(): ReactNode {
  return (
    <section className={styles.privacy}>
      <div className={styles.privacyInner}>
        <Heading as="h2" className={styles.privacyTitle}>
          Private by design
        </Heading>
        <p className={styles.privacySub}>
          Built local-first and least-privilege, for the most personal data you
          own.
        </p>
        <ul className={styles.privacyList}>
          {PRIVACY_POINTS.map((point) => (
            <li key={point} className={styles.privacyItem}>
              {point}
            </li>
          ))}
        </ul>
        <Link className={styles.privacyLink} to="/docs/reference/security-model">
          Read the security model →
        </Link>
      </div>
    </section>
  );
}

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout
      title={siteConfig.tagline}
      description="msgbrowse is a self-hosted, local-only browser, search engine, and AI-editorialized journal for your Signal and iMessage archives. Nothing leaves your machine.">
      <main className={styles.home}>
        <Hero />
        <FeatureGrid />
        <PrivacyBand />
      </main>
    </Layout>
  );
}
