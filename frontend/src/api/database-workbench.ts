import request, { getToken, type PageResult } from "./request";
import type {
  OpsDatabase,
  DatabaseEvent,
  DatabaseDiagnostic,
  DatabaseActivity,
} from "./operations";

export interface SchemaInfo {
  name: string;
  objects: number;
}
export interface RelationInfo {
  oid: number;
  schema: string;
  name: string;
  kind: string;
  estimatedRows: number;
  comment: string;
}
export interface RelationDetail {
  relation: RelationInfo;
  columns: Array<{
    name: string;
    type: string;
    nullable: boolean;
    default: string;
    comment: string;
    position: number;
    primaryKey: boolean;
  }>;
  indexes: Array<{
    name: string;
    definition: string;
    primary: boolean;
    unique: boolean;
    valid: boolean;
  }>;
  definition: string;
  constraints: Array<{
    name: string;
    type: string;
    definition: string;
    validated: boolean;
    deferrable: boolean;
  }>;
  triggers: Array<{ name: string; definition: string; enabled: string }>;
  sequence?: {
    type: string;
    start: string;
    min: string;
    max: string;
    increment: string;
    cycle: boolean;
    cache: string;
    last: string | null;
  };
}
export interface RoutineInfo {
  oid: number;
  schema: string;
  name: string;
  kind: string;
  arguments: string;
  result: string;
  comment: string;
}
export interface RoutineDetail {
  routine: RoutineInfo;
  definition: string;
  language: string;
  owner: string;
  securityDefiner: boolean;
  volatility: string;
}
export interface WorkspaceDraft {
  id: number;
  name: string;
  databaseId: number;
  revision: number;
  target: string;
  schema: string;
  sql: string;
  parameters: string;
  maxRows: number;
  timeoutSeconds: number;
}
export interface WorkspaceState {
  version: 1;
  tabs: WorkspaceDraft[];
  closedTabs: WorkspaceDraft[];
  activeTabId: number;
  databaseId: number;
  pane: string;
  editorHeight: number;
  navigationOpen: boolean;
}
export interface WorkspaceResponse {
  state: WorkspaceState;
  revision: number;
  updatedAt: string | null;
}
export interface SavedQuery {
  id: number;
  name: string;
  databaseId: number;
  schema: string;
  sql: string;
  parametersJSON: string;
  revision: number;
  updatedAt: string;
}
export const loadWorkspace = () =>
  request.get<any, WorkspaceResponse>("/operations/database-workspace", {
    silentError: true,
  });
export async function saveWorkspace(
  state: WorkspaceState,
  revision: number,
): Promise<WorkspaceResponse> {
  const body = JSON.stringify({ state, revision });
  const response = await fetch("/api/operations/database-workspace", {
    method: "PUT",
    headers: {
      Authorization: `Bearer ${getToken()}`,
      "Content-Type": "application/json",
    },
    body,
    keepalive: new TextEncoder().encode(body).length < 60000,
    signal: AbortSignal.timeout(15000),
  });
  const data = await response.json();
  if (!response.ok || data.code !== 0) {
    const error = Object.assign(new Error(data.message || "工作区保存失败"), {
      conflict: data.code === 16002,
    });
    throw error;
  }
  return data.data;
}
export const listSavedQueries = () =>
  request.get<any, SavedQuery[]>("/operations/saved-queries", {
    silentError: true,
  });
export const saveQueryFavorite = (data: Partial<SavedQuery>) =>
  data.id
    ? request.put<any, SavedQuery>(`/operations/saved-queries/${data.id}`, data)
    : request.post<any, SavedQuery>("/operations/saved-queries", data);
export const deleteQueryFavorite = (row: SavedQuery) =>
  request.delete(`/operations/saved-queries/${row.id}`, {
    params: { revision: row.revision },
  });
export const getRoutines = (id: number, schema: string) =>
  request.get<any, RoutineInfo[]>(`/operations/databases/${id}/catalog`, {
    params: { schema, kind: "routines" },
    silentError: true,
  });
export const getRoutine = (id: number, oid: number) =>
  request.get<any, RoutineDetail>(
    `/operations/databases/${id}/objects/${oid}`,
    { params: { kind: "routine" }, silentError: true },
  );
