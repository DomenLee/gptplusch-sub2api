import { apiClient } from './client'

export interface AccountState {
  account: { id: string; email: string; version: number } | null
  email?: string
  requiresMfa: boolean
}

let configRequest: Promise<{ enabled: boolean }> | undefined
export function getAccountConfig() {
  configRequest ||= apiClient.get<{ enabled: boolean }>('/auth/unified/config')
    .then(({ data }) => data)
    .catch((error) => {
      configRequest = undefined
      throw error
    })
  return configRequest
}

export async function getAccountState(): Promise<AccountState> {
  return (await apiClient.get<AccountState>('/auth/unified/account')).data
}
