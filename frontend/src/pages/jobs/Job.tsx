import { Link, useParams } from "react-router-dom";
import {
  ArrowLeft,
  ClipboardList,
  FileCode2,
  History,
  Play,
  RefreshCw,
  RotateCcw,
} from "lucide-react";
import {
  Skeleton,
  statusBadgeClass,
  useAction,
  useResource,
} from "../../components/shared";
import { dbPath, useCreateRequest } from "../migrations/shared";
import type { Definitions, Execution } from "../migrations/shared";

interface Batch {
  batch_number: number;
  direction: string;
  rows_affected: number;
  cursor_start?: string;
  cursor_end?: string;
  target_max_cursor?: string;
  status: string;
}
interface Job {
  database_id: string;
  request_id: string;
  migration_id: string;
  action_type: string;
  request_status: string;
  execution?: Execution;
  batch_progress?: Batch[];
  can_resume_batch: boolean;
  can_revert_batch: boolean;
}

const active = (job: Job) =>
  ["pending_queue", "running", "uncertain"].includes(job.request_status);

const terminalStageLabel: Record<string, string> = {
  failed: "Gagal",
  partial: "Parsial",
  partially_reverted: "Direvert sebagian",
  uncertain: "Rekonsiliasi",
};

// Cursor coverage is measurable even when the target has gaps between keys.
function cursorProgress(batches: Batch[] = []) {
  const first = batches[0];
  const last = batches[batches.length - 1];
  if (
    !first?.cursor_start ||
    !last?.cursor_end ||
    !last.target_max_cursor ||
    last.direction !== "forward"
  )
    return undefined;
  const start = Number(first.cursor_start);
  const end = Number(last.cursor_end);
  const max = Number(last.target_max_cursor);
  if (
    ![start, end, max].every(Number.isSafeInteger) ||
    max < end ||
    end < start
  )
    return undefined;
  return (end - start + 1) / (max - start + 1);
}

