# 界面与图标规范

SiAgent 使用现有 React + CSS 工作区结构，保留青绿色品牌和 Logo。布局以日常对话和配置效率为目标：减少重复标题与上下留白，保持清晰的文字层级、状态反馈和键盘操作。

## 系统图标

统一使用 [Phosphor Icons](https://phosphoricons.com/) 的 React 包 `@phosphor-icons/react`，MIT 许可证。业务组件通过 `web/src/icons.tsx` 的 `Icon` 使用图标；新增图标时先在该文件中增加具名导入和语义名称，再供其他组件引用。

```tsx
import { Icon } from "./icons";

<button onClick={create}>
  <Icon name="plus" size={16} />
  新建
</button>

<button aria-label="关闭" title="关闭" onClick={close}>
  <Icon name="close" />
</button>
```

规则：

- 默认尺寸 18px；紧凑按钮和行内图标 16px，外链与折叠箭头 14px，空状态 28px。
- 统一使用 `regular` 字重和 `currentColor`。默认页面星标的选中状态使用 `fill`；其他状态优先通过颜色和背景表达。
- 图标默认 `aria-hidden` 且不进入键盘焦点。纯图标操作必须在按钮或链接上提供明确的 `aria-label`，可另加 `title`。
- 新增系统图标继续从该库选择；不新增 Unicode 字符、Emoji 或手绘 SVG 充当操作图标。Logo、助手品牌标记和原生表单控件不属于系统图标组件。
- 图标按文件具名导入，避免开发时遍历整个图标库；生产资源只包含使用到的图标，无需字体文件、CDN 或外部请求。

已有名称包括 `chat`、`agent`、`models`、`knowledge`、`plugins`、`channels`、`settings`、`plus`、`close`、`star`、`trash`、`more`、`file`、`folder`、`upload`、`warning`、`history`、`logout` 与方向箭头。优先复用已有语义名称。

## 空间与视觉层级

- 桌面全局顶栏约 53px；移动端保持单行，约 57px。小屏显示 Logo 标记、模块名称、星标和退出图标，完整品牌文字在桌面展示。
- 配置标题使用一层标题和一行说明；对话标题保留当前会话名称，避免再重复模块名称。
- 工作区内容通常使用 16–28px 内边距；常用操作间隔 6–8px，字段和分组使用 12–20px 间隔。减少空白时优先调整容器，而不是持续缩小文字。
- 普通控件和导航使用 6px 圆角，资源面板 10px，输入编辑区与弹窗 12px。样式入口为 `web/src/style.css`，复用其中的颜色、边框与圆角变量。
- 对话输入区默认两行，随内容自动增高，最高 160px；继续输入时在编辑区内滚动。
- 状态信息同时保留文字，选中项、危险操作、禁用状态和键盘焦点应可区分。保留减少动态效果的系统偏好。

## 验证

先执行 `npm --prefix web run build`，再重建 Go 程序以嵌入最新资源。使用 `SIAGENT_CHROMIUM=/path/to/chromium npm --prefix web run test:browser` 验证真实服务和浏览器行为。

重点检查桌面与 320px/390px 手机宽度下的顶栏、模型表单、知识库、Skills/MCP、渠道、确认弹窗，以及对话输入、队列取消、默认页星标与键盘操作。截图由浏览器测试写入临时目录，可通过 `SIAGENT_BROWSER_ARTIFACTS` 指定位置。
