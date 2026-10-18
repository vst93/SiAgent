# 界面与图标规范

SiAgent 使用现有 React + CSS 工作区结构，保留青绿色品牌和 Logo。视觉方向是 **Workbench**：纸感背景、墨色文字、细线分层、单一强调色、等宽元数据、克制动效。目标是让界面更像一个长期使用的专业工具，而不是通用 AI 仪表盘。

## 界面语言

- 默认简体中文，支持英文；切换按钮位于桌面顶栏、移动端抽屉以及登录/首次设置页。
- 语言选择只保存在当前浏览器，不写入服务端数据。
- 新增界面文案时同步补充 `web/src/i18n-dict.ts`；接口文档在 `web/src/api-docs.tsx` 分别维护中英文内容。详见 [i18n](i18n.md)。

## 设计基础

### 颜色

样式入口为 `web/src/style.css`，优先复用其中的 CSS 变量，不新增孤立色值。

| 变量 | 用途 |
| --- | --- |
| `--bg` | 应用背景，冷调纸白 |
| `--paper` | 主内容、面板、输入背景 |
| `--surface` | 次级背景、hover、选中底色 |
| `--surface-raised` | 列表底、反馈、浮层邻近区域 |
| `--ink` / `--ink-strong` | 主文字 / 标题 |
| `--muted` / `--faint` | 次级文字 / 元数据 |
| `--teal` / `--teal-strong` / `--teal-soft` | 唯一强调色及其深浅 |
| `--line` / `--line-strong` | 常规分隔线 / 控件边框 |
| `--danger` / `--warning` | 危险 / 警告 |

规则：

- 全站只使用一个强调色，不新增第二品牌色。
- 不使用渐变文字、外发光、AI 紫、装饰性彩色状态点。
- 危险色只用于危险操作和错误，不作为装饰。
- 状态点只在表达真实服务状态时使用。

### 字体

```css
--font-sans: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI",
  "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
--font-mono: ui-monospace, "SFMono-Regular", "Cascadia Code",
  "JetBrains Mono", Menlo, Consolas, monospace;
```

- 正文 13–14px，行高 1.6–1.8。
- 页面标题 22–24px，区块标题 17–18px，正文 13–14px，元数据 10–12px。
- 数字、时间、ID、版本、计数使用 `--font-mono` 和 `font-variant-numeric: tabular-nums`。
- 不再让所有 `h1`/`h2` 使用同一字号。
- 不依赖只存在于 macOS 的字体作为唯一字体。

### 间距与圆角

- 4px 基础网格：4 / 8 / 12 / 16 / 24 / 32 / 48。
- 页面内边距：桌面 24–32px，移动 16px。
- 表单字段间距 12–16px，列表行高 36–48px。
- 配置内容最大宽度约 1080–1120px；对话正文最大宽度约 760–820px。
- 圆角规则：
  - 交互控件：`--radius-sm`（6px）；
  - 面板/卡片：`--radius-md`（10px）；
  - 弹窗/浮层：`--radius-lg`（12px）。
- 不混用规则之外的圆角；状态标签可以使用胶囊形。

### 阴影与动效

- 常规区域优先用 1px 细线和留白分层，不靠阴影。
- 浮层、菜单、弹窗使用 `--shadow-2`；普通控件使用 `--shadow-1`。
- 动效只用于 hover、展开、弹窗和状态变化，时长 120–180ms。
- 尊重 `prefers-reduced-motion`。

## 布局

- 顶栏桌面高度 56px，单行；品牌与模块切换左对齐，账户与默认页控制在右，不居中。
- 默认页只保留一个“设为默认/默认页”控件；点击 Logo 打开已配置的默认页。
- 顶栏移动端 48px，只保留品牌标记、当前页面标题和导航菜单按钮。
- 移动端把模块切换、配置栏目、默认页和退出登录收进侧滑导航，不再占用第二行；配置侧栏在移动端隐藏。
- 移动端对话标题不再单独占一行，Agent 选择、新对话、删除和历史下移到输入区上方的一行操作条。
- 配置侧栏桌面约 220px，选中项使用浅底 + 左侧 2px 强调线。
- 对话历史栏桌面约 236px，移动端为可展开面板。
- 页面只保留一层主标题；区块标题不与页面标题重复。
- 卡片只用于可选择的资源或需要明确边界的面板；不要用卡片堆满页面。
- 列表优先使用行分隔线，而不是每行一个卡片。

