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
        { path: '', redirect: '/upstreams' },
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

export default router
