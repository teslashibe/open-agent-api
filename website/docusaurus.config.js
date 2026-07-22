/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'Codex Chat API Operator Guide',
  tagline: 'Deploy, validate, and operate the Codex credential pool safely.',
  url: 'https://teslashibe.github.io',
  baseUrl: '/codex-chat-api/',
  organizationName: 'teslashibe',
  projectName: 'codex-chat-api',
  trailingSlash: true,
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },
  presets: [
    [
      'classic',
      {
        docs: {
          routeBasePath: 'docs',
          sidebarPath: require.resolve('./sidebars.js'),
        },
        blog: false,
        theme: {},
      },
    ],
  ],
  themeConfig: {
    navbar: {
      title: 'Codex Chat API',
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'operatorGuide',
          position: 'left',
          label: 'Operator guide',
        },
        {
          href: 'https://github.com/teslashibe/codex-chat-api',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      copyright: `Copyright © ${new Date().getFullYear()} codex-chat-api contributors.`,
    },
  },
};

module.exports = config;
