<script setup lang="ts">
import { useRoute } from "vue-router";
const route = useRoute();
</script>
<template>
  <section class="pg-module">
    <div
      class="pg-module-page"
      :class="{
        'pg-module-page--workbench': route.name === 'PostgreSQLWorkbench',
      }"
    >
      <router-view v-slot="{ Component }"
        ><keep-alive include="PostgreSQLWorkbenchView"
          ><component :is="Component" :key="route.path" /></keep-alive
      ></router-view>
    </div>
  </section>
</template>
<style scoped>
.pg-module {
  display: flex;
  flex: 1;
  min-height: 0;
  flex-direction: column;
  gap: 0;
  border: 0;
  border-radius: 0;
  overflow: hidden;
  background: var(--ui-panel);
}
.pg-module-page {
  flex: 1;
  min-height: 0;
  padding: 18px;
  overflow: auto;
  display: flex;
  flex-direction: column;
}
.pg-module-page--workbench {
  padding: 0;
  overflow: hidden;
}
</style>
