import request from "./request";
export interface ManagedService {
  id: number;
  serverId: number;
  name: string;
  mode: "script" | "systemd";
  unit: string;
  runUser: string;
  directory: string;
  startScript: string;
  identity: string;
  logPath: string;
  operatorsJSON: string;
  createdBy: string;
  canConfigure: boolean;
  canOperate: boolean;
}
export interface ManagedServiceStatus {
  status: string;
  message: string;
  checkedAt: string;
}
export interface ServiceOperation {
  id: number;
  serviceId: number;
  action: string;
  status: string;
  operator: string;
  message: string;
  startedAt: string;
  finishedAt: string | null;
}
const base = (host: number) => `/operations/hosts/${host}/services`;
export const listServices = (host: number) =>
  request.get<any, { services: ManagedService[]; canCreate: boolean }>(
    base(host),
  );
export const saveService = (host: number, row: Record<string, unknown>) =>
  row.id
    ? request.put(`${base(host)}/${row.id}`, row)
    : request.post(base(host), row);
export const deleteService = (host: number, id: number) =>
  request.delete(`${base(host)}/${id}`);
export const getServiceStatus = (host: number, id: number) =>
  request.get<any, ManagedServiceStatus>(`${base(host)}/${id}/status`);
export const getServiceLogs = (host: number, id: number) =>
  request.get<any, { text: string }>(`${base(host)}/${id}/logs`);
export const serviceAction = (
  host: number,
  id: number,
  action: "start" | "restart",
) =>
  request.post<any, ServiceOperation>(`${base(host)}/${id}/operations`, {
    action,
  });
export const getServiceOperations = (host: number, id: number) =>
  request.get<any, ServiceOperation[]>(`${base(host)}/${id}/operations`);
