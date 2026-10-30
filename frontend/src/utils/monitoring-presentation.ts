import type { MonitoringView } from "../api/monitoring";
import type { InspectionCommandResult } from "../api/operations";

export const componentLabels: Record<string, string> = {
  hosts: "Linux", postgresql: "PostgreSQL", kubernetes: "Kubernetes", minio: "MinIO",
  elasticsearch: "Elasticsearch", redis: "Redis", rabbitmq: "RabbitMQ",
};
export const monitorStatusLabels: Record<string, string> = {
  NORMAL: "正常", WARNING: "需关注", CRITICAL: "异常", ERROR: "数据获取失败",
  PARTIAL: "部分数据不可用", STALE: "数据已过期", DISABLED: "已停用", PENDING: "等待数据",
};
export function liveChecks(view?: MonitoringView): InspectionCommandResult[] {
  return view?.result?.reportChecks || view?.lanes?.find(l => l.lane === "live")?.result?.reportChecks || [];
}
function values(view?: MonitoringView): Record<string, number> {
  const result: Record<string, number> = {};
  for (const check of liveChecks(view)) {
    if (check.status !== "UNKNOWN") Object.assign(result, check.measurements || {});
  }
  Object.assign(result, view?.indicators || {});
  return result;
}
export function formatIndicator(value?: number, divisor = 1, decimals = 0): string {
  return value == null || !Number.isFinite(value) ? "—" : (value / divisor).toFixed(decimals);
}
type MetricSpec = { label: string; key: string; unit?: string; divisor?: number; decimals?: number };
const specs: Record<string, MetricSpec[]> = {
  postgresql: [
    { label: "当前连接", key: "connections" }, { label: "连接上限", key: "maxConnections" },
    { label: "锁等待", key: "lockWaits" }, { label: "慢 SQL", key: "slowQueries" },
    { label: "长事务", key: "longTransactions" }, { label: "物理备库连接", key: "physicalReplicas" },
  ],
  kubernetes: [
    { label: "节点", key: "nodes" }, { label: "异常节点", key: "unhealthyNodes" },
    { label: "Pod", key: "pods" }, { label: "异常 Pod", key: "unreadyPods" },
  ],
  minio: [
    { label: "节点", key: "nodes" }, { label: "在线磁盘", key: "onlineDisks" },
    { label: "离线磁盘", key: "offlineDisks" }, { label: "最高磁盘使用率", key: "maxDiskPercent", unit: "%", decimals: 1 },
  ],
  elasticsearch: [
    { label: "节点", key: "nodes" }, { label: "活跃分片", key: "activeShards" },
    { label: "未分配分片", key: "unassignedShards" }, { label: "最高 JVM 堆使用率", key: "maxHeapPercent", unit: "%", decimals: 1 },
  ],
  redis: [
    { label: "连接", key: "connections" }, { label: "操作速率", key: "operationsPerSecond", unit: "次/s" },
    { label: "已用内存", key: "memoryBytes", unit: "MiB", divisor: 1048576, decimals: 1 },
    { label: "累计淘汰键", key: "evictedKeys" },
  ],
  rabbitmq: [
    { label: "连接", key: "connections" }, { label: "消费者", key: "consumers" },
    { label: "待消费消息", key: "readyMessages" }, { label: "未确认消息", key: "unackedMessages" },
  ],
};
export function metricCards(kind: string, view?: MonitoringView) {
  const indicators = values(view);
  return (specs[kind] || []).map(spec => ({ ...spec, value: formatIndicator(indicators[spec.key], spec.divisor, spec.decimals) }));
}
export function instanceFacts(kind: string, view?: MonitoringView) {
  const checks = liveChecks(view);
  if (kind === "redis") {
    const info = checks.find(c => c.code === "redis-info")?.attributes;
    const persistence = checks.find(c => c.code === "redis-persistence")?.attributes;
    if (!info) return [];
    return [{ label: "版本", value: info.version || "—" }, { label: "角色", value: info.role === "master" ? "主节点" : info.role === "slave" ? "副本" : "—" }, { label: "RDB", value: persistence?.rdbStatus === "ok" ? "正常" : persistence?.rdbStatus === "err" ? "失败" : "—" }, { label: "AOF", value: persistence?.aofEnabled === "1" ? "已开启" : persistence?.aofEnabled === "0" ? "未开启" : "—" }];
  }
  if (kind === "rabbitmq") {
    const info = checks.find(c => c.code === "rabbitmq-overview")?.attributes;
    return info ? [{ label: "集群", value: info.clusterName || "—" }, { label: "版本", value: info.version || "—" }] : [];
  }
  if (kind === "elasticsearch") {
    const info = checks.find(c => c.code === "elasticsearch-health")?.attributes;
    return info ? [{ label: "集群", value: info.clusterName || "—" }, { label: "健康状态", value: ({ green: "正常", yellow: "副本未完全分配", red: "主分片不可用" }[info.health || ""] || "未识别") }] : [];
  }
  if (kind === "minio") {
    const info = checks.find(c => c.code === "cloudhis-3")?.attributes;
    return info ? [{ label: "部署架构", value: info.architecture || "待识别" }] : [];
  }
  if (kind !== "postgresql") return [];
  const deployment = checks.find(c => c.code === "cloudhis-2");
  const archive = checks.find(c => c.code === "postgresql-archive");
  return [
    { label: "部署架构", value: deployment?.attributes?.architecture || "待识别" },
    { label: "版本", value: deployment?.attributes?.version || "—" },
    { label: "实例角色", value: deployment?.attributes?.role || "—" },
    { label: "WAL 归档", value: archive?.attributes?.archiveState || "—" },
  ];
}
export function monitoringSummary(view: MonitoringView): string {
  if (view.lastError) return view.lastError;
  const i = values(view);
  const number = (key: string, digits = 0) => formatIndicator(i[key], 1, digits);
  switch (view.kind) {
    case "hosts": return `CPU ${number("cpuPercent", 1)}% · 内存 ${number("memoryPercent", 1)}% · 负载 ${number("load1", 2)}`;
    case "postgresql": return `连接 ${number("connections")} · 锁等待 ${number("lockWaits")} · 慢 SQL ${number("slowQueries")}`;
    case "kubernetes": return `节点 ${number("nodes")} · Pod ${number("pods")} · 异常 Pod ${number("unreadyPods")}`;
    case "minio": return `节点 ${number("nodes")} · 磁盘在线 ${number("onlineDisks")} / 离线 ${number("offlineDisks")}`;
    case "elasticsearch": return `节点 ${number("nodes")} · 活跃分片 ${number("activeShards")} · 未分配 ${number("unassignedShards")}`;
    case "redis": return `连接 ${number("connections")} · 操作 ${number("operationsPerSecond")} 次/s`;
    case "rabbitmq": return `连接 ${number("connections")} · 待消费 ${number("readyMessages")} · 未确认 ${number("unackedMessages")}`;
    default: return "等待数据";
  }
}
export function currentIssues(view?: MonitoringView) {
  return liveChecks(view).filter(c => ["WARNING", "CRITICAL", "UNKNOWN", "NOT_RUN"].includes(c.status)).map(c => ({
    name: c.name,
    status: c.status === "UNKNOWN" || c.status === "NOT_RUN" ? "数据不可用" : c.status === "CRITICAL" ? "异常" : "需关注",
    message: c.reason || c.value || "未取得数据",
    tables: (c.tables || []).filter(t => t.title === "当前异常 Pod"),
  }));
}
