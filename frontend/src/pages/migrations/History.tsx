import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Code2,
  ExternalLink,
  FastForward,
  RefreshCw,
  RotateCcw,
} from "lucide-react";
import {
  DatabaseSubNav,
  Skeleton,
  statusBadgeClass,
  useAction,
  useResource,
} from "../../components/shared";
import { dbPath, linearAllowed, useCreateRequest } from "./shared";
import type { Definitions, Execution } from "./shared";

function Snapshot({ record, close }: { record: Execution; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const el = ref.current;
    el?.showModal();
    return () => el?.close();
  }, []);
  return (
    <dialog ref={ref} onCancel={close} aria-labelledby="snapshot-title">
      <div className="row" style={{ marginBottom: "0.75rem" }}>
        <h2 id="snapshot-title" style={{ margin: 0 }}>
          Snapshot SQL yang Dieksekusi
        </h2>
        <span className={statusBadgeClass(record.status)}>{record.status}</span>
      </div>
      <p style={{ marginBottom: "0.5rem" }}>
        <code>{record.execution_id}</code>
        {record.attempt_number != null &&
          ` · Percobaan #${record.attempt_number}`}
      </p>
      {record.reverted_execution_id && (
        <p style={{ marginBottom: "0.5rem" }}>
          Membatalkan eksekusi: <code>{record.reverted_execution_id}</code>
        </p>
      )}
      {(record.error_message || record.error_sqlstate) && (
        <p className="m2-error" role="alert">
          {record.error_sqlstate ? `SQLSTATE ${record.error_sqlstate}: ` : ""}
          {record.error_message}
        </p>
      )}
      <pre>{record.executed_sql_snapshot}</pre>
      <div className="m2-actions" style={{ justifyContent: "flex-end" }}>
        <button className="primary" autoFocus onClick={close}>
          Tutup
        </button>
      </div>
    </dialog>
  );
}

