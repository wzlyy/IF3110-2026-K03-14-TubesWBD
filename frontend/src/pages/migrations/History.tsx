import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useAction, useResource } from "../../components/shared";
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
      <h2 id="snapshot-title">Snapshot SQL yang dieksekusi</h2>
      <p>
        <code>{record.execution_id}</code>
        {record.attempt_number != null &&
          ` · Percobaan #${record.attempt_number}`}
      </p>
      {record.reverted_execution_id && (
        <p>
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
      <button autoFocus onClick={close}>
        Tutup
      </button>
    </dialog>
  );
}
export default function History() {
  const { db = "" } = useParams();
  const base = dbPath(db);
  const [hide, setHide] = useState(false),
    [snapshot, setSnapshot] = useState<Execution | null>(null);
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
    s?.definitions?.filter(
      (d) =>
        linearAllowed(d, s) &&
        (s.rolled_back_migrations[d.id] || s.failed_migrations[d.id]),
    ) ?? [];
  return (
    <section className="m2">
      <h1>Riwayat eksekusi</h1>
      <Link to={base + "/migrations"}>Definisi migrasi</Link>
      <div className="m2-panel">
        <label>
          <input
            type="checkbox"
            checked={hide}
            onChange={(e) => setHide(e.target.checked)}
          />{" "}
          Sembunyikan rollback (Hide rollback)
        </label>
        {history.data && (
          <p>
            {history.data.total_attempts} percobaan ·{" "}
            {history.data.hidden_count} disembunyikan
          </p>
        )}
        {s?.access_level === "VIEW_EDIT" &&
          forward.map((d) => (
            <button
              key={d.id}
              disabled={action.busy}
              onClick={() => void action.run(() => create(db, d.id, "forward"))}
            >
              Ajukan forward · {d.name}
            </button>
          ))}
      </div>
      {!history.data && !history.error && <p>Memuat riwayat eksekusi…</p>}
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
                  "Batch / baris terkomit",
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
                    {e.migration_name}
                    <small>{e.migration_id}</small>v{e.migration_version} · r
                    {e.migration_revision}
                  </td>
                  <td>
                    {e.execution_id}
                    {e.attempt_number != null && (
                      <small>Percobaan #{e.attempt_number}</small>
                    )}
                  </td>
                  <td>
                    {e.action_type}
                    {e.reverted_execution_id && (
                      <small>
                        membatalkan <code>{e.reverted_execution_id}</code>
                      </small>
                    )}
                    {e.parent_execution_id &&
                      e.parent_execution_id !== e.reverted_execution_id && (
                        <small>
                          lanjutan <code>{e.parent_execution_id}</code>
                        </small>
                      )}
                  </td>
                  <td>{e.executed_by_username}</td>
                  <td>{new Date(e.started_at).toLocaleString()}</td>
                  <td>
                    {e.status}
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
                    <div className="m2-actions">
                      <button onClick={() => setSnapshot(e)}>Lihat SQL</button>
                      <Link to={"/jobs/" + e.job_id}>Detail job</Link>
                      {s?.access_level === "VIEW_EDIT" &&
                        !s.blocking_execution &&
                        s.active_tip?.execution_id === e.execution_id &&
                        s.definitions.some(
                          (d) => d.id === e.migration_id && !d.is_irreversible,
                        ) && (
                          <button
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
                            Ajukan rollback
                          </button>
                        )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {!history.data.executions?.length && (
            <p>Belum ada riwayat eksekusi untuk ditampilkan.</p>
          )}
        </div>
      )}
      {snapshot && (
        <Snapshot record={snapshot} close={() => setSnapshot(null)} />
      )}
    </section>
  );
}
