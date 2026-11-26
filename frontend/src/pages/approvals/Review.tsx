import { useState } from "react";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { api } from "../../api/client";
import { useAction, useResource } from "../../components/shared";
import { useAuth } from "../../context/AuthContext";
import { dbPath } from "../migrations/shared";
import type { Change, Definitions } from "../migrations/shared";

export function Queue() {
  const [query] = useSearchParams();
  const db = query.get("database_id");
  const { data, error } = useResource<{ requests: Change[] }>(
    (db ? dbPath(db) : "") + "/requests",
  );
  return (
    <section className="m2">
      <h1>Antrean tinjauan</h1>
      {db && <Link to={dbPath(db) + "/migrations"}>Definisi migrasi</Link>}
      {!data && !error && <p>Memuat daftar permintaan…</p>}
      {data && !data.requests?.length && <p>Belum ada permintaan perubahan.</p>}
      {data?.requests?.map((r) => (
        <article key={r.id} className="m2-panel">
          <h2>
            <Link to={"/requests/" + r.id}>{r.migration_name}</Link>
          </h2>
          <p>
            {r.database_name} · v{r.migration_version} · {r.action_type} ·{" "}
            <strong>{r.status}</strong>
            {r.approval_stale ? " · approval kedaluwarsa" : ""}
          </p>
          <p>Diajukan oleh {r.requested_by_username}</p>
        </article>
      ))}
    </section>
  );
}
export function Review() {
  const { id = "" } = useParams();
  const path = "/requests/" + id;
  const { data: r, refresh } = useResource<Change>(path);
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
      (a) => a.decision === "pending" && a.leader_user_id === user?.id,
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
      <Link to="/requests">Antrean tinjauan</Link>
      <h1>{r?.migration_name ?? "Permintaan perubahan"}</h1>
      {r && (
        <>
          <article className="m2-panel">
            <h2>
              {r.action_type} · {r.status}
            </h2>
            <p>
              {r.database_name} · Diajukan oleh {r.requested_by_username}
            </p>
            {r.approval_stale && (
              <p className="m2-error" role="alert">
                Persetujuan tidak lagi valid karena perubahan grant database atau leader grup. Ajukan ulang tinjauan atau batalkan permintaan ini.
              </p>
            )}
            {r.target_execution_id && (
              <p>
                Eksekusi sasaran <code>{r.target_execution_id}</code>
              </p>
            )}
            <h3>Pratinjau SQL</h3>
            <pre>{r.sql_preview}</pre>
          </article>
          <article className="m2-panel">
            <h2>Persetujuan grup</h2>
            {r.approvals?.length ? (
              <div className="m2-scroll">
                <table>
                  <thead>
                    <tr>
                      {["Grup", "Hak akses", "Leader", "Keputusan", "Komentar"].map(
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
              <p>Belum ada keputusan grup yang tercatat.</p>
            )}
            {canDecide && (
              <label>
                Komentar tinjauan
                <textarea
                  value={comment}
                  onChange={(e) => setComment(e.target.value)}
                />
              </label>
            )}
            <div className="m2-actions">
              {canEdit &&
                (r.status === "draft" ||
                  ((r.status === "pending_review" || r.status === "approved") &&
                    r.approval_stale)) && (
                  <button disabled={action.busy} onClick={() => submit("submit")}>
                    {r.status === "draft"
                      ? "Ajukan tinjauan"
                      : "Ajukan ulang tinjauan"}
                  </button>
                )}
              {canDecide && (
                <>
                  <button
                    disabled={action.busy || Boolean(r.approval_stale)}
                    onClick={() => submit("approve")}
                  >
                    Setujui
                  </button>
                  <button
                    disabled={action.busy}
                    onClick={() => submit("reject")}
                  >
                    Tolak
                  </button>
                </>
              )}
              {r.status === "approved" && canEdit && (
                <button
                  disabled={action.busy || Boolean(r.approval_stale)}
                  onClick={() => submit("enqueue")}
                >
                  Masukkan ke antrean eksekusi
                </button>
              )}
              {canEdit &&
                ["draft", "pending_review", "approved"].includes(r.status) && (
                  <button
                    disabled={action.busy}
                    onClick={() => submit("cancel")}
                  >
                    Batalkan permintaan
                  </button>
                )}
              {r.job_id && <Link to={"/jobs/" + r.job_id}>Pantau job</Link>}
              <button disabled={action.busy} onClick={refresh}>
                Segarkan status
              </button>
            </div>
          </article>
        </>
      )}
    </section>
  );
}
