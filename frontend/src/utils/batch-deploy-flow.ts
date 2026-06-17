export type DeployMode = 'immediate' | 'cutover'

export interface DeployFlowStep {
  key: string
  title: string
  description: string
  state: 'source' | 'check' | 'build' | 'deploy' | 'record'
}

export interface ImageReadyPipelineLike {
  id: number
  pipelineNo: string
  appName: string
  batchId?: string
  deployMode?: string
  triggerUser?: string
  createdAt?: string
}

export interface ImageReadyBatch {
  batchId: string
  source: string
  appCount: number
  readyCount: number
  failedCount: number
  triggerUser?: string
  createdAt?: string
  statusText: string
  pipelines: ImageReadyPipelineLike[]
}

export function buildDeployFlow(mode: DeployMode): DeployFlowStep[] {
  return [
    {
      key: 'source',
      title: '选择制品来源',
      description: '本地上传、服务器下载或从制品库选择 latest 制品，生成部署批次。',
      state: 'source',
    },
    {
      key: 'check',
      title: '制品校验与匹配',
      description: '校验 jar/zip 可读性，匹配微服务，异常和未匹配制品默认剔除。',
      state: 'check',
    },
    {
      key: 'build',
      title: '构建镜像',
      description: '按匹配结果构建并推送镜像，前端主应用会按 web-main 合并规则组装。',
      state: 'build',
    },
    mode === 'cutover'
      ? {
          key: 'wait',
          title: '等待卡点部署',
          description: '只保留镜像已就绪批次，不更新 Deployment，等待维护窗口手动触发。',
          state: 'deploy',
        }
      : {
          key: 'deploy',
          title: '更新 Deployment',
          description: '镜像构建成功后立即按并发规则滚动更新 Kubernetes Deployment。',
          state: 'deploy',
        },
    {
      key: 'record',
      title: '批次记录与回滚',
      description: '部署成功后更新最新制品，并记录更新前镜像，支持按批次整体回滚。',
      state: 'record',
    },
  ]
}

export function groupImageReadyPipelines(pipelines: ImageReadyPipelineLike[]): ImageReadyBatch[] {
  const groups = new Map<string, ImageReadyPipelineLike[]>()

  for (const pipeline of pipelines) {
    const batchId = pipeline.batchId?.trim()
    if (!batchId) {
      continue
    }
    const group = groups.get(batchId) || []
    group.push(pipeline)
    groups.set(batchId, group)
  }

  return Array.from(groups.entries())
    .map(([batchId, group]) => {
      const sorted = [...group].sort((a, b) => a.id - b.id)
      return {
        batchId,
        source: '卡点批量部署',
        appCount: sorted.length,
        readyCount: sorted.length,
        failedCount: 0,
        triggerUser: sorted[0]?.triggerUser,
        createdAt: sorted[0]?.createdAt,
        statusText: '镜像已就绪',
        pipelines: sorted,
      }
    })
    .sort((a, b) => {
      const aTime = a.createdAt ? new Date(a.createdAt).getTime() : 0
      const bTime = b.createdAt ? new Date(b.createdAt).getTime() : 0
      return aTime - bTime
    })
}
