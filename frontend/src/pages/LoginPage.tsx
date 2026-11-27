import { useState } from "react";
import { Database, Eye, EyeOff } from "lucide-react";
import { Navigate } from "react-router-dom";
import { useAuth } from "../context/AuthContext";
import { useApiState } from "../api/client";

export default function LoginPage() {
  const { user, login } = useAuth();
  const { pending } = useApiState();
  const [showPassword, setShowPassword] = useState(false);

  if (user) return <Navigate to="/databases" replace />;

  return (
    <main className="login">
      <section className="login-box" aria-labelledby="login-title">
        <div className="login-header">
          <div className="login-icon" aria-hidden="true">
            <Database size={28} strokeWidth={2} />
          </div>
          <h1 id="login-title">Database Platform</h1>
          <p>Masuk untuk mengelola database dan migration</p>
        </div>
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            const f = new FormData(e.currentTarget);
            try {
              await login(String(f.get("username")), String(f.get("password")));
            } catch {
              /* Banner handles login errors. */
            }
          }}
        >
          <label>
            Username
            <input
              name="username"
              placeholder="Masukkan username"
              autoComplete="username"
              required
              autoFocus
            />
          </label>
          <label>
            Password
            <div className="password-field">
              <input
                type={showPassword ? "text" : "password"}
                name="password"
                placeholder="Masukkan password"
                autoComplete="current-password"
                required
              />
              <button
                type="button"
                className="password-toggle"
                aria-label={
                  showPassword ? "Sembunyikan password" : "Tampilkan password"
                }
                onClick={() => setShowPassword((prev) => !prev)}
              >
                {showPassword ? <EyeOff size={17} /> : <Eye size={17} />}
              </button>
            </div>
          </label>
          <button className="primary login-submit" disabled={!!pending}>
            {pending ? "Memproses…" : "Masuk ke Platform"}
          </button>
        </form>
      </section>
    </main>
  );
}
