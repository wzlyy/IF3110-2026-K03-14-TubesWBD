import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  ArrowUpRight,
  ChevronLeft,
  ChevronRight,
  Database,
  FileCode2,
  GitCompare,
  History,
  Layers,
  Play,
  RefreshCw,
  Search,
  Table2,
} from "lucide-react";
import {
  DatabaseSubNav,
  GridTable,
  Skeleton,
  statusBadgeClass,
  useAction,
  useResource,
} from "../../components/shared";
import { mutate } from "../../api/client";
import type { Connection } from "../admin/Admin";

interface DB extends Connection {
  access_level: string;
  lock_state?: string;
}
interface Column {
  Name: string;
  DataType: string;
  Nullable: boolean;
  Default: string | null;
}
interface Key {
  Name: string;
  Type: string;
  Definition: string;
  Columns: string[];
}
interface Table {
  Schema: string;
  Name: string;
  Columns: Column[];
  Constraints: Key[];
  Indexes: Key[];
}
interface SchemaResponse {
  catalog: { Schemas: { Name: string; Tables: Table[] }[] };
  database: DB;
  access_level: string;
}
interface Page {
  data: {
    Columns: string[];
    Rows: { Text: string; Null: boolean }[][];
    Page: number;
    HasNext: boolean;
  } | null;
  page_size: number;
}

export function Databases() {
  const { data, error, refresh } = useResource<{ databases: DB[] }>("/databases");
  const [filter, setFilter] = useState("");
  const all = data?.databases || [];
  const items = all.filter((d) =>
    [d.name, d.db_name, d.host].some((s) =>
      s.toLowerCase().includes(filter.toLowerCase()),
    ),
  );
  const editCount = all.filter((d) => d.access_level === "VIEW_EDIT").length;
  const viewCount = all.filter((d) => d.access_level === "VIEW_ONLY").length;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Daftar Database</h1>
          <p className="page-subtitle">
            Pilih database target PostgreSQL untuk menjelajahi skema, melihat
            data, atau mengelola migrasi.
          </p>
        </div>
        <div className="actions">
          <button onClick={refresh}>
            <RefreshCw size={15} /> Segarkan
          </button>
          <Link className="primary" to="/requests">
            Antrean Tinjauan
          </Link>
        </div>
      </div>

      {data && (
        <div className="stats-strip">
          <div className="stat-card">
            <small>Total Koneksi</small>
            <strong>{all.length}</strong>
          </div>
          <div className="stat-card">
            <small>Akses VIEW_EDIT</small>
            <strong>{editCount}</strong>
          </div>
          <div className="stat-card">
            <small>Akses VIEW_ONLY</small>
            <strong>{viewCount}</strong>
          </div>
        </div>
      )}

      <div className="row toolbar">
        <h2>
          Koneksi tersedia{" "}
          {data && <span className="count">{items.length}</span>}
        </h2>
        <label className="search">
          <Search size={16} />
          <input
            aria-label="Cari database"
            placeholder="Cari nama, database, atau host…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </label>
      </div>

      {!data ? (
        !error && <Skeleton />
      ) : (
        <div className="cards">
          {items.map((d) => {
            const base = "/databases/" + d.id;
            return (
              <article className="db-card" key={d.id}>
                <Link className="db-card-link" to={base + "/schema"}>
                  <div className="row">
                    <span className="db-icon">
                      <Database size={20} />
                    </span>
                    <ArrowUpRight size={17} color="#64748b" />
                  </div>
                  <h2>{d.name}</h2>
                  <p>
                    {d.dbms.toUpperCase()} · {d.db_name} ({d.host}:{d.port})
                  </p>
                </Link>
                <footer>
                  <div className="db-card-meta">
                    <span className={statusBadgeClass(d.access_level)}>
                      {d.access_level}
                    </span>
                    {d.lock_state && (
                      <span className={statusBadgeClass(d.lock_state)}>
                        {d.lock_state}
                      </span>
                    )}
                  </div>
                  <div className="db-card-shortcuts">
                    <Link to={base + "/schema"}>
                      <Layers size={13} /> Skema
                    </Link>
                    <Link to={base + "/data"}>
                      <Table2 size={13} /> Data
                    </Link>
                    <Link to={base + "/migrations"}>
                      <FileCode2 size={13} /> Migrasi
                    </Link>
                    <Link to={base + "/history"}>
                      <History size={13} /> Riwayat
                    </Link>
                  </div>
                </footer>
              </article>
            );
          })}
          {!items.length && (
            <div className="panel" style={{ gridColumn: "1 / -1" }}>
              <p className="empty">
                {filter
                  ? "Database yang dicari tidak ditemukan."
                  : "Belum ada database yang dapat diakses oleh grup Anda."}
              </p>
            </div>
          )}
        </div>
      )}
    </>
  );
}

