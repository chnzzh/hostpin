import { describe, expect, it } from 'vitest'
import { maximumTrafficLimitGiB, suggestedTrafficUnit, trafficBytesFrom, trafficLimitBytesFromGiB, trafficLimitInGiB, trafficUsage, trafficUtilization, trafficValueInUnit } from './traffic'

describe('traffic accounting presentation', () => {
  it('supports every billing mode', () => {
    expect(trafficUsage(600, 300, 'sum')).toBe(900)
    expect(trafficUsage(600, 300, 'max')).toBe(600)
    expect(trafficUsage(600, 300, 'up')).toBe(300)
    expect(trafficUsage(600, 300, 'down')).toBe(600)
  })

  it('clamps invalid counters and quota utilization', () => {
    expect(trafficUsage(-5, Number.NaN, 'sum')).toBe(0)
    expect(trafficUtilization(900, 1_000)).toBe(90)
    expect(trafficUtilization(2, 100)).toBe(2)
    expect(trafficUtilization(1_500, 1_000)).toBe(100)
    expect(trafficUtilization(500, 0)).toBe(0)
  })
})

describe('traffic correction units', () => {
  it('converts editable binary units without losing whole bytes', () => {
    expect(trafficBytesFrom(1.5, 'GiB')).toBe(1_610_612_736)
    expect(trafficValueInUnit(1_610_612_736, 'GiB')).toBe(1.5)
    expect(trafficBytesFrom(-1, 'MiB')).toBeNaN()
  })

  it('chooses a compact unit for the current totals', () => {
    expect(suggestedTrafficUnit(500)).toBe('B')
    expect(suggestedTrafficUnit(8 * 1024 ** 2)).toBe('MiB')
    expect(suggestedTrafficUnit(3 * 1024 ** 4)).toBe('TiB')
  })
})

describe('traffic quota GiB input', () => {
  it('converts GiB quotas to byte-based API values', () => {
    expect(trafficLimitBytesFromGiB(1)).toBe(1_073_741_824)
    expect(trafficLimitBytesFromGiB(1000)).toBe(1_073_741_824_000)
    expect(trafficLimitBytesFromGiB(1.5)).toBe(1_610_612_736)
  })

  it('round-trips existing byte quotas without a data migration', () => {
    const existing = 123_456_789
    expect(trafficLimitBytesFromGiB(trafficLimitInGiB(existing))).toBe(existing)
    expect(trafficLimitInGiB(0)).toBe(0)
  })

  it('rejects values outside JavaScript integer precision', () => {
    expect(trafficLimitBytesFromGiB(-1)).toBeNaN()
    expect(trafficLimitBytesFromGiB(maximumTrafficLimitGiB + 1)).toBeNaN()
  })
})
