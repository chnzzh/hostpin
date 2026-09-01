import { toRaw } from 'vue'

export function clone<T>(value: T): T {
  return structuredClone(toRaw(value))
}
