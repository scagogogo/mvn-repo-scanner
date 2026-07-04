# mvn-repo-scanner 官网

基于 [VitePress](https://vitepress.dev/) 构建的文档站点。

## 本地开发

```bash
cd website
npm install
npm run dev
```

访问 VitePress 开发服务器输出的本地地址预览（默认 `http://localhost:5173/`，带 base 前缀）。

## 构建

```bash
npm run build
```

产物输出到 `.vitepress/dist/`。

## 目录结构

```text
website/
├── .vitepress/
│   └── config.ts        # VitePress 配置（导航、侧边栏、搜索）
├── index.md             # 首页（含 AI Agent 接入提示词）
├── ai-agent/            # AI Agent 接入指南
│   ├── index.md
│   ├── claude-code.md
│   └── codex.md
├── guide/               # 使用指南
│   ├── getting-started.md
│   ├── installation.md
│   ├── commands.md
│   ├── rules.md
│   ├── private-repo.md
│   ├── resume.md
│   ├── scheduling.md
│   └── output.md
├── principle/           # 工作原理
│   ├── overview.md
│   ├── cursor.md
│   ├── pipeline.md
│   ├── detection.md
│   └── persistence.md
├── package.json
└── package-lock.json
```

## 部署

通过 GitHub Actions 自动部署到 GitHub Pages（见 `.github/workflows/deploy.yml`）。

`push` 到 `main` 分支且改动 `website/` 时自动触发部署。

### 部署配置

- `base`（在 `.vitepress/config.ts`）：设为 `/mvn-repo-scanner/`，对应 GitHub Pages 项目站点路径 `https://<user>.github.io/mvn-repo-scanner/`
- 若使用自定义域名（如 `docs.example.com`），将 `base` 改为 `/`，并在仓库 Settings → Pages 配置自定义域名

### 启用 GitHub Pages

仓库 → Settings → Pages → Source 选择 **GitHub Actions**。

## 写作约定

- 全部使用简体中文
- 代码块标注语言（bash / json / go / text）
- 命令行示例用 `./mvn-repo-scanner`（相对路径，假设在项目根目录）
- AI 提示词放在 ```text 代码块里，方便用户一键复制
