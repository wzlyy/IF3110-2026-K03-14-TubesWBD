import { useEffect, useState, type ReactNode } from "react";
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
      {!rows.length && <p className="empty">Belum ada data.</p>}
    </div>
  );
}
export function useAction() {
  const [busy, setBusy] = useState(false),
    [notice, setNotice] = useState("");
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
  return { busy, notice, run };
}
export function Fields({
  name,
  label,
  type = "text",
  value,
  required = true,
}: {
  name: string;
  label: string;
  type?: string;
  value?: string | number;
  required?: boolean;
}) {
  return (
    <label>
      {label}
      <input name={name} type={type} defaultValue={value} required={required} />
    </label>
  );
}
