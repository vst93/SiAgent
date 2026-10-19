# SiAgent 界面方向调研

> 状态：已确认采用 B + A（Calm Precision），第一轮实现已完成。  
> 范围：视觉方向、桌面排版、参考项目、桌面封装注意事项。  
> 配套可视化对比：[`docs/ui-direction-lab.html`](ui-direction-lab.html)（直接用浏览器打开）。

## 0. 结论

当前界面最主要的问题不是“不好看”，而是**视觉语言仍然像通用 AI SaaS**：白卡片 + 浅灰底 + 青绿强调色 + 顶部模块切换。信息架构是清楚的，但缺少桌面工具应有的结构感、密度和排版秩序。

建议：

1. **以 B「Precision Workbench」作为结构基础**：常驻侧栏、三栏工作区、1px 线分层、紧凑但清晰的字号、状态栏、⌘K。
2. **吸收 A「Cupertino Calm」的克制**：减少卡片、减少阴影、更多留白、更少的强调色面积。
3. **吸收 C「Editorial Studio」的排版意识**：页面标题与正文拉开层级，元数据用等宽，对话像文档一样可读。
4. 如果只能选一个方向：**B**。如果品牌气质优先于工具效率：**A 或 C**。如果 Windows 企业用户绝对优先：**D**。

一句话概括目标气质：

> 安静、精确、有秩序感的桌面工作台；不是“AI 产品”，而是一件长期使用的专业工具。

---

## 1. 现状判断

### 1.1 已有优点

- 信息架构清楚：Agent 对话 / 配置双工作区，配置侧栏分组合理。
- 关键流程完整：模型、Agent、知识库、Skills/MCP、渠道、维护。
- 已有键盘焦点、`aria-*`、减少动态效果、移动端适配。
- 图标统一使用 Phosphor。
- 有浏览器测试覆盖主要流程和 320px / 390px 布局。

### 1.2 主要问题

**结构**

- 顶栏 56px 同时承载：品牌、模块切换、两个默认页星标、默认页文字、退出登录。信息密度过高，且“默认：Agent 对话”属于实现细节，不应常驻。
- 对话工作区没有稳定的桌面历史栏，切换会话不够直接。
- 配置侧栏 220px，内容区最大宽度约 1080px，但页面内又有多层标题和卡片，形成“标题墙”。
- 移动端和桌面端共用一套顶部导航，导致桌面端不像桌面应用，移动端又偏拥挤。

**视觉**

- 白卡片 + 浅灰底 + 青绿强调的组合，与大量 AI SaaS 高度同质化。
- 卡片、边框、浅色底、圆角重复出现，层级靠容器堆叠，而不是靠排版和留白。
- `h1` 24px、配置页 `h1` 20px、`h2` 17px，层级不够稳定。
- 青绿 `#146c5f` 偏深、使用面积偏大，容易显得“企业后台”。
- 阴影少但存在，颜色与背景不完全匹配。
- 状态点、标签、eyebrow 使用偏多，容易装饰化。

**排版**

- 字体栈依赖系统字体，macOS / Windows / Linux 观感不一致。
- 数字、时间、ID、版本、计数没有统一使用等宽字体和 tabular nums。
- 正文 14px / 1.6，按钮 12.5px，元数据 10–12px，尺度略碎。
- 中文正文行高和段落间距没有统一节奏。

---

## 2. 参考项目

> 以下项目主要用于**视觉与交互参考**，不是代码复用清单。复用代码或素材前需单独确认许可证。

### 2.1 AI / Agent 客户端（最接近 SiAgent）

