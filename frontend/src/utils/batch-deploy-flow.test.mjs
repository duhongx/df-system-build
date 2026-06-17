import assert from 'node:assert/strict'
import {
  buildDeployFlow,
  groupImageReadyPipelines,
} from './compiled/batch-deploy-flow.js'

assert.deepEqual(
  buildDeployFlow('immediate').map(step => step.title),
  ['选择制品来源', '制品校验与匹配', '构建镜像', '更新 Deployment', '批次记录与回滚'],
)

assert.deepEqual(
  buildDeployFlow('cutover').map(step => step.title),
  ['选择制品来源', '制品校验与匹配', '构建镜像', '等待卡点部署', '批次记录与回滚'],
)

const grouped = groupImageReadyPipelines([
  { id: 1, pipelineNo: 'his-gateway-0001', appName: 'his-gateway', batchId: 'batch-a', triggerUser: 'admin', createdAt: '2026-06-16 18:00:00' },
  { id: 2, pipelineNo: 'web-main-0001', appName: 'web-main', batchId: 'batch-a', triggerUser: 'admin', createdAt: '2026-06-16 18:01:00' },
  { id: 3, pipelineNo: 'web-cdr-0001', appName: 'web-cdr', batchId: 'batch-b', triggerUser: 'ops', createdAt: '2026-06-16 18:10:00' },
  { id: 4, pipelineNo: 'old-manual-0001', appName: 'old-app', triggerUser: 'admin', createdAt: '2026-06-16 17:00:00' },
])

assert.equal(grouped.length, 2)
assert.deepEqual(grouped[0], {
  batchId: 'batch-a',
  source: '卡点批量部署',
  appCount: 2,
  readyCount: 2,
  failedCount: 0,
  triggerUser: 'admin',
  createdAt: '2026-06-16 18:00:00',
  statusText: '镜像已就绪',
  pipelines: [
    { id: 1, pipelineNo: 'his-gateway-0001', appName: 'his-gateway', batchId: 'batch-a', triggerUser: 'admin', createdAt: '2026-06-16 18:00:00' },
    { id: 2, pipelineNo: 'web-main-0001', appName: 'web-main', batchId: 'batch-a', triggerUser: 'admin', createdAt: '2026-06-16 18:01:00' },
  ],
})
