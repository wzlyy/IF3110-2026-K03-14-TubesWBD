import { Database, ArrowRight } from "lucide-react";
import { Navigate } from "react-router-dom";
import { useAuth } from "../context/AuthContext";
import { useApiState } from "../api/client";
export default function LoginPage() {
  const { user, login } = useAuth(),
    { pending } = useApiState();
  if (user) return <Navigate to="/databases" replace />;
  return (
    <main className="login">
      <section className="login-box">
        <div className="brand">
          <Database /> Database Platform
        </div>
        <h1>Masuk</h1>
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
            <input name="username" autoComplete="username" required autoFocus />
          </label>
          <label>
            Password
            <input
              type="password"
              name="password"
              autoComplete="current-password"
              required
            />
          </label>
          <button className="primary" disabled={!!pending}>
            Masuk <ArrowRight size={16} />
          </button>
        </form>
      </section>
    </main>
  );
}