export function Explorer({ mode }: { mode: string }) {
  const { id = "" } = useParams();
  const base = "/databases/" + id;
  const schema = useResource<SchemaResponse>(base + "/schema");
  const tables = (schema.data?.catalog.Schemas || []).flatMap(
    (s) => s.Tables || [],
  );
  const dbMeta = schema.data?.database;
  const subtitle = dbMeta
    ? `${dbMeta.dbms.toUpperCase()} · ${dbMeta.db_name} (${dbMeta.host}:${dbMeta.port})`
    : undefined;

  return (
    <>
      <DatabaseSubNav
        dbId={id}
        title={dbMeta?.name || "Database"}
        subtitle={subtitle}
        accessLevel={schema.data?.access_level}
        actions={
          <button onClick={schema.refresh}>
            <RefreshCw size={15} /> Segarkan
          </button>
        }
      />
      {mode === "schema" &&
        (schema.data ? (
          <Schema tables={tables} />
        ) : (
          !schema.error && <Skeleton />
        ))}
      {mode === "data" && schema.data && <Data base={base} tables={tables} />}
      {mode === "data" && !schema.data && !schema.error && <Skeleton />}
      {mode === "explain" && (
        <Explain
          base={base}
          canAnalyze={schema.data?.access_level === "VIEW_EDIT"}
        />
      )}
      {mode === "diff" && (
        <Diff base={base} canEdit={schema.data?.access_level === "VIEW_EDIT"} />
      )}
    </>
  );
}

function Schema({ tables }: { tables: Table[] }) {
  if (!tables.length) {
    return (
      <div className="panel">
        <p className="empty">Belum ada tabel pada database target ini.</p>
      </div>
    );
  }
  return (
    <section>
      <div className="row toolbar">
        <h2>
          Daftar Tabel <span className="count">{tables.length}</span>
        </h2>
      </div>
      {tables.map((t, i) => (
        <details
          className="panel"
          key={JSON.stringify([t.Schema, t.Name])}
          open={i === 0}
        >
          <summary>
            <span>
              {t.Schema}.{t.Name}
            </span>
            <span className="actions">
              <span className="badge badge-neutral">
                {(t.Columns || []).length} kolom
              </span>
              <span className="badge badge-neutral">
                {(t.Constraints || []).length} constraint
              </span>
              <span className="badge badge-neutral">
                {(t.Indexes || []).length} index
              </span>
            </span>
          </summary>
          <h3>Kolom</h3>
          <GridTable
            columns={["Nama", "Tipe", "Nullable", "Default"]}
            rows={(t.Columns || []).map((c) => [
              <code>{c.Name}</code>,
              c.DataType,
              <span
                className={
                  c.Nullable ? "badge badge-warning" : "badge badge-success"
                }
              >
                {c.Nullable ? "Ya" : "NOT NULL"}
              </span>,
              c.Default ?? "—",
            ])}
          />
          {(
            [
              ["Constraint", t.Constraints],
              ["Index", t.Indexes],
            ] as const
          ).map(([title, keys]) => (
            <div key={title}>
              <h3>{title}</h3>
              <GridTable
                columns={["Nama", "Tipe", "Kolom", "Definisi"]}
                rows={(keys || []).map((k) => [
                  <code>{k.Name}</code>,
                  <span className="badge badge-neutral">{k.Type}</span>,
                  (k.Columns || []).join(", "),
                  k.Definition,
                ])}
              />
            </div>
          ))}
        </details>
      ))}
    </section>
  );
}

