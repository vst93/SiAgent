# 外置系统配置

SiAgent 的系统级配置可以放在操作系统用户 config 目录中，使用 JSON。业务数据（模型、Agent、知识库、渠道、密钥）仍由应用内管理页保存到数据目录的 SQLite；外置文件只管理系统启动参数和少量运行默认值。

## 路径

默认文件：

| 系统 | 路径 |
| --- | --- |
| Linux | `${XDG_CONFIG_HOME:-$HOME/.config}/SiAgent/config.json` |
| macOS | `~/Library/Application Support/SiAgent/config.json` |
| Windows / WSL | `%AppData%\SiAgent\config.json`（WSL 使用 Linux 路径） |

可用 `SIAGENT_CONFIG=/absolute/path/config.json` 指定其他文件。默认文件不存在时按“无外置配置”启动；显式指定 `SIAGENT_CONFIG` 但文件不存在时启动失败，避免误以为配置已生效。

建议文件权限 `0600`，目录 `0700`。配置只在进程启动时读取，修改后重启服务生效。服务安装时如果配置文件存在，systemd/launchd 单元会保存 `SIAGENT_CONFIG`；使用自定义路径后重新执行 `siagent service install`。

## JSON 格式

```json
{
  "data_dir": "/srv/siagent/data",
  "listen": "127.0.0.1:8080",
  "public_url": "https://agent.example.com",
  "base_path": "",
  "runtime": "/opt/siagent/runtime/dist/index.js",
  "node": "/opt/siagent/node/bin/node",
  "channel_worker": "/opt/siagent/runtime/dist/channel-wecom.js",
  "attachments": {
    "max_file_bytes": 10485760,
    "max_image_bytes": 4194304,
    "max_message_image_bytes": 5242880,
    "max_extracted_runes": 300000,
    "max_message_count": 8,
    "max_message_bytes": 41943040,
    "max_conversation_count": 40,
    "max_conversation_bytes": 104857600,
    "orphan_ttl_hours": 24
  },
  "knowledge": {
    "max_file_bytes": 20971520,
    "max_characters": 1000000,
    "max_chunks": 5000,
    "max_pdf_pages": 1000,
    "allowed_extensions": ["txt", "markdown", "pdf", "docx"]
  },
  "limits": {
    "concurrent": 2,
    "conversation_queue": 10,
    "global_queue": 100,
    "run_timeout_seconds": 300,
    "tool_timeout_seconds": 60,
    "tool_rounds": 10
  },
  "advanced_tools_disabled": false
}
```

所有字段都可省略。未知字段、JSON 语法错误或超出范围的 `limits` 会阻止启动，不会静默忽略。

### 启动参数

| JSON 字段 | 对应环境变量 | 对应命令行 |
| --- | --- | --- |
| `data_dir` | `SIAGENT_DATA_DIR` | `-data-dir` |
| `listen` | `SIAGENT_LISTEN` | `-listen` |
| `public_url` | `SIAGENT_PUBLIC_URL` | `-public-url` |
| `base_path` | `SIAGENT_BASE_PATH` | `-base-path` |
| `runtime` | `SIAGENT_RUNTIME` | `-runtime` |
| `node` | `SIAGENT_NODE` | `-node` |
| `channel_worker` | `SIAGENT_CHANNEL_WORKER` | `-channel-worker` |

`public_url` 只填写协议和主机（可带端口），不包含路径；HTTPS 会启用 Secure Cookie。`base_path` 用于 `/siagent/` 这类子路径部署。

### 附件与维护

`attachments` 字段直接覆盖默认值，修改后重启生效：

- `max_file_bytes`：单个文件上限，默认 10 MiB；
- `max_image_bytes`：单张图片上限，默认 4 MiB；
- `max_message_image_bytes`：单条消息图片合计上限，默认 5 MiB；
- `max_extracted_runes`：文件提取文本上限，默认 300,000 字符；
- `max_message_count`：单条消息附件数，默认 8；
- `max_message_bytes`：单条消息附件总大小，默认 40 MiB；
- `max_conversation_count`：单会话附件数，默认 40；
- `max_conversation_bytes`：单会话附件总大小，默认 100 MiB；
- `orphan_ttl_hours`：未发送附件清理时间，默认 24 小时。

文件仍存放在数据目录 `attachments/`，不会因为改配置目录而迁移；修改 `data_dir` 时请同时迁移数据和附件。

### 知识库上传

`knowledge` 控制知识库文档的接收范围。也可以在“配置 → 运行与维护 → 知识库上传限制”中直接修改并保存：保存后的设置写入数据库，立即作用于新上传和重新索引，并优先于这里的外置默认值。界面上的提示和单文件校验读取同一组值：

