import { createRouter, createWebHistory } from 'vue-router'
import VMList from './views/VMList.vue'

const router = createRouter({
  history: createWebHistory(),
  scrollBehavior(to) {
    if (to.hash) {
      return { el: to.hash, behavior: 'smooth' }
    }
    return { top: 0 }
  },
  routes: [
    { path: '/', name: 'vms', component: VMList },
    { path: '/user', name: 'profile', component: () => import('./views/Profile.vue') },
    { path: '/integrations', name: 'integrations', component: () => import('./views/Integrations.vue') },
    { path: '/shell', name: 'shell', component: () => import('./views/Shell.vue') },
    { path: '/new', name: 'new-vm', component: () => import('./views/NewVM.vue') },
    { path: '/docs', name: 'docs', component: () => import('./views/DocsEntry.vue') },
    { path: '/docs/list', name: 'docs-list', component: () => import('./views/DocsList.vue') },
    { path: '/docs/all', name: 'docs-all', component: () => import('./views/DocsEntry.vue') },
    { path: '/docs/:slug(.*)', name: 'docs-entry', component: () => import('./views/DocsEntry.vue') },
  ],
})

export default router
