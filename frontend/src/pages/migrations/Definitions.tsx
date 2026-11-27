import { useState } from "react";
import { Link, useLocation, useParams } from "react-router-dom";
import {
  AlertTriangle,
  CheckCircle2,
  Edit3,
  Lock,
  Plus,
  RefreshCw,
  Send,
} from "lucide-react";
import { api } from "../../api/client";
import {
  DatabaseSubNav,
  NoticeBanner,
  Skeleton,
  statusBadgeClass,
  useAction,
  useResource,
} from "../../components/shared";
import { dbPath, linearAllowed, useCreateRequest } from "./shared";
import type { Draft, Definition, Definitions } from "./shared";

const empty: Draft = {
  name: "",
  description: "",
  sql_up: "",
  sql_down: "",
  is_irreversible: false,
  irreversible_reason: "",
  execution_mode: "normal",
  batch_table: "",
  batch_cursor_column: "",
  batch_size: 1000,
};

function Editor({
  db,
  definition,
  initialSQL,
  done,
  cancel,
}: {
  db: string;
  definition?: Definition;
  initialSQL?: string;
  done: () => void;
  cancel: () => void;
}) {
  const [draft, setDraft] = useState<Draft>(() => {
    if (!definition) return { ...empty, sql_up: initialSQL ?? "" };
    return {
      name: definition.name,
      description: definition.description,
      sql_up: definition.sql_up,
      sql_down: definition.sql_down ?? "",
      is_irreversible: definition.is_irreversible,
      irreversible_reason: definition.irreversible_reason ?? "",
      execution_mode: definition.execution_mode,
      batch_table: definition.batch_table ?? "",
      batch_cursor_column: definition.batch_cursor_column ?? "",
      batch_size: definition.batch_size ?? empty.batch_size,
    };
  });
  const [message, setMessage] = useState("");
  const { busy, run } = useAction();
  const path = dbPath(db) + "/migrations";

  function set<K extends keyof Draft>(k: K, v: Draft[K]) {
    setMessage("");
    setDraft((prev) => ({ ...prev, [k]: v }));
  }

  const body = JSON.stringify({
    ...draft,
    batch_size: draft.execution_mode === "batch" ? draft.batch_size : null,
  });

  return (
    <form
      className="m2-panel m2-panel-accent"
      onSubmit={(e) => {
        e.preventDefault();
        void run(async () => {
          await api(path + (definition ? "/" + definition.id : ""), {
            method: definition ? "PUT" : "POST",
            body,
          });
          done();
        });
      }}
    >
      <h2>{definition ? `Ubah Draf · ${definition.name}` : "Buat Definisi Migrasi Baru"}</h2>
      <fieldset disabled={busy}>
        <div className="m2-form-grid">
          <label>
            Nama migrasi
            <input
              required
              placeholder="mis. add_orders_index"
              value={draft.name}
              onChange={(e) => set("name", e.target.value)}
            />
          </label>
          <label>
            Mode eksekusi
            <select
              value={draft.execution_mode}
              onChange={(e) => set("execution_mode", e.target.value)}
            >
              <option value="normal">Normal (satu transaksi asinkron)</option>
              <option value="batch">Batch (streaming keyset cursor)</option>
            </select>
          </label>
        </div>
        <label>
          Deskripsi perubahan
          <textarea
            style={{ minHeight: "76px" }}
            placeholder="Jelaskan dampak skema atau data untuk tinjauan leader grup…"
            value={draft.description}
            onChange={(e) => set("description", e.target.value)}
          />
        </label>
        <label>
          SQL Up (menerapkan perubahan)
          <textarea
            className="m2-sql"
            wrap="off"
            required
            placeholder="CREATE TABLE … / UPDATE …"
            value={draft.sql_up}
            onChange={(e) => set("sql_up", e.target.value)}
          />
        </label>
        <label className="check">
          <input
            type="checkbox"
            checked={draft.is_irreversible}
            onChange={(e) => set("is_irreversible", e.target.checked)}
          />
          Irreversible (tidak dapat di-rollback)
        </label>
        {draft.is_irreversible ? (
          <label>
            Alasan irreversible
            <input
              required
              placeholder="Jelaskan mengapa perubahan tidak dapat dibatalkan…"
              value={draft.irreversible_reason}
              onChange={(e) => set("irreversible_reason", e.target.value)}
            />
          </label>
        ) : (
          <label>
            SQL Down (membatalkan perubahan saat rollback)
            <textarea
              required
              className="m2-sql"
              wrap="off"
              placeholder="DROP TABLE … / UPDATE …"
              value={draft.sql_down}
              onChange={(e) => set("sql_down", e.target.value)}
            />
          </label>
        )}
        {draft.execution_mode === "batch" && (
          <>
            <div className="m2-form-grid">
              <label>
                Tabel batch
                <input
                  required
                  placeholder="public.benchmark_accounts"
                  value={draft.batch_table}
                  onChange={(e) => set("batch_table", e.target.value)}
                />
              </label>
              <label>
                Kolom cursor batch (PK/UNIQUE NOT NULL)
                <input
                  required
                  placeholder="id"
                  value={draft.batch_cursor_column}
                  onChange={(e) => set("batch_cursor_column", e.target.value)}
                />
              </label>
              <label>
                Ukuran batch (baris per transaksi)
                <input
                  type="number"
                  required
                  min="1"
                  value={draft.batch_size ?? ""}
                  onChange={(e) =>
                    set(
                      "batch_size",
                      e.target.value ? Number(e.target.value) : null,
                    )
                  }
                />
              </label>
            </div>
            <p className="m2-hint">
              Gunakan satu statement UPDATE pada tabel target tanpa WHERE atau
              dengan klausa <code>WHERE &lt;cursor&gt; BETWEEN $1 AND $2</code>{" "}
              sebagai batas inklusif.
            </p>
          </>
        )}
        <div className="m2-actions">
          <button
            type="button"
            onClick={() => {
              setMessage("");
              void run(async () => {
                const result = await api<{ message: string }>(
                  path + "/validate",
                  { method: "POST", body },
                );
                setMessage(result.message);
              });
            }}
          >
            <CheckCircle2 size={15} /> Validasi SQL / Dry-run
          </button>
          <button className="primary">Simpan Draf</button>
          <button type="button" onClick={cancel}>
            Batal
          </button>
        </div>
      </fieldset>
      <NoticeBanner
        kind="success"
        message={message}
        durationMs={5000}
        onClose={() => setMessage("")}
      />
    </form>
  );
}

