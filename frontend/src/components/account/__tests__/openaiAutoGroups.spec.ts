import { describe, expect, it } from 'vitest'
import type { Group } from '@/types'
import { defaultOpenAIHighRateGroupIDs } from '../openaiAutoGroups'

describe('defaultOpenAIHighRateGroupIDs', () => {
  it('uses multipliers, not group names, and excludes incompatible or virtual groups', () => {
    const groups = [
      { id: 1, name: '自用', platform: 'openai', status: 'active', rate_multiplier: 3 },
      { id: 2, name: '不降智', platform: 'openai', status: 'active', rate_multiplier: 1.5 },
      { id: 3, platform: 'openai', status: 'active', rate_multiplier: 2 },
      { id: 4, platform: 'openai', status: 'active', rate_multiplier: 2.5, auto_route_enabled: true },
      { id: 5, platform: 'openai', status: 'active', rate_multiplier: 2.5, require_oauth_only: true },
      { id: 6, platform: 'openai', status: 'inactive', rate_multiplier: 3 },
      { id: 7, platform: 'anthropic', status: 'active', rate_multiplier: 3 },
    ] as Group[]
    expect(defaultOpenAIHighRateGroupIDs(groups, 'apikey')).toEqual([1])
    expect(defaultOpenAIHighRateGroupIDs(groups, 'oauth')).toEqual([1, 5])
    expect(defaultOpenAIHighRateGroupIDs([], 'oauth')).toEqual([])
  })
})