## 组件

### 按钮

- 类型：primary / secondary / ghost / danger。
- 高度 32–36px；移动端主要操作 38–40px。
- `:active` 允许 1px 位移；`:focus-visible` 使用 2px 强调色轮廓。
- 文字与背景对比度达到 WCAG AA。
- 主操作标签不换行。

### 表单

- label 在输入上方；帮助文字在下方；错误在字段下方。
- 不用 placeholder 代替 label。
- focus 使用强调色边框 + 3px 低透明外环。
- 移动端输入字号不小于 16px，避免 iOS 自动放大。

### 列表与状态

- 列表行使用底边线分隔，hover 和 selected 明确。
- 选中态优先使用 `--teal-soft` + 左侧强调线。
- 状态标签使用小号 mono 文字和低饱和背景。
- loading / empty / error 三种状态都必须存在，不能只做成功态。

### 弹窗

- 使用 `--radius-lg` 和 `--shadow-2`。
- 支持 Esc 关闭、遮罩关闭、焦点回到触发点。
- 危险操作必须有明确说明和二次确认。

## 系统图标

统一使用 [Phosphor Icons](https://phosphoricons.com/) 的 React 包 `@phosphor-icons/react`，MIT 许可证。业务组件通过 `web/src/icons.tsx` 的 `Icon` 使用图标；新增图标时先在该文件中增加具名导入和语义名称，再供其他组件引用。

```tsx
import { Icon } from "./icons";

<button onClick={create}>
  <Icon name="plus" size={16} />
  新建
</button>
```

规则：

- 默认尺寸 18px；紧凑按钮和行内图标 16px，外链与折叠箭头 14px，空状态 28px。
- 统一使用 `regular` 字重和 `currentColor`。默认页面星标的选中状态使用 `fill`；其他状态优先通过颜色和背景表达。
- 图标默认 `aria-hidden` 且不进入键盘焦点。纯图标操作必须在按钮或链接上提供明确的 `aria-label`，可另加 `title`。
- 新增系统图标继续从该库选择；不新增 Unicode 字符、Emoji 或手绘 SVG 充当操作图标。
- 图标按文件具名导入，避免开发时遍历整个图标库。

已有名称包括 `chat`、`agent`、`models`、`knowledge`、`plugins`、`channels`、`settings`、`plus`、`close`、`star`、`trash`、`more`、`file`、`folder`、`upload`、`warning`、`history`、`logout` 与方向箭头。优先复用已有语义名称。

## 文案

- 说明“发生了什么、影响是什么、下一步做什么”，不写营销文案。
- 避免重复解释同一件事；能在字段帮助中说明的，不再加一段页面说明。
- 不用 em-dash 作为视觉分隔；范围使用普通连字符。
- 元数据分隔符克制使用，不把所有信息串成一条长句。
- 错误提示必须可操作，不只说“失败”。

## 验证

先执行 `npm --prefix web run build`，再重建 Go 程序以嵌入最新资源。使用 `SIAGENT_CHROMIUM=/path/to/chromium npm --prefix web run test:browser` 验证真实服务和浏览器行为。

重点检查：

- 桌面与 320px/390px 手机宽度下的顶栏、配置侧栏、对话历史、模型表单、知识库、Skills/MCP、渠道、确认弹窗；
- 对话输入、队列取消、来源展开、运行诊断；
- 默认页星标与键盘操作；
- loading / empty / error / disabled / focus 状态；
- 对比度、焦点环、横向溢出；
- `prefers-reduced-motion`。

截图由浏览器测试写入临时目录，可通过 `SIAGENT_BROWSER_ARTIFACTS` 指定位置。
