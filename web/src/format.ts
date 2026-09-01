export function bytes(value = 0, precision = 1): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : precision)} ${units[index]}`
}

export function rate(value = 0): string {
  return `${bytes(value)}/s`
}

export function percent(used = 0, total = 0): number {
  return total > 0 ? Math.min(100, Math.max(0, (used / total) * 100)) : 0
}

export function duration(seconds = 0): string {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (localeCode() === 'zh-CN') {
    if (days > 0) return `${days}天 ${hours}小时`
    if (hours > 0) return `${hours}小时 ${minutes}分钟`
    return `${minutes}分钟`
  }
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

export function relativeTime(value?: string): string {
  const chinese = localeCode() === 'zh-CN'
  if (!value) return chinese ? '从未' : 'never'
  const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000))
  if (chinese) {
    if (seconds < 10) return '刚刚'
    if (seconds < 60) return `${seconds} 秒前`
    if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
    if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`
    return `${Math.floor(seconds / 86400)} 天前`
  }
  if (seconds < 10) return 'now'
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

export function dateTime(value?: string): string {
  if (!value) return '—'
  return new Intl.DateTimeFormat(localeCode(), { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

export function localized(value?: string | Record<string, string>): string {
  if (!value) return ''
  if (typeof value === 'string') return value
  const language = localeCode()
  return value[language] ?? value[language.split('-')[0] ?? ''] ?? value.en ?? Object.values(value)[0] ?? ''
}
import { localeCode } from './i18n'
