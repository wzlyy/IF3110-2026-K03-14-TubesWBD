import { useState, type FormEvent, type ReactNode } from "react";
import { mutate } from "../../api/client";
import {
  Fields,
  GridTable,
  Skeleton,
  useAction,
  useResource,
} from "../../components/shared";
import type { User } from "../../context/AuthContext";
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
const values = (event: FormEvent<HTMLFormElement>) => {
  event.preventDefault();
  return Object.fromEntries(new FormData(event.currentTarget)) as Record<
    string,
    string
  >;
};
function useAdmin() {
  const action = useAction(),
    [revision, setRevision] = useState(0);
  function save(
    path: string,
    method: string,
    body?: unknown,
    done?: () => void,
    message = "Perubahan tersimpan.",
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
  action,
  children,
}: {
  title: string;
  action: ReturnType<typeof useAdmin>;
  children: ReactNode;
}) {
  return (
    <>
      <h1>{title}</h1>
      {action.notice && (
        <p className="success" role="status">
          {action.notice}
        </p>
      )}
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
  const a = useAdmin(),
    { data, error } = useResource<{ users: User[] }>(
      "/admin/users",
      a.revision,
    ),
    [reset, setReset] = useState<User>();
  return (
    <Frame title="Pengguna" action={a}>
      <form
        className="panel"
        onSubmit={(e) => {
          const form = e.currentTarget,
            f = values(e);
          void a.save(
            "users",
            "POST",
            { ...f, is_platform_admin: f.is_platform_admin === "on" },
            () => form.reset(),
          );
        }}
      >
        <h2>Buat pengguna</h2>
        <div className="fields">
          <Fields name="username" label="Username" />
          <Fields name="password" label="Password" type="password" />
          <label className="check">
            <input name="is_platform_admin" type="checkbox" />
            Administrator
          </label>
        </div>
        <button className="primary">Buat pengguna</button>
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
            );
          }}
        >
          <h2>Reset password · {reset.username}</h2>
          <Fields name="new_password" label="Password baru" type="password" />
          <button>Simpan password</button>{" "}
          <button type="button" onClick={() => setReset(undefined)}>
            Batal
          </button>
        </form>
      )}
      {!data ? (
        error ? null : (
          <Skeleton />
        )
      ) : (
        <GridTable
          columns={["Username", "Status", "Peran", "Tindakan"]}
          rows={data.users.map((u) => [
            u.username,
            u.is_active ? "Aktif" : "Nonaktif",
            u.is_platform_admin ? "Administrator" : "Anggota",
            <div className="actions">
              <button
                onClick={() =>
                  void a.save("users/" + u.id + "/toggle-active", "POST")
                }
              >
                {u.is_active ? "Nonaktifkan" : "Aktifkan"}
              </button>
              <button
                onClick={() =>
                  void a.save("users/" + u.id + "/toggle-admin", "POST")
                }
              >
                {u.is_platform_admin ? "Cabut admin" : "Jadikan admin"}
              </button>
              <button onClick={() => setReset(u)}>Reset password</button>
            </div>,
          ])}
        />
      )}
    </Frame>
  );
}
export function Groups() {
  const a = useAdmin(),
    groups = useResource<{ groups: Group[] }>("/admin/groups", a.revision),
    users = useResource<{ users: User[] }>("/admin/users"),
    [edit, setEdit] = useState<Group>();
  const options = (users.data?.users || [])
    .filter((u) => u.is_active)
    .map((u) => ({ id: u.id, name: u.username }));
  return (
    <Frame title="Grup" action={a}>
      <form
        className="panel"
        key={edit?.id || "create"}
        onSubmit={(e) => {
          const form = e.currentTarget,
            f = values(e);
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
        <h2>{edit ? "Edit grup" : "Buat grup"}</h2>
        <div className="fields">
          <Fields name="name" label="Nama grup" value={edit?.name} />
          <Pick
            name="leader_user_id"
            label="Pemimpin grup"
            options={options}
            value={edit?.leader_user_id}
          />
        </div>
        <button className="primary">{edit ? "Simpan" : "Buat grup"}</button>{" "}
        {edit && (
          <button type="button" onClick={() => setEdit(undefined)}>
            Batal
          </button>
        )}
      </form>
      {groups.error ? null : !groups.data ? (
        <Skeleton />
      ) : !groups.data?.groups.length ? (
        <p className="empty">Belum ada grup.</p>
      ) : (
        groups.data.groups.map((g) => (
          <article className="panel" key={g.id}>
            <div className="row">
              <div>
                <h2>{g.name}</h2>
                <p>Pemimpin · {g.leader_username}</p>
              </div>
              <div className="actions">
                <button onClick={() => setEdit(g)}>Edit</button>
                <button
                  onClick={() => {
                    if (confirm("Hapus grup " + g.name + "?"))
                      void a.save("groups/" + g.id, "DELETE");
                  }}
                >
                  Hapus
                </button>
              </div>
            </div>
            <ul className="members">
              {(g.members || []).map((m) => (
                <li key={m.user_id}>
                  {m.username}
                  {m.user_id !== g.leader_user_id && (
                    <button
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
                label="Anggota baru"
                options={options.filter(
                  (u) => !g.members?.some((m) => m.user_id === u.id),
                )}
              />
              <button>Tambah anggota</button>
            </form>
          </article>
        ))
      )}
    </Frame>
  );
}
export function Grants() {
  const a = useAdmin(),
    grants = useResource<{ grants: Grant[] }>("/admin/grants", a.revision),
    groups = useResource<{ groups: Group[] }>("/admin/groups"),
    connections = useResource<{ connections: Connection[] }>(
      "/admin/connections",
    );
  return (
    <Frame title="Hak akses" action={a}>
      <form
        className="panel"
        onSubmit={(e) => void a.save("grants", "POST", values(e))}
      >
        <h2>Atur akses grup</h2>
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
        <button className="primary">Simpan akses</button>
      </form>
      {grants.data ? (
        <GridTable
          columns={["Grup", "Database", "Hak akses", "Tindakan"]}
          rows={grants.data.grants.map((g) => [
            g.group_name,
            g.database_name,
            <span className="badge">{g.access_level}</span>,
            <button
              onClick={() => {
                if (confirm("Cabut hak akses grup " + g.group_name + "?"))
                  void a.save("grants", "DELETE", {
                    group_id: g.group_id,
                    database_id: g.database_id,
                  });
              }}
            >
              Cabut akses
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
  const a = useAdmin(),
    { data, error } = useResource<{ connections: Connection[] }>(
      "/admin/connections",
      a.revision,
    ),
    [edit, setEdit] = useState<Connection>();
  return (
    <Frame title="Koneksi" action={a}>
      <form
        key={edit?.id || "create"}
        className="panel"
        onSubmit={(e) => {
          const form = e.currentTarget,
            f = values(e);
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
        <h2>{edit ? "Edit koneksi" : "Tambah koneksi PostgreSQL"}</h2>
        <div className="fields">
          <Fields name="name" label="Nama koneksi" value={edit?.name} />
          <Fields name="host" label="Host" value={edit?.host} />
          <Fields
            name="port"
            label="Port"
            type="number"
            value={edit?.port || 5432}
          />
          <Fields name="db_name" label="Nama database" value={edit?.db_name} />
          <Fields name="username" label="Username" value={edit?.username} />
          <Fields
            name="password"
            label={edit ? "Password baru (opsional)" : "Password"}
            type="password"
            required={!edit}
          />
        </div>
        <button className="primary">Simpan koneksi</button>{" "}
        {edit && (
          <button type="button" onClick={() => setEdit(undefined)}>
            Batal
          </button>
        )}
      </form>
      {data ? (
        <GridTable
          columns={["Koneksi", "Database", "Host", "Tindakan"]}
          rows={(data.connections || []).map((c) => [
            c.name,
            c.db_name,
            c.host + ":" + c.port,
            <div className="actions">
              <button
                onClick={() =>
                  void a.save(
                    "connections/" + c.id + "/test",
                    "POST",
                    undefined,
                    undefined,
                    "Uji koneksi berhasil.",
                  )
                }
              >
                Uji koneksi
              </button>
              <button onClick={() => setEdit(c)}>Edit</button>
              <button
                onClick={() => {
                  if (confirm("Hapus koneksi " + c.name + "?"))
                    void a.save("connections/" + c.id, "DELETE");
                }}
              >
                Hapus
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
