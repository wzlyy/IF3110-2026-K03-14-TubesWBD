import {
  Database,
  Users,
  UsersRound,
  KeyRound,
  Cable,
  ClipboardList,
  LogOut,
  Menu,
  X,
  ChevronRight,
  ShieldCheck,
} from "lucide-react";
import { NavLink, Navigate, Outlet, useLocation } from "react-router-dom";
import { useEffect, useState } from "react";
import { useAuth } from "../context/AuthContext";

const adminLinks = [
  ["Pengguna", "users", Users],
  ["Grup & Leader", "groups", UsersRound],
  ["Hak Akses", "grants", KeyRound],
  ["Koneksi Target", "connections", Cable],
] as const;

function pageTitleFromPath(pathname: string): [string, string] {
  if (pathname.startsWith("/admin/users")) return ["Administrasi", "Pengguna"];
  if (pathname.startsWith("/admin/groups"))
    return ["Administrasi", "Grup & Leader"];
  if (pathname.startsWith("/admin/grants")) return ["Administrasi", "Hak Akses"];
  if (pathname.startsWith("/admin/connections"))
    return ["Administrasi", "Koneksi Target"];
  if (pathname.startsWith("/requests/"))
    return ["Ruang Kerja", "Detail Persetujuan"];
  if (pathname.startsWith("/requests"))
    return ["Ruang Kerja", "Antrean Tinjauan"];
  if (pathname.startsWith("/jobs/")) return ["Ruang Kerja", "Pemantauan Job"];
  if (/^\/databases\/[^/]+\/schema/.test(pathname))
    return ["Database", "Schema Viewer"];
  if (/^\/databases\/[^/]+\/data/.test(pathname))
    return ["Database", "Data Viewer"];
  if (/^\/databases\/[^/]+\/explain/.test(pathname))
    return ["Database", "EXPLAIN Plan"];
  if (/^\/databases\/[^/]+\/diff/.test(pathname))
    return ["Database", "Schema Diff"];
  if (/^\/databases\/[^/]+\/migrations/.test(pathname))
    return ["Database", "Definisi Migrasi"];
  if (/^\/databases\/[^/]+\/history/.test(pathname))
    return ["Database", "Riwayat Eksekusi"];
  return ["Ruang Kerja", "Daftar Database"];
}

export default function Layout() {
  const { user, logout } = useAuth();
  const [open, setOpen] = useState(false);
  const { pathname } = useLocation();

  useEffect(() => {
    setOpen(false);
  }, [pathname]);

  useEffect(() => {
    const close = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, []);

  if (!user) return <Navigate to="/login" replace />;

  const [sectionLabel, currentLabel] = pageTitleFromPath(pathname);
  const roleLabel = user.is_platform_admin
    ? "Administrator"
    : user.leader_group_ids?.length
      ? "Leader Grup"
      : "Anggota";

  return (
    <div className="shell">
      {open && (
        <button
          className="scrim"
          aria-label="Tutup navigasi"
          onClick={() => setOpen(false)}
        />
      )}
      <aside id="sidebar" className={open ? "open" : ""}>
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">
            <Database size={18} />
          </span>
          <span>Database Platform</span>
          <button
            className="mobile-only"
            aria-label="Tutup navigasi"
            onClick={() => setOpen(false)}
          >
            <X size={18} />
          </button>
        </div>
        <nav aria-label="Navigasi utama">
          <small>Ruang Kerja</small>
          <NavLink to="/databases">
            <Database />
            Database
          </NavLink>
          <NavLink to="/requests">
            <ClipboardList />
            Antrean Tinjauan
          </NavLink>
          {user.is_platform_admin && (
            <>
              <small>Administrasi</small>
              {adminLinks.map(([title, path, Icon]) => (
                <NavLink key={path} to={"/admin/" + path}>
                  <Icon />
                  {title}
                </NavLink>
              ))}
            </>
          )}
        </nav>
        <div className="profile">
          <span className="avatar" aria-hidden="true">
            {user.username.slice(0, 1).toUpperCase()}
          </span>
          <div>
            <b>{user.username}</b>
            <small>{roleLabel}</small>
          </div>
          <button
            aria-label="Keluar"
            title="Keluar dari platform"
            onClick={() => void logout().catch(() => {})}
          >
            <LogOut size={17} />
          </button>
        </div>
      </aside>
      <main className="workspace">
        <header>
          <div className="topbar-left">
            <button
              className="mobile-only"
              aria-label="Buka navigasi"
              aria-controls="sidebar"
              aria-expanded={open}
              onClick={() => setOpen(true)}
            >
              <Menu size={18} />
            </button>
            <div className="topbar-crumb" aria-label="Lokasi halaman">
              <span>{sectionLabel}</span>
              <ChevronRight size={14} />
              <strong>{currentLabel}</strong>
            </div>
          </div>
          <div className="topbar-actions">
            <NavLink to="/databases" className="topbar-pill">
              <Database size={14} />
              <span>Database</span>
            </NavLink>
            <NavLink to="/requests" className="topbar-pill">
              <ClipboardList size={14} />
              <span>Tinjauan</span>
            </NavLink>
            {user.is_platform_admin && (
              <NavLink to="/admin/users" className="topbar-pill">
                <ShieldCheck size={14} />
                <span>Admin</span>
              </NavLink>
            )}
          </div>
        </header>
        <div className="content">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