export default function JobPage() {
  const { id = "" } = useParams();
  const {
    data: j,
    error,
    refresh,
  } = useResource<Job>("/jobs/" + id, { polling: active });
  const action = useAction();
  const create = useCreateRequest();
  const defs = useResource<Definitions>(
    j && !active(j) ? dbPath(j.database_id) + "/migrations" : "",
  );
  const e = j?.execution;
  const stage =
    j?.request_status === "pending_queue"
      ? 3
      : j?.request_status === "running"
        ? 4
        : 5;

  return (
    <section className="m2">
      <Link className="back" to="/requests">
        <ArrowLeft size={15} /> Kembali ke Antrean Tinjauan
      </Link>
      <div className="page-head">
        <div>
          <div className="row" style={{ justifyContent: "flex-start" }}>
            <h1>{e?.migration_name ?? "Pemantauan Job"}</h1>
            {j && (
              <span className={statusBadgeClass(j.request_status)}>
                {j.request_status}
              </span>
            )}
          </div>
          <p className="page-subtitle">
            Job ID: <code>{id}</code>
            {j ? ` · Aksi ${j.action_type}` : ""}
          </p>
        </div>
        {j && (
          <div className="actions">
            <Link className="btn-link" to={"/requests/" + j.request_id}>
              <ClipboardList size={15} /> Permintaan
            </Link>
            <Link
              className="btn-link"
              to={dbPath(j.database_id) + "/migrations"}
            >
              <FileCode2 size={15} /> Definisi
            </Link>
            <Link className="btn-link" to={dbPath(j.database_id) + "/history"}>
              <History size={15} /> Riwayat
            </Link>
            <button onClick={refresh}>
              <RefreshCw size={15} /> Segarkan
            </button>
          </div>
        )}
      </div>

      {error && (
        <div className="m2-panel">
          <button onClick={refresh}>Coba Lagi</button>
        </div>
      )}
      {!j && !error && <Skeleton />}

      {j && (
        <>
          <div className="stats-strip">
            <div className="stat-card">
              <small>Status Job</small>
              <strong>{j.request_status}</strong>
            </div>
            <div className="stat-card">
              <small>Aksi</small>
              <strong>{j.action_type}</strong>
            </div>
            <div className="stat-card">
              <small>Batch Terkomit</small>
              <strong>{e?.total_batches_committed ?? 0}</strong>
            </div>
            <div className="stat-card">
              <small>Baris Terkomit</small>
              <strong>{e?.total_rows_affected ?? 0}</strong>
            </div>
          </div>

          <article className="m2-panel m2-panel-accent">
            <h2>Siklus Hidup Eksekusi</h2>
            <ol className="m2-stages" aria-label="Siklus hidup eksekusi">
              {[
                "Draf",
                "Menunggu tinjauan",
                "Disetujui",
                "Antrean",
                "Berjalan",
                terminalStageLabel[j.request_status] ?? "Selesai",
              ].map((label, i) => (
                <li
                  key={label}
                  className={i < stage ? "done-step" : undefined}
                  aria-current={i === stage ? "step" : undefined}
                >
                  {label}
                </li>
              ))}
            </ol>

            {(e?.error_message || e?.error_sqlstate) && (
              <p className="m2-error" role="alert">
                {e.error_sqlstate ? `[SQLSTATE ${e.error_sqlstate}] ` : ""}
                {e.error_message}
              </p>
            )}

            {j.request_status === "uncertain" && (
              <p className="m2-hint">
                Hasil eksekusi sedang direkonsiliasi terhadap bukti commit di
                database target sebelum migrasi lain dapat dijalankan.
              </p>
            )}

            <div className="m2-actions">
              {e &&
                defs.data?.access_level === "VIEW_EDIT" &&
                (["resume_batch", "revert_batch"] as const).map(
                  (type) =>
                    (type === "resume_batch"
                      ? j.can_resume_batch
                      : j.can_revert_batch) &&
                    defs.data?.blocking_execution?.execution_id ===
                      e.execution_id && (
                      <button
                        key={type}
                        className={
                          type === "resume_batch" ? "primary" : "btn-danger"
                        }
                        disabled={action.busy}
                        onClick={() =>
                          void action.run(() =>
                            create(
                              j.database_id,
                              j.migration_id,
                              type,
                              e.execution_id,
                            ),
                          )
                        }
                      >
                        {type === "resume_batch" ? (
                          <>
                            <Play size={15} /> Lanjutkan Batch (Resume)
                          </>
                        ) : (
                          <>
                            <RotateCcw size={15} /> Batalkan Batch (Revert)
                          </>
                        )}
                      </button>
                    ),
                )}
            </div>
          </article>

          <article className="m2-panel">
            <div className="row" style={{ marginBottom: "0.75rem" }}>
              <h2 style={{ margin: 0 }}>Progres Terkomit</h2>
              <span className="badge badge-neutral">
                {e?.total_batches_committed ?? 0} batch ·{" "}
                {e?.total_rows_affected ?? 0} baris
              </span>
            </div>
            {j.request_status !== "success" &&
            (active(j) || j.batch_progress?.length) ? (
              <progress
                value={cursorProgress(j.batch_progress)}
                max={1}
                aria-label="Progres cursor"
              />
            ) : null}
            {j.request_status === "success" && (
              <progress value={1} max={1} aria-label="Eksekusi selesai" />
            )}
            {!!j.batch_progress?.length && (
              <div className="m2-scroll" style={{ marginTop: "1rem" }}>
                <table>
                  <thead>
                    <tr>
                      {["Batch", "Arah", "Rentang Cursor", "Baris", "Status"].map(
                        (h) => (
                          <th key={h}>{h}</th>
                        ),
                      )}
                    </tr>
                  </thead>
                  <tbody>
                    {j.batch_progress.map((b) => (
                      <tr key={b.direction + ":" + b.batch_number}>
                        <td>
                          <strong>#{b.batch_number}</strong>
                        </td>
                        <td>
                          <span className="badge badge-neutral">
                            {b.direction}
                          </span>
                        </td>
                        <td>
                          <code>
                            {b.cursor_start} – {b.cursor_end}
                          </code>
                        </td>
                        <td>{b.rows_affected}</td>
                        <td>
                          <span className={statusBadgeClass(b.status)}>
                            {b.status}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </article>
        </>
      )}
    </section>
  );
}
