export interface UploadActivity {
  startedAt: number
  lastProgressAt: number
  transferredAt?: number
  finishedAt?: number
  loaded: number
  total?: number
  samples: { at: number; loaded: number }[]
}

const speedWindowMs = 10_000
export const uploadStallAfterMs = 30_000

export function createUploadActivity(now: number): UploadActivity {
  return { startedAt: now, lastProgressAt: now, loaded: 0, samples: [{ at: now, loaded: 0 }] }
}

export function recordUploadActivity(state: UploadActivity, loaded: number, total: number | undefined, now: number) {
  if (!Number.isFinite(loaded) || loaded < state.loaded) return
  if (total !== undefined && Number.isFinite(total) && total > 0) state.total = total
  if (loaded > state.loaded) {
    state.loaded = loaded
    state.lastProgressAt = now
    state.samples.push({ at: now, loaded })
    // Retain one sample before the window for interpolation at its boundary.
    while (state.samples.length > 2 && state.samples[1]!.at <= now - speedWindowMs) state.samples.shift()
  }
  if (state.total && loaded >= state.total) state.transferredAt ??= now
}

export function uploadPercentage(state: UploadActivity) {
  if (state.transferredAt !== undefined) return 100
  if (!state.total) return 0
  // Never round an incomplete transfer up to 100%.
  return Math.min(99.9, Math.max(0, Math.round(state.loaded / state.total * 1000) / 10))
}

export function uploadActivityStats(state: UploadActivity, now: number) {
  const endedAt = state.finishedAt ?? now
  const transferEnd = state.transferredAt ?? endedAt
  const elapsedMs = Math.max(0, endedAt - state.startedAt)
  const transferMs = Math.max(0, transferEnd - state.startedAt)
  const idleMs = Math.max(0, transferEnd - state.lastProgressAt)
  const windowStart = Math.max(state.startedAt, transferEnd - speedWindowMs)
  let before = state.samples[0]!
  let after: typeof before | undefined
  for (const sample of state.samples) {
    if (sample.at <= windowStart) before = sample
    else { after = sample; break }
  }
  let baseline = before.loaded
  if (after && before.at < windowStart && after.at > before.at) {
    baseline += (after.loaded - before.loaded) * (windowStart - before.at) / (after.at - before.at)
  }
  const recentMs = transferEnd - windowStart
  const recentBps = recentMs >= 1000 ? Math.max(0, state.loaded - baseline) / (recentMs / 1000) : undefined
  const averageBps = transferMs >= 1000 ? state.loaded / (transferMs / 1000) : undefined
  const stalled = state.transferredAt === undefined && state.finishedAt === undefined && idleMs >= uploadStallAfterMs
  const remainingSeconds = state.total && state.loaded < state.total && recentBps && transferMs >= 3000 && !stalled
    ? Math.ceil((state.total - state.loaded) / recentBps) : undefined
  return { elapsedMs, transferMs, idleMs, recentBps, averageBps, stalled, remainingSeconds }
}

// Keep transmitted GB-sized files in MB so another few KB remain visible.
export function formatUploadBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  if (bytes < 1024) return `${Math.floor(bytes)} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(2)} KB`
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`
}

export function formatUploadSpeed(bytesPerSecond: number | undefined) {
  return bytesPerSecond === undefined ? '计算中' : `${formatUploadBytes(bytesPerSecond)}/s`
}

export function formatUploadDuration(seconds: number) {
  const value = Math.max(0, Math.floor(seconds))
  if (value < 60) return `${value} 秒`
  if (value < 3600) return `${Math.floor(value / 60)} 分 ${value % 60} 秒`
  return `${Math.floor(value / 3600)} 小时 ${Math.floor(value % 3600 / 60)} 分 ${value % 60} 秒`
}

export function formatUploadPercent(value: number) {
  return `${value.toFixed(1)}%`
}
