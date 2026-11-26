import { useState } from "react";
import { Link, useLocation, useParams } from "react-router-dom";
import { api } from "../../api/client";
import { useAction, useResource } from "../../components/shared";
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
      className="m2-panel"
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
      <h2>{definition ? "Ubah draf" : "Migrasi baru"}</h2>
      <fieldset disabled={busy}>
        <label>
          Nama
          <input
            required
            value={draft.name}
            onChange={(e) => set("name", e.target.value)}
          />
        </label>
        <label>
          Deskripsi
          <textarea
            value={draft.description}
            onChange={(e) => set("description", e.target.value)}
          />
        </label>
        <label>
          SQL up
          <textarea
            className="m2-sql"
            wrap="off"
            required
            value={draft.sql_up}
            onChange={(e) => set("sql_up", e.target.value)}
          />
        </label>
        <label>
          <input
            type="checkbox"
            checked={draft.is_irreversible}
            onChange={(e) => set("is_irreversible", e.target.checked)}
          />{" "}
          Irreversible (tidak dapat di-rollback)
        </label>
        {draft.is_irreversible ? (
          <label>
            Alasan irreversible
            <input
              required
              value={draft.irreversible_reason}
              onChange={(e) => set("irreversible_reason", e.target.value)}
            />
          </label>
        ) : (
          <label>
            SQL down
            <textarea
              required
              className="m2-sql"
              wrap="off"
              value={draft.sql_down}
              onChange={(e) => set("sql_down", e.target.value)}
            />
          </label>
        )}
        <label>
          Mode eksekusi
          <select
            value={draft.execution_mode}
            onChange={(e) => set("execution_mode", e.target.value)}
          >
            <option value="normal">Normal</option>
            <option value="batch">Batch</option>
          </select>
        </label>
        {draft.execution_mode === "batch" && (
          <>
            <label>
              Tabel batch
              <input
                required
                value={draft.batch_table}
                onChange={(e) => set("batch_table", e.target.value)}
              />
            </label>
            <label>
              Kolom cursor batch
              <input
                required
                value={draft.batch_cursor_column}
                onChange={(e) => set("batch_cursor_column", e.target.value)}
              />
            </label>
            <label>
              Ukuran batch
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
            <p>
              Gunakan satu statement UPDATE tanpa WHERE atau dengan klausa WHERE{" "}
              {"<cursor>"} BETWEEN $1 AND $2 sebagai batas inklusif.
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
            Validasi SQL / Dry-run
          </button>
          <button>Simpan draf</button>
          <button type="button" onClick={cancel}>
            Batal
          </button>
        </div>
      </fieldset>
      <p role="status">{message}</p>
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
  return (
    <section className="m2">
      <header>
        <h1>Definisi migrasi</h1>
        <div className="m2-actions">
          <Link to={dbPath(db) + "/history"}>Riwayat eksekusi</Link>
          <Link to={"/requests?database_id=" + encodeURIComponent(db)}>
            Antrean tinjauan
          </Link>
          {data?.access_level === "VIEW_EDIT" && (
            <button
              onClick={() => {
                setInitialSQL("");
                setEditing("new");
              }}
            >
              Migrasi baru
            </button>
          )}
        </div>
      </header>
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
      {!data && !error && <p>Memuat definisi migrasi…</p>}
      {data && !(data.definitions ?? []).length && (
        <p>Belum ada definisi migrasi.</p>
      )}
      {data?.definitions?.map((d) => (
        <article key={d.id} className="m2-panel">
          <h2>{d.name}</h2>
          <p>
            v{d.version} · Revisi {d.revision} · {d.execution_mode}{" "}
            {d.is_immutable && (
              <strong className="m2-badge">Immutable (terkunci)</strong>
            )}
          </p>
          <p>{d.description}</p>
          <details>
            <summary>Definisi SQL</summary>
            <pre>{d.sql_up}</pre>
            <pre>{d.sql_down || d.irreversible_reason}</pre>
          </details>
          {data.access_level === "VIEW_EDIT" && (
            <div className="m2-actions">
              {!d.is_immutable && (
                <button onClick={() => setEditing(d)}>Ubah draf</button>
              )}
              <button
                disabled={
                  action.busy ||
                  !linearAllowed(d, data) ||
                  data.rolled_back_migrations[d.id]
                }
                onClick={() => void action.run(() => create(db, d.id, "up"))}
              >
                Ajukan up
              </button>
            </div>
          )}
        </article>
      ))}
    </section>
  );
}
