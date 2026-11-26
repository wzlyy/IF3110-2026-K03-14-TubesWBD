import { Link, useParams } from "react-router-dom";
import { useAction, useResource } from "../../components/shared";
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
  ["pending_queue", "running"].includes(job.request_status);
// Cursor coverage is measurable even when the target has gaps between keys.
function cursorProgress(batches: Batch[] = []) {
  const first = batches[0],
    last = batches[batches.length - 1];
  if (
    !first?.cursor_start ||
    !last?.cursor_end ||
    !last.target_max_cursor ||
    last.direction !== "forward"
  )
    return undefined;
  const start = Number(first.cursor_start),
    end = Number(last.cursor_end),
    max = Number(last.target_max_cursor);
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
      <h1>Pemantauan job</h1>
      {error && <button onClick={refresh}>Coba lagi</button>}
      {j && (
        <>
          <div className="m2-actions">
            <Link to={"/requests/" + j.request_id}>Permintaan perubahan</Link>
            <Link to={dbPath(j.database_id) + "/history"}>
              Riwayat eksekusi
            </Link>
          </div>
          <article className="m2-panel">
            <h2>{e?.migration_name ?? j.action_type}</h2>
            <p>
              Job <code>{id}</code>
            </p>
            <ol className="m2-stages" aria-label="Siklus hidup eksekusi">
              {[
                "Draf",
                "Menunggu tinjauan",
                "Disetujui",
                "Antrean",
                "Berjalan",
                "Selesai",
              ].map((label, i) => (
                <li key={label} aria-current={i === stage ? "step" : undefined}>
                  {label}
                </li>
              ))}
            </ol>
            <p role="status">
              Status <strong className="m2-badge">{j.request_status}</strong>
            </p>
            {(e?.error_message || e?.error_sqlstate) && (
              <p className="m2-error">
                {e.error_sqlstate ? `[${e.error_sqlstate}] ` : ""}
                {e.error_message}
              </p>
            )}
            {j.request_status === "uncertain" && (
              <p>
                Hasil eksekusi memerlukan rekonsiliasi sebelum migrasi lain
                dapat dijalankan.
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
                        {type === "resume_batch"
                          ? "Lanjutkan batch"
                          : "Batalkan batch (revert)"}
                      </button>
                    ),
                )}
            </div>
          </article>
          <article className="m2-panel">
            <h2>Progres terkomit</h2>
            <p>
              {e?.total_batches_committed ?? 0} batch ·{" "}
              {e?.total_rows_affected ?? 0} baris
            </p>
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
              <div className="m2-scroll">
                <table>
                  <thead>
                    <tr>
                      {["Batch", "Rentang cursor", "Baris", "Status"].map(
                        (h) => (
                          <th key={h}>{h}</th>
                        ),
                      )}
                    </tr>
                  </thead>
                  <tbody>
                    {j.batch_progress.map((b) => (
                      <tr key={b.direction + ":" + b.batch_number}>
                        <td>{b.batch_number}</td>
                        <td>
                          {b.cursor_start} – {b.cursor_end}
                        </td>
                        <td>{b.rows_affected}</td>
                        <td>{b.status}</td>
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
