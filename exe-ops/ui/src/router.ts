import { createRouter, createWebHistory } from 'vue-router'
import Dashboard from './views/Dashboard.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'dashboard', component: Dashboard },
    { path: '/deploy', name: 'deploy', component: () => import('./views/Deploy.vue') },
    { path: '/hosts', name: 'hosts', component: () => import('./views/Hosts.vue') },
    { path: '/cd', name: 'cd', component: () => import('./views/CD.vue') },
  ],
})

export default router