| 项目 | 技术栈 | 值得参考的部分 | 链接 |
| --- | --- | --- | --- |
| Cherry Studio | Electron + React | 桌面 AI 工作台的整体壳、模型/助手切换、设置页组织、深浅色完成度 | [GitHub](https://github.com/CherryHQ/cherry-studio) · [官网](https://cherry-ai.com) |
| Chatbox | Electron + React | 简洁的会话列表、输入区、Local-first 的设置层级 | [GitHub](https://github.com/chatboxai/chatbox) · [官网](https://chatboxai.app) |
| Jan | Tauri + React | 安静的空白状态、模型管理、离线优先的叙事 | [GitHub](https://github.com/menloresearch/jan) |
| LobeChat | Next.js + React | 成熟的 Agent / 插件 / 设置信息架构，命令面板与大量细节 | [GitHub](https://github.com/lobehub/lobe-chat) · [官网](https://lobehub.com) |
| AnythingLLM | Electron + React | 工作区（Workspace）+ 文档 + RAG 的管理方式 | [GitHub](https://github.com/Mintplex-Labs/anything-llm) · [官网](https://anythingllm.com) |
| DeepChat | Electron + React | 干净的对话界面、工具调用与模型切换 | [GitHub](https://github.com/ThinkInAIXYZ/deepchat) |
| Open WebUI | Svelte | 管理后台的功能完整度；视觉偏工具化，不建议照搬 | [GitHub](https://github.com/open-webui/open-webui) |
| LibreChat | React | 多模型、多提供商配置的完整度 | [GitHub](https://github.com/danny-avila/LibreChat) |

**重点看**：Cherry Studio 的桌面壳和设置页、Chatbox 的输入区、Jan 的空白状态、LobeChat 的信息架构。

### 2.2 桌面生产力 / 工具（结构参考）

| 项目 | 技术栈 | 值得参考的部分 | 链接 |
| --- | --- | --- | --- |
| Spacedrive | Tauri + React | 三栏布局、右侧 Inspector、命令面板、桌面级细节 | [GitHub](https://github.com/spacedriveapp/spacedrive) · [官网](https://spacedrive.com) |
| Zed | Rust + GPUI | 极简工具栏、13px 高频信息、状态栏、键盘优先 | [GitHub](https://github.com/zed-industries/zed) · [官网](https://zed.dev) |
| Zen Browser | Firefox fork | 垂直侧栏、减少浏览器 chrome、安静的工作区 | [GitHub](https://github.com/zen-browser/desktop) · [官网](https://zen-browser.app) |
| AFFiNE | React + Electron | 文档 / 白板 / 数据库融合的桌面工作区 | [GitHub](https://github.com/toeverything/AFFiNE) · [官网](https://affine.pro) |
| AppFlowy | Flutter + Rust | Notion 式侧栏、页面树、桌面设置 | [GitHub](https://github.com/AppFlowy-IO/AppFlowy) |
| Twenty | React + NestJS | 设置页、数据表格、空状态、企业产品的克制感 | [GitHub](https://github.com/twentyhq/twenty) · [官网](https://twenty.com) |
| Maybe | Rails + React | 编辑式排版、留白、财务产品的安静气质（项目已归档，仅作参考） | [GitHub](https://github.com/maybe-finance/maybe) |
| Hoppscotch | Vue | 开发工具的密集表单、环境切换、请求/响应分栏 | [GitHub](https://github.com/hoppscotch/hoppscotch) · [官网](https://hoppscotch.io) |

**闭源但值得体验**：

- [Linear](https://linear.app/)：键盘优先、命令面板、深色模式的层次控制。
- [Raycast](https://www.raycast.com/)：命令面板、紧凑列表、macOS 原生感。
- [Things 3](https://culturedcode.com/things/)：侧栏、工具栏、留白的教科书。
- [Notion Calendar](https://www.notion.com/product/calendar)：统一工具栏 + 侧栏 + 内容区。
- [Warp](https://www.warp.dev/)：终端类产品的状态栏、块状输出、命令补全。
- [Craft](https://www.craft.do/)：文档型产品的排版与侧栏。

### 2.3 设计系统与组件基础

| 资源 | 用途 | 链接 |
| --- | --- | --- |
| Apple HIG | macOS 窗口、侧栏、工具栏、字体、材质 | [HIG](https://developer.apple.com/design/human-interface-guidelines/) |
| Fluent 2 | Windows 11 / Mica / Segoe UI Variable / 类型阶梯 | [Fluent 2](https://fluent2.microsoft.design/) |
| GNOME HIG | Linux 桌面、Header Bar、Boxed List、Adwaita | [GNOME HIG](https://developer.gnome.org/hig/) |
| shadcn/ui | 可拥有代码的 React 组件基础（Radix + Tailwind） | [ui.shadcn.com](https://ui.shadcn.com/) |
| Radix Themes | 开箱可用的可访问主题 | [radix-ui.com/themes](https://www.radix-ui.com/themes) |
| Base UI | 无样式、可访问的 React primitives（Radix/MUI 团队） | [base-ui.com](https://base-ui.com/) |
| Origin UI | 大量可复制的表单 / 布局模式 | [originui.com](https://originui.com/) |
| Carbon | IBM 企业级密度与数据表格 | [carbondesignsystem.com](https://carbondesignsystem.com/) |
| Primer | GitHub 式开发工具视觉 | [primer.style](https://primer.style/) |
| Polaris | Shopify 式设置页、表单、空状态 | [polaris.shopify.com](https://polaris.shopify.com/) |
| Lobe UI | LobeChat 的设计系统，可参考组件细节 | [GitHub](https://github.com/lobehub/lobe-ui) |
| Untitled UI | Figma UI Kit，适合快速比对布局 | [untitledui.com](https://www.untitledui.com/) |

**建议**：不引入一套完整组件库来“换皮”。保留现有 React + 手写 CSS，把设计 token、布局壳、基础控件重做；需要复杂交互时再引入 Radix primitives。这样不会被组件库的默认观感绑架。

### 2.4 灵感与截图库

| 站点 | 适合看什么 | 链接 |
| --- | --- | --- |
| Refero | 真实产品截图，可按 Web / Desktop / 设置页筛选；**最适合本项目** | [refero.design](https://refero.design/) |
| Mobbin | 真实 App / Web 流程与屏幕 | [mobbin.com](https://mobbin.com/) |
| SaaS Frame | 真实 SaaS 页面与产品截图 | [saasframe.io](https://www.saasframe.io/) |
| UI Sources | 真实产品交互流程 | [uisources.com](https://www.uisources.com/) |
| Nicely Done | 精选产品界面 | [nicelydone.club](https://nicelydone.club/) |
| Minimal Gallery | 极简网页参考 | [minimal.gallery](https://minimal.gallery/) |
| Godly | 高概念网页；只取细节，不取结构 | [godly.website](https://godly.website/) |
| Land-book | 落地页参考；与产品 UI 关系不大 | [land-book.com](https://land-book.com/) |
| Siteinspire | 网页设计索引 | [siteinspire.com](https://www.siteinspire.com/) |
| Typewolf | 字体搭配与字体气质 | [typewolf.com](https://www.typewolf.com/) |
| Fonts In Use | 真实字体应用案例 | [fontsinuse.com](https://fontsinuse.com/) |
| Page Flows | 产品操作流程（部分付费） | [pageflows.com](https://pageflows.com/) |

使用建议：先看 **Refero** 的 Desktop / Settings / AI 分类，再看 **Mobbin** 的桌面产品流程。不要从 Dribbble 的“概念稿”反推结构。

---

## 3. 桌面排版规范

### 3.1 平台默认值

| 平台 | 正文字号 / 行高 | 最小字号 | 标题参考 | 说明 |
| --- | --- | --- | --- | --- |
| macOS | 13pt / 16pt | 10pt | Title 1 22/26，Title 2 17/22，Title 3 15/20 | Apple HIG 的 macOS Body 是 13pt；桌面工具默认偏紧 |
| Windows 11 / Fluent 2 | 14px / 20px | 12px | Subtitle 20/28，Title 28/36 | Segoe UI Variable；正文 14px，标题用 Semibold |
| Web 桌面应用（推荐） | 13–14px / 20–22px | 11px | 页面 22/30，区块 17/24 | 中文界面建议 14px 起，密集工具可用 13px |
| GNOME | 使用 Adwaita Sans（Inter 变体） | — | 使用标准 `body` / `heading` / `caption` | 不建议硬编码字号，使用相对值 |

**结论**：SiAgent 的正文建议 **14px / 22px**，密集列表和元数据用 **13px / 20px**、**11px / 16px**。不要把所有正文压到 12px，也不要全站 16px。

### 3.2 类型阶梯（建议）

| 角色 | 字号 / 行高 | 字重 | 用途 |
| --- | --- | --- | --- |
| Display | 28 / 36 | 650 | 空状态、欢迎页，少量使用 |
| Page title | 22 / 30 | 650 | 每个页面只出现一次 |
| Section title | 17 / 24 | 600 | 配置分组、面板标题 |
| Body | 14 / 22 | 400 | 中文正文、表单、对话 |
| Body dense | 13 / 20 | 400 | 列表、表格、侧栏 |
| Secondary | 12 / 18 | 400 | 帮助文字、说明 |
| Meta | 11 / 16 | 500 mono | 时间、ID、版本、计数 |
| Micro | 10 / 14 | 500 mono | 极少量标签，避免全大写 |

规则：

- 页面标题和区块标题不要同字号。
- 中文正文行高不低于 1.6；长文对话可用 1.7。
- 数字、时间、ID、版本、计数统一 `font-variant-numeric: tabular-nums`。
- 等宽字体只用于元数据、代码、ID，不用于正文。
- 全大写 + 宽字距的小标签（eyebrow）每 3 个区块最多 1 个。

### 3.3 中文 / 拉丁字体搭配

**拉丁无衬线**：

- Geist（OFL，现代、紧凑，适合工具）
- Inter（OFL，中性、可读，但已被大量 AI 产品使用）
- IBM Plex Sans（OFL，工程感）
- SF Pro（仅 Apple 平台，不要打包分发）
- Segoe UI Variable（Windows 系统字体，不要打包分发）

**中文无衬线**：

- PingFang SC（macOS 系统字体，不打包）
- MiSans（小米，免费商用，需确认最新许可）
- HarmonyOS Sans SC（华为，免费商用，需确认最新许可）
- Noto Sans SC / 思源黑体（OFL，最稳妥的分发选择）
- 阿里巴巴普惠体 3.0（免费商用）

**等宽**：

- Geist Mono（OFL）
- JetBrains Mono（OFL）
- IBM Plex Mono（OFL）
- Maple Mono（OFL，含中文，适合 ID / 日志）
- Sarasa Gothic（OFL，中文等宽）

**建议字体栈**（不打包时）：

```css
--font-ui:
  -apple-system, BlinkMacSystemFont, "Segoe UI Variable", "Segoe UI",
  "PingFang SC", "MiSans", "HarmonyOS Sans SC", "Noto Sans SC",
  "Microsoft YaHei", sans-serif;
--font-mono:
  ui-monospace, "SFMono-Regular", "Cascadia Code", "JetBrains Mono",
  "Maple Mono", Menlo, Consolas, monospace;
```

如果希望 Windows / Linux / macOS 观感一致，建议打包 **Geist**（拉丁）+ **Noto Sans SC**（中文，按 unicode-range 子集化）。中文全量字体体积较大，桌面应用可接受，Web 部署需要子集化或按需加载。

### 3.4 间距、行高、宽度

- 4px 基础网格：4 / 8 / 12 / 16 / 24 / 32 / 48。
- 页面内边距：桌面 24–32px；窗口窄于 1100px 时 20–24px。
- 表单字段间距：16–20px；label 与 input 8px；帮助文字 6px。
- 列表行高：紧凑 28px，默认 32–36px，舒适 40–44px。
- 控件高度：28 / 30 / 32 / 36px；桌面默认 30–32px。
- 对话正文最大宽度：68–76 个字符（中文约 32–38 字），建议 720–780px。
- 配置内容最大宽度：960–1080px；单列表单 560–680px。
- 侧栏宽度：导航 220–260px；会话列表 260–300px；Inspector 280–320px。
- 状态栏高度：24–28px。

### 3.5 圆角、阴影、动效

**圆角只选一套规则**，例如：

- 4 / 6 / 8：控件 4，面板 6，浮层 8（更工具化）。
- 6 / 8 / 12：控件 6，面板 8，浮层 12（更柔和）。

不要在同一页面混用 10 / 12 / 16 而没有规则。

**阴影**：

- 常规区域用 1px 细线 + 留白分层，不用阴影。
- 浮层 / 菜单 / 弹窗：`0 8px 24px rgba(20, 24, 30, .08)`，阴影带背景色调。
- 不要纯黑阴影，不要外发光。

**动效**：

- 120–180ms，`cubic-bezier(.2,.7,.3,1)`。
- 只用于 hover、展开、弹窗、状态变化。
- 尊重 `prefers-reduced-motion`。
- 产品 UI 不建议大面积 spring / 视差 / 滚动动画。

### 3.6 桌面布局模式

**推荐结构：三栏**

```text
┌─────────────────────────────────────────────────────────┐
│ 标题栏 / 统一工具栏   ● ● ●    SiAgent      ⌘K   状态   │
├──────────┬──────────────────┬───────────────────────────┤
│ 导航栏   │ 会话 / 资源列表   │ 内容区                    │
│ 56–64px  │ 240–300px        │ 自适应，正文最大 780px    │
│ 或 220px │                  │                           │
└──────────┴──────────────────┴───────────────────────────┘
```

- **对话**：导航 + 会话列表 + 对话。
- **配置**：导航 + 配置栏目 + 内容（或导航直接展开配置栏目，两栏）。
- **可选 Inspector**：右侧元数据 / 来源 / 工具调用详情，按需展开。
- **状态栏**：运行时、模型、上下文、版本、同步状态。适合技术工具，但不要堆成信息条。

**桌面细节**：

- 统一工具栏 44–52px，窗口控制按钮与内容留出安全区。
- 面板可拖拽调整宽度，宽度持久化。
- `⌘K` / `Ctrl+K` 命令面板：跳转页面、切换 Agent、新建会话、搜索知识库。
- 键盘快捷键：新建会话、切换侧栏、聚焦输入、发送、停止。
- 空状态、加载骨架、错误态、禁用态必须完整。
- 默认浅色，深色作为一等模式，从第一天就设计。
- 窄窗口（< 900px）侧栏变抽屉；< 720px 回到单栏移动布局。

---

## 4. 四个方向

可视化对比见 [`ui-direction-lab.html`](ui-direction-lab.html)。以下是决策摘要。

### A. Cupertino Calm（苹果式）

- **关键词**：安静、留白、半透明侧栏、统一工具栏、软层级。
- **参考**：Apple HIG、Things 3、Raycast、Notion Calendar。
- **字体**：13/16 正文，15/20 区块，22/26 页面标题。
- **形状**：10–12px 面板，8px 控件，软阴影。
- **适合**：对话为主、macOS 用户多、希望显得高级且不吵。
- **风险**：配置页偏松；半透明在 Windows/Linux 上容易失真；容易变成“仿 macOS”。
- **保留青绿的方式**：降低饱和度，只用于选中态、焦点和关键动作。

### B. Precision Workbench（专业工具）

- **关键词**：高密度、键盘优先、1px 线、几乎无阴影、状态可见。
- **参考**：Linear、Raycast、Zed、Spacedrive、GitHub Primer、Cherry Studio。
- **字体**：13/20 正文，11/16 元数据，20/26 页面标题。
- **形状**：6–8px，1px 边框，深色模式完成度高。
- **适合**：技术管理员、配置项多、会话长、需要效率和可信度。
- **风险**：容易做得冷、硬、像 IDE；浅色模式必须同样精致；对对齐和间距要求最高。
- **保留青绿的方式**：用更亮的 mint-teal 作为唯一交互色，深色底上尤其有效。

### C. Editorial Studio（编辑式）

- **关键词**：纸感、墨色、衬线标题、等宽元数据、文档感。
- **参考**：Maybe、Stripe Press、Readwise Reader、Craft、iA Writer、Are.na。
- **字体**：14/24 正文，衬线 22–30 标题，mono 9–10 元数据。
- **形状**：3–4px，hairline，几乎无阴影。
- **适合**：知识库、长文对话、个人 / 小团队；希望产品有作者气质。
- **风险**：配置页容易像文档；中文衬线搭配需要谨慎；文案质量要求高。
- **保留青绿的方式**：深绿作为“墨水色”，用于标题、链接和选中态。

### D. Fluent Native（Windows 原生）

- **关键词**：Mica 分层、8px 圆角、14px 正文、设置卡片。
- **参考**：Fluent 2、Windows 11 Settings、PowerToys、Files。
- **字体**：14/20 正文，12/16 辅助，28/36 页面标题。
- **形状**：8px 面板，4px 控件，轻阴影。
- **适合**：Windows 用户为主、企业采购、需要“正式软件”的观感。
- **风险**：最容易回到“普通 SaaS”；macOS 上不协调；品牌辨识度最弱。
- **保留青绿的方式**：作为系统强调色，使用面积可以比现在更大。

### 方向对比

| 维度 | A Cupertino | B Precision | C Editorial | D Fluent |
| --- | --- | --- | --- | --- |
| 气质 | 高级、安静 | 专业、高效 | 有作者气质 | 正式、可靠 |
| 信息密度 | 低–中 | 高 | 中 | 中 |
| 配置页适配 | 中 | 高 | 中–低 | 高 |
| 对话阅读感 | 高 | 中–高 | 高 | 中 |
| 桌面原生感 | 高（macOS） | 高（跨平台） | 中 | 高（Windows） |
| 品牌辨识度 | 中 | 中–高 | 高 | 低 |
| 实现风险 | 中 | 中–高 | 高 | 低 |
| 推荐度 | 备选 | **首选** | 品牌优先时 | Windows 优先时 |

---

## 5. 推荐方案：Calm Precision

**B 的结构 + A 的克制 + C 的排版意识。**

具体建议：

- **布局**：桌面三栏。左侧 56–64px 图标导航或 220–240px 文字导航；中间会话列表 260–280px；右侧内容。
- **顶栏**：44–48px 统一工具栏，整合窗口控制、当前页面、搜索 / ⌘K、账户。移除顶部模块切换和默认页星号。
- **字号**：正文 14/22；列表 13/20；元数据 11/16 mono；页面标题 22/30；区块 17/24。
- **颜色**：中性石墨 + 纸白；青绿只用于选中、焦点、关键动作和真实状态。默认浅色，深色完整。
- **形状**：控件 6px，面板 8px，浮层 12px；常规区域用 1px 线，不用阴影。
- **密度**：默认 32–36px 行高；提供“紧凑 / 舒适”切换（可选）。
- **对话**：用户消息右侧浅底，助手消息无气泡或极浅底；工具调用和来源折叠为一行元数据，可展开。
- **配置**：去掉多层标题墙；页面标题只出现一次；分组用细线和留白，不用卡片堆叠。
- **空状态**：一句说明 + 一个主操作 + 一个示例，不用大 Logo + eyebrow + 两行说明的模板。
- **品牌**：保留 `S·` 标记和青绿，但把青绿从“大面积底色”改为“精确的点缀”。

这套方案的好处是：既能满足桌面应用的结构感，又不会把产品做成 IDE；既有品牌辨识度，又不会牺牲配置页的效率。

---

## 6. 桌面封装注意事项

### 6.1 技术路线

| 方案 | 优点 | 代价 | 适合 |
| --- | --- | --- | --- |
| **Wails** | Go 原生、体积小、与现有 Go 后端最贴近 | 需要适配现有 HTTP / 静态资源结构；生态比 Electron 小 | 希望长期用 Go 维护 |
| **Tauri 2** | 安装包小、WebView 复用系统、自动更新成熟、可 sidecar 启动 Go | Rust 工具链；跨平台 WebView 差异 | 想要现代桌面壳 |
| **Electron** | 最成熟、行为最可预测、调试方便 | 包体大、内存高 | 快速交付、团队熟悉前端 |

建议：**先让 Web UI 在浏览器里完成桌面级布局和键盘交互，再选壳**。不要把设计耦合到 Wails / Tauri / Electron 的 API 上。封装时只需要一层薄适配：窗口控制、标题栏拖拽区、窗口状态持久化、更新检查。

### 6.2 标题栏与平台差异

- **macOS**：`titleBarStyle: hiddenInset`（Electron）或 Tauri 的 `titleBarStyle: Overlay`；交通灯左侧预留约 78px；可选的 vibrancy 只用于侧栏，不用于内容区。
- **Windows**：`titleBarOverlay` 或自绘标题栏；窗口控制按钮右侧预留约 138px；Mica 只用于窗口背景，不用于卡片。
- **Linux**：优先使用客户端装饰 / Header Bar；不要假设有交通灯或 Windows 控制按钮。
- **拖拽区**：`-webkit-app-region: drag`，交互元素设置 `no-drag`；双击标题栏最大化。
- **窗口状态**：持久化尺寸、位置、最大化状态、侧栏宽度、主题、密度。

### 6.3 桌面级细节清单

- 窗口最小尺寸（例如 960×640），低于此尺寸切换为紧凑布局。
- 记住上次打开的页面、会话、滚动位置。
- `⌘K` / `Ctrl+K` 命令面板。
- `⌘N` 新建会话，`⌘,` 打开设置，`Esc` 关闭浮层。
- 系统菜单 / 托盘（可选）：显示主窗口、检查更新、退出。
- 更新提示不要打断对话：状态栏或设置页内展示。
- 离线 / 运行时异常要有明确状态，不要只显示“失败”。
- 首次启动的引导应像桌面应用的 welcome window，而不是网页弹窗。

---

## 7. 下一步

1. **选方向**：A / B / C / D，或明确“B + A 的混合”。
2. **确认 5 个约束**：
   - 默认浅色还是深色？
   - 青绿保留为深绿，还是改成更亮的 mint？
   - 侧栏用 56px 图标栏 + 列表，还是 220–240px 文字栏？
   - 正文 13px 紧凑，还是 14px 舒适？
   - 字体只用系统栈，还是打包 Geist + Noto Sans SC？
3. **只做两个页面的可点击原型**：对话三栏 + Agent 配置。不改后端，不一次性重写全部页面。
4. **在 1280×800、1440×900、1024×768 三种窗口下检查**，同时检查浅色 / 深色、320 / 390px 移动端。
5. **先迁移设计 token 和布局壳，再逐页替换**。保留路由、测试选择器和现有业务流程，避免“重构导致功能回退”。

---

## 8. 选择清单

请按优先级回答：

1. 你希望用户第一眼觉得 SiAgent 是：
   - A 高级、安静、像 macOS 应用
   - B 专业、高效、像 Linear / Raycast
   - C 有作者气质、像一份编辑中的文档
   - D 正式、可靠、像 Windows 系统应用
2. 默认主题：浅色 / 深色 / 跟随系统？
3. 品牌色：保留深青绿 / 改成更亮的 mint / 中性色 + 少量青绿？
4. 侧栏结构：图标栏 + 会话列表 / 单列文字侧栏 / 可折叠双栏？
5. 正文密度：13px 紧凑 / 14px 舒适 / 可切换？
6. 字体：系统栈 / 打包 Geist + Noto Sans SC？
7. 配置页：单列 680px / 双列 1080px？
8. 桌面壳：Wails / Tauri / Electron / 暂不封装？

确认后，下一步进入**视觉原型**，而不是直接改全站。
