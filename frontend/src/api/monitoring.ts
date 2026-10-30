import request from "./request";
import type { InspectionResult, InspectionRun } from "./operations";

export const componentKinds = [
  { value: "postgresql", label: "PostgreSQL" },
  { value: "kubernetes", label: "Kubernetes" },
  { value: "minio", label: "MinIO" },
  { value: "elasticsearch", label: "Elasticsearch" },
  { value: "redis", label: "Redis" },
  { value: "rabbitmq", label: "RabbitMQ" },
];
export interface MonitoringLane {
  lane: string;
  intervalSeconds: number;
  collectedAt: string | null;
  lastAttemptAt: string | null;
  lastError: string;
  stale: boolean;
  collecting: boolean;
  result?: InspectionResult;
}
export interface MonitoringView extends MonitoringLane {
  targetType: string;
  id: number;
  kind: string;
  name: string;
  endpoint: string;
  enabled: boolean;
  status: string;
  summary: string;
  indicators: Record<string, number>;
  lanes?: MonitoringLane[];
}
export interface MonitoringPoint { collectedAt: string; indicators: Record<string, number> }
export const listMonitoring = () => request.get<any, MonitoringView[]>("/operations/monitoring");
export const getMonitoring = (kind: string, id: number) => request.get<any, MonitoringView>(`/operations/monitoring/${kind}/${id}`, { params: { view: "dashboard" } });
export const getMonitoringHistory = (kind: string, id: number, hours: number) => request.get<any, {samples: MonitoringPoint[]; stepSeconds: number}>(`/operations/monitoring/${kind}/${id}/history`, {params: {hours}});
export const refreshMonitoring = (kind: string, id: number) => request.post(`/operations/monitoring/${kind}/${id}/refresh`);
export interface ComponentOptions {
  namespace?: string;
  alias?: string;
  dailyFile?: string;
  backupDirectory?: string;
}
export interface ComponentConnection {
  id: number;
  kind: string;
  name: string;
  endpoint: string;
  username: string;
  source: string;
  serverId: number;
  enabled: boolean;
  optionsJSON: string;
  revision: number;
}
export interface ComponentInput {
  id?: number;
  kind: string;
  name: string;
  endpoint: string;
  username: string;
  secret: string;
  clearSecret: boolean;
  serverId: number;
  enabled: boolean;
  options: ComponentOptions;
}
export interface CollectionSample {
  id: number;
  targetType: string;
  targetId: number;
  revision: number;
  collectedAt: string;
  resultJSON: string;
}
export const listComponents = () =>
  request.get<any, ComponentConnection[]>("/operations/components");
export const saveComponent = (row: ComponentInput) =>
  row.id
    ? request.put(`/operations/components/${row.id}`, row)
    : request.post("/operations/components", row);
export const deleteComponent = (id: number) =>
  request.delete(`/operations/components/${id}`);
export const getCollection = (kind: string, id: number) =>
  request.get<any, CollectionSample | null>(
    `/operations/collections/${kind}/${id}`,
  );
export const collectComponent = (id: number) =>
  request.post<any, InspectionResult>(
    `/operations/components/${id}/collect`,
    {},
    { timeout: 110000 },
  );
export const collectDatabaseInspection = (id: number) =>
  request.post<any, InspectionResult>(
    `/operations/databases/${id}/inspection`,
    {},
    { timeout: 135000 },
  );
export const createConfiguredInspection = (
  name: string,
  targets?: Array<{ type: string; id: number }>,
) =>
  request.post<any, InspectionRun>("/operations/configured-inspections", {
    name,
    targets,
  });
