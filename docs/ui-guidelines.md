# 界面与图标规范

SiAgent 使用 React + CSS 工作区结构，保留青绿色品牌和 `S·` Logo。当前视觉方向是 **Calm Precision**，由两个方向合并而来：

- **Precision Workbench** 的结构：常驻侧栏、三栏对话、1px 线分层、紧凑信息密度、键盘优先。
- **Cupertino Calm** 的克制：更少卡片和阴影、更多留白、强调色只用于选中和关键动作。

目标是让界面像一个长期使用的桌面工作台，而不是通用 AI 仪表盘。方向对比与参考项目见 [界面方向调研](ui-direction-research.md) 和 [方向对比页](ui-direction-lab.html)。

## 界面语言

- 默认简体中文，支持英文；切换按钮位于桌面侧栏底部、移动端抽屉以及登录/首次设置页。
- 语言选择只保存在当前浏览器，不写入服务端数据。
- 新增界面文案时同步补充 `web/src/i18n-dict.ts`；接口文档在 `web/src/api-docs.tsx` 分别维护中英文内容。详见 [i18n](i18n.md)。

## 布局

### 桌面

```text
┌──────────────┬──────────────────┬───────────────────────────┐
│ 应用侧栏     │ 对话记录 / 资源  │ 内容区                    │
│ 236px        │ 264px（仅对话）  │ 自适应                    │
│ 品牌 + 导航  │ 列表 + 状态      │ 标题 + 正文 + 输入        │
│ 状态 + 账户  │                  │                           │
└──────────────┴──────────────────┴───────────────────────────┘
```

- **应用侧栏**：236px，包含品牌、工作区导航、配置分组、服务状态、默认页、语言、主题和退出；可收起为 64px 图标栏，偏好保存在 `siagent.rail-collapsed`。
- **对话工作区**：对话记录 264px + 对话主区；主区正文和输入框最大宽度 760px。
- **配置工作区**：侧栏直接切换配置栏目，内容区单列，最大宽度 1120px。
- **窄窗口**：1180px 以下侧栏 220px、对话记录 240px，对话头部提供“收起对话记录”按钮；980px 以下切换为移动布局。
- **移动端**：48px 顶栏（菜单、页面标题、主题切换）+ 单栏内容；导航收进右侧抽屉；对话记录通过操作条展开。
- 页面只保留一层主标题。配置页标题在内容区顶部，对话页标题在对话头部。
- 长标题默认截断为单行或两行，并保留 `title` 提示；列表行不因名称过长而无限增高。
- 列表优先使用行分隔线和留白，不用卡片堆叠；卡片只用于可选择的资源或需要明确边界的面板。

### 桌面封装

- 后续封装为桌面应用时，侧栏顶部和对话头部可作为窗口拖拽区。
- macOS 交通灯左侧预留约 78px；Windows 窗口控制按钮右侧预留约 138px；Linux 使用客户端装饰。
- 窗口最小尺寸建议 960×640；低于 980px 使用移动布局。
- 持久化窗口尺寸、位置、侧栏宽度、主题和默认页。

## 设计基础

### 颜色

样式入口为 `web/src/style.css`。所有颜色通过 `:root` 和 `[data-theme="dark"]` 的语义变量提供，不在组件中新增孤立色值。

| 变量 | 用途 |
| --- | --- |
| `--bg` | 应用画布 |
| `--paper` | 主内容、面板、输入背景 |
| `--surface` | hover、选中底色、次级背景 |
| `--surface-raised` | 侧栏、对话记录、浮层邻近区域 |
| `--surface-sunken` | 凹陷区域 |
| `--ink` / `--ink-strong` | 主文字 / 标题 |
| `--muted` / `--faint` | 次级文字 / 元数据 |
| `--teal` / `--teal-strong` / `--teal-soft` | 唯一强调色及其深浅 |
| `--line` / `--line-strong` | 常规分隔线 / 控件边框 |
| `--danger` / `--warning` | 危险 / 警告 |
| `--danger-soft` / `--warning-soft` / `--info-soft` / `--success-soft` | 状态底色 |
| `--on-accent` | 强调色上的文字 |
| `--overlay` | 遮罩 |

