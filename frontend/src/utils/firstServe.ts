import type { ProxyGroup } from '@/types'

export interface FirstServeConfig {
  ttl_minutes: number
  ttft_seconds: number
  max_switches: number
  cooldown_seconds: number
  proxy_mode: 'all' | 'selected'
  proxy_ids: number[]
}

export const firstServeFields = [
  { key: 'ttl_minutes', min: 1, max: 1440 },
  { key: 'ttft_seconds', min: 1, max: 300 },
  { key: 'max_switches', min: 1, max: 20 },
  { key: 'cooldown_seconds', min: 1, max: 3600 }
] as const

export function readFirstServeConfig(extra?: Record<string, unknown>): FirstServeConfig {
  const defaults: FirstServeConfig = {
    ttl_minutes: 30, ttft_seconds: 15, max_switches: 3, cooldown_seconds: 60,
    proxy_mode: 'all', proxy_ids: []
  }
  const raw = extra?.openai_first_serve
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return defaults
  const config = raw as Partial<FirstServeConfig>
  return { ...defaults, ...config, proxy_ids: Array.isArray(config.proxy_ids) ? [...config.proxy_ids] : [] }
}

export function firstServeIssue(config: FirstServeConfig, groupId: number | null, groups: ProxyGroup[]) {
  if (!groupId) return { key: 'groupRequired', params: {} }
  for (const field of firstServeFields) {
    if (!Number.isInteger(config[field.key]) || config[field.key] < field.min || config[field.key] > field.max) {
      return { key: 'numberInvalid', params: { field: field.key, min: field.min, max: field.max } }
    }
  }
  if (config.proxy_mode === 'all') return null
  if (new Set(config.proxy_ids).size < 2) return { key: 'selectTwo', params: {} }
  const group = groups.find(item => item.id === groupId)
  const invalid = config.proxy_ids.filter(id => !group?.proxy_ids.includes(id))
  if (invalid.length) return { key: 'outsideGroup', params: { ids: invalid.map(id => `#${id}`).join(', ') } }
  return null
}
