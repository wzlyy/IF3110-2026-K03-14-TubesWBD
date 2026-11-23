import { useEffect, useState, type ReactNode } from "react";
import { api, clearError } from "../api/client";
export function useResource<T>(
  path: string,
  revision = 0,
): { data?: T; error?: string } {
  const [state, setState] = useState<{
    path: string;
    revision: number;
    data?: T;
    error?: string;
  }>();
  useEffect(() => {
    const controller = new AbortController();
    api<T>(path, { signal: controller.signal })
      .then((data) => {
        if (!controller.signal.aborted) setState({ path, revision, data });
      })
      .catch((e) => {
        if (!controller.signal.aborted)
          setState({ path, revision, error: e.message });
      });
    return () => controller.abort();
  }, [path, revision]);
  return state?.path === path && state.revision === revision ? state : {};
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
  async function run(fn: () => Promise<unknown>, success: string) {
    if (busy) return;
    setBusy(true);
    setNotice("");
    clearError();
    try {
      await fn();
      setNotice(success);
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
