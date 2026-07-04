import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'

// https://vitepress.dev/reference/site-config
export default withMermaid(
  defineConfig({
    lang: 'zh-CN',
    title: 'mvn-repo-scanner',
    description: 'Maven 仓库敏感内容扫描器 — 扫描中央仓库与私服中的密码、密钥、证书等泄露',

    // 站点根路径，部署到 GitHub Pages 的 /mvn-repo-scanner/ 子路径下。
    // 若部署到自定义域名根路径，请改为 '/'。
    base: '/mvn-repo-scanner/',

    // README.md 是给 GitHub 仓库查看的开发说明，不应作为站点页面构建。
    srcExclude: ['README.md'],

    mermaid: {
      // Mermaid 主题跟随站点；暗色模式下用 dark。
      theme: 'default',
    },

  // GitHub 仓库链接（顶部导航栏图标）
  themeConfig: {
    repo: 'scagogogo/mvn-repo-scanner',
    repoLabel: 'GitHub',

    // 文档头部 logo 文字（保持与 title 一致即可）
    siteTitle: 'mvn-repo-scanner',

    nav: [
      { text: '首页', link: '/' },
      { text: '使用指南', link: '/guide/getting-started' },
      { text: '工作原理', link: '/principle/overview' },
      { text: 'AI 接入', link: '/ai-agent/' },
    ],

    sidebar: {
      '/guide/': [
        {
          text: '开始',
          items: [
            { text: '快速开始', link: '/guide/getting-started' },
            { text: '安装', link: '/guide/installation' },
          ],
        },
        {
          text: '核心功能',
          items: [
            { text: '命令参考', link: '/guide/commands' },
            { text: '检测规则', link: '/guide/rules' },
            { text: '私有仓库认证', link: '/guide/private-repo' },
            { text: '断点续扫', link: '/guide/resume' },
            { text: '任务调度', link: '/guide/scheduling' },
            { text: '结果输出', link: '/guide/output' },
          ],
        },
        {
          text: '进阶',
          items: [
            { text: '常见问题排查', link: '/guide/troubleshooting' },
            { text: '最佳实践', link: '/guide/best-practices' },
            { text: '实测案例', link: '/guide/case-studies' },
            { text: '常见问答', link: '/guide/faq' },
          ],
        },
      ],
      '/principle/': [
        {
          text: '工作原理',
          items: [
            { text: '总体架构', link: '/principle/overview' },
            { text: '树形遍历与游标恢复', link: '/principle/cursor' },
            { text: '四阶段流水线', link: '/principle/pipeline' },
            { text: '敏感内容检测', link: '/principle/detection' },
            { text: '持久化与任务管理', link: '/principle/persistence' },
          ],
        },
      ],
      '/ai-agent/': [
        {
          text: 'AI Agent 接入',
          items: [
            { text: '概览', link: '/ai-agent/' },
            { text: 'Claude Code', link: '/ai-agent/claude-code' },
            { text: 'Codex', link: '/ai-agent/codex' },
          ],
        },
      ],
    },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/scagogogo/mvn-repo-scanner' },
    ],

    search: {
      provider: 'local',
      options: {
        translations: {
          button: {
            buttonText: '搜索文档',
            buttonAriaLabel: '搜索文档',
          },
          modal: {
            noResultsText: '无法找到相关结果',
            resetButtonTitle: '清除查询条件',
            footer: {
              selectText: '选择',
              navigateText: '切换',
            },
          },
        },
      },
    },

    footer: {
      message: '基于 Apache-2.0 协议发布',
      copyright: 'Copyright © 2026 scagogogo',
    },

    outline: {
      label: '本页目录',
      level: [2, 3],
    },

    docFooter: {
      prev: '上一页',
      next: '下一页',
    },

    lastUpdated: {
      text: '最后更新于',
    },
  },
}),
)
