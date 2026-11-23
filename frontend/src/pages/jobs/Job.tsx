import { Link, useParams } from "react-router-dom";
import {
  dbPath,
  Feedback,
  useAction,
  useCreateRequest,
  useResource,
} from "../migrations/shared";
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
  const { data: j, error, refresh } = useResource<Job>("/jobs/" + id, active);
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
      <h1>Job monitor</h1>
      <Feedback error={error || defs.error || action.error} />
      {error && <button onClick={refresh}>Retry</button>}
      {j && (
        <>
          <div className="m2-actions">
            <Link to={"/requests/" + j.request_id}>Change request</Link>
            <Link to={dbPath(j.database_id) + "/history"}>
              Execution history
            </Link>
          </div>
          <article className="m2-panel">
            <h2>{e?.migration_name ?? j.action_type}</h2>
            <p>
              Job <code>{id}</code>
            </p>
            <ol className="m2-stages" aria-label="Execution lifecycle">
              {[
                "Draft",
                "Pending review",
                "Approved",
                "Queue",
                "Running",
                "Terminal",
              ].map((label, i) => (
                <li key={label} aria-current={i === stage ? "step" : undefined}>
                  {label}
                </li>
              ))}
            </ol>
            <p role="status">
              Status <strong className="m2-badge">{j.request_status}</strong>
            </p>
            {e?.error_message && <p className="m2-error">{e.error_message}</p>}
            {j.request_status === "uncertain" && (
              <p>
                Execution outcome requires reconciliation before another
                migration can proceed.
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
                          ? "Resume batch"
                          : "Revert batch"}
                      </button>
                    ),
                )}
            </div>
          </article>
          <article className="m2-panel">
            <h2>Committed progress</h2>
            <p>
              {e?.total_batches_committed ?? 0} batches ·{" "}
              {e?.total_rows_affected ?? 0} rows
            </p>
            {j.request_status !== "success" &&
            (active(j) || j.batch_progress?.length) ? (
              <progress
                value={cursorProgress(j.batch_progress)}
                max={1}
                aria-label="Cursor progress"
              />
            ) : null}
            {j.request_status === "success" && (
              <progress value={1} max={1} aria-label="Execution complete" />
            )}
            {!!j.batch_progress?.length && (
              <div className="m2-scroll">
                <table>
                  <thead>
                    <tr>
                      {["Batch", "Cursor range", "Rows", "Status"].map((h) => (
                        <th key={h}>{h}</th>
                      ))}
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
