import type {ReactNode} from 'react';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';

import styles from './index.module.css';

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout
      title="Open Agent API"
      description={siteConfig.tagline}>
      <div className={styles.page}>
        <header className={styles.hero}>
          <div className={styles.heroMesh} aria-hidden="true" />
          <div className="container">
            <div className={styles.heroInner}>
              <p className={styles.brand}>Open Agent API</p>
              <h1 className={styles.headline}>
                OpenAI-compatible agent API over your local CLI sessions.
              </h1>
              <p className={styles.support}>
                One `/v1` endpoint for Codex, Claude Code, and Antigravity —
                Cursor Agent BYOK, Docker, or an in-cluster gateway. No `sk-` keys.
              </p>
              <div className={styles.actions}>
                <Link className={styles.primaryCta} to="/docs/intro">
                  Read the docs
                </Link>
                <Link className={styles.secondaryCta} to="/docs/cursor/byok-ngrok">
                  Cursor BYOK setup
                </Link>
              </div>
            </div>
          </div>
        </header>

        <section className={styles.section}>
          <div className="container">
            <h2 className={styles.sectionTitle}>Start where you need</h2>
            <p className={styles.sectionText}>
              Install locally, wire credentials, then point Cursor or your apps
              at the same OpenAI-shaped API.
            </p>
            <div className={styles.pathGrid}>
              <div className={styles.pathItem}>
                <h3>
                  <Link to="/docs/install/docker">Docker</Link>
                </h3>
                <p>Compose on localhost with host OAuth mounts.</p>
              </div>
              <div className={styles.pathItem}>
                <h3>
                  <Link to="/docs/install/kubernetes">Kubernetes</Link>
                </h3>
                <p>In-cluster gateway behind a bearer secret.</p>
              </div>
              <div className={styles.pathItem}>
                <h3>
                  <Link to="/docs/models/catalog">Models</Link>
                </h3>
                <p>Full slug catalog across every upstream surface.</p>
              </div>
            </div>
          </div>
        </section>
      </div>
    </Layout>
  );
}
