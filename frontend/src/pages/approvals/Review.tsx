import { useState } from "react";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import {
  Activity,
  ArrowLeft,
  Check,
  FileCode2,
  History,
  Play,
  RefreshCw,
  Send,
  X,
} from "lucide-react";
import { api } from "../../api/client";
import {
  Skeleton,
  statusBadgeClass,
  useAction,
  useResource,
} from "../../components/shared";
import { useAuth } from "../../context/AuthContext";
import { dbPath } from "../migrations/shared";
import type { Change, Definitions } from "../migrations/shared";

const filterOptions = [
  ["all", "Semua"],
  ["pending_review", "Menunggu Tinjauan"],
  ["approved", "Disetujui"],
  ["draft", "Draf"],
  ["terminal", "Berjalan / Selesai"],
] as const;

export function Queue() {
  const [query] = useSearchParams();
  const db = query.get("database_id");
  const { data, error, refresh } = useResource<{ requests: Change[] }>(
    (db ? dbPath(db) : "") + "/requests",
  );
  const [statusFilter, setStatusFilter] = useState<string>("all");
  const all = data?.requests ?? [];
  const filtered = all.filter((r) => {
    if (statusFilter === "all") return true;
    if (statusFilter === "terminal") {
      return !["draft", "pending_review", "approved"].includes(r.status);
    }
    return r.status === statusFilter;
  });

  return (
    <section className="m2">
      {db && (
        <Link className="back" to={dbPath(db) + "/migrations"}>
          <ArrowLeft size={15} /> Kembali ke Definisi Migrasi
        </Link>
      )}
      <div className="page-head">
        <div>
          <h1>Antrean Tinjauan & Persetujuan</h1>
          <p className="page-subtitle">
            Tinjau, setujui, atau antrekan permintaan perubahan database dari
            semua grup berakses.
          </p>
        </div>
        <div className="actions">
          <button onClick={refresh}>
            <RefreshCw size={15} /> Segarkan
          </button>
          <Link className="btn-link" to="/databases">
            Daftar Database
          </Link>
        </div>
      </div>

      {data && (
        <div className="filter-pills" role="group" aria-label="Filter status">
          {filterOptions.map(([key, label]) => (
            <button
              key={key}
              className={statusFilter === key ? "active" : ""}
              onClick={() => setStatusFilter(key)}
            >
              {label}
            </button>
          ))}
        </div>
      )}

      {!data && !error && <Skeleton />}
      {data && !filtered.length && (
        <div className="m2-panel">
          <p className="empty">
            Belum ada permintaan perubahan pada kategori ini.
          </p>
        </div>
      )}

      {filtered.map((r) => (
        <article key={r.id} className="m2-panel">
          <div className="row">
            <div>
              <h2 style={{ margin: 0 }}>
                <Link to={"/requests/" + r.id}>{r.migration_name}</Link>
              </h2>
              <div className="m2-meta-row">
                <span className="badge badge-neutral">{r.database_name}</span>
                <span className="badge badge-neutral">
                  v{r.migration_version}
                </span>
                <span className="badge badge-info">{r.action_type}</span>
                <span className={statusBadgeClass(r.status)}>{r.status}</span>
                {r.approval_stale && (
                  <span className="badge badge-danger">
                    Approval kedaluwarsa
                  </span>
                )}
              </div>
              <p style={{ fontSize: "0.84rem" }}>
                Diajukan oleh <strong>{r.requested_by_username}</strong>
              </p>
            </div>
            <div className="m2-actions" style={{ margin: 0 }}>
              {r.job_id && (
                <Link className="btn-link btn-sm" to={"/jobs/" + r.job_id}>
                  <Activity size={14} /> Pantau Job
                </Link>
              )}
              <Link className="primary btn-sm" to={"/requests/" + r.id}>
                Buka Detail
              </Link>
            </div>
          </div>
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
      <Link className="back" to="/requests">
        <ArrowLeft size={15} /> Kembali ke Antrean Tinjauan
      </Link>
      <div className="page-head">
        <div>
          <div className="row" style={{ justifyContent: "flex-start" }}>
            <h1>{r?.migration_name ?? "Permintaan Perubahan"}</h1>
            {r && <span className={statusBadgeClass(r.status)}>{r.status}</span>}
          </div>
          {r && (
            <p className="page-subtitle">
              {r.database_name} · v{r.migration_version} · Aksi{" "}
              <code>{r.action_type}</code> · Diajukan oleh{" "}
              <strong>{r.requested_by_username}</strong>
            </p>
          )}
        </div>
        {r && (
          <div className="actions">
            <Link
              className="btn-link"
              to={dbPath(r.database_id) + "/migrations"}
            >
              <FileCode2 size={15} /> Definisi
            </Link>
            <Link className="btn-link" to={dbPath(r.database_id) + "/history"}>
              <History size={15} /> Riwayat
            </Link>
            <button disabled={action.busy} onClick={refresh}>
              <RefreshCw size={15} /> Segarkan
            </button>
          </div>
        )}
      </div>

      {!r && <Skeleton />}
      {r && (
        <>
          <article className="m2-panel m2-panel-accent">
            <div className="row" style={{ marginBottom: "0.5rem" }}>
              <h2 style={{ margin: 0 }}>Pratinjau SQL Aktual</h2>
              {r.target_execution_id && (
                <span className="badge badge-neutral">
                  Target: {r.target_execution_id.slice(0, 8)}…
                </span>
              )}
            </div>
            {r.approval_stale && (
              <p className="m2-error" role="alert">
                Persetujuan tidak lagi valid karena perubahan grant database
                atau leader grup. Ajukan ulang tinjauan agar daftar persetujuan
                grup diperbarui.
              </p>
            )}
            <pre>{r.sql_preview}</pre>
          </article>

          <article className="m2-panel">
            <h2>Persetujuan Leader Grup</h2>
            {r.approvals?.length ? (
              <div className="m2-scroll" style={{ marginTop: "0.65rem" }}>
                <table>
                  <thead>
                    <tr>
                      {[
                        "Grup",
                        "Hak Akses",
                        "Leader",
                        "Keputusan",
                        "Komentar",
                      ].map((h) => (
                        <th key={h}>{h}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {r.approvals.map((a) => (
                      <tr key={a.group_id}>
                        <td>
                          <strong>{a.group_name}</strong>
                        </td>
                        <td>
                          <span
                            className={statusBadgeClass(a.required_access_level)}
                          >
                            {a.required_access_level}
                          </span>
                        </td>
                        <td>{a.leader_username}</td>
                        <td>
                          <span className={statusBadgeClass(a.decision)}>
                            {a.decision}
                          </span>
                        </td>
                        <td>{a.comment || "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p className="m2-hint">
                Belum ada daftar persetujuan grup. Klik{" "}
                <strong>Ajukan Tinjauan</strong> untuk meminta persetujuan
                leader dari semua grup yang memiliki akses ke database ini.
              </p>
            )}

            {canDecide && (
              <label style={{ marginTop: "1rem" }}>
                Komentar tinjauan (opsional)
                <textarea
                  style={{ minHeight: "82px" }}
                  placeholder="Tuliskan catatan kompatibilitas skema/data…"
                  value={comment}
                  onChange={(e) => setComment(e.target.value)}
                />
              </label>
            )}

            <div className="m2-actions" style={{ marginTop: "1rem" }}>
              {canEdit &&
                (r.status === "draft" ||
                  ((r.status === "pending_review" || r.status === "approved") &&
                    r.approval_stale)) && (
                  <button
                    className="primary"
                    disabled={action.busy}
                    onClick={() => submit("submit")}
                  >
                    <Send size={15} />
                    {r.status === "draft"
                      ? "Ajukan Tinjauan"
                      : "Ajukan Ulang Tinjauan"}
                  </button>
                )}
              {canDecide && (
                <>
                  <button
                    className="btn-success"
                    disabled={action.busy || Boolean(r.approval_stale)}
                    onClick={() => submit("approve")}
                  >
                    <Check size={15} /> Setujui
                  </button>
                  <button
                    className="btn-danger"
                    disabled={action.busy}
                    onClick={() => submit("reject")}
                  >
                    <X size={15} /> Tolak
                  </button>
                </>
              )}
              {r.status === "approved" && canEdit && (
                <button
                  className="primary"
                  disabled={action.busy || Boolean(r.approval_stale)}
                  onClick={() => submit("enqueue")}
                >
                  <Play size={15} /> Masukkan ke Antrean Eksekusi
                </button>
              )}
              {r.job_id && (
                <Link className="primary" to={"/jobs/" + r.job_id}>
                  <Activity size={15} /> Pantau Job
                </Link>
              )}
            </div>
          </article>
        </>
      )}
    </section>
  );
}