function Data({ base, tables }: { base: string; tables: Table[] }) {
  const [params, setParams] = useSearchParams();
  const selected =
    tables.find(
      (t) =>
        t.Schema === params.get("schema") && t.Name === params.get("table"),
    ) || tables[0];
  const rawPage = Number(params.get("page") || "1");
  const page = Number.isInteger(rawPage) && rawPage >= 1 ? rawPage : 1;

  function query(t: Table, p: number) {
    return new URLSearchParams({
      schema: t.Schema,
      table: t.Name,
      page: String(p),
    });
  }

  const result = useResource<Page>(
    base + "/data?" + (selected ? query(selected, page) : ""),
  );

  if (!selected) {
    return (
      <div className="panel">
        <p className="empty">Belum ada tabel untuk ditampilkan datanya.</p>
      </div>
    );
  }

  const data = result.data?.data;

  return (
    <section>
      <div className="panel">
        <div className="row">
          <label className="table-picker" style={{ margin: 0, flex: "1 1 260px" }}>
            Pilih Tabel
            <select
              value={tables.indexOf(selected)}
              onChange={(e) =>
                setParams(query(tables[Number(e.target.value)], 1))
              }
            >
              {tables.map((t, i) => (
                <option key={i} value={i}>
                  {t.Schema}.{t.Name}
                </option>
              ))}
            </select>
          </label>
          <div className="actions">
            {data && (
              <span className="badge badge-neutral">
                Halaman {data.Page} · maks {result.data?.page_size} baris
              </span>
            )}
            <button onClick={result.refresh}>
              <RefreshCw size={14} /> Muat Ulang
            </button>
          </div>
        </div>
      </div>

      {data ? (
        <>
          <GridTable
            columns={data.Columns}
            rows={(data.Rows || []).map((r) =>
              r.map((c) =>
                c.Null ? <span className="null">NULL</span> : c.Text,
              ),
            )}
          />
          <div className="row pagination">
            <span>
              Menampilkan halaman <strong>{data.Page}</strong> (
              {(data.Rows || []).length} baris · kapasitas{" "}
              {result.data?.page_size}/halaman)
            </span>
            <div className="actions">
              <button
                disabled={page <= 1}
                onClick={() => setParams(query(selected, page - 1))}
              >
                <ChevronLeft size={15} /> Sebelumnya
              </button>
              <button
                disabled={!data.HasNext}
                onClick={() => setParams(query(selected, page + 1))}
              >
                Berikutnya <ChevronRight size={15} />
              </button>
            </div>
          </div>
        </>
      ) : (
        !result.error && <Skeleton />
      )}
    </section>
  );
}

interface Step {
  Label: string;
  Relation: string;
  Index: string;
  Scan: string;
  Join: string;
  Rows: number | null;
  Cost: number | null;
  ActualTime: number | null;
  ActualRows: number | null;
  Children: Step[];
}
interface Plan {
  Root: Step | null;
  Raw: string;
  ExecutionTime: number | null;
  PlanningTime: number | null;
}

function PlanStep({ step }: { step: Step }) {
  return (
    <li>
      <b>{step.Label}</b>{" "}
      {[step.Relation, step.Index, step.Scan, step.Join]
        .filter(Boolean)
        .join(" · ")}
      <p>
        {[
          ["Biaya", step.Cost],
          ["Estimasi baris", step.Rows],
          ["Waktu aktual (ms)", step.ActualTime],
          ["Baris aktual", step.ActualRows],
        ]
          .filter(([, v]) => v != null)
          .map(([k, v]) => k + " " + v)
          .join(" · ")}
      </p>
      {!!step.Children?.length && (
        <ul>
          {step.Children.map((s, i) => (
            <PlanStep key={i} step={s} />
          ))}
        </ul>
      )}
    </li>
  );
}

function Explain({ base, canAnalyze }: { base: string; canAnalyze: boolean }) {
  const [plan, setPlan] = useState<Plan>();
  const [analyze, setAnalyze] = useState(false);
  const a = useAction();

  return (
    <>
      <form
        className="panel"
        onSubmit={(e) => {
          e.preventDefault();
          const f = new FormData(e.currentTarget);
          setPlan(undefined);
          void a.run(async () => {
            const r = await mutate<{ plan: Plan }>(base + "/explain", "POST", {
              sql_query: f.get("sql"),
              analyze,
              confirm_analyze: f.has("confirm"),
            });
            setPlan(r.plan);
          }, "");
        }}
      >
        <h2>Execution Plan (EXPLAIN)</h2>
        <p className="page-subtitle" style={{ marginBottom: "0.9rem" }}>
          Analisis rencana eksekusi query SELECT/DML secara aman di dalam
          transaksi yang selalu di-rollback.
        </p>
        <label>
          Query SQL
          <textarea
            name="sql"
            required
            wrap="off"
            placeholder="SELECT * FROM public.nama_tabel LIMIT 10;"
            spellCheck={false}
          />
        </label>
        {canAnalyze && (
          <label className="check">
            <input
              type="checkbox"
              checked={analyze}
              onChange={(e) => setAnalyze(e.target.checked)}
            />
            Aktifkan EXPLAIN ANALYZE (eksekusi fisik dalam transaksi rollback)
          </label>
        )}
        {analyze && (
          <label className="check">
            <input required name="confirm" type="checkbox" />
            Saya menyetujui eksekusi query di target. Efek seperti perubahan
            sequence dapat bertahan setelah rollback.
          </label>
        )}
        <div className="actions" style={{ marginTop: "0.9rem" }}>
          <button className="primary" disabled={a.busy}>
            <Play size={15} /> Jalankan Explain
          </button>
        </div>
      </form>
      {plan && (
        <section className="panel">
          <div className="row" style={{ marginBottom: "0.75rem" }}>
            <h2>Hasil Execution Plan</h2>
            <div className="actions">
              {plan.PlanningTime != null && (
                <span className="badge badge-info">
                  Planning: {plan.PlanningTime} ms
                </span>
              )}
              {plan.ExecutionTime != null && (
                <span className="badge badge-success">
                  Execution: {plan.ExecutionTime} ms
                </span>
              )}
            </div>
          </div>
          {plan.Root && (
            <ul className="plan">
              <PlanStep step={plan.Root} />
            </ul>
          )}
          <details style={{ marginTop: "1rem" }}>
            <summary>Lihat Plan Mentah (JSON)</summary>
            <pre>{plan.Raw}</pre>
          </details>
        </section>
      )}
    </>
  );
}

