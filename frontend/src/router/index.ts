import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/LoginView.vue'),
      meta: { public: true, title: '登录' },
    },
    {
      path: '/',
      component: () => import('@/layouts/AdminLayout.vue'),
      children: [
        { path: '', redirect: '/dashboard' },
        {
          path: 'dashboard',
          name: 'dashboard',
          component: () => import('@/views/DashboardView.vue'),
          meta: { title: '仪表盘' },
        },
        {
          path: 'upstreams',
          name: 'upstreams',
          component: () => import('@/views/UpstreamsView.vue'),
          meta: { title: '提供商' },
        },
        { path: 'keys', redirect: '/upstreams' },
        {
          path: 'api-keys',
          name: 'api-keys',
          component: () => import('@/views/ApiKeysView.vue'),
          meta: { title: 'API 密钥' },
        },
        { path: 'route-groups', redirect: '/api-keys' },
        { path: 'balances', redirect: '/upstreams' },
        { path: 'status', redirect: '/upstreams' },
        { path: 'prices', redirect: '/api-keys' },
        {
          path: 'scheduler',
          name: 'scheduler',
          component: () => import('@/views/SchedulerView.vue'),
          meta: { title: '调度' },
        },
        {
          path: 'alerts',
          name: 'alerts',
          component: () => import('@/views/AlertSettingsView.vue'),
          meta: { title: '余额提醒' },
        },
        {
          path: 'intel',
          name: 'intel',
          component: () => import('@/views/IntelTestsView.vue'),
          meta: { title: '测智' },
        },
        {
          path: 'logs',
          name: 'logs',
          component: () => import('@/views/RequestLogsView.vue'),
          meta: { title: '请求记录' },
        },
        { path: 'consumers', redirect: '/api-keys' },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

const APP_TITLE = '供货商管理'

router.beforeEach((to) => {
  const auth = useAuthStore()
  if (to.meta.public) {
    if (auth.isAuthed && to.path === '/login') return { path: '/' }
    return true
  }
  if (!auth.isAuthed) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  return true
})

router.afterEach((to) => {
  const pageTitle = to.meta.title as string | undefined
  document.title = pageTitle ? `${pageTitle} · ${APP_TITLE}` : APP_TITLE
})

export default router
