import { useNavigate } from "react-router-dom";
import { api } from "../../api/client";

export interface Draft {
  name: string;
  description: string;
  sql_up: string;
  sql_down: string;
  is_irreversible: boolean;
  irreversible_reason: string;
  execution_mode: string;
  batch_table: string;
  batch_cursor_column: string;
  batch_size: number | null;
}
export interface Definition extends Draft {
  id: string;
  version: number;
  revision: number;
  is_immutable: boolean;
}
export interface Execution {
  migration_id: string;
  execution_id: string;
  job_id: string;
  migration_name: string;
  migration_version: number;
  migration_revision: number;
  action_type: string;
  reverted_execution_id?: string;
  parent_execution_id?: string;
  attempt_number?: number;
  started_at: string;
  executed_by_username: string;
  status: string;
  total_batches_committed: number;
  total_rows_affected: number;
  executed_sql_snapshot: string;
  error_message?: string;
  error_sqlstate?: string;
}
export interface Definitions {
  access_level: string;
  definitions: Definition[];
  active_tip?: Execution;
  blocking_execution?: Execution;
  applied_migration_ids: Record<string, boolean>;
  rolled_back_migrations: Record<string, boolean>;
  failed_migrations: Record<string, boolean>;
}
export interface Change {
  id: string;
  database_id: string;
  database_name: string;
  migration_name: string;
  migration_version: number;
  status: string;
  action_type: string;
  target_execution_id?: string;
  job_id?: string;
  sql_preview: string;
  approval_stale?: boolean;
  requested_by_username: string;
  approvals?: {
    group_id: string;
    group_name: string;
    leader_user_id: string;
    leader_username: string;
    required_access_level: string;
    decision: string;
    comment?: string;
  }[];
}
export const dbPath = (db: string) => "/databases/" + encodeURIComponent(db);
export function useCreateRequest() {
  const navigate = useNavigate();
  return async (
    db: string,
    migration_id: string,
    action_type: string,
    target_execution_id?: string,
  ) => {
    const result = await api<Change>(dbPath(db) + "/requests", {
      method: "POST",
      body: JSON.stringify({ migration_id, action_type, target_execution_id }),
    });
    navigate("/requests/" + result.id);
  };
}
export function linearAllowed(d: Definition, s: Definitions) {
  const tip = s.active_tip?.migration_version ?? 0;
  return (
    !s.blocking_execution &&
    !s.applied_migration_ids[d.id] &&
    d.version > tip &&
    !s.definitions.some(
      (prior) =>
        prior.version > tip &&
        prior.version < d.version &&
        !s.applied_migration_ids[prior.id] &&
        (s.rolled_back_migrations[prior.id] || !s.failed_migrations[prior.id]),
    )
  );
}