export interface QueryRequest {
  sql: string;
  schema: string;
  revision: number;
  parameters?: unknown[];
  mode: "query" | "explain" | "export";
  maxRows: number;
  timeoutSeconds: number;
}
export interface QueryResult {
  recordId: number;
  databaseId: number;
  target: string;
  schema: string;
  status: string;
  error: string;
  sqlState: string;
  columns: Array<{ name: string; type: string }>;
  rows: Array<Array<string | null>>;
  rowCount: number;
  hasMore: boolean;
  durationMs: number;
  startedAt: string;
  finishedAt: string;
  plan?: Array<{ Plan: Record<string, any> }>;
  warning: string;
}
export interface QueryRecord {
  id: number;
  databaseId: number;
  revision: number;
  target: string;
  schema: string;
  sql: string;
  parametersJSON: string;
  kind: string;
  status: string;
  error: string;
  sqlState: string;
  rows: number;
  hasMore: boolean;
  durationMs: number;
  operator: string;
  startedAt: string;
}
export interface Activity extends DatabaseActivity {
  backendStart: string | null;
  stateChange: string | null;
  backendType: string;
  queryId: string;
  rootBlockingPids: number[];
  lockWaitSeconds: number | null;
}
export interface Snapshot extends Omit<DatabaseDiagnostic, "activities"> {
  activities: Activity[];
  statsVisible: boolean;
  blockingEdges: Array<{ blockerPid: number; waitingPid: number }>;
  blockingGroups: Array<{
    rootPid: number;
    waitingPids: number[];
    maxWaitSeconds: number | null;
    preparedTransaction: boolean;
    missingSession: boolean;
    cycle: boolean;
  }>;
  sessionSummary: {
    total: number;
    active: number;
    idle: number;
    idleInTransaction: number;
    waiting: number;
    longTransactions: number;
  };
}
export interface SlowTemplate {
  id: string;
  template: string;
  database: string;
  phase: string;
  logCount: number;
  totalMs: number;
  averageMs: number;
  maxMs: number;
  timeShare: number;
  firstSeen: string;
  lastSeen: string;
  samples: DatabaseEvent[];
  users: string[];
  clients: string[];
  normalized: boolean;
}
export interface SlowReport {
  from: string;
  to: string;
  logCount: number;
  templateCount: number;
  totalMs: number;
  maxMs: number;
  templates: SlowTemplate[];
  trend: Array<{
    time: string;
    logCount: number;
    totalMs: number;
    maxMs: number;
  }>;
  stepSeconds: number;
  source: OpsDatabase;
}
export interface StatementStat {
  queryId: string;
  username: string;
  sql: string;
  calls: number;
  totalMs: number;
  averageMs: number;
  maxMs: number;
  minMs: number;
  rows: number;
  sharedHits: number;
  sharedReads: number;
  tempWrites: number;
  timeShare: number;
}
export interface StatementReport {
  available: boolean;
  reason: string;
  checkedAt: string;
  statsReset: string | null;
  items: StatementStat[];
  totalTemplates: number;
  totalCalls: number;
  totalMs: number;
}
const base = (id: number) => `/operations/databases/${id}`;
export const getSchemas = (id: number) =>
  request.get<any, SchemaInfo[]>(`${base(id)}/catalog`, { silentError: true });
export const getRelations = (id: number, schema: string) =>
  request.get<any, RelationInfo[]>(`${base(id)}/catalog`, {
    params: { schema },
    silentError: true,
  });
export const getRelation = (id: number, oid: number) =>
  request.get<any, RelationDetail>(`${base(id)}/objects/${oid}`, {
    silentError: true,
  });
export const queryDatabase = (
  id: number,
  data: QueryRequest,
  signal?: AbortSignal,
) =>
  request.post<any, QueryResult>(`${base(id)}/query`, data, {
    signal,
    timeout: 40000,
    silentError: true,
  });
export const getSnapshot = (id: number) =>
  request.get<any, Snapshot>(`${base(id)}/diagnostics`, { silentError: true });
export const getQueryRecords = (id: number, page = 1) =>
  request.get<any, PageResult<QueryRecord>>(`${base(id)}/query-records`, {
    params: { page, pageSize: 25 },
    silentError: true,
  });
export const getSlowAnalysis = (id: number, params: Record<string, unknown>) =>
  request.get<any, SlowReport>(`${base(id)}/slow-analysis`, {
    params,
    silentError: true,
  });
export const getStatementStatistics = (id: number, order: string) =>
  request.get<any, StatementReport>(`${base(id)}/statements`, {
    params: { order },
    silentError: true,
  });
export async function exportQuery(
  id: number,
  data: QueryRequest,
  signal?: AbortSignal,
) {
  const response = await fetch(`/api${base(id)}/query`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${getToken()}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ ...data, mode: "export" }),
    signal,
  });
  if (
    !response.ok ||
    response.headers.get("content-type")?.includes("application/json")
  ) {
    const body = await response.json().catch(() => ({}));
    throw new Error(body.data?.error || body.message || "导出失败");
  }
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = `query-${response.headers.get("X-Query-Record-ID") || Date.now()}.csv`;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 60000);
}
