import { createRouter, createWebHistory } from 'vue-router'
import { useSessionStore } from './stores/session'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'overview', component: () => import('./views/OverviewView.vue') },
    { path: '/latency', name: 'latency', component: () => import('./views/LatencyView.vue') },
    { path: '/nodes/:id', name: 'node', component: () => import('./views/NodeView.vue') },
    { path: '/share/:token', name: 'share', component: () => import('./views/ShareView.vue') },
    { path: '/share/:token/nodes/:id', name: 'shared-node', component: () => import('./views/NodeView.vue') },
    { path: '/login', name: 'login', component: () => import('./views/LoginView.vue') },
    { path: '/setup', name: 'setup', component: () => import('./views/SetupView.vue') },
    { path: '/admin/:section?', name: 'admin', component: () => import('./views/AdminView.vue'), meta: { admin: true } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  const session = useSessionStore()
  if (!session.initialized) {
    try {
      await session.refresh()
    } catch {
      // Views provide a retry surface if the server is temporarily unavailable.
    }
  }
  if (session.initialized && !session.setupComplete && to.name !== 'setup') return { name: 'setup' }
  if (session.initialized && session.setupComplete && to.name === 'setup') return { name: 'overview' }
  if (to.meta.admin && !session.loggedIn) return { name: 'login', query: { next: to.fullPath } }
  if (to.name === 'login' && session.loggedIn) return { name: 'admin' }
  return true
})

export default router
