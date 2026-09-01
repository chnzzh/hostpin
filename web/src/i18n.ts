import { computed, ref } from 'vue'

export type AppLocale = 'zh-CN' | 'en-US'

const localePreferenceKey = 'hostpin-locale-preference'
const legacyLocaleKey = 'hostpin-locale'

function supportedLocale(value: string | null | undefined): AppLocale | null {
  const normalized = value?.trim().replace('_', '-').toLowerCase()
  if (!normalized) return null
  if (normalized === 'zh' || normalized.startsWith('zh-')) return 'zh-CN'
  if (normalized === 'en' || normalized.startsWith('en-')) return 'en-US'
  return null
}

function storedLocalePreference(): AppLocale | null {
  try {
    if (typeof localStorage === 'undefined' || typeof localStorage.getItem !== 'function') return null
    // Older releases wrote legacyLocaleKey during every mount, so it cannot prove a manual choice.
    return supportedLocale(localStorage.getItem(localePreferenceKey))
  } catch {
    return null
  }
}

function browserLanguages(): readonly string[] {
  if (typeof navigator === 'undefined') return []
  if (Array.isArray(navigator.languages) && navigator.languages.length > 0) return navigator.languages
  return navigator.language ? [navigator.language] : []
}

export function detectBrowserLocale(languages: readonly string[] = browserLanguages()): AppLocale {
  for (const language of languages) {
    const detected = supportedLocale(language)
    if (detected) return detected
  }
  return 'en-US'
}

export function resolveInitialLocale(stored: string | null, languages: readonly string[]): AppLocale {
  return supportedLocale(stored) ?? detectBrowserLocale(languages)
}

function persistLocalePreference(value: AppLocale) {
  try {
    if (typeof localStorage !== 'undefined' && typeof localStorage.setItem === 'function') {
      localStorage.setItem(localePreferenceKey, value)
      localStorage.setItem(legacyLocaleKey, value)
    }
  } catch {
    // Language selection still works when storage is blocked or unavailable.
  }
}

const storedLocale = storedLocalePreference()
let hasExplicitPreference = storedLocale !== null
const locale = ref<AppLocale>(storedLocale ?? detectBrowserLocale())

function applyLocale(value: AppLocale) {
  if (typeof document !== 'undefined') {
    document.documentElement.lang = value
    document.documentElement.dataset.locale = value
  }
}

applyLocale(locale.value)

export function setLocale(value: AppLocale) {
  hasExplicitPreference = true
  locale.value = value
  persistLocalePreference(value)
  applyLocale(value)
}

export function syncLocaleWithBrowser() {
  if (hasExplicitPreference) return
  locale.value = detectBrowserLocale()
  applyLocale(locale.value)
}

export function watchBrowserLocale() {
  syncLocaleWithBrowser()
  if (typeof window === 'undefined' || typeof window.addEventListener !== 'function') return () => {}
  window.addEventListener('languagechange', syncLocaleWithBrowser)
  return () => window.removeEventListener('languagechange', syncLocaleWithBrowser)
}

export function useLocale() {
  const isChinese = computed(() => locale.value === 'zh-CN')
  const text = (chinese: string, english: string) => isChinese.value ? chinese : english
  return {
    locale,
    isChinese,
    text,
    setLocale,
  }
}

export function localeCode() {
  return locale.value
}
