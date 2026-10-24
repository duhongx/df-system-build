import type { SQLChangeBatch, SQLChangeFile, SQLExecutionFileFilter } from '../api/postgresql'

export interface SQLExecutionSummary {
  type: 'success' | 'warning' | 'error' | 'info'
  title: string
  progress: string
  description: string
  location?: { fileId?: number; fileName: string; lineNumber?: number }
}

// File failures contain the entire SQL before a separate SQLSTATE/error block.
// Keep that source in the details dialog, never in the progress banner.
export function compactSQLExecutionMessage(message: string): string {
  let text = message.trim()
  const sql = /第\s*(\d+)(?:\s*[-–]\s*\d+)?\s*行\s*SQL\s*[:：]/i.exec(text)
  if (sql) {
    const diagnostics = [...text.matchAll(/\r?\nSQLSTATE\s*[:：]\s*[A-Z0-9]{5}[^\S\n]*\r?\n/gi)].pop()
    text = diagnostics && diagnostics.index! > sql.index
      ? `第 ${sql[1]} 行：${text.slice(diagnostics.index! + diagnostics[0].length).replace(/；后续语句未执行\s*$/, '')}`
      : `第 ${sql[1]} 行执行失败，完整原因请查看执行详情。`
  }
  const compact = text.replace(/\s+/g, ' ').trim()
  return compact.length > 180 ? `${compact.slice(0, 180)}…` : compact
}

function failureLocation(batch: SQLChangeBatch, files: SQLChangeFile[]): SQLExecutionSummary['location'] {
  // Older batches have no stoppedFileId. Resolve their recorded filename, not
  // the first currently failed file (which may belong to a different run).
  const legacyName = /^文件\s+(.+?\.sql)\s+执行失败[:：]/i.exec(batch.executeMessage || '')?.[1]
  const name = batch.stoppedFileName || legacyName
  const file = files.find(file => batch.stoppedFileId ? file.id === batch.stoppedFileId : file.fileName === name)
  if (!file && !name) return undefined
  const line = /第\s*(\d+)(?:\s*[-–]\s*\d+)?\s*行/.exec(batch.executeMessage || '')?.[1]
  return { fileId: file?.id || batch.stoppedFileId, fileName: name || file!.fileName, lineNumber: line ? Number(line) : undefined }
}

// Batch outcome distinguishes completion with deferred failures from a new
// failure that actually stopped the queue. Current selection may be elsewhere.
export function describeSQLExecution(
  batch: SQLChangeBatch,
  batchFiles: SQLChangeFile[],
  excluded: SQLExecutionFileFilter[] = [],
): SQLExecutionSummary {
  const processed = batchFiles.filter(file => ['SUCCESS', 'FAILED', 'PARTIAL_FAILED', 'SKIPPED', 'UNKNOWN'].includes(file.executeStatus)).length
  const waiting = batchFiles.filter(file => ['PENDING', 'QUEUED', 'CANCELED'].includes(file.executeStatus)).length
  const active = batchFiles.find(file => file.executeStatus === 'RUNNING')
  const processedCount = batch.executeStatus === 'RUNNING' ? processed : batch.successFiles + batch.failedFiles + batch.skippedFiles
  const counts = `入队 ${batch.totalFiles} 个文件，已处理 ${processedCount} 个；成功 ${batch.successFiles}，含失败 ${batch.failedFiles}，跳过 ${batch.skippedFiles}。未入队 ${excluded.length} 个。`
  // The toolbar shows only file counts. SQL and diagnostics belong to the
  // statement row and the explicit details dialog, regardless of outcome.
  const progress = `文件 ${processedCount}/${batch.totalFiles} · 成功 ${batch.successFiles} · 含失败 ${batch.failedFiles} · 跳过 ${batch.skippedFiles} · 未入队 ${excluded.length}`
  const message = compactSQLExecutionMessage(batch.executeMessage || '')
  const location = failureLocation(batch, batchFiles)
  const failure = `${location ? `停止文件：${location.fileName}。` : ''}${message}`
  if (batch.executeStatus === 'RUNNING') {
    return {
      type: 'info',
      title: batch.stopRequested ? '已收到停止请求，等待当前语句结束' : active ? `正在执行：${active.fileName}` : '正在准备下一文件',
      progress: `已处理 ${processed}/${batch.totalFiles} 个文件 · 未入队 ${excluded.length} 个`,
      description: `进度 ${processed}/${batch.totalFiles}；未入队 ${excluded.length} 个。${compactSQLExecutionMessage(active?.executeMessage || '')}`,
    }
  }
  if (batch.outcome === 'COMPLETED' || (!batch.outcome && batch.executeStatus === 'SUCCESS')) {
    const needsAttention = batch.failedFiles > 0 || excluded.length > 0
    return {
      type: needsAttention ? 'warning' : 'success',
      title: needsAttention ? '本轮可执行 SQL 已全部处理，仍有待处理项' : '本轮执行完成',
      progress,
      description: `${counts}${needsAttention ? '失败 SQL 需单独重试；未入队文件请查看下方原因清单。' : ''}`,
    }
  }
  if (batch.outcome === 'CANCELED' || batch.executeStatus === 'CANCELED') {
    return { type: 'warning', title: '执行已停止', progress, description: `${counts}${batch.stopUser ? `停止操作人：${batch.stopUser}。` : ''}${message} 后续 ${waiting} 个入队文件未执行。`, location }
  }
  if (batch.outcome === 'INTERRUPTED') {
    return { type: 'error', title: '执行已中断，需要核实结果', progress, description: `${counts}${failure}`, location }
  }
  if (batch.outcome === 'ERROR') {
    return { type: 'error', title: '执行因错误停止', progress, description: `${counts}${failure} 后续 ${waiting} 个入队文件未执行。`, location }
  }
  return {
    type: 'warning', title: '历史批次已结束，请查看失败和未执行文件',
    progress,
    description: `${counts}${failure || '旧批次未保存明确的结束原因。'}`,
    location,
  }
}
