import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import NewVM from '../views/NewVM.vue'

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    checkHostname: vi.fn(),
    fetchIntegrations: vi.fn(),
  }
})

import { checkHostname, fetchIntegrations } from '../api/client'
const mockCheckHostname = vi.mocked(checkHostname)
const mockFetchIntegrations = vi.mocked(fetchIntegrations)

async function mountNewVM(query: Record<string, string | string[]> = {}) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/new', name: 'new-vm', component: NewVM }],
  })
  router.push({ name: 'new-vm', query })
  await router.isReady()
  const wrapper = mount(NewVM, { global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

describe('NewVM', () => {
  beforeEach(() => {
    vi.useRealTimers()
    vi.clearAllMocks()
    mockCheckHostname.mockResolvedValue({ valid: true, available: true, message: '' })
    mockFetchIntegrations.mockResolvedValue({
      integrations: [],
      githubIntegrations: [],
      proxyIntegrations: [],
      reflectionIntegrations: [],
      githubAccounts: [],
      githubEnabled: false,
      githubAppSlug: '',
      hasPushTokens: false,
      allTags: ['prod', 'web'],
      tagVMs: { prod: ['vm-a'], web: ['vm-b'] },
      tagIntegrationSummaries: [
        { tag: 'prod', integrations: ['github-main', 'proxy-api', 'reflection'], more: 1 },
        { tag: 'web', integrations: [], more: 0 },
      ],
      boxes: [],
      integrationScheme: 'exe',
      boxHost: 'exe.xyz',
      hasTeam: false,
    })
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url === '/api/ideas') return new Response(JSON.stringify([]), { status: 200 })
      return new Response('{}', { status: 404 })
    }))
  })

  it('shows compact tag options with integration names', async () => {
    const wrapper = await mountNewVM()

    await wrapper.find('summary').trigger('click')

    expect(wrapper.text()).toContain('#prod')
    expect(wrapper.text()).toContain('github-main, proxy-api, reflection +1')
    expect(wrapper.text()).toContain('#web')
    expect(wrapper.text()).toContain('No integrations')
  })

  it('submits selected tags', async () => {
    const submit = vi.fn()
    vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(submit)
    vi.useFakeTimers()
    const wrapper = await mountNewVM({ name: 'my-vm' })
    await vi.advanceTimersByTimeAsync(600)
    await flushPromises()

    await wrapper.find('summary').trigger('click')
    await wrapper.findAll('.tag-option')[0].trigger('click')
    await wrapper.find('form').trigger('submit.prevent')

    const forms = Array.from(document.body.querySelectorAll('form'))
    const posted = forms[forms.length - 1]
    const tags = posted.querySelector('input[name="tags"]') as HTMLInputElement
    expect(tags.value).toBe('prod')
    expect(submit).toHaveBeenCalled()
  })

  it('prefills prompt from query params', async () => {
    const wrapper = await mountNewVM({ prompt: 'Build a dashboard' })

    const promptInput = wrapper.find('textarea.prompt-input')
    expect((promptInput.element as HTMLTextAreaElement).value).toBe('Build a dashboard')
  })

  it('prefills prompt from the first repeated query param', async () => {
    const wrapper = await mountNewVM({ prompt: ['Build a dashboard', 'with auth'] })

    const promptInput = wrapper.find('textarea.prompt-input')
    expect((promptInput.element as HTMLTextAreaElement).value).toBe('Build a dashboard')
  })
})
