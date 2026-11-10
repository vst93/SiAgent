import { useEffect, useRef, useState } from "react";
import { Icon } from "./icons";
import { MessageMarkdown } from "./message-markdown";
import { KnowledgeDialog as SettingsDialog } from "./knowledge-dialog";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type UpdateStatus = {
  mode: "installer" | "service" | "docker" | "managed" | "source";
  supported: boolean;
  automatic: boolean;
  current: string;
  latest: string;
  available: boolean;
  phase: string;
  message: string;
  backup?: string;
  notes?: string;
  downloaded?: number;
  download_total?: number;
};
const megabytes = (bytes: number) =>
  bytes >= 1024 * 1024 * 1024
    ? `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
    : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
const backupTime = (value: number) =>
  new Date(value).toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
type Backup = {
  name: string;
  version: string;
  created: number;
  bytes: number;
  files: number;
  current: boolean;
};
const phases: Record<string, string> = {
  idle: "就绪",
  checking: "检查中",
  downloading: "下载校验中",
  ready: "校验完成",
  waiting: "等待现有任务结束",
  restarting: "正在重启",
  completed: "更新完成",
  rolled_back: "已恢复旧版本",
  failed: "更新失败",
};
export function ApplicationUpdates({ request }: { request: Api }) {
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const restarting = useRef(false);
  const [reconnecting, setReconnecting] = useState(false);
  const requestRef = useRef(request);
  requestRef.current = request;
  useEffect(() => {
    let disposed = false;
    const refresh = async () => {
      try {
        const next = await requestRef.current<UpdateStatus>("/updates");
        if (disposed) return;
        setStatus(next);
        setReconnecting(false);
        setError("");
        if (next.automatic || next.phase === "restarting") restarting.current = true;
        if (
          restarting.current &&
          ["completed", "rolled_back"].includes(next.phase)
        ) {
          restarting.current = false;
          window.location.reload();
        }
        if (["failed", "idle"].includes(next.phase)) restarting.current = false;
      } catch (error) {
        if (disposed) return;
        if (restarting.current) setReconnecting(true);
        else
          setError(
            error instanceof Error ? error.message : "无法读取更新状态。",
          );
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 2000);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, []);
  async function action(path: string, body?: unknown) {
    setPending(true);
    setError("");
    if (path === "start") restarting.current = true;
    try {
      setStatus(await request<UpdateStatus>(`/updates/${path}`, "POST", body));
    } catch (error) {
      if (path === "start") restarting.current = false;
      setError(
        error instanceof Error ? error.message : "更新操作未完成，请重试。",
      );
    } finally {
      setPending(false);
    }
  }
  if (!status)
    return (
      <section className="application-updates panel">
        <header className="panel-head">
          <div>
            <h2>应用更新</h2>
            <p>检查版本、下载校验并在空闲时备份重启。</p>
          </div>
        </header>
        <div className="panel-body update-body">
          <div className="update-status-line">
            {!error && (
              <span
                className="update-spinner"
                role="status"
                aria-label="正在读取更新状态"
              />
            )}
            <p className="muted">{error || "正在读取更新状态…"}</p>
          </div>
        </div>
      </section>
    );
  const busy =
    pending || ["checking", "downloading", "waiting", "restarting"].includes(status.phase) || status.automatic;
  return (
    <section className="application-updates panel" aria-label="应用更新">
      <header className="panel-head">
        <div>
          <h2>应用更新</h2>
          <p>检查版本、下载校验并在空闲时备份重启。</p>
        </div>
        <span className={`update-phase phase-${status.phase}`}>
          {phases[status.phase] ?? status.phase}
        </span>
      </header>
      <div className="panel-body update-body">
        <div className="update-version-row">
          <div className="update-version">
            <span>当前版本</span>
            <strong>{status.current}</strong>
          </div>
          {status.latest && status.available && (
            <>
              <Icon name="arrow-right" size={14} />
              <div className="update-version">
                <span>最新版本</span>
                <strong>{status.latest}</strong>
              </div>
            </>
          )}
        </div>
        {status.phase === "downloading" && (
          <div className="update-progress" role="status" aria-live="polite">
            <div
              className={`update-progress-track ${
                status.download_total ? "" : "indeterminate"
              }`}
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={status.download_total || undefined}
              aria-valuenow={status.download_total ? status.downloaded : undefined}
              aria-label="正在下载发布包"
            >
              <span
                style={
                  status.download_total
                    ? {
                        width: `${Math.min(
                          100,
                          Math.round(
                            ((status.downloaded ?? 0) / status.download_total) *
                              100,
                          ),
                        )}%`,
                      }
                    : undefined
                }
              />
            </div>
            <small>
              {status.download_total
                ? `${megabytes(status.downloaded ?? 0)} / ${megabytes(
                    status.download_total,
                  )}（${Math.min(
                    100,
                    Math.round(
                      ((status.downloaded ?? 0) / status.download_total) * 100,
                    ),
                  )}%）`
                : `${megabytes(status.downloaded ?? 0)} 已下载`}
            </small>
          </div>
        )}
        <div className="update-status-line">
          {busy && (
            <span
              className="update-spinner"
              role="status"
              aria-label="更新进行中"
            />
          )}
          <p
            className={
              status.phase === "failed" || status.phase === "rolled_back"
                ? "context-warning"
                : "muted"
            }
            role="status"
          >
            {reconnecting
              ? "更新正在重启服务，暂时断开连接。正在自动重连，请勿手动启动另一实例；成功后页面会自动刷新。"
              : status.message}
          </p>
        </div>
        {!!status.notes && (
          <section className="update-notes" aria-labelledby="update-notes-title">
            <header>
              <h3 id="update-notes-title">更新内容</h3>
              {status.latest && <span>{status.latest}</span>}
            </header>
            <div className="update-notes-body">
              <MessageMarkdown content={status.notes} />
            </div>
          </section>
        )}
        {status.available && !status.notes && (
          <p className="muted update-notes-empty">
            该版本没有提供更新说明。
          </p>
        )}
        {!status.supported && (
          <p className="muted">
            {status.mode === "docker"
              ? "此部署请手动更新镜像并重建容器，保留原数据卷。"
              : status.mode === "managed"
                ? "请通过原服务管理器停止服务、更新程序并重新启动。"
                : "源码部署请拉取代码并重新构建；独立安装包请使用官方安装脚本升级。"}
          </p>
        )}
        {status.supported && (
          <p className="muted">
            发起更新后会自动下载校验、等待现有任务结束、备份数据并重启，关闭页面也会继续。新版本启动失败时自动恢复旧版本和数据。
          </p>
        )}
        <div className="form-actions">
          <button
            type="button"
            disabled={busy || status.phase === "ready"}
            onClick={() => void action("check")}
          >
            检查新版本
          </button>
          {status.supported && (status.available || status.phase === "ready") && (
            <button
              type="button"
              className="primary"
              disabled={busy}
              onClick={() => void action("start", { confirm: true })}
            >
              更新并自动重启
            </button>
          )}
          {status.supported && ["ready", "waiting"].includes(status.phase) && (
            <button
              type="button"
              disabled={pending}
              onClick={() => void action("cancel")}
            >
              取消本次更新
            </button>
          )}
        </div>
        <BackupManager request={request} enabled={status.supported} />
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        <small>
          命令行可使用 <code>siagent update</code> 更新，或{" "}
          <code>siagent help</code> 查看帮助。
        </small>
      </div>
    </section>
  );
}

// Offline snapshots are written before every update and are never pruned, so a
// long-lived install accumulates them. They are listed on demand (sizing walks
// the tree) and can be removed one at a time or all at once.
function BackupManager({ request, enabled }: { request: Api; enabled: boolean }) {
  const [backups, setBackups] = useState<Backup[] | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState("");
  const [feedback, setFeedback] = useState("");
  const [deleting, setDeleting] = useState<Backup | null>(null);
  const [deleteAll, setDeleteAll] = useState(false);
  const [dialogError, setDialogError] = useState("");

  async function refresh() {
    setError("");
    try {
      const result = await request<{ backups: Backup[] }>("/backups");
      setBackups(result.backups);
    } catch (failure) {
      setError(
        failure instanceof Error ? failure.message : "无法读取备份列表。",
      );
      setBackups([]);
    }
  }
  useEffect(() => {
    if (enabled) void refresh();
  }, [enabled]);
  if (!enabled) return null;

  const total = (backups ?? []).reduce((sum, item) => sum + item.bytes, 0);

  async function removeOne(backup: Backup) {
    setPending(backup.name);
    setDialogError("");
    try {
      await request(`/backups/${encodeURIComponent(backup.name)}`, "DELETE");
      setBackups((values) =>
        (values ?? []).filter((item) => item.name !== backup.name),
      );
      setDeleting(null);
      setFeedback(`已删除备份 ${backup.name}。`);
    } catch (failure) {
      setDialogError(
        failure instanceof Error ? failure.message : "删除未完成，请重试。",
      );
    } finally {
      setPending("");
    }
  }
  async function removeAll() {
    setPending("all");
    setDialogError("");
    try {
      const result = await request<{ deleted: number }>(
        "/backups/delete-all",
        "POST",
        { confirm: true },
      );
      setDeleteAll(false);
      setFeedback(`已删除 ${result.deleted} 个备份。`);
      await refresh();
    } catch (failure) {
      setDialogError(
        failure instanceof Error ? failure.message : "删除未完成，请重试。",
      );
    } finally {
      setPending("");
    }
  }

  return (
    <section className="backup-manager" aria-label="更新备份">
      <header className="backup-head">
        <div>
          <h3>
            更新备份 <span>{backups?.length ?? 0}</span>
          </h3>
          <p>
            每次停机更新前的完整数据快照。备份含私密数据，不会自动删除。
            {backups && backups.length > 0 && ` 共 ${megabytes(total)}。`}
          </p>
        </div>
        <div className="backup-actions">
          <button
            type="button"
            disabled={!!pending}
            onClick={() => void refresh()}
          >
            {backups === null ? "读取中…" : "刷新"}
          </button>
          <button
            type="button"
            className="danger-ghost"
            disabled={!!pending || !backups?.length}
            onClick={() => {
              setDeleteAll(true);
              setDialogError("");
            }}
          >
            全部清理
          </button>
        </div>
      </header>
      {feedback && (
        <p className="success backup-feedback" role="status">
          {feedback}
        </p>
      )}
      {error && (
        <p className="error backup-feedback" role="alert">
          {error}
        </p>
      )}
      {backups === null ? (
        <p className="muted">正在读取备份…</p>
      ) : !backups.length ? (
        <p className="muted">暂无备份。</p>
      ) : (
        <ul className="backup-list">
          {backups.map((backup) => (
            <li key={backup.name}>
              <div className="backup-copy">
                <strong title={backup.name}>
                  {backup.version}
                  {backup.current && (
                    <span className="backup-current">最近一次</span>
                  )}
                </strong>
                <small title={backup.name}>
                  {backupTime(backup.created)} · {megabytes(backup.bytes)} ·{" "}
                  {backup.files} 个文件
                </small>
              </div>
              <button
                type="button"
                className="danger-ghost"
                disabled={!!pending}
                aria-label={`删除备份 ${backup.name}`}
                onClick={() => {
                  setDeleting(backup);
                  setDialogError("");
                }}
              >
                删除
              </button>
            </li>
          ))}
        </ul>
      )}
      {deleting && (
        <SettingsDialog
          title={`删除备份“${deleting.name}”？`}
          pending={!!pending}
          onClose={() => setDeleting(null)}
        >
          <p>
            这会删除该次更新前的完整数据快照（{megabytes(deleting.bytes)}）。
            删除后无法用这个快照回退数据。
          </p>
          {deleting.current && (
            <p className="permission-notice">
              这是最近一次更新引用的备份，也是当前记录的默认回退点。确认新版本运行正常后再删除。
            </p>
          )}
          {dialogError && (
            <p role="alert" className="error">
              {dialogError}
            </p>
          )}
          <footer>
            <button
              data-dialog-cancel
              disabled={!!pending}
              onClick={() => setDeleting(null)}
            >
              取消
            </button>
            <button
              className="danger-button"
              disabled={!!pending}
              onClick={() => void removeOne(deleting)}
            >
              {pending ? "正在删除…" : "确认删除"}
            </button>
          </footer>
        </SettingsDialog>
      )}
      {deleteAll && (
        <SettingsDialog
          title="清理全部更新备份？"
          pending={!!pending}
          onClose={() => setDeleteAll(false)}
        >
          <p>
            这会删除全部 {backups?.length ?? 0} 个快照，释放约{" "}
            {megabytes(total)}。删除后无法再用任何历史快照回退数据。
          </p>
          <p className="permission-notice">
            只删除更新前生成的备份目录；数据目录、程序版本和无关文件不受影响。
          </p>
          {dialogError && (
            <p role="alert" className="error">
              {dialogError}
            </p>
          )}
          <footer>
            <button
              data-dialog-cancel
              disabled={!!pending}
              onClick={() => setDeleteAll(false)}
            >
              取消
            </button>
            <button
              className="danger-button"
              disabled={!!pending}
              onClick={() => void removeAll()}
            >
              {pending ? "正在清理…" : "全部清理"}
            </button>
          </footer>
        </SettingsDialog>
      )}
    </section>
  );
}
