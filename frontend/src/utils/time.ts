/**
 * Format ISO time string to "YYYY-MM-DD HH:mm:ss"
 */
export function formatTime(time: string | null | undefined): string {
  if (!time) return '-'
  try {
    const d = new Date(time)
    if (isNaN(d.getTime())) return time
    const y = d.getFullYear()
    const m = String(d.getMonth() + 1).padStart(2, '0')
    const day = String(d.getDate()).padStart(2, '0')
    const h = String(d.getHours()).padStart(2, '0')
    const min = String(d.getMinutes()).padStart(2, '0')
    const s = String(d.getSeconds()).padStart(2, '0')
    return `${y}-${m}-${day} ${h}:${min}:${s}`
  } catch {
    return time || '-'
  }
}

/**
 * Format ISO time string to Beijing time "YYYY-MM-DD HH:mm:ss"
 */
export function formatBeijingTime(time: string | null | undefined): string {
  if (!time) return '-'
  try {
    const d = new Date(time)
    if (isNaN(d.getTime())) return time
    const parts = new Intl.DateTimeFormat('zh-CN', {
      timeZone: 'Asia/Shanghai',
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false,
      hourCycle: 'h23',
    }).formatToParts(d)
    const get = (type: string) => parts.find(part => part.type === type)?.value || ''
    return `${get('year')}-${get('month')}-${get('day')} ${get('hour')}:${get('minute')}:${get('second')}`
  } catch {
    return time || '-'
  }
}
