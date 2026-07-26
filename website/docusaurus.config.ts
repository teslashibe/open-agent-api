import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'Open Agent API',
  tagline: 'Your local CLI sessions, behind an OpenAI-shaped API',
  favicon: 'img/favicon.svg',

  url: 'https://teslashibe.github.io',
  baseUrl: '/open-agent-api/',

  organizationName: 'teslashibe',
  projectName: 'open-agent-api',

  onBrokenLinks: 'throw',

  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          editUrl:
            'https://github.com/teslashibe/open-agent-api/tree/main/website/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: 'img/logo.svg',
    navbar: {
      title: 'Open Agent API',
      logo: {
        alt: 'Open Agent API',
        src: 'img/logo.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Docs',
        },
        {
          href: 'https://github.com/teslashibe/open-agent-api',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {label: 'Install', to: '/docs/install'},
            {label: 'Auth', to: '/docs/auth/overview'},
            {label: 'Cursor BYOK', to: '/docs/cursor/byok-ngrok'},
            {label: 'Models', to: '/docs/models/catalog'},
          ],
        },
        {
          title: 'Repo',
          items: [
            {
              label: 'GitHub',
              href: 'https://github.com/teslashibe/open-agent-api',
            },
            {
              label: 'AGENTS.md',
              href: 'https://github.com/teslashibe/open-agent-api/blob/main/AGENTS.md',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} teslashibe`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'yaml'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
