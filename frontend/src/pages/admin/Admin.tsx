import { useState, type FormEvent, type ReactNode } from "react";
import { NavLink } from "react-router-dom";
import {
  Cable,
  KeyRound,
  Plus,
  PlugZap,
  Shield,
  Trash2,
  UserPlus,
  Users as UsersIcon,
  UsersRound,
} from "lucide-react";
import { mutate } from "../../api/client";
import {
  Fields,
  GridTable,
  NoticeBanner,
  Skeleton,
  statusBadgeClass,
  useAction,
  useResource,
} from "../../components/shared";
import { useAuth, type User } from "../../context/AuthContext";

interface Group {
  id: string;
  name: string;
  leader_user_id: string;
  leader_username: string;
  members: { user_id: string; username: string }[];
}
export interface Connection {
  id: string;
  name: string;
  host: string;
  port: number;
  db_name: string;
  username: string;
  dbms: string;
}
interface Grant {
  group_id: string;
  group_name: string;
  database_id: string;
  database_name: string;
  access_level: string;
}

const adminSubTabs = [
  ["/admin/users", "Pengguna", UsersIcon],
  ["/admin/groups", "Grup & Leader", UsersRound],
  ["/admin/grants", "Hak Akses", KeyRound],
  ["/admin/connections", "Koneksi Target", Cable],
] as const;

const values = (event: FormEvent<HTMLFormElement>) => {
  event.preventDefault();
  return Object.fromEntries(new FormData(event.currentTarget)) as Record<
    string,
    string
  >;
};

function useAdmin() {
  const action = useAction();
  const [revision, setRevision] = useState(0);
  function save(
    path: string,
    method: string,
    body?: unknown,
    done?: () => void,
    message = "Perubahan berhasil disimpan.",
  ) {
    return action.run(async () => {
      await mutate("/admin/" + path, method, body);
      done?.();
      setRevision((n) => n + 1);
    }, message);
  }
  return { ...action, revision, save };
}

function Frame({
  title,
  subtitle,
  action,
  children,
}: {
  title: string;
  subtitle: string;
  action: ReturnType<typeof useAdmin>;
  children: ReactNode;
}) {
  return (
    <>
      <div className="page-head">
        <div>
          <h1>{title}</h1>
          <p className="page-subtitle">{subtitle}</p>
        </div>
      </div>
      <nav className="tabs" aria-label="Menu administrasi">
        {adminSubTabs.map(([to, label, Icon]) => (
          <NavLink key={to} to={to}>
            <Icon size={15} />
            {label}
          </NavLink>
        ))}
      </nav>
      <NoticeBanner
        kind="success"
        message={action.notice}
        durationMs={5000}
        onClose={action.clearNotice}
      />
      <fieldset className="admin-page" disabled={action.busy}>
        {children}
      </fieldset>
    </>
  );
}

function Pick({
  name,
  label,
  options,
  value,
}: {
  name: string;
  label: string;
  options: { id: string; name: string }[];
  value?: string;
}) {
  return (
    <label>
      {label}
      <select
        name={name}
        aria-label={label}
        defaultValue={value || ""}
        required
      >
        <option value="" disabled>
          Pilih {label.toLowerCase()}
        </option>
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {o.name}
          </option>
        ))}
      </select>
    </label>
  );
}

