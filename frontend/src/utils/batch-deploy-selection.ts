export interface DeployableMatchLike {
  updateUsed?: boolean
  fileName: string
  matched: boolean
  valid: boolean
  skipped: boolean
}

export function isDeployableMatch(result: DeployableMatchLike) {
  return result.matched && result.valid && !result.skipped && !result.updateUsed
}

export function selectedDeployableFileNames(results: DeployableMatchLike[]) {
  return results.filter(isDeployableMatch).map(result => result.fileName)
}

interface DeployScopeItem {
  regions?: {appName: string; role: string; appCode?: string; path: string}[]
  fileName: string
  appName: string
  appType: string
  vueRole?: string
  appCode?: string
}

export function buildDeployConfirmation(items: DeployScopeItem[], autoRollout = false) {
  const shared = items.filter(item => item.appType === 'vue' && ['main', 'sub', 'cdn'].includes(item.vueRole || ''))
  const independent = items.filter(item => !shared.includes(item))
  const lines = [`将为 ${items.length} 个制品制作镜像。`]
  if (independent.length) {
    lines.push(`独立更新（每个应用各一个镜像、一个部署任务）：\n${independent.map(item => `${item.appName}：${item.fileName}`).join('\n')}`)
  }
  if (shared.length) {
    lines.push(`合并更新 web-main（共用一个镜像、一个部署任务）：\n${shared.map(item => `${item.vueRole === 'main' ? '主应用' : item.vueRole === 'cdn' ? '公共资源' : `子应用 ${item.appCode || item.appName}`}：${item.fileName}`).join('\n')}`)
    const regions = shared.flatMap(item => item.regions || [])
    if (regions.length) lines.push(`实际更新区域：\n${regions.map(region => `${region.appName} → ${region.path}`).join('\n')}`)
    lines.push('替换本次主应用和子应用，保留未更新的线上子应用。主包内嵌内容仅按显式整包清单更新；同一个子应用不能同时来自主应用包和独立 ZIP。')
  }
  lines.push(`${autoRollout ? '本地环境：本次将制作、推送镜像并自动滚动更新。' : '本步骤仅制作并推送镜像，镜像就绪后单独提交滚动更新。'}一个部署任务失败后停止该任务，其他任务继续；不自动回滚。已提交更新的制品不能再次使用，再次更新需重新上传。`)
  return lines.join('\n\n')
}
