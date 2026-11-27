import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link, NavLink } from "react-router-dom";
import {
  ArrowLeft,
  ClipboardList,
  FileCode2,
  GitCompare,
  History,
  Layers,
  Table2,
  TerminalSquare,
} from "lucide-react";
import { api, clearError } from "../api/client";

export interface ResourceOptions<T> {
  revision?: number;
  polling?: (value: T) => boolean;
  intervalMs?: number;
}

export function useResource<T>(
  path: string,
  revisionOrOptions: number | ResourceOptions<T> = 0,
): { data?: T; error?: string; refresh: () => void } {
  const externalRevision =
    typeof revisionOrOptions === "number"
      ? revisionOrOptions
      : (revisionOrOptions.revision ?? 0);
  const polling =
    typeof revisionOrOptions === "number"
      ? undefined
      : revisionOrOptions.polling;
  const intervalMs =
    typeof revisionOrOptions === "number"
      ? 2000
      : (revisionOrOptions.intervalMs ?? 2000);
  const [localRevision, setLocalRevision] = useState(0);
  const revision = externalRevision + localRevision;
  const [state, setState] = useState<{
    path: string;
    revision: number;
    data?: T;
    error?: string;
  }>();
  useEffect(() => {
    if (!path) return;
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();
    const load = async () => {
      try {
        const data = await api<T>(path, { signal: controller.signal });
        if (!alive || controller.signal.aborted) return;
        setState({ path, revision, data });
        if (polling?.(data)) timer = setTimeout(() => void load(), intervalMs);
      } catch (e) {
        if (!alive || controller.signal.aborted) return;
        setState({
          path,
          revision,
          error: e instanceof Error ? e.message : String(e),
        });
      }
    };
    void load();
    return () => {
      alive = false;
      clearTimeout(timer);
      controller.abort();
    };
  }, [path, revision, polling, intervalMs]);
  const current =
    state?.path === path && state.revision === revision
      ? { data: state.data, error: state.error }
      : {};
  return {
    ...current,
    refresh: () => {
      clearError();
      setLocalRevision((n) => n + 1);
    },
  };
}

export function Skeleton() {
  return (
    <div className="skeleton" role="status" aria-label="Memuat data">
      <i />
      <i />
      <i />
    </div>
  );
}

export function GridTable({
  columns,
  rows,
}: {
  columns: string[];
  rows: ReactNode[][];
}) {
  return (
    <div className="overflow-x-auto table-scroll">
      <table>
        <thead>
          <tr>
            {columns.map((c, i) => (
              <th key={i}>{c}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i}>
              {row.map((cell, j) => (
                <td key={j}>{cell}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {!rows.length && <p className="empty">Belum ada data untuk ditampilkan.</p>}
    </div>
  );
}

export function useAction() {
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  async function run(fn: () => Promise<unknown>, success = "") {
    if (busy) return;
    setBusy(true);
    setNotice("");
    clearError();
    try {
      await fn();
      if (success) setNotice(success);
    } catch {
      /* Shared API banner displays the server message. */
    } finally {
      setBusy(false);
    }
  }
  return { busy, notice, clearNotice: () => setNotice(""), run };
}

export function NoticeBanner({
  kind,
  message,
  durationMs,
  onClose,
}: {
  kind: "error" | "success";
  message: string;
  durationMs: number;
  onClose: () => void;
}) {
  const [paused, setPaused] = useState(false);
  const remainingRef = useRef(durationMs);
  const startedAtRef = useRef(Date.now());
  const lastKeyRef = useRef("");
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    const key = `${kind}:${message}:${durationMs}`;
    if (lastKeyRef.current !== key) {
      lastKeyRef.current = key;
      remainingRef.current = durationMs;
      setPaused(false);
    }
    if (!message || paused) return;
    startedAtRef.current = Date.now();
    const timer = setTimeout(() => {
      onCloseRef.current();
    }, Math.max(0, remainingRef.current));
    return () => {
      clearTimeout(timer);
      const elapsed = Date.now() - startedAtRef.current;
      remainingRef.current = Math.max(0, remainingRef.current - elapsed);
    };
  }, [kind, message, durationMs, paused]);

  if (!message) return null;

  const isError = kind === "error";
  return (
    <div
      key={message}
      role={isError ? "alert" : "status"}
      className={isError ? "error-banner toast-banner" : "success toast-banner"}
      data-paused={paused ? "true" : "false"}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      <span className="toast-text">{message}</span>
      <button
        type="button"
        className="toast-close"
        onClick={onClose}
        aria-label="Tutup pesan"
      >
        ×
      </button>
      <span
        className="toast-progress"
        style={{ animationDuration: `${durationMs}ms` }}
        aria-hidden="true"
      />
    </div>
  );
}

export function Fields({
  name,
  label,
  type = "text",
  value,
  required = true,
  placeholder,
}: {
  name: string;
  label: string;
  type?: string;
  value?: string | number;
  required?: boolean;
  placeholder?: string;
}) {
  return (
    <label>
      {label}
      <input
        name={name}
        type={type}
        defaultValue={value}
        required={required}
        placeholder={placeholder}
      />
    </label>
  );
}

export function statusBadgeClass(status: string): string {
  switch (status.toLowerCase()) {
    case "success":
    case "approved":
    case "committed":
    case "aktif":
    case "view_edit":
      return "badge badge-success";
    case "failed":
    case "rejected":
    case "nonaktif":
      return "badge badge-danger";
    case "pending_review":
    case "pending_queue":
    case "pending":
    case "partial":
    case "partially_reverted":
    case "uncertain":
    case "held_partial":
    case "held_uncertain":
      return "badge badge-warning";
    case "view_only":
    case "reverted":
      return "badge badge-info";
    case "running":
    case "batch":
      return "badge";
    default:
      return "badge badge-neutral";
  }
}

const dbSubTabs = [
  ["schema", "Skema", Layers],
  ["data", "Data", Table2],
  ["explain", "Explain", TerminalSquare],
  ["diff", "Schema Diff", GitCompare],
  ["migrations", "Migrasi", FileCode2],
  ["history", "Riwayat", History],
] as const;

export function DatabaseSubNav({
  dbId,
  title,
  subtitle,
  accessLevel,
  actions,
}: {
  dbId: string;
  title: string;
  subtitle?: string;
  accessLevel?: string;
  actions?: ReactNode;
}) {
  const base = "/databases/" + encodeURIComponent(dbId);
  return (
    <>
      <Link className="back" to="/databases">
        <ArrowLeft size={15} /> Semua Database
      </Link>
      <div className="page-head">
        <div>
          <div className="row" style={{ justifyContent: "flex-start" }}>
            <h1>{title}</h1>
            {accessLevel && (
              <span className={statusBadgeClass(accessLevel)}>
                {accessLevel}
              </span>
            )}
          </div>
          {subtitle && <p className="page-subtitle">{subtitle}</p>}
        </div>
        <div className="actions">
          <Link
            className="btn-link"
            to={"/requests?database_id=" + encodeURIComponent(dbId)}
          >
            <ClipboardList size={15} /> Antrean Tinjauan
          </Link>
          {actions}
        </div>
      </div>
      <nav className="tabs" aria-label="Menu database">
        {dbSubTabs.map(([path, label, Icon]) => (
          <NavLink key={path} to={base + "/" + path}>
            <Icon size={15} />
            {label}
          </NavLink>
        ))}
      </nav>
    </>
  );
}