export function Users() {
  const a = useAdmin();
  const { user: me } = useAuth();
  const { data, error } = useResource<{ users: User[] }>(
    "/admin/users",
    a.revision,
  );
  const [reset, setReset] = useState<User>();

  return (
    <Frame
      title="Manajemen Pengguna"
      subtitle="Kelola akun pengguna platform, status aktif, peran administrator, dan reset password."
      action={a}
    >
      <form
        className="panel"
        onSubmit={(e) => {
          const form = e.currentTarget;
          const f = values(e);
          void a.save(
            "users",
            "POST",
            { ...f, is_platform_admin: f.is_platform_admin === "on" },
            () => form.reset(),
          );
        }}
      >
        <h2>Buat Pengguna Baru</h2>
        <div className="fields">
          <Fields name="username" label="Username" placeholder="mis. editor_tim_a" />
          <Fields
            name="password"
            label="Password"
            type="password"
            placeholder="Minimal 6 karakter"
          />
          <label className="check">
            <input name="is_platform_admin" type="checkbox" />
            Administrator Platform
          </label>
        </div>
        <button className="primary">
          <UserPlus size={15} /> Buat Pengguna
        </button>
      </form>

      {reset && (
        <form
          className="panel"
          onSubmit={(e) => {
            const f = values(e);
            void a.save(
              "users/" + reset.id + "/reset-password",
              "POST",
              f,
              () => setReset(undefined),
              "Password pengguna berhasil diperbarui.",
            );
          }}
        >
          <h2>Reset Password · {reset.username}</h2>
          <div className="fields">
            <Fields
              name="new_password"
              label="Password baru"
              type="password"
              placeholder="Minimal 6 karakter"
            />
          </div>
          <div className="actions">
            <button className="primary">Simpan Password</button>
            <button type="button" onClick={() => setReset(undefined)}>
              Batal
            </button>
          </div>
        </form>
      )}

      {!data ? (
        error ? null : <Skeleton />
      ) : (
        <GridTable
          columns={["Username", "Status", "Peran", "Tindakan"]}
          rows={data.users.map((u) => [
            <strong>{u.username}</strong>,
            <span className={statusBadgeClass(u.is_active ? "aktif" : "nonaktif")}>
              {u.is_active ? "Aktif" : "Nonaktif"}
            </span>,
            <span className={u.is_platform_admin ? "badge" : "badge badge-neutral"}>
              {u.is_platform_admin ? "Administrator" : "Anggota"}
            </span>,
            <div className="actions">
              <button
                className={u.is_active ? "btn-sm btn-danger" : "btn-sm"}
                disabled={u.id === me?.id}
                title={
                  u.id === me?.id
                    ? "Tidak dapat menonaktifkan akun sendiri"
                    : undefined
                }
                onClick={() =>
                  void a.save("users/" + u.id + "/toggle-active", "POST")
                }
              >
                {u.is_active ? "Nonaktifkan" : "Aktifkan"}
              </button>
              <button
                className="btn-sm"
                disabled={u.id === me?.id && u.is_platform_admin}
                title={
                  u.id === me?.id && u.is_platform_admin
                    ? "Tidak dapat mencabut hak admin akun sendiri"
                    : undefined
                }
                onClick={() =>
                  void a.save("users/" + u.id + "/toggle-admin", "POST")
                }
              >
                <Shield size={13} />
                {u.is_platform_admin ? "Cabut Admin" : "Jadikan Admin"}
              </button>
              <button className="btn-sm" onClick={() => setReset(u)}>
                Reset Password
              </button>
            </div>,
          ])}
        />
      )}
    </Frame>
  );
}

