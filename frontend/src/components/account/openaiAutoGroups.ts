import type { Group } from '@/types'

export function defaultOpenAIHighRateGroupIDs(groups: (Group & { auto_route_enabled?: boolean })[], accountType: string): number[] {
  return groups
    .filter(group => group.platform === 'openai' && group.status === 'active' && !group.auto_route_enabled && Number.isFinite(group.rate_multiplier) && group.rate_multiplier > 2 && (accountType !== 'apikey' || !group.require_oauth_only))
    .map(group => group.id)
}
