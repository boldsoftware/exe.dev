import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import VMList from '../views/VMList.vue'
import type { BoxInfo, DashboardData } from '../api/client'

function makeBox(overrides: Partial<BoxInfo> = {}): BoxInfo {
  return {
    name: 'test-vm',
    status: 'running',
    image: 'default',
    region: 'us-west',
    createdAt: '2024-01-01',
    updatedAt: '2024-01-02',
    sshCommand: 'ssh test-vm@exe.dev',
    proxyURL: 'https://test-vm.exe.cloud',
    terminalURL: 'https://test-vm.exe.cloud/terminal',
    shelleyURL: '',
    vscodeURL: '',
    proxyPort: 8080,
    proxyShare: 'private',
    routeKnown: true,
    sharedUserCount: 0,
    shareLinkCount: 0,
    totalShareCount: 0,
    sharedEmails: [],
    shareLinks: [],
    displayTags: [],
    hasCreationLog: false,
    isTeamShared: false,
    ...overrides,
  }
}

function makeDashboard(overrides: Partial<DashboardData> = {}): DashboardData {
  return {
    user: { email: 'test@example.com', region: 'us-west', regionDisplay: 'US West', newsletterSubscribed: false },
    boxes: [],
    sharedBoxes: [],
    teamSharedBoxes: [],
    teamBoxes: [],
    hasTeam: false,
    inviteCount: 0,
    canRequestInvites: false,
    sshCommand: 'ssh exe.dev',
    replHost: 'exe.dev',
    showIntegrations: false,
    billing: {
      periodStart: '2024-01-01',
      periodEnd: '2024-02-01',
    },
    ...overrides,
  }
}

async function mountVMList(dashboardData: DashboardData) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: VMList },
      { path: '/new', component: { template: '' } },
    ],
  })
  router.push('/')
  await router.isReady()

  const wrapper = mount(VMList, {
    global: {
      plugins: [router],
    },
  })
  await flushPromises()
  return wrapper
}

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    fetchDashboard: vi.fn(),
    fetchVMUsage: vi.fn().mockResolvedValue({ metrics: [] }),
  }
})

import { fetchDashboard } from '../api/client'
const mockFetchDashboard = vi.mocked(fetchDashboard)

describe('VMList', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
  })

  it('renders loading state initially', () => {
    // Never resolve so we stay in loading state
    mockFetchDashboard.mockReturnValue(new Promise(() => {}))
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: VMList },
        { path: '/new', component: { template: '' } },
      ],
    })
    const wrapper = mount(VMList, { global: { plugins: [router] } })
    expect(wrapper.text()).toContain('Loading')
  })

  it('renders empty state when no VMs exist', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMList(makeDashboard())
    expect(wrapper.text()).toContain('No VMs yet')
    expect(wrapper.text()).toContain('ssh exe.dev')
  })

  it('renders a list of VMs', async () => {
    const data = makeDashboard({
      boxes: [
        makeBox({ name: 'alpha-vm', status: 'running' }),
        makeBox({ name: 'beta-vm', status: 'stopped' }),
        makeBox({ name: 'gamma-vm', status: 'creating' }),
      ],
    })
    mockFetchDashboard.mockResolvedValue(data)
    const wrapper = await mountVMList(data)

    expect(wrapper.text()).toContain('alpha-vm')
    expect(wrapper.text()).toContain('beta-vm')
    expect(wrapper.text()).toContain('gamma-vm')
    // Verify all 3 VMCard components rendered
    const cards = wrapper.findAllComponents({ name: 'VMCard' })
    expect(cards).toHaveLength(3)
  })

  it('filters VMs by search query', async () => {
    const data = makeDashboard({
      boxes: [
        makeBox({ name: 'web-server' }),
        makeBox({ name: 'api-server' }),
        makeBox({ name: 'db-server' }),
      ],
    })
    mockFetchDashboard.mockResolvedValue(data)
    const wrapper = await mountVMList(data)

    const input = wrapper.find('.search-input')
    await input.setValue('api')
    await flushPromises()

    const cards = wrapper.findAllComponents({ name: 'VMCard' })
    expect(cards).toHaveLength(1)
    expect(wrapper.text()).toContain('api-server')
    expect(wrapper.text()).not.toContain('web-server')
    expect(wrapper.text()).not.toContain('db-server')
  })

  it('filters VMs by tag with # prefix', async () => {
    const data = makeDashboard({
      boxes: [
        makeBox({ name: 'prod-vm', displayTags: ['prod'] }),
        makeBox({ name: 'staging-vm', displayTags: ['staging'] }),
      ],
    })
    mockFetchDashboard.mockResolvedValue(data)
    const wrapper = await mountVMList(data)

    const input = wrapper.find('.search-input')
    await input.setValue('#prod')
    await flushPromises()

    const cards = wrapper.findAllComponents({ name: 'VMCard' })
    expect(cards).toHaveLength(1)
    expect(wrapper.text()).toContain('prod-vm')
  })

  it('shows no-match state when filter matches nothing', async () => {
    const data = makeDashboard({
      boxes: [makeBox({ name: 'my-vm' })],
    })
    mockFetchDashboard.mockResolvedValue(data)
    const wrapper = await mountVMList(data)

    const input = wrapper.find('.search-input')
    await input.setValue('nonexistent')
    await flushPromises()

    expect(wrapper.text()).toContain('No VMs match')
  })

  it('renders shared VMs section', async () => {
    const data = makeDashboard({
      boxes: [makeBox({ name: 'my-vm' })],
      sharedBoxes: [
        { name: 'friend-vm', ownerEmail: 'friend@example.com', proxyURL: 'https://friend-vm.exe.cloud' },
      ],
    })
    mockFetchDashboard.mockResolvedValue(data)
    const wrapper = await mountVMList(data)

    expect(wrapper.text()).toContain('Shared with you')
    expect(wrapper.text()).toContain('friend-vm')
    expect(wrapper.text()).toContain('friend@example.com')
  })

  it('renders error state on fetch failure', async () => {
    mockFetchDashboard.mockRejectedValue(new Error('Network error'))
    const wrapper = await mountVMList(makeDashboard())

    expect(wrapper.text()).toContain('Failed to load VMs')
    expect(wrapper.text()).toContain('Network error')
  })

  it('groups VMs by tag when group option is tag', async () => {
    const data = makeDashboard({
      boxes: [
        makeBox({ name: 'prod-1', displayTags: ['prod'] }),
        makeBox({ name: 'prod-2', displayTags: ['prod'] }),
        makeBox({ name: 'dev-1', displayTags: ['dev'] }),
        makeBox({ name: 'loose-vm' }),
      ],
    })
    mockFetchDashboard.mockResolvedValue(data)

    // Default view is group by tag
    localStorage.setItem('exe-vm-view-options', JSON.stringify({ sort: 'name', group: 'tag' }))
    const wrapper = await mountVMList(data)

    // Should see group headers
    expect(wrapper.text()).toContain('#dev')
    expect(wrapper.text()).toContain('#prod')
    expect(wrapper.text()).toContain('Untagged')
  })
})