规则：

- 全站只使用一个强调色，不新增第二品牌色。
- 青绿色只用于选中态、焦点、关键动作和真实状态，不作为大面积装饰。
- 深色模式通过 `<html data-theme="dark">` 切换，偏好保存在 `siagent.theme`。
- 不使用渐变文字、外发光、AI 紫、装饰性彩色状态点。
- 危险色只用于危险操作和错误，不作为装饰。
- 状态点只在表达真实服务状态时使用。

### 字体

```css
--font-sans: "Geist Variable", ui-sans-serif, -apple-system, BlinkMacSystemFont,
  "Segoe UI Variable", "Segoe UI", "PingFang SC", "Hiragino Sans GB",
  "Microsoft YaHei", "Noto Sans SC", sans-serif;
--font-mono: "Geist Mono Variable", ui-monospace, "SFMono-Regular",
  "Cascadia Code", "JetBrains Mono", Menlo, Consolas, monospace;
```

- 拉丁字符和数字使用自托管 Geist，中文回退到系统字体，避免额外加载大体积中文字库。
- 正文 14px / 22px；密集列表 13px / 20px；元数据 11px / 16px；页面标题 22px / 30px；区块标题 17px / 24px。
- 中文正文行高不低于 1.6；长文对话可用 1.7。
- 数字、时间、ID、版本、计数使用 `--font-mono` 和 `font-variant-numeric: tabular-nums`。
- 全大写 + 宽字距的小标签只用于真正的元数据分组，每 3 个区块最多 1 个。
- 不依赖只存在于 macOS 的字体作为唯一字体。

### 间距与圆角

- 4px 基础网格：4 / 8 / 12 / 16 / 24 / 32 / 48。
- 页面内边距：桌面 24–32px，移动 16px。
- 表单字段间距 16–20px，列表行高 32–36px。
- 配置内容最大宽度 1120px；对话正文和输入框最大宽度 760px。
- 圆角规则：
  - 交互控件：`--radius-sm`（6px）；
  - 面板/卡片：`--radius-md`（8px）；
  - 弹窗/浮层：`--radius-lg`（12px）。
- 状态标签可以使用胶囊形，其余控件不混用规则之外的圆角。

### 阴影与动效

- 常规区域用 1px 细线和留白分层，不靠阴影。
- 浮层、菜单、弹窗使用 `--shadow-2`；普通控件使用 `--shadow-1` 或不使用阴影。
- 动效只用于 hover、展开、弹窗和状态变化，时长 120–180ms。
- 尊重 `prefers-reduced-motion`。
- 产品 UI 不使用大面积 spring、视差或滚动动画。

### 毛玻璃

- 只用于有内容透出的浮层表面：命令面板、移动抽屉、行菜单、浮动“回到最新”、吸顶表头、对话框。
- 使用 `--glass` / `--glass-strong` / `--glass-border` / `--glass-highlight`，配合 `backdrop-filter: blur(12–24px) saturate(1.1–1.2)`。
- 不支持 `backdrop-filter` 或 `prefers-reduced-transparency: reduce` 时回退到纯色 `--paper`，不能牺牲可读性。
- 不用于聊天正文、表单背景、大面积内容区或静态侧栏；毛玻璃是功能层的提示，不是装饰。

## 组件

### 按钮

- 类型：primary / secondary / ghost / danger。
- 高度 30–32px；移动端主要操作 38–40px。
- `:active` 允许 1px 位移；`:focus-visible` 使用 2px 强调色轮廓。
- 文字与背景对比度达到 WCAG AA；`--on-accent` 用于强调色按钮文字。
- 主操作标签不换行。

### 表单

- label 在输入上方；帮助文字在下方；错误在字段下方。
- 不用 placeholder 代替 label。
- focus 使用强调色边框 + `--focus-ring`。
- 移动端输入字号不小于 16px，避免 iOS 自动放大。
- 复选框、单选框和开关在深色模式下保持可见边界。

### 列表与状态

