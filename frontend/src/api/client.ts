import { useSyncExternalStore } from "react";

let csrfToken = "";
let state = { pending: 0, error: "" };
const subscribers = new Set<() => void>();
function update(changes: Partial<typeof state>) {
  state = { ...state, ...changes };
  subscribers.forEach((fn) => fn());
}
export const clearError = () => update({ error: "" });
export const setToken = (token: string) => {
  csrfToken = token;
};
export function useApiState() {
  return useSyncExternalStore(
    (fn) => {
      subscribers.add(fn);
      return () => {
        subscribers.delete(fn);
      };
    },
    () => state,
  );
}

export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set("Accept", "application/json");
  if (options.body) headers.set("Content-Type", "application/json");
  if (csrfToken && /^(POST|PUT|PATCH|DELETE)$/i.test(options.method || "GET"))
    headers.set("X-CSRF-Token", csrfToken);
  update({ pending: state.pending + 1 });
  try {
    const response = await fetch("/api/v1" + path, {
      ...options,
      credentials: "include",
      headers,
    });
    const body = await response.text();
    let result;
    try {
      result = body ? JSON.parse(body) : undefined;
    } catch {
      throw new Error("Respons server tidak valid (" + response.status + ").");
    }
    if (!response.ok) {
      if (response.status === 401 && path !== "/auth/login")
        window.dispatchEvent(new Event("session-expired"));
      throw new Error(
        result?.error || "Permintaan gagal (" + response.status + ").",
      );
    }
    return result as T;
  } catch (e) {
    if (!options.signal?.aborted && path !== "/auth/me")
      update({ error: e instanceof Error ? e.message : "Permintaan gagal." });
    throw e;
  } finally {
    update({ pending: state.pending - 1 });
  }
}

export const mutate = <T>(path: string, method: string, body?: unknown) =>
  api<T>(path, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
