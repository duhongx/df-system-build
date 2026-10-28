import request from "./request";

export interface AccessRole {
  name: string;
  login: boolean;
  superuser: boolean;
  createDB: boolean;
  createRole: boolean;
  replication: boolean;
  bypassRLS: boolean;
  inherit: boolean;
  limit: number;
  validUntil: string;
  memberOf: string[];
}
export interface AccessSchema {
  name: string;
  owner: string;
  objects: number;
}
export interface AccessCatalog {
  database: string;
  actor: string;
  version: number;
  superuser: boolean;
  canCreateRoles: boolean;
  canCreateSchema: boolean;
  roles: AccessRole[];
  schemas: AccessSchema[];
}
export interface AccessRequest {
  revision: number;
  action:
    | "create_role"
    | "create_schema"
    | "permissions"
    | "membership"
    | "password";
  role: string;
  password: string;
  login: boolean;
  schema: string;
  owner: string;
  mode: "" | "readOnly" | "readWrite";
  allSchemas: boolean;
  schemas: string[];
  future: boolean;
  creators: string[];
  memberRoles: string[];
  remove: boolean;
  previewHash?: string;
}
export interface AccessPlan {
  hash: string;
  target: string;
  summary: string;
  sql: string[];
  blockers: string[];
  notes: string[];
  schemas: string[];
}
export interface AccessResult {
  recordId: number;
  status: string;
  message: string;
  warning: string;
}
export interface AccessPrivileges {
  role: string;
  schemas: Array<{
    schema: string;
    owner: string;
    usage: boolean;
    create: boolean;
    tables: number;
    readable: number;
    writable: number;
    sequences: number;
    advanceable: number;
  }>;
  defaults: Array<{
    creator: string;
    schema: string;
    kind: string;
    source: string;
    privileges: string;
  }>;
}
const base = (id: number) => `/operations/databases/${id}/access`;
export const getDatabaseAccess = (id: number) =>
  request.get<any, AccessCatalog>(base(id), { silentError: true });
export const getRolePrivileges = (id: number, role: string) =>
  request.get<any, AccessPrivileges>(`${base(id)}/privileges`, {
    params: { role },
    silentError: true,
  });
export const previewDatabaseAccess = (id: number, data: AccessRequest) =>
  request.post<any, AccessPlan>(`${base(id)}/preview`, data, {
    timeout: 50000,
    silentError: true,
  });
export const applyDatabaseAccess = (id: number, data: AccessRequest) =>
  request.post<any, AccessResult>(`${base(id)}/apply`, data, {
    timeout: 50000,
    silentError: true,
  });