- 列表行使用底边线或 hover 背景分隔，选中态使用中性 `--surface` 底 + `--line` 细描边，强调色只用于图标或关键文字，不使用左侧色条。
- 对话记录行高 32px；Agent 列表行高约 44px。
- Agent 卡片的 hover 必须覆盖整项；内部按钮不单独铺底色，避免只亮名字区域。
- 长标题使用 `flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap`；需要两行时使用 `-webkit-line-clamp: 2`，并保留 `title`。
- 对话标题的 flex 子项必须允许收缩，否则长单词或长中文串会顶出历史栏或头部边框。
- 状态标签使用小号 mono 文字和低饱和背景。
- loading / empty / error 三种状态都必须存在，不能只做成功态。
- 空状态包含一句说明、一个主操作或少量建议，不使用大 Logo + eyebrow + 两行说明的模板。

### 命令面板

- `⌘K` / `Ctrl K` 打开，或点击侧栏“搜索或跳转”和移动端搜索图标。
- 支持按页面名称、操作和分组过滤；上下键移动，Enter 执行，Esc 关闭。
- 结果优先匹配页面标题，其次匹配分组；当前项保持在可视区域内。
- 动作包括跳转页面、新对话、设置默认页、切换主题和退出登录。

### 代码块与长对话

- Markdown 代码块右上角提供“复制”按钮，触屏设备常显，桌面端 hover 或键盘聚焦时显示。
- 长对话滚动离开底部后显示“回到最新”，点击平滑回到最新消息；减少动态效果时改为立即跳转。
- 对话记录在 981–1180px 可收起，偏好保存在 `siagent.history-collapsed`。

### 标签页

- 配置页内标签使用底部 2px 强调线，高度 34–36px。
- 页面级标签与编辑器标签共用同一套几何，不另造样式。

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

- 默认尺寸 16px；导航和紧凑按钮 16px，外链与折叠箭头 14px，空状态 28px。
- 统一使用 `regular` 字重和 `currentColor`。默认页面星标的选中状态使用 `fill`；其他状态优先通过颜色和背景表达。
- 图标默认 `aria-hidden` 且不进入键盘焦点。纯图标操作必须在按钮或链接上提供明确的 `aria-label`，可另加 `title`。
- 新增系统图标继续从该库选择；不新增 Unicode 字符、Emoji 或手绘 SVG 充当操作图标。
- 图标按文件具名导入，避免开发时遍历整个图标库。

已有名称包括 `chat`、`agent`、`models`、`knowledge`、`plugins`、`channels`、`settings`、`plus`、`close`、`star`、`trash`、`more`、`menu`、`search`、`sidebar`、`key`、`package`、`file`、`folder`、`upload`、`warning`、`history`、`logout`、`moon`、`sun`、`arrow-down` 与方向箭头。优先复用已有语义名称。

## 文案

- 说明“发生了什么、影响是什么、下一步做什么”，不写营销文案。
- 避免重复解释同一件事；能在字段帮助中说明的，不再加一段页面说明。
- 不用 em-dash 作为视觉分隔；范围使用普通连字符。
- 元数据分隔符克制使用，不把所有信息串成一条长句。
- 错误提示必须可操作，不只说“失败”。

## 验证

先执行 `npm --prefix web run build`，再重建 Go 程序以嵌入最新资源。使用 `SIAGENT_CHROMIUM=/path/to/chromium npm --prefix web run test:browser` 验证真实服务和浏览器行为。

重点检查：

- 1440×900、1280×800、1000×700 三种桌面窗口；
- 980px 断点前后的侧栏、对话记录和配置内容；
- 320px / 390px 手机宽度下的顶栏、抽屉、配置表单、对话输入和来源展开；
- 浅色 / 深色模式下的对比度、焦点环、边框、状态底色；
- 默认页星标、主题切换、语言切换、对话记录收起状态的持久化；
- `⌘K` / `Ctrl K` 命令面板、代码复制、回到最新；
- loading / empty / error / disabled / focus 状态；
- 横向溢出、`prefers-reduced-motion`。

截图由浏览器测试写入临时目录，可通过 `SIAGENT_BROWSER_ARTIFACTS` 指定位置。
