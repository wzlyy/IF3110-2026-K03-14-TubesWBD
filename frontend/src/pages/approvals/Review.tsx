import { useState } from "react";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { api } from "../../api/client";
import { useAuth } from "../../context/AuthContext";
import { dbPath, Feedback, useAction, useResource } from "../migrations/shared";
import type { Change, Definitions } from "../migrations/shared";

export function Queue() {
  const [query] = useSearchParams();
  const db = query.get("database_id");
  const { data, error } = useResource<{ requests: Change[] }>(
    (db ? dbPath(db) : "") + "/requests",
  );
  return (
    <section className="m2">
      <h1>Review queue</h1>
      {db && <Link to={dbPath(db) + "/migrations"}>Migration definitions</Link>}
      <Feedback error={error} />
      {!data && !error && <p>Loading requests…</p>}
      {data && !data.requests?.length && <p>No change requests.</p>}
      {data?.requests?.map((r) => (
        <article key={r.id} className="m2-panel">
          <h2>
            <Link to={"/requests/" + r.id}>{r.migration_name}</Link>
          </h2>
          <p>
            {r.database_name} · v{r.migration_version} · {r.action_type} ·{" "}
            <strong>{r.status}</strong>
          </p>
          <p>Requested by {r.requested_by_username}</p>
        </article>
      ))}
    </section>
  );
}
export function Review() {
  const { id = "" } = useParams();
  const path = "/requests/" + id;
  const { data: r, error, refresh } = useResource<Change>(path);
  const defs = useResource<Definitions>(
    r ? dbPath(r.database_id) + "/migrations" : "",
  );
  const { user } = useAuth();
  const [comment, setComment] = useState("");
  const action = useAction();
  const navigate = useNavigate();
  const canEdit = defs.data?.access_level === "VIEW_EDIT";
  const canDecide =
    r?.status === "pending_review" &&
    r.approvals?.some(
      (a) =>
        a.decision === "pending" &&
        a.leader_user_id === user?.id &&
        user?.leader_group_ids?.includes(a.group_id),
    );
  function submit(verb: string) {
    void action.run(async () => {
      const result = await api<{ job_id?: string }>(path + "/" + verb, {
        method: "POST",
        body: ["approve", "reject"].includes(verb)
          ? JSON.stringify({ comment })
          : undefined,
      });
      if (verb === "enqueue" && result.job_id)
        navigate("/jobs/" + result.job_id);
      else {
        setComment("");
        refresh();
      }
    });
  }
  return (
    <section className="m2">
      <Link to="/requests">Review queue</Link>
      <h1>{r?.migration_name ?? "Change request"}</h1>
      <Feedback error={error || defs.error || action.error} />
      {r && (
        <>
          <article className="m2-panel">
            <h2>
              {r.action_type} · {r.status}
            </h2>
            <p>
              {r.database_name} · Requested by {r.requested_by_username}
            </p>
            {r.target_execution_id && (
              <p>
                Target execution <code>{r.target_execution_id}</code>
              </p>
            )}
            <h3>SQL preview</h3>
            <pre>{r.sql_preview}</pre>
          </article>
          <article className="m2-panel">
            <h2>Group approvals</h2>
            {r.approvals?.length ? (
              <div className="m2-scroll">
                <table>
                  <thead>
                    <tr>
                      {["Group", "Access", "Leader", "Decision", "Comment"].map(
                        (h) => (
                          <th key={h}>{h}</th>
                        ),
                      )}
                    </tr>
                  </thead>
                  <tbody>
                    {r.approvals.map((a) => (
                      <tr key={a.group_id}>
                        <td>{a.group_name}</td>
                        <td>{a.required_access_level}</td>
                        <td>{a.leader_username}</td>
                        <td>{a.decision}</td>
                        <td>{a.comment}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p>No group decisions recorded.</p>
            )}
            {canDecide && (
              <label>
                Review comment
                <textarea
                  value={comment}
                  onChange={(e) => setComment(e.target.value)}
                />
              </label>
            )}
            <div className="m2-actions">
              {r.status === "draft" && canEdit && (
                <button disabled={action.busy} onClick={() => submit("submit")}>
                  Submit review
                </button>
              )}
              {canDecide && (
                <>
                  <button
                    disabled={action.busy}
                    onClick={() => submit("approve")}
                  >
                    Approve
                  </button>
                  <button
                    disabled={action.busy}
                    onClick={() => submit("reject")}
                  >
                    Reject
                  </button>
                </>
              )}
              {r.status === "approved" && canEdit && (
                <button
                  disabled={action.busy}
                  onClick={() => submit("enqueue")}
                >
                  Enqueue execution
                </button>
              )}
              {r.job_id && <Link to={"/jobs/" + r.job_id}>Monitor job</Link>}
              <button disabled={action.busy} onClick={refresh}>
                Refresh status
              </button>
            </div>
          </article>
        </>
      )}
    </section>
  );
}
