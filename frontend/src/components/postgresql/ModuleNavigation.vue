<script setup lang="ts">
import { useRoute } from "vue-router";
const route = useRoute();
const links = [
  { path: "/postgresql/workbench", label: "数据库工作台", icon: "Monitor" },
  { path: "/postgresql/sql-execution", label: "SQL 变更发布", icon: "Upload" },
  {
    path: "/postgresql/view-dependency-change",
    label: "视图依赖变更",
    icon: "Connection",
  },
  { path: "/postgresql/instances", label: "发布连接", icon: "Setting" },
];
</script>
<template>
  <div class="database-module-header">
    <strong><span>P</span>PostgreSQL</strong>
    <nav aria-label="PostgreSQL 模块导航">
      <router-link
        v-for="link in links"
        :key="link.path"
        :to="link.path"
        :class="{ active: route.path === link.path }"
        ><el-icon><component :is="link.icon" /></el-icon
        >{{ link.label }}</router-link
      >
    </nav>
  </div>
</template>
<style scoped>
.database-module-header {
  display: flex;
  align-items: center;
  gap: 22px;
  min-width: 0;
  height: 48px;
}
.database-module-header > strong {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  font-weight: 600;
  white-space: nowrap;
}
.database-module-header > strong > span {
  font: 700 18px Georgia;
  color: white;
  background: #31648d;
  display: grid;
  place-items: center;
  width: 24px;
  height: 26px;
  border-radius: 4px;
}
nav {
  height: 100%;
  display: flex;
  gap: 23px;
  overflow: auto;
}
nav a {
  font-size: 12px;
  color: var(--ui-text);
  text-decoration: none;
  white-space: nowrap;
  display: flex;
  align-items: center;
  gap: 6px;
  border-bottom: 2px solid transparent;
  padding-top: 2px;
}
nav a.active {
  color: var(--ui-primary);
  border-bottom-color: var(--ui-primary);
  font-weight: 550;
}
@media (max-width: 1100px) {
  .database-module-header > strong {
    display: none;
  }
  nav {
    gap: 16px;
  }
}
@media (max-width: 800px) {
  nav a .el-icon {
    display: none;
  }
  nav {
    gap: 12px;
  }
  nav a {
    font-size: 11px;
  }
}
</style>
