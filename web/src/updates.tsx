import { useEffect, useRef, useState } from "react";
import { Icon } from "./icons";
import { MessageMarkdown } from "./message-markdown";

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
          {status.latest && (
            <>
              <Icon name="arrow-right" size={14} />
              <div className="update-version">
                <span>最新版本</span>
                <strong>{status.latest}</strong>
              </div>
            </>
          )}
        </div>
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
        {status.backup && (
          <p className="muted">
            最近更新备份：<code>{status.backup}</code>
            。备份含私密数据，不会自动删除；请按需要离线保存或清理。
          </p>
        )}
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