export function Groups() {
  const a = useAdmin();
  const groups = useResource<{ groups: Group[] }>("/admin/groups", a.revision);
  const users = useResource<{ users: User[] }>("/admin/users");
  const [edit, setEdit] = useState<Group>();
  const options = (users.data?.users || [])
    .filter((u) => u.is_active)
    .map((u) => ({ id: u.id, name: u.username }));

  return (
    <Frame
      title="Grup & Leader Persetujuan"
      subtitle="Setiap grup wajib memiliki leader aktif yang berwenang menyetujui atau menolak perubahan migrasi."
      action={a}
    >
      <form
        className="panel"
        key={edit?.id || "create"}
        onSubmit={(e) => {
          const form = e.currentTarget;
          const f = values(e);
          void a.save(
            edit ? "groups/" + edit.id : "groups",
            edit ? "PUT" : "POST",
            f,
            () => {
              setEdit(undefined);
              form.reset();
            },
          );
        }}
      >
        <h2>{edit ? `Edit Grup · ${edit.name}` : "Buat Grup Baru"}</h2>
        <div className="fields">
          <Fields
            name="name"
            label="Nama grup"
            value={edit?.name}
            placeholder="mis. team-core"
          />
          <Pick
            name="leader_user_id"
            label="Pemimpin grup"
            options={options}
            value={edit?.leader_user_id}
          />
        </div>
        <div className="actions">
          <button className="primary">
            <Plus size={15} /> {edit ? "Simpan Perubahan" : "Buat Grup"}
          </button>
          {edit && (
            <button type="button" onClick={() => setEdit(undefined)}>
              Batal
            </button>
          )}
        </div>
      </form>

      {groups.error ? null : !groups.data ? (
        <Skeleton />
      ) : !groups.data?.groups.length ? (
        <div className="panel">
          <p className="empty">Belum ada grup terdaftar.</p>
        </div>
      ) : (
        groups.data.groups.map((g) => (
          <article className="panel" key={g.id}>
            <div className="row" style={{ marginBottom: "0.75rem" }}>
              <div>
                <div className="row" style={{ justifyContent: "flex-start" }}>
                  <h2 style={{ margin: 0 }}>{g.name}</h2>
                  <span className="badge">Leader: {g.leader_username}</span>
                  <span className="badge badge-neutral">
                    {(g.members || []).length} anggota
                  </span>
                </div>
              </div>
              <div className="actions">
                <button className="btn-sm" onClick={() => setEdit(g)}>
                  Edit Grup
                </button>
                <button
                  className="btn-sm btn-danger"
                  onClick={() => {
                    if (confirm("Hapus grup " + g.name + "?"))
                      void a.save("groups/" + g.id, "DELETE");
                  }}
                >
                  <Trash2 size={13} /> Hapus
                </button>
              </div>
            </div>
            <ul className="members">
              {(g.members || []).map((m) => (
                <li key={m.user_id}>
                  <span className="actions">
                    <strong>{m.username}</strong>
                    {m.user_id === g.leader_user_id && (
                      <span className="badge badge-info">Leader</span>
                    )}
                  </span>
                  {m.user_id !== g.leader_user_id && (
                    <button
                      className="btn-sm btn-danger"
                      onClick={() =>
                        void a.save(
                          "groups/" + g.id + "/members/" + m.user_id,
                          "DELETE",
                        )
                      }
                      aria-label={"Keluarkan " + m.username}
                    >
                      Keluarkan
                    </button>
                  )}
                </li>
              ))}
            </ul>
            <form
              className="fields"
              onSubmit={(e) => {
                const f = values(e);
                void a.save("groups/" + g.id + "/members", "POST", f);
              }}
            >
              <Pick
                name="user_id"
                label="Tambahkan anggota baru"
                options={options.filter(
                  (u) => !g.members?.some((m) => m.user_id === u.id),
                )}
              />
              <button>
                <UserPlus size={15} /> Tambah Anggota
              </button>
            </form>
          </article>
        ))
      )}
    </Frame>
  );
}

export function Grants() {
  const a = useAdmin();
  const grants = useResource<{ grants: Grant[] }>("/admin/grants", a.revision);
  const groups = useResource<{ groups: Group[] }>("/admin/groups");
  const connections = useResource<{ connections: Connection[] }>(
    "/admin/connections",
  );

  return (
    <Frame
      title="Hak Akses Database"
      subtitle="Tetapkan izin VIEW_ONLY atau VIEW_EDIT per grup terhadap setiap database target."
      action={a}
    >
      <form
        className="panel"
        onSubmit={(e) => void a.save("grants", "POST", values(e))}
      >
        <h2>Atur Hak Akses Grup</h2>
        <div className="fields">
          <Pick
            label="Grup"
            name="group_id"
            options={groups.data?.groups || []}
          />
          <Pick
            label="Database"
            name="database_id"
            options={connections.data?.connections || []}
          />
          <label>
            Tingkat akses
            <select name="access_level">
              <option>VIEW_ONLY</option>
              <option>VIEW_EDIT</option>
            </select>
          </label>
        </div>
        <button className="primary">
          <KeyRound size={15} /> Simpan Hak Akses
        </button>
      </form>
      {grants.data ? (
        <GridTable
          columns={["Grup", "Database", "Hak Akses", "Tindakan"]}
          rows={grants.data.grants.map((g) => [
            <strong>{g.group_name}</strong>,
            g.database_name,
            <span className={statusBadgeClass(g.access_level)}>
              {g.access_level}
            </span>,
            <button
              className="btn-sm btn-danger"
              onClick={() => {
                if (confirm("Cabut hak akses grup " + g.group_name + "?"))
                  void a.save("grants", "DELETE", {
                    group_id: g.group_id,
                    database_id: g.database_id,
                  });
              }}
            >
              <Trash2 size={13} /> Cabut Akses
            </button>,
          ])}
        />
      ) : (
        !grants.error && <Skeleton />
      )}
    </Frame>
  );
}