export default function History() {
  const { db = "" } = useParams();
  const base = dbPath(db);
  const [hide, setHide] = useState(false);
  const [snapshot, setSnapshot] = useState<Execution | null>(null);
  const history = useResource<{
    executions: Execution[];
    total_attempts: number;
    hidden_count: number;
  }>(base + "/history?hide_rollback=" + hide);
  const defs = useResource<Definitions>(base + "/migrations");
  const action = useAction();
  const create = useCreateRequest();
  const s = defs.data;
  const forward =
    s && !s.blocking_execution
      ? (s.definitions ?? []).filter(
          (d) =>
            linearAllowed(d, s) &&
            (s.rolled_back_migrations[d.id] || s.failed_migrations[d.id]),
        )
      : [];

  return (
    <section className="m2">
      <DatabaseSubNav
        dbId={db}
        title="Riwayat Eksekusi"
        subtitle="Catatan append-only seluruh attempt migrasi (berhasil, gagal, rollback, forward, dan batch)."
        accessLevel={s?.access_level}
        actions={
          <button
            onClick={() => {
              history.refresh();
              defs.refresh();
            }}
          >
            <RefreshCw size={15} /> Segarkan
          </button>
        }
      />

      {history.data && (
        <div className="stats-strip">
          <div className="stat-card">
            <small>Total Percobaan</small>
            <strong>{history.data.total_attempts}</strong>
          </div>
          <div className="stat-card">
            <small>Disembunyikan</small>
            <strong>{history.data.hidden_count}</strong>
          </div>
          <div className="stat-card">
            <small>Active Tip</small>
            <strong>
              {s?.active_tip ? `v${s.active_tip.migration_version}` : "—"}
            </strong>
          </div>
        </div>
      )}

      <div className="m2-panel">
        <div className="row">
          <label className="check" style={{ margin: 0 }}>
            <input
              type="checkbox"
              checked={hide}
              onChange={(e) => setHide(e.target.checked)}
            />
            Sembunyikan rollback (Hide rollback)
          </label>
          {s?.access_level === "VIEW_EDIT" && forward.length > 0 && (
            <div className="m2-actions" style={{ margin: 0 }}>
              {forward.map((d) => (
                <button
                  key={d.id}
                  className="primary btn-sm"
                  disabled={action.busy}
                  onClick={() =>
                    void action.run(() => create(db, d.id, "forward"))
                  }
                >
                  <FastForward size={14} /> Ajukan Forward · {d.name}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      {!history.data && !history.error && <Skeleton />}
      {history.data && (
        <div
          className="m2-scroll"
          tabIndex={0}
          aria-label="Tabel riwayat eksekusi"
        >
          <table>
            <thead>
              <tr>
                {[
                  "Migrasi",
                  "ID Eksekusi",
                  "Aksi",
                  "Dieksekusi oleh",
                  "Waktu mulai",
                  "Status",
                  "Batch / baris",
                  "Tindakan",
                ].map((label) => (
                  <th key={label}>{label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(history.data.executions ?? []).map((e) => (
                <tr key={e.execution_id}>
                  <td>
                    <strong>{e.migration_name}</strong>
                    <small>
                      v{e.migration_version} · r{e.migration_revision}
                    </small>
                    <small>
                      <code>{e.migration_id.slice(0, 8)}…</code>
                    </small>
                  </td>
                  <td>
                    <code>{e.execution_id.slice(0, 8)}…</code>
                    {e.attempt_number != null && (
                      <small>Percobaan #{e.attempt_number}</small>
                    )}
                  </td>
                  <td>
                    <span className="badge badge-neutral">{e.action_type}</span>
                    {e.reverted_execution_id && (
                      <small>
                        membatalkan{" "}
                        <code>{e.reverted_execution_id.slice(0, 8)}…</code>
                      </small>
                    )}
                    {e.parent_execution_id &&
                      e.parent_execution_id !== e.reverted_execution_id && (
                        <small>
                          lanjutan{" "}
                          <code>{e.parent_execution_id.slice(0, 8)}…</code>
                        </small>
                      )}
                  </td>
                  <td>{e.executed_by_username}</td>
                  <td>{new Date(e.started_at).toLocaleString()}</td>
                  <td>
                    <span className={statusBadgeClass(e.status)}>
                      {e.status}
                    </span>
                    {(e.error_message || e.error_sqlstate) && (
                      <small className="m2-error">
                        {e.error_sqlstate ? `[${e.error_sqlstate}] ` : ""}
                        {e.error_message}
                      </small>
                    )}
                  </td>
                  <td>
                    {e.total_batches_committed} / {e.total_rows_affected}
                  </td>
                  <td>
                    <div className="m2-actions" style={{ margin: 0 }}>
                      <button
                        className="btn-sm"
                        onClick={() => setSnapshot(e)}
                      >
                        <Code2 size={13} /> Lihat SQL
                      </button>
                      <Link className="btn-link btn-sm" to={"/jobs/" + e.job_id}>
                        <ExternalLink size={13} /> Detail Job
                      </Link>
                      {s?.access_level === "VIEW_EDIT" &&
                        !s.blocking_execution &&
                        s.active_tip?.execution_id === e.execution_id &&
                        s.definitions.some(
                          (d) => d.id === e.migration_id && !d.is_irreversible,
                        ) && (
                          <button
                            className="btn-sm btn-danger"
                            disabled={action.busy}
                            onClick={() =>
                              void action.run(() =>
                                create(
                                  db,
                                  e.migration_id,
                                  "rollback",
                                  e.execution_id,
                                ),
                              )
                            }
                          >
                            <RotateCcw size={13} /> Ajukan Rollback
                          </button>
                        )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {!history.data.executions?.length && (
            <p className="empty">Belum ada riwayat eksekusi untuk ditampilkan.</p>
          )}
        </div>
      )}
      {snapshot && (
        <Snapshot record={snapshot} close={() => setSnapshot(null)} />
      )}
    </section>
  );
}