export default function MigrationPage() {
  const { db = "" } = useParams();
  const { data, error, refresh } = useResource<Definitions>(
    dbPath(db) + "/migrations",
  );
  const { state } = useLocation();
  const [initialSQL, setInitialSQL] = useState(
    typeof state?.sql_up === "string" ? state.sql_up : "",
  );
  const [editing, setEditing] = useState<Definition | "new" | null>(
    initialSQL ? "new" : null,
  );
  const action = useAction();
  const create = useCreateRequest();
  const definitions = data?.definitions ?? [];

  return (
    <section className="m2">
      <DatabaseSubNav
        dbId={db}
        title="Definisi Migrasi"
        subtitle="Susun, validasi, dan ajukan perubahan skema atau data berversi untuk persetujuan leader grup."
        accessLevel={data?.access_level}
        actions={
          <>
            <button onClick={refresh}>
              <RefreshCw size={15} /> Segarkan
            </button>
            {data?.access_level === "VIEW_EDIT" && (
              <button
                className="primary"
                onClick={() => {
                  setInitialSQL("");
                  setEditing("new");
                }}
              >
                <Plus size={15} /> Migrasi Baru
              </button>
            )}
          </>
        }
      />

      {data && (
        <div className="stats-strip">
          <div className="stat-card">
            <small>Total Definisi</small>
            <strong>{definitions.length}</strong>
          </div>
          <div className="stat-card">
            <small>Active Tip Saat Ini</small>
            <strong>
              {data.active_tip ? `v${data.active_tip.migration_version}` : "—"}
            </strong>
          </div>
          <div className="stat-card">
            <small>Status Database</small>
            <strong>
              {data.blocking_execution
                ? data.blocking_execution.status
                : "Siap"}
            </strong>
          </div>
        </div>
      )}

      {data?.blocking_execution && (
        <div className="alert-warning" role="alert">
          <div>
            <strong>
              <AlertTriangle
                size={15}
                style={{ display: "inline", verticalAlign: "-2px", marginRight: 6 }}
              />
              Database tertahan oleh eksekusi berstatus{" "}
              <code>{data.blocking_execution.status}</code>
            </strong>
            <p style={{ color: "#92400e", fontSize: "0.84rem" }}>
              Migrasi v{data.blocking_execution.migration_version} (
              {data.blocking_execution.migration_name}) perlu dilanjutkan atau
              dibatalkan melalui halaman pemantauan job
              {data.blocking_allows_repair
                ? ", atau ajukan migrasi perbaikan baru dengan versi lebih tinggi."
                : "."}
            </p>
          </div>
          <Link
            className="primary btn-sm"
            to={"/jobs/" + data.blocking_execution.job_id}
          >
            Buka Monitor Job
          </Link>
        </div>
      )}

      {editing && data?.access_level === "VIEW_EDIT" && (
        <Editor
          key={editing === "new" ? "new" : editing.id}
          db={db}
          initialSQL={initialSQL}
          definition={editing === "new" ? undefined : editing}
          done={() => {
            setEditing(null);
            refresh();
          }}
          cancel={() => setEditing(null)}
        />
      )}

      {!data && !error && <Skeleton />}
      {data && !definitions.length && (
        <div className="m2-panel">
          <p className="empty">
            Belum ada definisi migrasi untuk database ini.
          </p>
        </div>
      )}

      {definitions.map((d) => {
        const applied = Boolean(data?.applied_migration_ids[d.id]);
        const rolledBack = Boolean(data?.rolled_back_migrations[d.id]);
        const canUp = Boolean(
          data && linearAllowed(d, data) && !rolledBack,
        );
        return (
          <article key={d.id} className="m2-panel">
            <div className="row">
              <div>
                <h2 style={{ margin: 0 }}>{d.name}</h2>
                <div className="m2-meta-row">
                  <span className="badge badge-neutral">v{d.version}</span>
                  <span className="badge badge-neutral">Revisi {d.revision}</span>
                  <span className={statusBadgeClass(d.execution_mode)}>
                    {d.execution_mode}
                  </span>
                  {d.is_irreversible && (
                    <span className="badge badge-warning">Irreversible</span>
                  )}
                  {d.is_immutable && (
                    <span className="m2-badge">
                      <Lock size={12} /> Immutable (terkunci)
                    </span>
                  )}
                  {applied && (
                    <span className="badge badge-success">Terpasang</span>
                  )}
                  {rolledBack && !applied && (
                    <span className="badge badge-info">
                      Pernah di-rollback (gunakan Forward di Riwayat)
                    </span>
                  )}
                </div>
              </div>
              {data?.access_level === "VIEW_EDIT" && (
                <div className="m2-actions" style={{ margin: 0 }}>
                  {!d.is_immutable && (
                    <button className="btn-sm" onClick={() => setEditing(d)}>
                      <Edit3 size={14} /> Ubah Draf
                    </button>
                  )}
                  <button
                    className="primary btn-sm"
                    disabled={action.busy || !canUp}
                    onClick={() =>
                      void action.run(() => create(db, d.id, "up"))
                    }
                  >
                    <Send size={14} /> Ajukan Up
                  </button>
                </div>
              )}
            </div>
            {d.description && (
              <p style={{ margin: "0.45rem 0 0.7rem" }}>{d.description}</p>
            )}
            <details>
              <summary>Lihat Definisi SQL</summary>
              <div className="m2-sql-split">
                <div>
                  <small>SQL Up</small>
                  <pre>{d.sql_up}</pre>
                </div>
                <div>
                  <small>
                    {d.is_irreversible ? "Alasan Irreversible" : "SQL Down"}
                  </small>
                  <pre>{d.sql_down || d.irreversible_reason}</pre>
                </div>
              </div>
            </details>
          </article>
        );
      })}
    </section>
  );
}
