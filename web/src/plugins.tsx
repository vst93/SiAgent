import { Icon } from "./icons";
import {
  FormEvent,
  InputHTMLAttributes,
  useEffect,
  useMemo,
  useState,
} from "react";
import { CapabilitySettings, type Skill } from "./capabilities";
import { MessageMarkdown } from "./message-markdown";
import { LinkedAgents } from "./linked-agents";
import { KnowledgeDialog as SettingsDialog } from "./knowledge-dialog";
import { appURL } from "./urls";
import {
  emptyLinks,
  groupByScope,
  isAllSpace,
  useSpaceScope,
  type CapabilityLinks,
  type LinkedAgent,
} from "./catalog-scope";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "操作未完成，请稍后重试。";
const size = (bytes: number) =>
  bytes < 1024
    ? `${bytes} B`
    : bytes >= 1048576
      ? `${(bytes / 1048576).toFixed(1)} MiB`
      : `${(bytes / 1024).toFixed(1)} KiB`;

// Downloads are authenticated by the session cookie; the response is streamed
// as a ZIP, so it cannot go through the JSON request helper.
async function downloadArchive(path: string, fallback: string) {
  const response = await fetch(appURL(`/api${path}`));
  if (!response.ok) {
    if (response.status === 401)
      window.dispatchEvent(new Event("siagent:session-expired"));
    let message = "导出未完成，请稍后重试。";
    try {
      const data = (await response.json()) as { error?: string };
      if (data.error) message = data.error;
    } catch {
      // Keep the generic message for non-JSON failures.
    }
    throw new Error(message);
  }
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(disposition)?.[1];
  const plain = /filename="([^"]+)"/i.exec(disposition)?.[1];
  const filename = encoded
    ? decodeURIComponent(encoded)
    : (plain ?? fallback);
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

export function PluginsWorkspace({
  request,
  agents,
  spaceAgentID,
  space,
  onSpaceAgent,
}: {
  request: Api;
  agents: { id: string; name: string; archived: boolean }[];
  spaceAgentID: string;
  space: string;
  onSpaceAgent: (id: string) => void;
}) {
  const [tab, setTab] = useState<"skills" | "mcp">("skills");
  const [mcpVisited, setMCPVisited] = useState(false);
  function select(value: typeof tab) {
    setTab(value);
    if (value === "mcp") setMCPVisited(true);
  }
  return (
    <section className="plugins-workspace">
      <div
        className="plugin-tabs"
        role="tablist"
        aria-label="插件类型"
        onKeyDown={(event) => {
          if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key))
            return;
          event.preventDefault();
          const next =
            event.key === "Home"
              ? "skills"
              : event.key === "End"
                ? "mcp"
                : tab === "skills"
                  ? "mcp"
                  : "skills";
          select(next);
          event.currentTarget
            .querySelector<HTMLButtonElement>(`#plugin-tab-${next}`)
            ?.focus();
        }}
      >
        <button
          role="tab"
          id="plugin-tab-skills"
          aria-selected={tab === "skills"}
          aria-controls="plugin-skills"
          tabIndex={tab === "skills" ? 0 : -1}
          onClick={() => select("skills")}
        >
          Skills <span>技能文件</span>
        </button>
        <button
          role="tab"
          id="plugin-tab-mcp"
          aria-selected={tab === "mcp"}
          aria-controls="plugin-mcp"
          tabIndex={tab === "mcp" ? 0 : -1}
          onClick={() => select("mcp")}
        >
          MCP <span>工具服务</span>
        </button>
      </div>
      <div
        role="tabpanel"
        id="plugin-skills"
        aria-labelledby="plugin-tab-skills"
        hidden={tab !== "skills"}
      >
        <SkillCatalog
          request={request}
          agents={agents}
          spaceAgentID={spaceAgentID}
          space={space}
          onSpaceAgent={onSpaceAgent}
        />
      </div>
      <div
        role="tabpanel"
        id="plugin-mcp"
        className="plugin-mcp"
        aria-labelledby="plugin-tab-mcp"
        hidden={tab !== "mcp"}
      >
        {mcpVisited && <CapabilitySettings request={request} catalog />}
      </div>
    </section>
  );
}

