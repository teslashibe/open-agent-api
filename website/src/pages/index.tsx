import type {ReactNode} from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

import styles from './index.module.css';

function HomepageHeader() {
  const {siteConfig} = useDocusaurusContext();
  return (
    <header className={clsx('hero hero--primary', styles.heroBanner)}>
      <div className="container">
        <Heading as="h1" className="hero__title">
          {siteConfig.title}
        </Heading>
        <p className="hero__subtitle">{siteConfig.tagline}</p>
        <div className={styles.buttons}>
          <Link className="button button--secondary button--lg" to="/docs/intro">
            Get started
          </Link>
          <Link
            className="button button--outline button--secondary button--lg"
            to="/docs/cursor/byok-ngrok"
            style={{marginLeft: '0.75rem'}}>
            Cursor BYOK
          </Link>
        </div>
      </div>
    </header>
  );
}

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout title={siteConfig.title} description={siteConfig.tagline}>
      <HomepageHeader />
      <main>
        <section className="container margin-vert--lg">
          <div className="row">
            <div className="col col--4">
              <h3>
                <Link to="/docs/install/docker">Docker</Link>
              </h3>
              <p>Run locally with Compose and host OAuth mounts.</p>
            </div>
            <div className="col col--4">
              <h3>
                <Link to="/docs/install/kubernetes">Kubernetes</Link>
              </h3>
              <p>In-cluster gateway for apps behind a bearer secret.</p>
            </div>
            <div className="col col--4">
              <h3>
                <Link to="/docs/models/catalog">Models</Link>
              </h3>
              <p>Full Codex, Antigravity, and Claude Code slug catalog.</p>
            </div>
          </div>
        </section>
      </main>
    </Layout>
  );
}
