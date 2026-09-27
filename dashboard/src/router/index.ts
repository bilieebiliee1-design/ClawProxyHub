import { createRouter, createWebHistory } from 'vue-router'
import { getToken, getRole } from '../api/client'

// 路由 base 自适应：面板除了本地根路径，还会经隧道挂载在 /panel/ 下
// （app/tunnelpage.go 的 overlay：/panel/ → SPA，静态资源走 /assets，API 走 /admin）。
// 同一份构建两处皆可用；location 在 /panel/ 下即以 /panel/ 为 base。
const routerBase = location.pathname.startsWith('/panel/') ? '/panel/' : '/'

// guest 角色可访问的路径（与后端 menusForRole 保持一致）
const GUEST_PATHS = ['dashboard', 'logs', 'profile']

const router = createRouter({
  history: createWebHistory(routerBase),
  routes: [
    { path: '/login', component: () => import('../views/auth/Login.vue') },
    { path: '/setup', component: () => import('../views/auth/Setup.vue') },
    {
      path: '/',
      component: () => import('../layouts/AppLayout.vue'),
      children: [
        { path: '', redirect: '/dashboard' },
        { path: 'dashboard', component: () => import('../views/dashboard/Dashboard.vue') },
        { path: 'plugins', component: () => import('../views/plugins/Plugins.vue') },
        { path: 'instances', component: () => import('../views/instances/Instances.vue') },
        { path: 'accounts', component: () => import('../views/accounts/Accounts.vue') },
        { path: 'groups', component: () => import('../views/groups/Groups.vue') },
        { path: 'proxies', component: () => import('../views/proxies/Proxies.vue') },
        { path: 'routes', component: () => import('../views/routes/Routes.vue') },
        { path: 'keys', component: () => import('../views/keys/Keys.vue') },
        { path: 'oauth', component: () => import('../views/oauth/OAuth.vue') },
        { path: 'tasks', component: () => import('../views/tasks/Tasks.vue') },
        { path: 'logs', component: () => import('../views/logs/Logs.vue') },
        { path: 'settings', component: () => import('../views/settings/Settings.vue') },
        { path: 'profile', component: () => import('../views/profile/Profile.vue') },
      ],
    },
  ],
})

router.beforeEach((to) => {
  if (to.path !== '/login' && to.path !== '/setup' && !getToken()) return '/login'
  // guest 只读：访问配置类路径重定向回概览
  if (getToken() && getRole() !== 'admin') {
    const seg = to.path.replace(/^\//, '').split('/')[0]
    if (seg && !GUEST_PATHS.includes(seg) && to.path !== '/') return '/dashboard'
  }
})

export default router
