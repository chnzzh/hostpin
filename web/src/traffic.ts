export type TrafficMode = 'sum' | 'max' | 'up' | 'down'
export type TrafficCorrectionUnit = 'B' | 'KiB' | 'MiB' | 'GiB' | 'TiB'

export const trafficCorrectionUnits: Record<TrafficCorrectionUnit, number> = {
  B: 1,
  KiB: 1024,
  MiB: 1024 ** 2,
  GiB: 1024 ** 3,
  TiB: 1024 ** 4,
}

export const maximumTrafficLimitGiB = Math.floor(Number.MAX_SAFE_INTEGER / trafficCorrectionUnits.GiB)

function counter(value: number | undefined) {
  return Number.isFinite(value) ? Math.max(0, value ?? 0) : 0
}

export function trafficUsage(rxBytes: number | undefined, txBytes: number | undefined, mode: string | undefined) {
  const rx = counter(rxBytes)
  const tx = counter(txBytes)
  switch (mode as TrafficMode) {
    case 'max': return Math.max(rx, tx)
    case 'up': return tx
    case 'down': return rx
    default: return rx + tx
  }
}

export function trafficUtilization(usedBytes: number, limitBytes: number | undefined) {
  const limit = counter(limitBytes)
  if (limit === 0) return 0
  return Math.min(Math.max(counter(usedBytes) / limit * 100, 0), 100)
}

export function trafficBytesFrom(value: number, unit: TrafficCorrectionUnit) {
  const bytes = Math.round(value * trafficCorrectionUnits[unit])
  return Number.isFinite(bytes) && value >= 0 && Number.isSafeInteger(bytes) ? bytes : Number.NaN
}

export function trafficValueInUnit(bytes: number, unit: TrafficCorrectionUnit) {
  const value = counter(bytes) / trafficCorrectionUnits[unit]
  return Number(value.toFixed(unit === 'B' ? 0 : 3))
}

export function trafficLimitInGiB(bytes: number | undefined) {
  return counter(bytes) / trafficCorrectionUnits.GiB
}

export function trafficLimitBytesFromGiB(value: number) {
  return trafficBytesFrom(value, 'GiB')
}

export function suggestedTrafficUnit(...values: number[]): TrafficCorrectionUnit {
  const maximum = Math.max(0, ...values.map(counter))
  if (maximum >= trafficCorrectionUnits.TiB) return 'TiB'
  if (maximum >= trafficCorrectionUnits.GiB) return 'GiB'
  if (maximum >= trafficCorrectionUnits.MiB) return 'MiB'
  if (maximum >= trafficCorrectionUnits.KiB) return 'KiB'
  return 'B'
}
