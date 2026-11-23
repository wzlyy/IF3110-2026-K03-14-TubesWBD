import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  dbPath,
  Feedback,
  linearAllowed,
  useAction,
  useCreateRequest,
  useResource,
} from "./shared";
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
      <h2 id="snapshot-title">Executed SQL snapshot</h2>
      <p>{record.execution_id}</p>
      <pre>{record.executed_sql_snapshot}</pre>
      <button autoFocus onClick={close}>
        Close
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
      <h1>Execution history</h1>
      <Link to={base + "/migrations"}>Migration definitions</Link>
      <div className="m2-panel">
        <label>
          <input
            type="checkbox"
            checked={hide}
            onChange={(e) => setHide(e.target.checked)}
          />{" "}
          Hide rollback
        </label>
        {history.data && (
          <p>
            {history.data.total_attempts} attempts · {history.data.hidden_count}{" "}
            hidden
          </p>
        )}
        {s?.access_level === "VIEW_EDIT" &&
          forward.map((d) => (
            <button
              key={d.id}
              disabled={action.busy}
              onClick={() => void action.run(() => create(db, d.id, "forward"))}
            >
              Request forward · {d.name}
            </button>
          ))}
      </div>
      <Feedback error={history.error || defs.error || action.error} />
      {!history.data && !history.error && <p>Loading history…</p>}
      {history.data && (
        <div
          className="m2-scroll"
          tabIndex={0}
          aria-label="Execution history table"
        >
          <table>
            <thead>
              <tr>
                {[
                  "Migration",
                  "Execution",
                  "Action",
                  "Executed by",
                  "Started",
                  "Status",
                  "Committed batches / rows",
                  "Actions",
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
                  <td>{e.execution_id}</td>
                  <td>{e.action_type}</td>
                  <td>{e.executed_by_username}</td>
                  <td>{new Date(e.started_at).toLocaleString()}</td>
                  <td>{e.status}</td>
                  <td>
                    {e.total_batches_committed} / {e.total_rows_affected}
                  </td>
                  <td>
                    <div className="m2-actions">
                      <button onClick={() => setSnapshot(e)}>View SQL</button>
                      <Link to={"/jobs/" + e.job_id}>Job</Link>
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
                            Request rollback
                          </button>
                        )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {!history.data.executions?.length && <p>No executions to show.</p>}
        </div>
      )}
      {snapshot && (
        <Snapshot record={snapshot} close={() => setSnapshot(null)} />
      )}
    </section>
  );
}
