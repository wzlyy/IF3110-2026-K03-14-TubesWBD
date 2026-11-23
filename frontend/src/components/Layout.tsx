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
  PanelLeft,
} from "lucide-react";
import { NavLink, Navigate, Outlet, useLocation } from "react-router-dom";
import { useEffect, useState } from "react";
import { useAuth } from "../context/AuthContext";
const adminLinks = [
  ["Pengguna", "users", Users],
  ["Grup", "groups", UsersRound],
  ["Hak akses", "grants", KeyRound],
  ["Koneksi", "connections", Cable],
] as const;
export default function Layout() {
  const { user, logout } = useAuth(),
    [open, setOpen] = useState(false),
    { pathname } = useLocation();
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
          <Database /> Database Platform
          <button
            className="mobile-only"
            aria-label="Tutup navigasi"
            onClick={() => setOpen(false)}
          >
            <X />
          </button>
        </div>
        <nav>
          <small>Ruang kerja</small>
          <NavLink to="/databases">
            <Database />
            Database
          </NavLink>
          <NavLink to="/requests">
            <ClipboardList />
            Permintaan
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
          <span className="avatar">
            {user.username.slice(0, 1).toUpperCase()}
          </span>
          <div>
            <b>{user.username}</b>
            <small>
              {user.is_platform_admin ? "Administrator" : "Anggota"}
            </small>
          </div>
          <button
            aria-label="Keluar"
            title="Keluar"
            onClick={() => void logout().catch(() => {})}
          >
            <LogOut size={18} />
          </button>
        </div>
      </aside>
      <main className="workspace">
        <header>
          <button
            className="mobile-only"
            aria-label="Buka navigasi"
            aria-controls="sidebar"
            aria-expanded={open}
            onClick={() => setOpen(true)}
          >
            <Menu />
          </button>
          <PanelLeft className="desktop-only" size={16} />
          {pathname.startsWith("/admin/") ? "Administrasi" : "Ruang kerja"}
        </header>
        <div className="content">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
