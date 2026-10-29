<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import InspectionsView from "./InspectionsView.vue";
import ComponentInspectionPanel from "../components/ComponentInspectionPanel.vue";
import { componentKinds } from "../api/monitoring";
const route = useRoute(),
  router = useRouter();
const kind = computed(() => String(route.params.kind || "reports"));
const tabs = [
  { value: "reports", label: "巡检报告" },
  { value: "hosts", label: "Linux 主机" },
  ...componentKinds,
];
</script>
<template>
  <div class="monitoring-center">
    <header>
      <div>
        <h2>监控中心</h2>
        <p>按需检查系统运行情况，形成可阅读、可存档的巡检报告。</p>
      </div>
      <el-button
        @click="router.push('/settings/environment#inspection-components')"
        >组件连接配置</el-button
      >
    </header>
    <nav aria-label="监控中心导航">
      <router-link
        v-for="t in tabs"
        :key="t.value"
        :to="t.value === 'reports' ? '/monitoring' : `/monitoring/${t.value}`"
        :class="{ active: kind === t.value }"
        >{{ t.label }}</router-link
      >
    </nav>
    <main>
      <InspectionsView v-if="kind === 'reports'" /><ComponentInspectionPanel
        v-else-if="tabs.some((t) => t.value === kind)"
        :kind="kind"
      /><el-empty v-else description="未找到组件页面" />
    </main>
  </div>
</template>
<style scoped>
.monitoring-center {
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.monitoring-center header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.monitoring-center h2 {
  margin: 0;
  font-size: 21px;
  font-weight: 600;
}
.monitoring-center header p {
  font-size: 13px;
  color: #7c8b9e;
  margin: 9px 0 0;
}
.monitoring-center nav {
  display: flex;
  gap: 25px;
  flex-wrap: wrap;
  border-bottom: 1px solid #dfe5ed;
}
.monitoring-center nav a {
  font-size: 14px;
  color: #60728b;
  text-decoration: none;
  padding: 0 0 14px;
}
.monitoring-center nav a.active {
  color: #2875ce;
  border-bottom: 2px solid #2875ce;
  font-weight: 600;
}
main {
  background: #fff;
  border: 1px solid #e1e7ef;
  border-radius: 8px;
  padding: 22px;
}
</style>
