import { describe, expect, it, vi } from 'vitest'
import { bytes, duration, percent, relativeTime } from './format'
import { setLocale } from './i18n'

describe('telemetry formatting', () => {
  it('formats bytes and resource percentages', () => {
    expect(bytes(1024)).toBe('1.0 KiB')
    expect(bytes(0)).toBe('0 B')
    expect(percent(925, 1000)).toBe(92.5)
    expect(percent(100, 0)).toBe(0)
  })

  it('formats durations', () => {
    setLocale('en-US')
    expect(duration(90061)).toBe('1d 1h')
    expect(duration(3660)).toBe('1h 1m')
    setLocale('zh-CN')
    expect(duration(90061)).toBe('1天 1小时')
  })

  it('formats relative server time', () => {
    setLocale('en-US')
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-24T00:00:00Z'))
    expect(relativeTime('2026-08-23T23:59:30Z')).toBe('30s ago')
    expect(relativeTime('2026-08-23T22:00:00Z')).toBe('2h ago')
    setLocale('zh-CN')
    expect(relativeTime('2026-08-23T23:59:30Z')).toBe('30 秒前')
    vi.useRealTimers()
  })
})