| 字段 | 默认 | 范围 | 说明 |
| --- | --- | --- | --- |
| `max_file_bytes` | 20 MiB | 1 MiB–1 GiB | 单个文档上限。放大后解析时间、内存与磁盘占用同比增加，PDF 还会受 `max_pdf_pages` 限制。 |
| `max_characters` | 1,000,000 | 1,000–50,000,000 | 单文档提取文本上限 |
| `max_chunks` | 5,000 | 1–200,000 | 单文档片段上限，影响索引大小与检索延迟 |
| `max_pdf_pages` | 1,000 | 1–10,000 | PDF 页数上限 |
| `allowed_extensions` | `txt`、`markdown`、`pdf`、`docx` | 每项 1–16 个字符 | 允许上传的格式，不带点；`markdown` 同时接受 `.md` 与 `.markdown` |

`allowed_extensions` 省略时使用内置格式，并额外接受已安装解析扩展声明的格式；显式填写后只接受列出的格式（解析扩展格式也必须列出）。不在列表内的文件会在解析前被拒绝，返回 413/400 并说明允许的格式。修改上限不会重新处理已有文档，但重新索引会按新上限校验。

### 运行限制

`limits` 是**首次运行/数据库尚无已保存运行限制时的默认值**。管理员在“运行与维护”保存后，SQLite 中的值优先于 JSON；再次修改 JSON 不会覆盖已保存的界面设置。删除数据目录或恢复旧备份时按新数据库的默认路径重新应用。字段范围与界面一致：

- `concurrent`：≥1，同时执行的任务数（一个任务是一次用户提问及其后的回答和工具调用）；
- `conversation_queue`：≥1，单个会话等待的任务数，先进先出；
- `global_queue`：不少于 `conversation_queue`，整个实例的等待总数；
- `run_timeout_seconds`：1–604800 秒（7 天），单次任务从开始执行到结束的总时限；
- `tool_timeout_seconds`：1–86400 秒（1 天），单次 Skill/MCP/本地命令调用的时限；
- `tool_rounds`：1–10000，一次任务中模型连续调用工具的批次数。

这些值没有产品级硬上限：不同服务器的 CPU、内存和模型服务配额差异很大，管理员按实际部署决定即可。平台只校验“正整数、总等待不少于会话等待、不超过 Runtime 协议能表达的时限”。默认值仍是并发 2 / 会话等待 10 / 实例等待 100 / 运行 300 秒 / 工具 60 秒 / 工具 10 轮，适合单机小规模使用。

`GET /api/limits` 在返回这些值的同时附带 `capacity`，分别报告**宿主规格**与**本进程可见规格**：

| 字段 | 含义 |
| --- | --- |
| `host_cores` / `host_memory_bytes` | `/proc/cpuinfo` 的物理处理器数与 `/proc/meminfo` 的 MemTotal |
| `cpu_cores` / `memory_bytes` | 实际可用于本进程的数量，已叠加 CPU 亲和性与 cgroup v1/v2 配额 |
| `cpu_limit` / `memory_limit` | 若受限，标明来自 `cgroup v1` 或 `cgroup v2` |
| `runtime_active` | 单个 Runtime 进程的同时运行上限（32） |
| `recommended` | 按有效规格推算的建议值 |

两者分开是有意的：容器或 systemd 单元的配额可能远小于机器实际能力，只显示其中一个都会误导。推荐并发按“**每核 2 个任务**”估算——一次运行大部分时间在等待模型接口，CPU 很少跑满——再用“每 640 MiB 内存一个任务”封顶，最后限制在 Runtime 上限内，并保证至少 2；推荐会话等待为并发的 5 倍、实例等待为并发的 50 倍。

界面显示宿主规格（受限时同时标注本进程可见值）、建议值说明和“使用推荐值”按钮，但不会自动应用，也不会因超出建议而拒绝保存。

超过 Runtime 并发上限的设置同样有效：平台会把多出的任务留在队列里等待，而不是拒绝。

## 应用顺序

从低到高：

1. 内置默认值（以及安装器提供的程序/Node/Runtime 路径）；
2. 系统 `SiAgent/config.json`；
3. `SIAGENT_*` 环境变量；
4. 命令行参数（`-data-dir`、`-listen` 等）；
5. 数据目录 SQLite 中已保存的业务配置（模型、Agent、知识库、渠道、运行限制、知识库上传限制等）在运行时优先于外置默认值。

因此调试时可用环境变量或命令行临时覆盖 JSON，不必改文件；生产环境建议把稳定值写入 JSON 或服务单元，不要把密钥放进 JSON：模型/API 密钥仍通过管理界面保存并加密。

`advanced_tools_disabled` 是部署级高级工具 kill switch 的 JSON 形式；`SIAGENT_DISABLE_ADVANCED_TOOLS` 环境变量优先于 JSON（显式 `false` 也可以覆盖 JSON）。界面不能绕过该开关。

## 示例

```sh
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/SiAgent"
chmod 700 "${XDG_CONFIG_HOME:-$HOME/.config}/SiAgent"
$EDITOR "${XDG_CONFIG_HOME:-$HOME/.config}/SiAgent/config.json"
```

修改后：

```sh
siagent service restart
# 或前台运行时重新启动 siagent
```