type UploadFile = { file: File; path: string };
type UploadResult = Skill | { skills: Skill[]; replaced: number };
function SkillUpload({
  request,
  agentID,
  agentName,
  onClose,
  onUploaded,
}: {
  request: Api;
  agentID: string;
  agentName: string;
  onClose: () => void;
  onUploaded: (skills: Skill[]) => void;
}) {
  const [mode, setMode] = useState<"package" | "folder">("package");
  const [files, setFiles] = useState<UploadFile[]>([]);
  const [trusted, setTrusted] = useState(false);
  const [overwrite, setOverwrite] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const bytes = files.reduce((sum, item) => sum + item.file.size, 0);
  const folderAttributes = {
    webkitdirectory: "",
    directory: "",
  } as InputHTMLAttributes<HTMLInputElement>;
  function choose(values: FileList | null) {
    const entries = Array.from(values ?? []).map((file) => ({
      file,
      path:
        mode === "folder"
          ? file.webkitRelativePath.split("/").slice(1).join("/")
          : file.name,
    }));
    setFiles(entries);
    setTrusted(false);
    setError("");
  }
  async function upload(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!files.length) return;
    // A folder may hold one skill or a bundle of top-level skill folders.
    const singleSkill = files.some((item) => item.path === "SKILL.md");
    const overLimit = singleSkill
      ? files.length > 200 || bytes > 8 * 1048576
      : files.length > 2000 || bytes > 80 * 1048576;
    if (
      mode === "folder" &&
      (overLimit || files.some((item) => item.file.size > 2 * 1048576))
    ) {
      setError(
        singleSkill
          ? "单个技能最多 200 个文件，单文件 2 MiB、总计 8 MiB，请整理后重新选择。"
          : "技能包集合最多 2000 个文件，单文件 2 MiB、总计 80 MiB，请整理后重新选择。",
      );
      return;
    }
    if (mode === "folder" && !singleSkill) {
      const topLevel = new Set(
        files.map((item) => item.path.split("/")[0]).filter(Boolean),
      );
      const bundled = [...topLevel].every((folder) =>
        files.some((item) => item.path === `${folder}/SKILL.md`),
      );
      if (!topLevel.size || !bundled) {
        setError(
          "请选择根部包含 SKILL.md 的技能文件夹，或包含多个技能文件夹的上级文件夹。",
        );
        return;
      }
    }
    if (mode === "package" && bytes > 10 * 1048576) {
      setError("技能包最多 10 MiB，解包后总计最多 80 MiB。");
      return;
    }
    setPending(true);
    try {
      const body = new FormData();
      body.append("trusted", String(trusted));
      body.append("overwrite", String(overwrite));
      if (agentID) body.append("mount", agentID);
      for (const item of files)
        body.append(mode === "folder" ? "files" : "file", item.file, item.path);
      const result = await request<UploadResult>("/skills", "POST", body);
      onUploaded("skills" in result ? result.skills : [result]);
    } catch (error) {
      setError(errorText(error));
    } finally {
      setPending(false);
    }
  }
  return (
    <SettingsDialog
      title={agentName ? `上传 Skill 到${agentName}` : "上传 Skill"}
      pending={pending}
      onClose={onClose}
    >
      <form className="skill-upload-form" onSubmit={upload}>
        <fieldset disabled={pending}>
          <label>
            上传方式
            <select
              aria-label="上传方式"
              value={mode}
              onChange={(event) => {
                setMode(event.target.value as typeof mode);
                setFiles([]);
                setError("");
              }}
            >
              <option value="package">ZIP 技能包 / SKILL.md</option>
              <option value="folder">本机文件夹</option>
            </select>
          </label>
          <div className="skill-upload-picker">
            <label>
              {mode === "folder" ? "选择技能文件夹" : "选择技能包"}
              <input
                key={mode}
                type="file"
                required
                {...(mode === "folder"
                  ? folderAttributes
                  : { accept: ".zip,.md" })}
                multiple={mode === "folder"}
                onChange={(event) => choose(event.target.files)}
              />
            </label>
          </div>
          {!!files.length && (
            <div className="skill-upload-selection">
              <strong>
                {mode === "folder"
                  ? `${files.length} 个文件`
                  : files[0].file.name}{" "}
                · {size(bytes)}
              </strong>
              {mode === "folder" && (
                <ul>
                  {files.map((item) => (
                    <li key={item.path}>
                      {item.path}
                      <span>{size(item.file.size)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
          <p className="muted">
            根部须有 SKILL.md；ZIP
            也可包含单一顶层文件夹或多个技能文件夹。上传后由系统管理目录，可保留
            references、scripts 等子目录。
          </p>
          <details className="skill-upload-guide">
            <summary>
              <Icon name="caret-right" size={14} className="disclosure-icon" />
              格式示例与上传限制
            </summary>
            <pre>
              {
                "---\nname: order-guide\ndescription: 订单处理指引。\n---\n先核对订单号，再使用已授权的工具。"
              }
            </pre>
            <p>
              单个技能最多 200 个文件、400 个目录项，单文件 2 MiB、总计 8 MiB；
              多个技能打包时总计最多 80 MiB。SKILL.md 最多 64,000
              字节。不接受符号链接或特殊文件。
            </p>
          </details>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={overwrite}
              onChange={(event) => setOverwrite(event.target.checked)}
            />
            覆盖同名技能（保留原有 Agent 挂载关系）
          </label>
          <label className="checkbox-label">
            <input
              type="checkbox"
              required
              checked={trusted}
              onChange={(event) => setTrusted(event.target.checked)}
            />
            我已确认该技能来自可信来源
          </label>
          <p className="muted">
            上传不执行脚本，也不安装依赖。
            {agentName
              ? `保存后自动挂载到${agentName}，其他 Agent 可在技能列表中一键复用。`
              : "到 Agent 配置中挂载后，技能指令和按需读取的资源才能提供给该 Agent。"}
          </p>
        </fieldset>
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        <footer>
          <button
            type="button"
            data-dialog-cancel
            disabled={pending}
            onClick={onClose}
          >
            取消
          </button>
          <button
            className="primary"
            disabled={pending || !files.length || !trusted}
          >
            {pending ? "正在上传…" : "上传并保存"}
          </button>
        </footer>
      </form>
    </SettingsDialog>
  );
}

type FileNode = {
  name: string;
  path: string;
  file?: Skill["files"][number];
  children: Map<string, FileNode>;
};
function fileTree(files: Skill["files"]) {
  const root: FileNode = { name: "", path: "", children: new Map() };
  for (const file of files) {
    let node = root;
    for (const name of file.path.split("/")) {
      if (!node.children.has(name))
        node.children.set(name, {
          name,
          path: node.path ? `${node.path}/${name}` : name,
          children: new Map(),
        });
      node = node.children.get(name)!;
    }
    node.file = file;
  }
  return root;
}
function SkillTree({
  node,
  selected,
  onSelect,
}: {
  node: FileNode;
  selected: string;
  onSelect: (path: string) => void;
}) {
  return (
    <ul>
      {Array.from(node.children.values())
        .sort(
          (a, b) =>
            Number(b.path === "SKILL.md") - Number(a.path === "SKILL.md") ||
            Number(!!a.file) - Number(!!b.file) ||
            a.name.localeCompare(b.name),
        )
        .map((child) => (
          <li key={child.path}>
            {child.file ? (
              <button
                title={child.path}
                aria-pressed={selected === child.path}
                onClick={() => onSelect(child.path)}
              >
                <Icon name="file" size={16} />
                {child.name}
              </button>
            ) : (
              <details open>
                <summary>
                  <Icon
                    name="caret-right"
                    size={14}
                    className="disclosure-icon"
                  />
                  <Icon name="folder" size={16} /> {child.name}
                </summary>
                <SkillTree
                  node={child}
                  selected={selected}
                  onSelect={onSelect}
                />
              </details>
            )}
          </li>
        ))}
    </ul>
  );
}
function SkillDetail({
  request,
  skill,
  mounted,
  allSpace,
  scopeName,
  others,
  pending,
  onMount,
  onArchive,
  onExport,
  onUpdated,
}: {
  request: Api;
  skill: Skill;
  mounted: boolean;
  allSpace: boolean;
  scopeName: string;
  others: LinkedAgent[];
  pending: boolean;
  onMount: () => void;
  onArchive: () => void;
  onExport: () => void;
  onUpdated: (skill: Skill) => void;
}) {
  const [path, setPath] = useState("SKILL.md");
  const [content, setContent] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [mode, setMode] = useState<"preview" | "edit">("preview");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saveError, setSaveError] = useState("");
  // "Open" and "pending" must be separate: sharing one flag disabled the whole
  // form the moment the dialog appeared.
  const [adding, setAdding] = useState(false);
  const [addPending, setAddPending] = useState(false);
  const [newFile, setNewFile] = useState({ path: "", content: "" });
  const [addError, setAddError] = useState("");
  const [removing, setRemoving] = useState("");
  const resource = skill.files.find((file) => file.path === path);
  const tree = useMemo(() => fileTree(skill.files), [skill.files]);
  const markdown = /\.md$/i.test(path);
  useEffect(() => {
    let active = true;
    setContent(null);
    setDraft("");
    setMode("preview");
    setError("");
    setSaveError("");
    if (resource && resource.size <= 128000)
      void request<{ content: string }>(
        `/skills/${skill.id}/files/${path.split("/").map(encodeURIComponent).join("/")}`,
      )
        .then((result) => {
          if (active) {
            setContent(result.content);
            setDraft(result.content);
          }
        })
        .catch((error) => {
          if (active) setError(errorText(error));
        });
    return () => {
      active = false;
    };
  }, [skill.id, path, resource?.size]);
  async function save() {
    setSaving(true);
    setSaveError("");
    try {
      const updated = await request<Skill>(
        `/skills/${skill.id}/files/${path.split("/").map(encodeURIComponent).join("/")}`,
        "PUT",
        { content: draft },
      );
      onUpdated(updated);
      setContent(draft);
      setMode("preview");
    } catch (error) {
      setSaveError(errorText(error));
    } finally {
      setSaving(false);
    }
  }
  async function addFile(event: FormEvent) {
    event.preventDefault();
    setAddPending(true);
    setAddError("");
    try {
      const updated = await request<Skill>(
        `/skills/${skill.id}/files`,
        "POST",
        { path: newFile.path.trim(), content: newFile.content },
      );
      onUpdated(updated);
      setPath(newFile.path.trim());
      setAdding(false);
      setNewFile({ path: "", content: "" });
    } catch (failure) {
      setAddError(errorText(failure));
    } finally {
      setAddPending(false);
    }
  }
  async function removeFile(target: string) {
    setRemoving(target);
    setError("");
    try {
      const updated = await request<Skill>(
        `/skills/${skill.id}/files/${target.split("/").map(encodeURIComponent).join("/")}`,
        "DELETE",
      );
      onUpdated(updated);
      if (path === target) setPath("SKILL.md");
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setRemoving("");
    }
  }
  const preview = path === "SKILL.md"
    ? (content ?? "").replace(/^---\n[\s\S]*?\n---\n/, "")
    : content;
  return (
    <section className="skill-detail" aria-label="技能详情">
      <header className="skill-detail-head">
        <div className="skill-title">
          <h2 title={skill.name}>{skill.name}</h2>
          <p className="skill-meta">
            <span>{skill.files.length} 个文件</span>
            <span>{size(skill.files.reduce((sum, file) => sum + file.size, 0))}</span>
            <span>{skill.compatibility ? "已声明运行要求" : "未声明外部依赖"}</span>
          </p>
        </div>
        <div className="skill-head-actions">
          {mounted ? (
            <span className="skill-mount-state">
              <Icon name="check" size={14} />
              {allSpace ? "已被 Agent 挂载" : `已挂载到${scopeName}`}
            </span>
          ) : allSpace ? null : (
            <button
              className="primary"
              disabled={pending}
              onClick={onMount}
            >
              {pending ? "正在挂载…" : `挂载到${scopeName}`}
            </button>
          )}
          <button onClick={onExport}>导出 ZIP</button>
          <button className="danger-ghost" onClick={onArchive}>
            删除技能
          </button>
        </div>
      </header>
      <p className="skill-description">{skill.description}</p>
      {others.length > 0 && (
        <p className="skill-shared-note">
          同一份技能文件也被 {others.map((agent) => agent.name).join("、")} 使用；
          修改内容会同时生效，不会产生副本。
        </p>
      )}
      <LinkedAgents request={request} kind="skills" id={skill.id} />
      <details className="skill-compatibility">
        <summary>
          <Icon name="caret-right" size={14} className="disclosure-icon" />
          运行要求与依赖
        </summary>
        <p>
          {skill.compatibility ||
            "未声明运行环境依赖，可在文件列表中检查脚本和依赖清单。"}
        </p>
        <p>
          脚本作为资源保存，不自动执行；需要调用外部工具时须单独配置 MCP
          并授权。
        </p>
      </details>
      <div className="skill-resource-browser">
        <nav className="skill-file-tree" aria-label="技能文件">
          <header className="skill-pane-head">
            <span>文件</span>
            <span>{skill.files.length}</span>
          </header>
          <div className="skill-file-tree-scroll">
            <SkillTree node={tree} selected={path} onSelect={setPath} />
          </div>
          <div className="skill-file-tree-actions">
            <button
              type="button"
              disabled={adding}
              onClick={() => {
                setAdding(true);
                setNewFile({ path: "", content: "" });
                setAddError("");
              }}
            >
              <Icon name="plus" size={14} /> 添加文件
            </button>
          </div>
        </nav>
        <section className="skill-file-preview">
          <header className="skill-pane-head">
            <div className="skill-file-path">
              <Icon name="file" size={16} />
              <strong title={path}>{path}</strong>
              {resource && <span>{size(resource.size)}</span>}
            </div>
            <div
              className="skill-file-actions"
              role="group"
              aria-label="文件视图"
            >
              {path !== "SKILL.md" && (
                <button
                  type="button"
                  className="danger-ghost"
                  disabled={!!removing}
                  onClick={() => void removeFile(path)}
                  title="删除此文件"
                >
                  {removing === path ? "正在删除…" : "删除文件"}
                </button>
              )}
              <button
                type="button"
                aria-pressed={mode === "preview"}
                onClick={() => setMode("preview")}
              >
                预览
              </button>
              <button
                type="button"
                aria-pressed={mode === "edit"}
                disabled={content === null || !resource || resource.size > 128000}
                onClick={() => setMode("edit")}
              >
                编辑
              </button>
            </div>
          </header>
          <div className="skill-file-body">
            {error ? (
              <p className="error skill-file-message" role="alert">
                {error}
              </p>
            ) : resource && resource.size > 128000 ? (
              <p className="muted skill-file-message">
                文件已保存，超过 128 KB 文本预览上限。
              </p>
            ) : content === null ? (
              <p className="muted skill-file-message" role="status">
                正在读取文件…
              </p>
            ) : mode === "edit" ? (
              <div className="skill-file-editor">
                <textarea
                  aria-label={`编辑 ${path}`}
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  spellCheck={false}
                  maxLength={path === "SKILL.md" ? 64000 : 128000}
                  disabled={saving}
                />
                {saveError && (
                  <p className="error skill-file-message" role="alert">
                    {saveError}
                  </p>
                )}
                <div className="skill-editor-actions">
                  <span className="muted">
                    {draft.length.toLocaleString()} 字符
                  </span>
                  <button
                    type="button"
                    disabled={saving}
                    onClick={() => {
                      setDraft(content);
                      setSaveError("");
                      setMode("preview");
                    }}
                  >
                    取消
                  </button>
                  <button
                    type="button"
                    className="primary"
                    disabled={saving || draft === content}
                    onClick={() => void save()}
                  >
                    {saving ? "正在保存…" : "保存文件"}
                  </button>
                </div>
              </div>
            ) : markdown ? (
              <div className="skill-markdown-preview">
                <MessageMarkdown content={preview || ""} />
              </div>
            ) : (
              <pre>{content}</pre>
            )}
          </div>
        </section>
      </div>
      {adding && (
        <SettingsDialog title="添加技能文件" pending={addPending} onClose={() => setAdding(false)}>
          <form className="skill-create-form" onSubmit={addFile}>
            <fieldset disabled={addPending}>
              <label htmlFor="skill-new-path">
                文件路径
                <input
                  id="skill-new-path"
                  value={newFile.path}
                  onChange={(event) =>
                    setNewFile({ ...newFile, path: event.target.value })
                  }
                  placeholder="references/status.md"
                  required
                  maxLength={200}
                  spellCheck={false}
                />
              </label>
              <p className="muted">
                相对技能根目录，例如 references/status.md 或 scripts/check.py。
              </p>
              <label htmlFor="skill-new-content">
                文件内容
                <textarea
                  id="skill-new-content"
                  value={newFile.content}
                  onChange={(event) =>
                    setNewFile({ ...newFile, content: event.target.value })
                  }
                  rows={10}
                  maxLength={128000}
                  spellCheck={false}
                />
              </label>
              <p className="muted">
                仅支持 UTF-8 文本，最多 128 KB。脚本作为资源保存，不会自动执行。
              </p>
            </fieldset>
            {addError && (
              <p className="error" role="alert">
                {addError}
              </p>
            )}
            <footer>
              <button
                type="button"
                data-dialog-cancel
                disabled={addPending}
                onClick={() => setAdding(false)}
              >
                取消
              </button>
              <button
                className="primary"
                disabled={addPending || !newFile.path.trim()}
              >
                {addPending ? "正在添加…" : "添加文件"}
              </button>
            </footer>
          </form>
        </SettingsDialog>
      )}
    </section>
  );
}

type SkillSort = "name" | "recent" | "files" | "size";

// The catalog is scoped to one Agent: the scope picker decides where an upload
// lands and which rows read as "mine". Skills are never copied — a shared skill
// simply appears in more than one scope, and its badge is derived from usage.
function SkillCatalog({
  request,
  agents,
  spaceAgentID,
  space,
  onSpaceAgent,
}: {
  request: Api;
  agents: { id: string; name: string; archived: boolean }[];
  spaceAgentID: string;
  space: string;
  onSpaceAgent: (id: string) => void;
}) {
  const scope = useSpaceScope(agents, spaceAgentID, onSpaceAgent);
  const all = isAllSpace(space);
  const [skills, setSkills] = useState<Skill[]>([]);
  const [links, setLinks] = useState<CapabilityLinks>(emptyLinks);
  const [selected, setSelected] = useState("");
  const [query, setQuery] = useState("");
  // Skills nobody mounts are the least interesting rows; they stay collapsed so
  // the two groups that need a decision are always on screen.
  const [showUnused, setShowUnused] = useState(false);
  // Other Agents' skills are opt-in: sharing is an action, not a default view.
  const [showAvailable, setShowAvailable] = useState(false);
  const [sort, setSort] = useState<SkillSort>(() => {
    try {
      const stored = localStorage.getItem("siagent.skill-sort");
      if (
        stored === "name" ||
        stored === "files" ||
        stored === "size" ||
        stored === "recent"
      )
        return stored;
    } catch {
      // Fall through to the default order.
    }
    return "name";
  });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState("");
  const [upload, setUpload] = useState(false);
  const [creating, setCreating] = useState(false);
  const [archiving, setArchiving] = useState<Skill | null>(null);
  const [pending, setPending] = useState(false);
  const [archiveError, setArchiveError] = useState("");
  const [reload, setReload] = useState(0);
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    void Promise.all([
      request<Skill[]>("/skills"),
      request<CapabilityLinks>("/capability-links"),
    ])
      .then(([values, usage]) => {
        if (active) {
          setSkills(values);
          setLinks(usage);
        }
      })
      .catch((error) => {
        if (active) setError(errorText(error));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [reload]);
  useEffect(() => {
    try {
      localStorage.setItem("siagent.skill-sort", sort);
    } catch {
      // The order still applies for this session.
    }
  }, [sort]);
  const sizeOf = (skill: Skill) =>
    skill.files.reduce((sum, file) => sum + file.size, 0);
  const filtered = skills.filter((skill) =>
    `${skill.name} ${skill.description}`
      .toLowerCase()
      .includes(query.toLowerCase()),
  );
  const ordered = (values: Skill[]) =>
    [...values].sort((a, b) => {
      if (sort === "files") return b.files.length - a.files.length;
      if (sort === "size") return sizeOf(b) - sizeOf(a);
      // "recent" keeps the server order (newest first); "name" is A-Z.
      return sort === "recent" ? 0 : a.name.localeCompare(b.name, "zh-CN");
    });
  // A concrete space answers one question: what does THIS Agent have? Other
  // Agents' skills are available on demand but not in the default list, which is
  // what made a 12-skill catalog feel mixed together. "All" flattens everything
  // and labels each row with its users instead.
  const groups = all
    ? { mounted: [], available: [], unused: [] }
    : groupByScope(filtered, links.skills, scope.agentID);
  const visible = all
    ? ordered(filtered)
    : ordered([
        ...groups.mounted.map((entry) => entry.item),
        ...(showAvailable || query
          ? groups.available.map((entry) => entry.item)
          : []),
        ...groups.unused,
      ]);
  const current = visible.find((skill) => skill.id === selected) ?? visible[0];
  const usersOf = (id: string) => links.skills[id] ?? [];
  const othersOf = (id: string) =>
    usersOf(id).filter((agent) => agent.id !== scope.agentID);
  async function reloadUsage() {
    try {
      setLinks(await request<CapabilityLinks>("/capability-links"));
    } catch {
      // Grouping falls back to the previous snapshot; the list stays usable.
    }
  }
  async function exportArchive(
    path: string,
    fallback: string,
    message: string,
  ) {
    setExporting(true);
    setExportError("");
    setFeedback("");
    try {
      await downloadArchive(path, fallback);
      setFeedback(message);
    } catch (error) {
      setExportError(errorText(error));
    } finally {
      setExporting(false);
    }
  }
  async function mount(skill: Skill) {
    setPending(true);
    setError("");
    setFeedback("");
    try {
      await request(
        `/agents/${scope.agentID}/skills/${skill.id}/mount`,
        "POST",
      );
      await reloadUsage();
      setFeedback(`“${skill.name}”已挂载到${scope.agent?.name ?? "当前 Agent"}。`);
    } catch (error) {
      setError(errorText(error));
    } finally {
      setPending(false);
    }
  }
  const row = (skill: Skill) => {
    const users = usersOf(skill.id);
    return (
      <button
        key={skill.id}
        className="skill-list-item"
        aria-current={current?.id === skill.id ? "true" : undefined}
        onClick={() => setSelected(skill.id)}
      >
        <span className="skill-list-text">
          <strong title={skill.name}>{skill.name}</strong>
          <span title={skill.description}>{skill.description}</span>
        </span>
        <span className="skill-list-meta">
          {all ? (
            <span
              className="skill-scope-badge"
              title={
                users.length
                  ? users.map((agent) => agent.name).join("、")
                  : "没有任何 Agent 挂载"
              }
            >
              {users.length ? `${users.length} 个 Agent` : "未挂载"}
            </span>
          ) : users.length > 1 ? (
            <span
              className="skill-scope-badge"
              title={users.map((agent) => agent.name).join("、")}
            >
              共享 · {users.length}
            </span>
          ) : (
            <>
              {skill.files.length}
              <span className="skill-list-meta-label">文件</span>
            </>
          )}
        </span>
      </button>
    );
  };
  const empty = !skills.length;
  return (
    <div className="skill-catalog">
      <header className="plugin-section-heading">
        <div>
          <h2>
            技能库 <span>{skills.length}</span>
          </h2>
          <p>
            上传到当前 Agent 会自动挂载；同一份技能可被多个 Agent 使用，不会复制。
          </p>
        </div>
        <div className="plugin-heading-actions">
          <button
            type="button"
            disabled={loading || !skills.length || exporting}
            onClick={() =>
              void exportArchive(
                "/skills/export",
                "siagent-skills.zip",
                "已导出全部技能。",
              )
            }
          >
            <Icon name="arrow-down" size={16} />
            {exporting ? "正在导出…" : "导出全部"}
          </button>
          <button
            className="primary"
            disabled={loading || exporting}
            onClick={() => setUpload(true)}
          >
            <Icon name="plus" size={16} /> 上传 Skill
          </button>
          <button
            type="button"
            disabled={loading || exporting}
            onClick={() => setCreating(true)}
          >
            <Icon name="file" size={16} /> 新建技能
          </button>
        </div>
      </header>
      {feedback && (
        <p className="success plugin-feedback" role="status">
          {feedback}
        </p>
      )}
      {exportError && (
        <p className="error plugin-feedback" role="alert">
          {exportError}
        </p>
      )}
      {error && (
        <p className="error plugin-feedback" role="alert">
          {error}
          <button onClick={() => setReload((value) => value + 1)}>
            重新读取
          </button>
        </p>
      )}
      {loading ? (
        <p className="muted" role="status">
          正在读取技能库…
        </p>
      ) : empty ? (
        <div className="plugin-empty">
          <Icon name="plugins" size={28} />
          <h3>上传第一个 Skill</h3>
          <p>
            选择 ZIP 技能包、SKILL.md
            或本机文件夹。系统会创建独立的技能目录，保留包内文件结构。
          </p>
          <p>上传后会自动挂载到当前 Agent，无需再逐个勾选。</p>
        </div>
      ) : (
        <div className="skill-catalog-layout">
          <aside className="skill-catalog-list">
            <div className="pane-heading">
              <h2>
                技能列表 <span>{visible.length}</span>
              </h2>
            </div>
            <div className="skill-list-filter">
              <input
                type="search"
                aria-label="搜索技能"
                placeholder="搜索名称或说明"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
              <label className="skill-list-sort">
                <span className="sr-only">技能排序方式</span>
                <select
                  aria-label="技能排序方式"
                  value={sort}
                  onChange={(event) =>
                    setSort(event.target.value as SkillSort)
                  }
                >
                  <option value="name">名称 A→Z</option>
                  <option value="recent">最近上传</option>
                  <option value="files">文件数最多</option>
                  <option value="size">体积最大</option>
                </select>
              </label>
            </div>
            <nav className="skill-list" aria-label="技能列表">
              {all && visible.map(row)}
              {!all && !!groups.mounted.length && (
                <>
                  <p className="skill-list-group">
                    已挂载 · {scope.agent?.name ?? "当前 Agent"}
                    <span>{groups.mounted.length}</span>
                  </p>
                  {ordered(groups.mounted.map((entry) => entry.item)).map(row)}
                </>
              )}
              {!all && !!groups.available.length && (
                <>
                  <button
                    type="button"
                    className="skill-list-group skill-list-toggle"
                    aria-expanded={showAvailable || !!query}
                    onClick={() => setShowAvailable((value) => !value)}
                  >
                    其他 Agent 在用
                    <span>{groups.available.length}</span>
                  </button>
                  {(showAvailable || !!query) &&
                    ordered(groups.available.map((entry) => entry.item)).map(
                      row,
                    )}
                </>
              )}
              {!all && !!groups.unused.length && (
                <>
                  <button
                    type="button"
                    className="skill-list-group skill-list-toggle"
                    aria-expanded={showUnused || !!query}
                    onClick={() => setShowUnused((value) => !value)}
                  >
                    未挂载
                    <span>{groups.unused.length}</span>
                  </button>
                  {(showUnused || !!query) && ordered(groups.unused).map(row)}
                </>
              )}
            </nav>
            {!visible.length && (
              <p className="muted">没有匹配的技能。</p>
            )}
          </aside>
          {current ? (
            <SkillDetail
              key={current.id}
              request={request}
              skill={current}
              mounted={
                all
                  ? usersOf(current.id).length > 0
                  : groups.mounted.some((entry) => entry.item.id === current.id)
              }
              allSpace={all}
              scopeName={scope.agent?.name ?? "当前 Agent"}
              others={othersOf(current.id)}
              pending={pending}
              onMount={() => void mount(current)}
              onArchive={() => {
                setArchiving(current);
                setArchiveError("");
              }}
              onExport={() =>
                void exportArchive(
                  `/skills/${current.id}/export`,
                  `${current.name}.zip`,
                  `“${current.name}”已导出。`,
                )
              }
              onUpdated={(updated) =>
                setSkills((values) =>
                  values.map((value) =>
                    value.id === updated.id ? updated : value,
                  ),
                )
              }
            />
          ) : (
            <p className="muted">请调整搜索条件。</p>
          )}
        </div>
      )}
      {creating && (
        <SkillCreate
          request={request}
          agentID={scope.agentID}
          agentName={scope.agent?.name ?? ""}
          onClose={() => setCreating(false)}
          onCreated={(skill) => {
            setSkills((values) => [skill, ...values]);
            setSelected(skill.id);
            setQuery("");
            setCreating(false);
            void reloadUsage();
            const where = scope.agent?.name
              ? `已挂载到${scope.agent.name}`
              : "已创建";
            setFeedback(`“${skill.name}”${where}。`);
          }}
        />
      )}
      {upload && (
        <SkillUpload
          request={request}
          agentID={scope.agentID}
          agentName={scope.agent?.name ?? ""}
          onClose={() => setUpload(false)}
          onUploaded={(uploaded) => {
            const existing = new Set(skills.map((skill) => skill.name));
            const replaced = uploaded.filter((skill) =>
              existing.has(skill.name),
            ).length;
            setSkills((values) => {
              const next = [...values];
              for (const skill of uploaded) {
                const index = next.findIndex((item) => item.id === skill.id);
                if (index >= 0) next[index] = skill;
                else next.unshift(skill);
              }
              return next;
            });
            setSelected(uploaded[uploaded.length - 1]?.id ?? "");
            setQuery("");
            setUpload(false);
            void reloadUsage();
            const where = scope.agent?.name
              ? `已挂载到${scope.agent.name}`
              : "已上传";
            if (uploaded.length === 1 && replaced === 0) {
              setFeedback(`“${uploaded[0].name}”已上传，${where}。`);
            } else if (uploaded.length === 1) {
              setFeedback(
                `“${uploaded[0].name}”已覆盖同名技能，原有 Agent 挂载关系保留。`,
              );
            } else {
              setFeedback(
                `已导入 ${uploaded.length} 个技能${replaced ? `，覆盖 ${replaced} 个同名技能` : ""}，${where}。`,
              );
            }
          }}
        />
      )}
      {archiving && (
        <SettingsDialog
          title={`删除技能“${archiving.name}”？`}
          pending={pending}
          onClose={() => setArchiving(null)}
        >
          <p>
            将永久删除技能文件，并自动从所有关联 Agent
            中移除。已有任务使用的历史快照与运行记录保留；此操作不可撤销。
          </p>
          <LinkedAgents request={request} kind="skills" id={archiving.id} />
          {archiveError && (
            <p role="alert" className="error">
              {archiveError}
            </p>
          )}
          <footer>
            <button
              data-dialog-cancel
              disabled={pending}
              onClick={() => setArchiving(null)}
            >
              取消
            </button>
            <button
              className="danger-button"
              disabled={pending}
              onClick={async () => {
                setPending(true);
                setArchiveError("");
                try {
                  await request(`/skills/${archiving.id}`, "DELETE");
                  setSkills((values) =>
                    values.filter((skill) => skill.id !== archiving.id),
                  );
                  setArchiving(null);
                  void reloadUsage();
                  setFeedback("技能已删除，关联 Agent 已自动解除；历史任务保留。");
                } catch (error) {
                  setArchiveError(errorText(error));
                } finally {
                  setPending(false);
                }
              }}
            >
              {pending ? "正在删除…" : "确认删除"}
            </button>
          </footer>
        </SettingsDialog>
      )}
    </div>
  );
}

// Creating a Skill by hand writes the same SKILL.md an upload would produce.
// The template keeps the frontmatter valid so the editor is not a syntax quiz.
const skillTemplate = `---
name: my-skill
description: 一句话说明这个技能解决什么问题。
---

## 何时使用

说明触发场景。

## 步骤

1. 第一步
2. 第二步
`;

function SkillCreate({
  request,
  agentID,
  agentName,
  onClose,
  onCreated,
}: {
  request: Api;
  agentID: string;
  agentName: string;
  onClose: () => void;
  onCreated: (skill: Skill) => void;
}) {
  const [content, setContent] = useState(skillTemplate);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  // A live name preview means an invalid frontmatter fails before the request,
  // with the same rule the server applies.
  const name = /^---\n[\s\S]*?^name:\s*(\S+)\s*$/m.exec(content)?.[1] ?? "";
  const nameValid = /^[a-z0-9]+(-[a-z0-9]+)*$/.test(name) && name.length <= 64;
  async function submit(event: FormEvent) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      const created = await request<Skill>("/skills/new", "POST", {
        content,
        mount: agentID,
      });
      onCreated(created);
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setPending(false);
    }
  }
  return (
    <SettingsDialog
      title={agentName ? `新建技能到${agentName}` : "新建技能"}
      pending={pending}
      onClose={onClose}
    >
      <form className="skill-create-form" onSubmit={submit}>
        <fieldset disabled={pending}>
          <label htmlFor="skill-create-content">
            SKILL.md
            <textarea
              id="skill-create-content"
              value={content}
              onChange={(event) => setContent(event.target.value)}
              spellCheck={false}
              maxLength={64000}
              rows={16}
              required
            />
          </label>
          <p className="skill-create-hint">
            {name ? (
              nameValid ? (
                <>
                  技能标识 <code>{name}</code>
                </>
              ) : (
                <>
                  技能标识 <code>{name}</code> 无效：只能使用小写字母、数字和单连字符。
                </>
              )
            ) : (
              "frontmatter 需要 name 与 description 两项。"
            )}
            {agentName && ` 保存后自动挂载到${agentName}。`}
          </p>
          <p className="muted">
            创建后可在详情中继续添加参考文件和脚本；与上传的技能完全等价，也可以被其他 Agent 复用。
          </p>
        </fieldset>
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        <footer>
          <button
            type="button"
            data-dialog-cancel
            disabled={pending}
            onClick={onClose}
          >
            取消
          </button>
          <button
            className="primary"
            disabled={pending || !nameValid || !content.trim()}
          >
            {pending ? "正在创建…" : "创建技能"}
          </button>
        </footer>
      </form>
    </SettingsDialog>
  );
}
