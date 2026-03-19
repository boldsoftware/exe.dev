import { createRouter, createWebHistory } from 'vue-router'
import Dashboard from './views/Dashboard.vue'
import ServerList from './views/ServerList.vue'
import ServerDetails from './views/ServerDetails.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'dashboard', component: Dashboard },
    { path: '/servers', name: 'servers', component: ServerList },
    { path: '/servers/:name', name: 'server-details', component: ServerDetails },
    { path: '/alerts', name: 'alerts', component: () => import('./views/Alerts.vue') },
    { path: '/storage', name: 'storage', component: () => import('./views/Storage.vue') },
    { path: '/components', name: 'components', component: () => import('./views/Components.vue') },
    { path: '/updates', name: 'updates', component: () => import('./views/Updates.vue') },
    { path: '/deploy', name: 'deploy', component: () => import('./views/Deploy.vue') },
    { path: '/agent', name: 'agent', component: () => import('./views/Agent.vue') },
  ],
})

export default router