interface DiffResult {
  generated_sql: string;
  has_destructive: boolean;
  diff_result: {
    IsIdentical: boolean;
    Changes: {
      Schema: string;
      Table: string;
      Object: string;
      Description: string;
      OldValue: string;
      NewValue: string;
      Action: string;
      Destructive: boolean;
    }[];
  };
}

function Diff({ base, canEdit }: { base: string; canEdit: boolean }) {
  const [result, setResult] = useState<DiffResult>();
  const a = useAction();

  return (
    <>
      <form
        className="panel"
        onSubmit={(e) => {
          e.preventDefault();
          const f = new FormData(e.currentTarget);
          setResult(undefined);
          void a.run(
            async () =>
              setResult(
                await mutate<DiffResult>(base + "/diff", "POST", {
                  target_schema_json: f.get("target"),
                }),
              ),
            "",
          );
        }}
      >
        <h2>Bandingkan Skema (Schema Diff)</h2>
        <p className="page-subtitle" style={{ marginBottom: "0.9rem" }}>
          Tempel definisi skema target dalam format JSON untuk membandingkan
          perubahan dan menghasilkan kandidat DDL.
        </p>
        <label>
          Skema target (JSON)
          <textarea
            required
            name="target"
            wrap="off"
            spellCheck={false}
            placeholder={'{"tables": [...]}'}
          />
        </label>
        <div className="actions" style={{ marginTop: "0.9rem" }}>
          <button className="primary" disabled={a.busy}>
            <GitCompare size={15} /> Bandingkan Skema
          </button>
        </div>
      </form>
      {result && (
        <section className="panel">
          <div className="row" style={{ marginBottom: "0.75rem" }}>
            <h2>
              {result.diff_result.IsIdentical
                ? "Skema Identik"
                : "Perubahan Skema Terdeteksi"}
            </h2>
            {result.has_destructive && (
              <span className="badge badge-danger" role="alert">
                Mengandung perubahan destruktif
              </span>
            )}
          </div>
          {!result.diff_result.IsIdentical && (
            <GridTable
              columns={[
                "Tindakan",
                "Objek",
                "Deskripsi",
                "Sebelum",
                "Sesudah",
                "Destruktif",
              ]}
              rows={(result.diff_result.Changes || []).map((c) => [
                <span className="badge badge-neutral">{c.Action}</span>,
                <code>
                  {[c.Schema, c.Table, c.Object].filter(Boolean).join(".")}
                </code>,
                c.Description,
                c.OldValue || "—",
                c.NewValue || "—",
                <span
                  className={
                    c.Destructive ? "badge badge-danger" : "badge badge-success"
                  }
                >
                  {c.Destructive ? "Ya" : "Tidak"}
                </span>,
              ])}
            />
          )}
          <h3>Kandidat DDL</h3>
          <pre>{result.generated_sql || "—"}</pre>
          {canEdit && result.generated_sql && (
            <div className="actions" style={{ marginTop: "1rem" }}>
              <Link
                className="primary"
                to={base + "/migrations"}
                state={{ sql_up: result.generated_sql }}
              >
                <FileCode2 size={15} /> Bawa SQL ke Editor Migrasi
              </Link>
            </div>
          )}
        </section>
      )}
    </>
  );
}
