<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { InspectionCommandResult } from "../api/operations";
const props = defineProps<{check: InspectionCommandResult}>();
const search = ref(""), page = ref(1), pageSize = ref(25);
const table = computed(() => props.check.tables?.[0]);
const filtered = computed(() => (table.value?.rows || []).filter(row => row.join(" ").toLowerCase().includes(search.value.trim().toLowerCase())));
const visible = computed(() => filtered.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value));
const unavailable = computed(() => props.check.status === "UNKNOWN" || props.check.status === "NOT_RUN");
const label = computed(() => props.check.attributes?.availability === "not_configured" ? "未接入日志" : props.check.attributes?.stale === "true" ? "数据过期" : unavailable.value ? "数据不可用" : props.check.attributes?.estimated === "true" ? "统计估算" : props.check.status === "WARNING" ? "需关注" : props.check.status === "CRITICAL" ? "异常" : "");
const labelType = computed(()=>props.check.status==='CRITICAL'?'danger':props.check.attributes?.stale==='true'||props.check.status==='WARNING'?'warning':'info');
watch([search,pageSize,() => props.check.code],()=>{page.value=1;});
watch(()=>filtered.value.length, n=>{page.value=Math.min(page.value,Math.max(1,Math.ceil(n/pageSize.value)));});
</script>
<template>
  <section class="monitor-data-table">
    <header><div><h4>{{check.name}}</h4><el-tag v-if="label" size="small" :type="labelType">{{label}}</el-tag></div><span v-if="check.collectedAt && !check.collectedAt.startsWith('0001')" class="muted">更新于 {{new Date(check.collectedAt).toLocaleString('zh-CN',{hour12:false})}}</span></header>
    <p v-if="check.reason && (table?.rows?.length||0)>0 && (unavailable || check.status==='WARNING' || check.status==='CRITICAL' || check.attributes?.stale==='true')" class="message">{{check.reason}}</p>
    <el-input v-if="(table?.rows?.length||0)>10" v-model="search" clearable placeholder="搜索名称或内容" style="max-width:360px;margin:12px 0" />
    <el-table v-if="table && table.rows?.length" :data="visible" size="small" max-height="460" stripe>
      <el-table-column v-for="(column,i) in table.columns" :key="i" :label="column" :min-width="/SQL|命令|表名|队列|Slot|slot/.test(column)?220:110" show-overflow-tooltip><template #default="{row}">{{row[i]}}</template></el-table-column>
    </el-table>
    <el-empty v-else :description="unavailable ? check.reason || '未取得数据' : check.value || '暂无记录'" :image-size="44" />
    <p v-if="table?.note" class="muted">{{table.note}}</p>
    <el-pagination v-if="filtered.length>pageSize" v-model:current-page="page" v-model:page-size="pageSize" :page-sizes="[10,25,50]" :total="filtered.length" layout="total, sizes, prev, pager, next" style="margin-top:14px" />
  </section>
</template>
<style scoped>
.monitor-data-table{border:1px solid #e0e7f0;border-radius:8px;padding:18px;min-width:0}.monitor-data-table header,.monitor-data-table header>div{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.monitor-data-table header{justify-content:space-between;margin-bottom:12px}.monitor-data-table h4{margin:0;font-size:14px}.muted{font-size:12px;color:#718198;line-height:1.6}.message{font-size:13px;line-height:1.7;color:#8d641e}
</style>