export function Connections() {
  const a = useAdmin();
  const { data, error } = useResource<{ connections: Connection[] }>(
    "/admin/connections",
    a.revision,
  );
  const [edit, setEdit] = useState<Connection>();

  return (
    <Frame
      title="Koneksi Database Target"
      subtitle="Kelola kredensial koneksi PostgreSQL terenkripsi dan uji keterhubungan ke server target."
      action={a}
    >
      <form
        key={edit?.id || "create"}
        className="panel"
        onSubmit={(e) => {
          const form = e.currentTarget;
          const f = values(e);
          void a.save(
            edit ? "connections/" + edit.id : "connections",
            edit ? "PUT" : "POST",
            { ...f, port: Number(f.port), dbms: "postgres" },
            () => {
              setEdit(undefined);
              form.reset();
            },
          );
        }}
      >
        <h2>
          {edit ? `Edit Koneksi · ${edit.name}` : "Tambah Koneksi PostgreSQL"}
        </h2>
        <div className="fields">
          <Fields
            name="name"
            label="Nama koneksi"
            value={edit?.name}
            placeholder="mis. target-postgres-1"
          />
          <Fields
            name="host"
            label="Host"
            value={edit?.host}
            placeholder="target-postgres"
          />
          <Fields
            name="port"
            label="Port"
            type="number"
            value={edit?.port || 5432}
          />
          <Fields
            name="db_name"
            label="Nama database"
            value={edit?.db_name}
            placeholder="target_db"
          />
          <Fields
            name="username"
            label="Username"
            value={edit?.username}
            placeholder="postgres"
          />
          <Fields
            name="password"
            label={edit ? "Password baru (opsional)" : "Password"}
            type="password"
            required={!edit}
          />
        </div>
        <div className="actions">
          <button className="primary">
            <Plus size={15} /> Simpan Koneksi
          </button>
          {edit && (
            <button type="button" onClick={() => setEdit(undefined)}>
              Batal
            </button>
          )}
        </div>
      </form>
      {data ? (
        <GridTable
          columns={["Nama Koneksi", "Database", "Host & Port", "Tindakan"]}
          rows={(data.connections || []).map((c) => [
            <strong>{c.name}</strong>,
            <code>{c.db_name}</code>,
            <code>
              {c.host}:{c.port}
            </code>,
            <div className="actions">
              <button
                className="btn-sm"
                onClick={() =>
                  void a.save(
                    "connections/" + c.id + "/test",
                    "POST",
                    undefined,
                    undefined,
                    "Uji koneksi berhasil tersambung ke PostgreSQL target.",
                  )
                }
              >
                <PlugZap size={13} /> Uji Koneksi
              </button>
              <button className="btn-sm" onClick={() => setEdit(c)}>
                Edit
              </button>
              <button
                className="btn-sm btn-danger"
                onClick={() => {
                  if (confirm("Hapus koneksi " + c.name + "?"))
                    void a.save("connections/" + c.id, "DELETE");
                }}
              >
                <Trash2 size={13} /> Hapus
              </button>
            </div>,
          ])}
        />
      ) : (
        !error && <Skeleton />
      )}
    </Frame>
  );
}
