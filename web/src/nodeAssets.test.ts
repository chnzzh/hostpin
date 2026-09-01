import { describe, expect, it } from 'vitest'
import { countryFlagSource, systemLogoSource } from './nodeAssets'

describe('node identity assets', () => {
  it('uses real flag assets for valid two-letter country codes', () => {
    expect(countryFlagSource('jp')).toBe('/assets/flags/JP.svg?v=flag-icons-v1')
    expect(countryFlagSource(' US ')).toBe('/assets/flags/US.svg?v=flag-icons-v1')
    expect(countryFlagSource('中国')).toBe('')
    expect(countryFlagSource('')).toBe('')
  })

  it('selects CF-Monitor system icons from the reported operating system', () => {
    expect(systemLogoSource('Alpine Linux 3.24')).toBe('/assets/logo/os-alpine.webp?v=cf-monitor-v1')
    expect(systemLogoSource('Debian GNU/Linux 12')).toBe('/assets/logo/os-debian.svg?v=cf-monitor-v1')
    expect(systemLogoSource('OpenWrt 24.10')).toBe('/assets/logo/os-openwrt.svg?v=cf-monitor-v1')
    expect(systemLogoSource('Windows Server 2025')).toBe('/assets/logo/os-windows.svg?v=cf-monitor-v1')
  })

  it('falls back to the generic system icon', () => {
    expect(systemLogoSource('CustomOS')).toBe('/assets/logo/os-unknown.svg?v=cf-monitor-v1')
    expect(systemLogoSource()).toBe('/assets/logo/os-unknown.svg?v=cf-monitor-v1')
  })
})
