import { useState } from "react";
import { Link, NavLink, useParams, useSearchParams } from "react-router-dom";
import { ArrowUpRight, Database, Search } from "lucide-react";
import {
  GridTable,
  Skeleton,
  useResource,
  useAction,
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
  const { data, error } = useResource<{ databases: DB[] }>("/databases"),
    [filter, setFilter] = useState("");
  const items = (data?.databases || []).filter((d) =>
    [d.name, d.db_name, d.host].some((s) =>
      s.toLowerCase().includes(filter.toLowerCase()),
    ),
  );
  return (
    <>
      <h1>Database</h1>
      <div className="row toolbar">
        <h2>
          Koneksi tersedia{" "}
          {data && <span className="count">{data.databases.length}</span>}
        </h2>
        <label className="search">
          <Search size={16} />
          <input
            aria-label="Cari database"
            placeholder="Cari database"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </label>
      </div>
      {!data ? (
        !error && <Skeleton />
      ) : (
        <div className="cards">
          {items.map((d) => (
            <Link
              className="db-card"
              key={d.id}
              to={"/databases/" + d.id + "/schema"}
            >
              <div className="row">
                <span className="db-icon">
                  <Database size={22} />
                </span>
                <ArrowUpRight size={17} />
              </div>
              <h2>{d.name}</h2>
              <p>
                {d.dbms} · {d.db_name}
              </p>
              <footer>
                <span className="badge">{d.access_level}</span>
                {d.lock_state && <span>{d.lock_state}</span>}
              </footer>
            </Link>
          ))}
          {!items.length && (
            <p className="empty">
              {filter
                ? "Database tidak ditemukan."
                : "Belum ada database yang dapat diakses."}
            </p>
          )}
        </div>
      )}
    </>
  );
}
const tabs = [
  ["schema", "Skema"],
  ["data", "Data"],
  ["explain", "Explain"],
  ["diff", "Schema Diff"],
  ["migrations", "Migrasi"],
  ["history", "Riwayat"],
];
export function Explorer({ mode }: { mode: string }) {
  const { id } = useParams(),
    base = "/databases/" + id;
  const schema = useResource<SchemaResponse>(base + "/schema");
  const tables = (schema.data?.catalog.Schemas || []).flatMap(
    (s) => s.Tables || [],
  );
  return (
    <>
      <Link className="back" to="/databases">
        ← Database
      </Link>
      <h1>{schema.data?.database.name || "Database"}</h1>
      <nav className="tabs">
        {tabs.map(([path, title]) => (
          <NavLink key={path} to={base + "/" + path}>
            {title}
          </NavLink>
        ))}
      </nav>
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
  if (!tables.length) return <p className="empty">Belum ada tabel.</p>;
  return (
    <section>
      <h2>
        Tabel <span className="count">{tables.length}</span>
      </h2>
      {tables.map((t, i) => (
        <details
          className="panel"
          key={JSON.stringify([t.Schema, t.Name])}
          open={i === 0}
        >
          <summary>
            {t.Schema}.{t.Name}
          </summary>
          <h3>Kolom</h3>
          <GridTable
            columns={["Nama", "Tipe", "Nullable", "Default"]}
            rows={(t.Columns || []).map((c) => [
              c.Name,
              c.DataType,
              c.Nullable ? "Ya" : "Tidak",
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
                  k.Name,
                  k.Type,
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
  if (!selected) return <p className="empty">Belum ada tabel.</p>;
  const data = result.data?.data;
  return (
    <section>
      <label className="table-picker">
        Tabel
        <select
          value={tables.indexOf(selected)}
          onChange={(e) => setParams(query(tables[Number(e.target.value)], 1))}
        >
          {tables.map((t, i) => (
            <option key={i} value={i}>
              {t.Schema}.{t.Name}
            </option>
          ))}
        </select>
      </label>
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
              Halaman {data.Page} · {result.data?.page_size} baris per halaman
            </span>
            <div className="actions">
              <button
                disabled={page <= 1}
                onClick={() => setParams(query(selected, page - 1))}
              >
                Sebelumnya
              </button>
              <button
                disabled={!data.HasNext}
                onClick={() => setParams(query(selected, page + 1))}
              >
                Berikutnya
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
  const [plan, setPlan] = useState<Plan>(),
    [analyze, setAnalyze] = useState(false),
    a = useAction();
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
        <h2>Execution plan</h2>
        <label>
          Query SQL
          <textarea
            name="sql"
            required
            placeholder="SELECT …"
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
            EXPLAIN ANALYZE
          </label>
        )}
        {analyze && (
          <label className="check">
            <input required name="confirm" type="checkbox" />
            Saya menyetujui eksekusi query di target. Efek seperti perubahan
            sequence dapat bertahan setelah rollback.
          </label>
        )}
        <button className="primary" disabled={a.busy}>
          Jalankan Explain
        </button>
      </form>
      {plan && (
        <section className="panel">
          <h2>Hasil Explain</h2>
          {plan.PlanningTime != null && (
            <p>Waktu perencanaan: {plan.PlanningTime} ms</p>
          )}
          {plan.ExecutionTime != null && (
            <p>Waktu eksekusi: {plan.ExecutionTime} ms</p>
          )}
          {plan.Root && (
            <ul className="plan">
              <PlanStep step={plan.Root} />
            </ul>
          )}
          <details>
            <summary>Plan mentah</summary>
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
  const [result, setResult] = useState<DiffResult>(),
    a = useAction();
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
        <h2>Bandingkan skema</h2>
        <label>
          Skema target (JSON)
          <textarea
            required
            name="target"
            spellCheck={false}
            placeholder={'{"tables": [...]}'}
          />
        </label>
        <button className="primary" disabled={a.busy}>
          Bandingkan skema
        </button>
      </form>
      {result && (
        <section className="panel">
          <h2>
            {result.diff_result.IsIdentical
              ? "Skema identik"
              : "Perubahan skema"}
          </h2>
          {result.has_destructive && (
            <p role="alert">Pratinjau berisi perubahan destruktif.</p>
          )}
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
                c.Action,
                [c.Schema, c.Table, c.Object].filter(Boolean).join("."),
                c.Description,
                c.OldValue || "—",
                c.NewValue || "—",
                c.Destructive ? "Ya" : "Tidak",
              ])}
            />
          )}
          <h3>Kandidat DDL</h3>
          <pre>{result.generated_sql || "—"}</pre>
          {canEdit && result.generated_sql && (
            <Link
              className="primary"
              to={base + "/migrations"}
              state={{ sql_up: result.generated_sql }}
            >
              Bawa SQL ke editor migrasi
            </Link>
          )}
        </section>
      )}
    </>
  );
}
