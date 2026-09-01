import { describe, expect, it } from 'vitest'
import { detectBrowserLocale, resolveInitialLocale } from './i18n'

describe('locale selection', () => {
  it('maps Chinese browser variants to the Chinese interface', () => {
    expect(detectBrowserLocale(['zh-CN'])).toBe('zh-CN')
    expect(detectBrowserLocale(['zh-TW'])).toBe('zh-CN')
    expect(detectBrowserLocale(['zh-Hant-HK'])).toBe('zh-CN')
  })

  it('uses English for English or unsupported browser languages', () => {
    expect(detectBrowserLocale(['en-GB'])).toBe('en-US')
    expect(detectBrowserLocale(['ja-JP'])).toBe('en-US')
    expect(detectBrowserLocale([])).toBe('en-US')
  })

  it('uses the first supported browser preference', () => {
    expect(detectBrowserLocale(['fr-FR', 'zh-CN', 'en-US'])).toBe('zh-CN')
  })

  it('keeps an explicit selection ahead of browser detection', () => {
    expect(resolveInitialLocale('en-US', ['zh-CN'])).toBe('en-US')
    expect(resolveInitialLocale('zh-CN', ['en-US'])).toBe('zh-CN')
    expect(resolveInitialLocale('invalid', ['zh-CN'])).toBe('zh-CN')
  })
})
