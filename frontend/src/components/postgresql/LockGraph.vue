<script setup lang="ts">
import { computed } from "vue";
import type { Snapshot } from "../../api/database-workbench";
const props = defineProps<{
  snapshot: Snapshot;
  root?: number;
  selected?: number;
}>();
const emit = defineEmits<{ select: [number] }>();
const layout = computed(() => {
  const edges = props.snapshot.blockingEdges || [];
  const sessions = new Map(props.snapshot.activities.map((a) => [a.pid, a]));
  const included = new Set<number>();
  const add = (pid: number) => {
    if (included.has(pid)) return;
    included.add(pid);
    for (const edge of edges)
      if (edge.blockerPid === pid) add(edge.waitingPid);
      else if (edge.waitingPid === pid) add(edge.blockerPid);
  };
  if (props.root !== undefined) add(props.root);
  else
    for (const edge of edges) {
      included.add(edge.blockerPid);
      included.add(edge.waitingPid);
    }
  const filtered = edges.filter(
    (e) => included.has(e.blockerPid) && included.has(e.waitingPid),
  );
  const levels = new Map<number, number>();
  const visit = (pid: number, depth: number, path: Set<number>) => {
    if (path.has(pid)) return;
    if ((levels.get(pid) ?? Infinity) <= depth) return;
    levels.set(pid, depth);
    const next = new Set(path).add(pid);
    for (const e of filtered)
      if (e.blockerPid === pid) visit(e.waitingPid, depth + 1, next);
  };
  for (const pid of included)
    if (!filtered.some((e) => e.waitingPid === pid)) visit(pid, 0, new Set());
  for (const pid of included) if (!levels.has(pid)) visit(pid, 0, new Set());
  const columns = new Map<number, number[]>();
  for (const pid of included) {
    const level = levels.get(pid) || 0;
    const col = columns.get(level) || [];
    col.push(pid);
    columns.set(level, col);
  }
  const nodes = [...included].map((pid) => {
    const level = levels.get(pid) || 0;
    const col = columns.get(level)!;
    return {
      pid,
      x: 20 + level * 248,
      y: 22 + col.indexOf(pid) * 104,
      session: sessions.get(pid),
      root: !filtered.some((e) => e.waitingPid === pid),
    };
  });
  const byID = new Map(nodes.map((n) => [n.pid, n]));
  return {
    nodes,
    edges: filtered.map((e) => {
      const a = byID.get(e.blockerPid)!;
      const b = byID.get(e.waitingPid)!;
      return {
        ...e,
        path: `M ${a.x + 206} ${a.y + 37} C ${a.x + 230} ${a.y + 37}, ${b.x - 24} ${b.y + 37}, ${b.x} ${b.y + 37}`,
      };
    }),
    width: Math.max(480, ...nodes.map((n) => n.x + 230)),
    height: Math.max(180, ...nodes.map((n) => n.y + 100)),
  };
});
</script>
<template>
  <div class="lock-canvas">
    <svg
      :width="layout.width"
      :height="layout.height"
      role="group"
      aria-label="会话阻塞关系，箭头从阻塞者指向等待者"
    >
      <defs>
        <clipPath id="pg-lock-node-content">
          <rect x="14" y="8" width="178" height="60" />
        </clipPath>
        <marker
          id="pg-lock-arrow"
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--ui-muted)" />
        </marker>
      </defs>
      <path
        v-for="edge in layout.edges"
        :key="`${edge.blockerPid}-${edge.waitingPid}`"
        :d="edge.path"
        fill="none"
        stroke="var(--ui-muted)"
        stroke-opacity=".55"
        stroke-width="1.5"
        marker-end="url(#pg-lock-arrow)"
      />
      <g
        v-for="node in layout.nodes"
        :key="node.pid"
        :transform="`translate(${node.x},${node.y})`"
        class="lock-node"
        role="button"
        tabindex="0"
        :aria-label="`查看 PID ${node.pid}`"
        @click="emit('select', node.pid)"
        @keydown.enter="emit('select', node.pid)"
      >
        <title>
          {{
            node.session
              ? `${node.session.username} · ${node.session.state} · ${node.session.dbName}`
              : "会话信息不可见"
          }}
        </title>
        <rect
          width="206"
          height="76"
          rx="5"
          :fill="node.pid === selected ? 'var(--ui-tint)' : 'var(--ui-panel)'"
          :stroke="
            node.pid === selected ? 'var(--ui-primary)' : 'var(--ui-border)'
          "
        />
        <rect
          width="3"
          height="60"
          x="0"
          y="8"
          rx="1"
          :fill="node.root ? '#e7a037' : 'var(--ui-primary)'"
        />
        <g clip-path="url(#pg-lock-node-content)">
          <text x="14" y="23" class="pid">
            {{ node.pid === 0 ? "预备事务" : `PID ${node.pid}` }}
          </text>
          <text x="14" y="43" class="secondary">
            {{
              node.session
                ? `${node.session.username} · ${node.session.state}`
                : "会话信息不可见或已结束"
            }}
          </text>
          <text x="14" y="61" class="secondary">
            {{
              node.root ? "阻塞源" : `等待 ${node.session?.waitEvent || "锁"}`
            }}
            · {{ node.session?.dbName || "—" }}
          </text>
        </g>
      </g>
    </svg>
  </div>
</template>
<style scoped>
.lock-canvas {
  flex-shrink: 0;
  min-height: 180px;
  overflow: auto;
  background: var(--ui-panel-soft);
  border-top: 1px solid var(--ui-border);
  border-bottom: 1px solid var(--ui-border);
  max-height: 350px;
}
.lock-node {
  cursor: pointer;
}
.lock-node:hover rect:first-child {
  stroke: var(--ui-primary);
}
text {
  font:
    11px ui-monospace,
    SFMono-Regular,
    Consolas,
    monospace;
  fill: var(--ui-heading);
}
.pid {
  font-weight: 650;
  font-size: 12px;
}
.secondary {
  fill: var(--ui-muted);
  font-size: 10px;
}
</style>
