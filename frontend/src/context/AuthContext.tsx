import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { api, clearError, mutate, setToken } from "../api/client";
export interface User {
  id: string;
  username: string;
  is_platform_admin: boolean;
  leader_group_ids: string[];
  is_active: boolean;
}
interface Session {
  user: User;
  csrf_token: string;
}
interface Auth {
  user: User | null;
  ready: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}
const Context = createContext<Auth | undefined>(undefined);
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [ready, setReady] = useState(false);
  const accept = (s: Session) => {
    setToken(s.csrf_token);
    setUser(s.user);
  };
  const forget = () => {
    setToken("");
    setUser(null);
  };
  useEffect(() => {
    window.addEventListener("session-expired", forget);
    api<Session>("/auth/me")
      .then(accept)
      .catch(forget)
      .finally(() => setReady(true));
    return () => window.removeEventListener("session-expired", forget);
  }, []);
  async function login(username: string, password: string) {
    clearError();
    accept(
      await mutate<Session>("/auth/login", "POST", { username, password }),
    );
  }
  async function logout() {
    await mutate("/auth/logout", "POST");
    forget();
    clearError();
  }
  return (
    <Context.Provider value={{ user, ready, login, logout }}>
      {children}
    </Context.Provider>
  );
}
export function useAuth() {
  const ctx = useContext(Context);
  if (!ctx) throw new Error("AuthProvider required");
  return ctx;
}
