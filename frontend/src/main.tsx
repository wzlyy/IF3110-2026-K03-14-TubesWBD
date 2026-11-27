import { useEffect } from "react";
import { createRoot } from "react-dom/client";
import {
  BrowserRouter,
  Routes,
  Route,
  Navigate,
  Outlet,
  useLocation,
} from "react-router-dom";
import { AuthProvider, useAuth } from "./context/AuthContext";
import { clearError, useApiState } from "./api/client";
import { NoticeBanner, Skeleton } from "./components/shared";
import Layout from "./components/Layout";
import LoginPage from "./pages/LoginPage";
import { Users, Groups, Grants, Connections } from "./pages/admin/Admin";
import { Databases, Explorer } from "./pages/databases/Explorer";
import { migrationRoutes } from "./pages/migrations/routes";
import "./styles.css";
function AdminOnly() {
  return useAuth().user?.is_platform_admin ? (
    <Outlet />
  ) : (
    <Navigate to="/databases" replace />
  );
}
function App() {
  const { ready } = useAuth(),
    { pending, error } = useApiState(),
    location = useLocation();
  useEffect(() => {
    clearError();
  }, [location.pathname]);
  return (
    <>
      {pending > 0 && (
        <div className="loadbar" role="progressbar" aria-label="Memuat" />
      )}
      <NoticeBanner
        kind="error"
        message={error}
        durationMs={8000}
        onClose={clearError}
      />
      {!ready ? (
        <Skeleton />
      ) : (
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route element={<Layout />}>
            <Route index element={<Navigate to="/databases" replace />} />
            <Route path="/databases" element={<Databases />} />
            {["schema", "data", "explain", "diff"].map((page) => (
              <Route
                key={page}
                path={"/databases/:id/" + page}
                element={<Explorer mode={page} key={page} />}
              />
            ))}
            <Route element={<AdminOnly />}>
              {Object.entries({
                users: Users,
                groups: Groups,
                grants: Grants,
                connections: Connections,
              }).map(([path, Page]) => (
                <Route key={path} path={"/admin/" + path} element={<Page />} />
              ))}
            </Route>
            {migrationRoutes}
            <Route path="*" element={<Navigate to="/databases" replace />} />
          </Route>
        </Routes>
      )}
    </>
  );
}
createRoot(document.getElementById("root")!).render(
  <BrowserRouter>
    <AuthProvider>
      <App />
    </AuthProvider>
  </BrowserRouter>,
);
