import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'Open Chat API',
  tagline: 'OpenAI-compatible chat over local CLI sessions',
  favicon: 'img/favicon.svg',

  url: 'https://teslashibe.github.io',
  baseUrl: '/open-chat-api/',

  organizationName: 'teslashibe',
  projectName: 'open-chat-api',

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
            'https://github.com/teslashibe/open-chat-api/tree/main/website/',
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
      title: 'Open Chat API',
      logo: {
        alt: 'Open Chat API',
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
          href: 'https://github.com/teslashibe/open-chat-api',
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
              href: 'https://github.com/teslashibe/open-chat-api',
            },
            {
              label: 'AGENTS.md',
              href: 'https://github.com/teslashibe/open-chat-api/blob/main/AGENTS.md',
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
