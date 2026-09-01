import { defineStore } from 'pinia'
import { api } from '../api'
import type { SiteSettings } from '../types'

interface SessionResponse {
  setup_complete: boolean
  logged_in: boolean
  username?: string
  two_factor_enabled?: boolean
  csrf_token?: string
}

interface SiteResponse {
  setup_complete: boolean
  authenticated: boolean
  settings: SiteSettings
  version: { version: string; commit: string; protocol: number }
}

export const useSessionStore = defineStore('session', {
  state: () => ({
    initialized: false,
    setupComplete: false,
    loggedIn: false,
    username: '',
    site: null as SiteSettings | null,
    version: '',
  }),
  actions: {
    async refresh() {
      const [session, site] = await Promise.all([
        api<SessionResponse>('/api/v1/auth/me'),
        api<SiteResponse>('/api/v1/public/site'),
      ])
      this.setupComplete = session.setup_complete
      this.loggedIn = session.logged_in
      this.username = session.username ?? ''
      this.site = site.settings
      this.version = site.version.version
      this.initialized = true
    },
    async login(username: string, password: string, totpCode = '') {
      await api('/api/v1/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username, password, totp_code: totpCode }),
      })
      await this.refresh()
    },
    async logout() {
      await api('/api/v1/auth/logout', { method: 'POST' })
      this.loggedIn = false
      this.username = ''
    },
  },
})
