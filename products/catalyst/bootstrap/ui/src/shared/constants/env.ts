export type AppMode = 'saas' | 'selfhosted'

export const APP_MODE: AppMode =
  (import.meta.env['VITE_APP_MODE'] as AppMode) ?? 'saas'

export const IS_SAAS = APP_MODE === 'saas'
export const IS_SELFHOSTED = APP_MODE === 'selfhosted'
export const APP_VERSION = import.meta.env['VITE_APP_VERSION'] ?? 'dev'
