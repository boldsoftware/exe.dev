import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import VMDetail from '../views/VMDetail.vue'
import type { BoxInfo, DashboardData, ProfileData, BoxLLMUsageResponse } from '../api/client'

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

function makeBox(overrides: Partial<BoxInfo> = {}): BoxInfo {
  return {
    name: 'my-vm',
    status: 'running',
    image: 'ubuntu-22.04',
    region: 'us-west',
    createdAt: '2024-01-15',
    updatedAt: '2024-01-20',
    sshCommand: 'ssh my-vm@exe.dev',
    proxyURL: 'https://my-vm.exe.cloud',
    terminalURL: 'https://my-vm.exe.cloud/terminal',
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
    emoji: '',
    ...overrides,
  }
}

function makeDashboard(overrides: Partial<DashboardData> = {}): DashboardData {
  return {
    user: { email: 'test@example.com', region: 'us-west', regionDisplay: 'US West', newsletterSubscribed: false },
    boxes: [makeBox()],
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

function makeProfile(overrides: Partial<ProfileData> = {}): ProfileData {
  return {
    user: { email: 'test@example.com', region: 'us-west', regionDisplay: 'US West', newsletterSubscribed: false },
    sshKeys: [],
    passkeys: [],
    siteSessions: [],
    sharedBoxes: [],
    teamInfo: null,
    pendingTeamInvites: [],
    canEnableTeam: false,
    credits: { planName: 'Pro', balance: 0, currency: 'usd' } as any,
    basicUser: false,
    showIntegrations: false,
    canEmailSupport: false,
    inviteCount: 0,
    canRequestInvites: false,
    boxes: [{ name: 'my-vm', status: 'running' }],
    availableRegions: [],
    billingPeriodStart: '2024-01-01T00:00:00Z',
    billingPeriodEnd: '2024-02-01T00:00:00Z',
    ...overrides,
  }
}

function makeBoxLLMUsage(overrides: Partial<BoxLLMUsageResponse> = {}): BoxLLMUsageResponse {
  return {
    models: [
      { model: 'claude-sonnet-4-20250514', provider: 'anthropic', cost: '$1.50' },
      { model: 'gpt-4o', provider: 'openai', cost: '$0.25' },
    ],
    totalCost: '$1.75',
    periodStart: '2024-01-01T00:00:00Z',
    periodEnd: '2024-02-01T00:00:00Z',
    ...overrides,
  }
}

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    fetchDashboard: vi.fn(),
    fetchBoxLLMUsage: vi.fn(),
    fetchProfile: vi.fn(),
    fetchVMLiveMetrics: vi.fn(),
    fetchVMsLive: vi.fn(),
  }
})

import { fetchDashboard, fetchBoxLLMUsage, fetchProfile, fetchVMLiveMetrics, fetchVMsLive } from '../api/client'
const mockFetchDashboard = vi.mocked(fetchDashboard)
const mockFetchBoxLLMUsage = vi.mocked(fetchBoxLLMUsage)
const mockFetchProfile = vi.mocked(fetchProfile)
const mockFetchVMLiveMetrics = vi.mocked(fetchVMLiveMetrics)
const mockFetchVMsLive = vi.mocked(fetchVMsLive)

// ---------------------------------------------------------------------------
// Mount helper
// ---------------------------------------------------------------------------

async function mountVMDetail(vmName = 'my-vm') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/vm/:name', component: VMDetail },
    ],
  })
  router.push(`/vm/${vmName}`)
  await router.isReady()

  const wrapper = mount(VMDetail, {
    global: { plugins: [router] },
  })
  await flushPromises()
  return wrapper
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('VMDetail', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    // Default: LLM usage and profile never resolve (test loading states separately)
    mockFetchBoxLLMUsage.mockResolvedValue(makeBoxLLMUsage({ models: [], totalCost: '$0.00' }))
    mockFetchProfile.mockReturnValue(new Promise(() => {}))
    mockFetchVMsLive.mockResolvedValue({ vms: [], pool: { cpu_used: 0, cpu_max: 0, mem_used_bytes: 0, mem_max_bytes: 0 } })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // --- Loading & error states ---

  it('shows loading spinner while fetching', () => {
    mockFetchDashboard.mockReturnValue(new Promise(() => {}))
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/vm/:name', component: VMDetail }],
    })
    router.push('/vm/my-vm')
    const wrapper = mount(VMDetail, { global: { plugins: [router] } })
    expect(wrapper.text()).toContain('Loading')
  })

  it('shows error state when dashboard fetch fails', async () => {
    mockFetchDashboard.mockRejectedValue(new Error('Network failure'))
    const wrapper = await mountVMDetail()
    expect(wrapper.text()).toContain('Network failure')
    expect(wrapper.find('button').text()).toContain('Retry')
  })

  it('retries load on Retry click', async () => {
    mockFetchDashboard
      .mockRejectedValueOnce(new Error('Temporary error'))
      .mockResolvedValueOnce(makeDashboard())
    const wrapper = await mountVMDetail()
    expect(wrapper.text()).toContain('Temporary error')
    await wrapper.find('button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('my-vm')
    expect(wrapper.text()).not.toContain('Temporary error')
  })

  it('shows not-found state when VM is missing from dashboard', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({ boxes: [] }))
    const wrapper = await mountVMDetail('ghost-vm')
    expect(wrapper.text()).toContain('"ghost-vm" not found')
    expect(wrapper.text()).toContain('Back to VMs')
  })

  // --- Core VM info rendering ---

  it('renders VM name and breadcrumb', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    expect(wrapper.find('h1').text()).toBe('my-vm')
    expect(wrapper.text()).toContain('my-vm') // breadcrumb
  })

  it('renders region, image, and created date in details grid', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    const text = wrapper.text()
    expect(text).toContain('us-west')
    expect(text).toContain('ubuntu-22.04')
    expect(text).toContain('2024-01-15')
  })

  it('renders SSH command', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.ssh-cmd').text()).toBe('ssh my-vm@exe.dev')
  })

  it('hides SSH row when sshCommand is empty', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({ boxes: [makeBox({ sshCommand: '' })] }))
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.ssh-row').exists()).toBe(false)
  })

  it('renders tags', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ displayTags: ['prod', 'web'] })],
    }))
    const wrapper = await mountVMDetail()
    const tagsRow = wrapper.find('.tags-row')
    expect(tagsRow.exists()).toBe(true)
    expect(tagsRow.text()).toContain('#prod')
    expect(tagsRow.text()).toContain('#web')
  })

  it('shows empty Tags row with Add Tag button when no tags', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    const tagsRow = wrapper.find('.tags-row')
    expect(tagsRow.exists()).toBe(true)
    expect(tagsRow.text()).not.toContain('#')
    expect(tagsRow.text()).toContain('Add Tag')
  })

  // --- Action buttons ---

  it('renders HTTPS and Terminal action buttons', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    const pills = wrapper.findAll('.action-btn-expanded')
    const texts = pills.map(p => p.text())
    expect(texts.some(t => t.includes('HTTPS'))).toBe(true)
    expect(texts.some(t => t.includes('Terminal'))).toBe(true)
  })

  it('shows Shelley button only when shelleyURL is set', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ shelleyURL: 'https://my-vm.exe.cloud/shelley' })],
    }))
    const wrapper = await mountVMDetail()
    const pills = wrapper.findAll('.action-btn-expanded')
    expect(pills.some(p => p.text().includes('Shelley'))).toBe(true)
  })

  it('hides Shelley button when shelleyURL is empty', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    const pills = wrapper.findAll('.action-btn-expanded')
    expect(pills.some(p => p.text().includes('Shelley'))).toBe(false)
  })

  it('shows Editor button only when vscodeURL is set', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ vscodeURL: 'vscode://vscode-remote/ssh-remote+my-vm/home/exedev' })],
    }))
    const wrapper = await mountVMDetail()
    const pills = wrapper.findAll('.action-btn-expanded')
    expect(pills.some(p => p.text().includes('Editor'))).toBe(true)
  })

  it('shows PUBLIC badge when proxyShare is public', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ proxyShare: 'public' })],
    }))
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.badge-public').text()).toBe('PUBLIC')
  })

  it('shows TEAM badge when isTeamShared', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ isTeamShared: true })],
    }))
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.badge-team').text()).toBe('TEAM')
  })

  it('does not show PUBLIC/TEAM badges for a normal private VM', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.badge-public').exists()).toBe(false)
    expect(wrapper.find('.badge-team').exists()).toBe(false)
  })

  // --- Team sharing action button ---

  it('hides Share with Team action when hasTeam is false', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({ hasTeam: false }))
    const wrapper = await mountVMDetail()
    const items = wrapper.findAll('.action-btn-expanded').map(i => i.text())
    expect(items.some(t => t.includes('Team'))).toBe(false)
  })

  it('shows Share with Team action when hasTeam is true', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({ hasTeam: true }))
    const wrapper = await mountVMDetail()
    const items = wrapper.findAll('.action-btn-expanded').map(i => i.text())
    expect(items.some(t => t.includes('Team'))).toBe(true)
  })

  // --- Shelley Usage ---

  it('renders LLM usage with the API period label', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      billing: { periodStart: '2024-03-15', periodEnd: '2024-04-15' },
    }))
    mockFetchProfile.mockResolvedValue(makeProfile())
    mockFetchBoxLLMUsage.mockResolvedValue(makeBoxLLMUsage({
      periodStart: '2024-04-01T00:00:00Z',
      periodEnd: '2024-05-01T00:00:00Z',
    }))
    const wrapper = await mountVMDetail()
    const llmSection = wrapper.find('.llm-usage-section')
    expect(llmSection.exists()).toBe(true)
    expect(llmSection.text()).toContain('Shelley Usage')
    expect(llmSection.text()).toContain('April 1')
    expect(llmSection.text()).toContain('May 1')
    expect(llmSection.text()).not.toContain('March 1')
    expect(llmSection.text()).toContain('claude-sonnet-4-20250514')
    expect(llmSection.text()).toContain('$1.75')
  })

  it('hides LLM usage section when there is no usage', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchProfile.mockResolvedValue(makeProfile())
    mockFetchBoxLLMUsage.mockResolvedValue(makeBoxLLMUsage({ models: [], totalCost: '$0.00' }))
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.llm-usage-section').exists()).toBe(false)
  })

  it('logs when VM LLM usage fetch fails', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchProfile.mockResolvedValue(makeProfile())
    mockFetchBoxLLMUsage.mockRejectedValue(new Error('llm down'))
    await mountVMDetail()
    expect(consoleError).toHaveBeenCalledWith('Failed to load VM LLM usage:', expect.any(Error))
  })

  // --- Editor modal ---

  it('opens editor modal when Editor button clicked', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ vscodeURL: 'vscode://vscode-remote/ssh-remote+my-vm/home/exedev' })],
    }))
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.modal-overlay').exists()).toBe(false)
    const editorBtn = wrapper.findAll('.action-btn-expanded').find(p => p.text().includes('Editor'))
    await editorBtn!.trigger('click')
    expect(wrapper.find('.modal-overlay').exists()).toBe(true)
    expect(wrapper.find('.modal-title').text()).toBe('Open in Editor')
  })

  it('closes editor modal when close button clicked', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ vscodeURL: 'vscode://vscode-remote/ssh-remote+my-vm/home/exedev' })],
    }))
    const wrapper = await mountVMDetail()
    const editorBtn = wrapper.findAll('.action-btn-expanded').find(p => p.text().includes('Editor'))
    await editorBtn!.trigger('click')
    expect(wrapper.find('.modal-overlay').exists()).toBe(true)
    await wrapper.find('.modal-close').trigger('click')
    expect(wrapper.find('.modal-overlay').exists()).toBe(false)
  })

  it('generates correct vscode URL from vscodeURL', async () => {
    const vscodeURL = 'vscode://vscode-remote/ssh-remote+my-vm/home/exedev'
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ vscodeURL })],
    }))
    localStorage.setItem('preferred-editor', 'vscode')
    const wrapper = await mountVMDetail()
    const editorBtn = wrapper.findAll('.action-btn-expanded').find(p => p.text().includes('Editor'))
    await editorBtn!.trigger('click')
    const url = wrapper.find('.editor-url').text()
    expect(url).toContain('vscode://vscode-remote/ssh-remote+my-vm')
  })

  it('generates cursor URL when cursor editor selected', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ vscodeURL: 'vscode://vscode-remote/ssh-remote+my-vm/home/exedev' })],
    }))
    localStorage.setItem('preferred-editor', 'cursor')
    const wrapper = await mountVMDetail()
    const editorBtn = wrapper.findAll('.action-btn-expanded').find(p => p.text().includes('Editor'))
    await editorBtn!.trigger('click')
    const url = wrapper.find('.editor-url').text()
    expect(url).toContain('cursor://vscode-remote/ssh-remote+my-vm')
  })

  // --- Pool section (resource pool bars) ---

  it('shows pool section with stacked bars when pool data and live metrics are available', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMLiveMetrics.mockResolvedValue({
      name: 'my-vm',
      status: 'running',
      cpu_percent: 42.5,
      mem_bytes: 1073741824,
      swap_bytes: 0,
      disk_bytes: 3221225472,
      disk_logical_bytes: 5368709120,
      disk_capacity_bytes: 30 * 1024 * 1024 * 1024,
      mem_capacity_bytes: 8 * 1024 * 1024 * 1024,
      cpus: 2,
      net_rx_bytes: 1048576,
      net_tx_bytes: 524288,
    })
    mockFetchVMsLive.mockResolvedValue({
      vms: [
        { name: 'my-vm', status: 'running', cpu_percent: 42.5, cpus: 2, mem_bytes: 1073741824, mem_capacity_bytes: 8 * 1024 * 1024 * 1024, disk_bytes: 0, disk_logical_bytes: 0, disk_capacity_bytes: 0, net_rx_bytes: 0, net_tx_bytes: 0 },
        { name: 'other-vm', status: 'running', cpu_percent: 100, cpus: 4, mem_bytes: 0, mem_capacity_bytes: 16 * 1024 * 1024 * 1024, disk_bytes: 0, disk_logical_bytes: 0, disk_capacity_bytes: 0, net_rx_bytes: 0, net_tx_bytes: 0 },
      ],
      pool: { cpu_used: 1.425, cpu_max: 8, mem_used_bytes: 24 * 1024 * 1024 * 1024, mem_max_bytes: 32 * 1024 * 1024 * 1024 },
    })
    const wrapper = await mountVMDetail()
    const poolSection = wrapper.find('.pool-section')
    expect(poolSection.exists()).toBe(true)
    expect(poolSection.text()).toContain('Resource Pool (live)')
    // Should have stacked bar segments
    expect(poolSection.findAll('.pool-seg-this').length).toBeGreaterThanOrEqual(1)
    expect(poolSection.findAll('.pool-seg-other').length).toBeGreaterThanOrEqual(1)
  })

  it('shows correct CPU values in pool section', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMLiveMetrics.mockResolvedValue({
      name: 'my-vm',
      status: 'running',
      cpu_percent: 150,
      mem_bytes: 0,
      swap_bytes: 0,
      disk_bytes: 0,
      disk_logical_bytes: 0,
      disk_capacity_bytes: 0,
      mem_capacity_bytes: 0,
      cpus: 2,
      net_rx_bytes: 0,
      net_tx_bytes: 0,
    })
    mockFetchVMsLive.mockResolvedValue({
      vms: [
        { name: 'my-vm', status: 'running', cpu_percent: 150, cpus: 2, mem_bytes: 0, mem_capacity_bytes: 0, disk_bytes: 0, disk_logical_bytes: 0, disk_capacity_bytes: 0, net_rx_bytes: 0, net_tx_bytes: 0 },
      ],
      pool: { cpu_used: 1.5, cpu_max: 8, mem_used_bytes: 0, mem_max_bytes: 0 },
    })
    const wrapper = await mountVMDetail()
    const poolSection = wrapper.find('.pool-section')
    expect(poolSection.exists()).toBe(true)
    // thisCPU = 150/100 = 1.5, totalCPU = 1.5, maxCPU = 8
    const cpuRow = poolSection.findAll('.pool-row')[0]
    expect(cpuRow.find('.pool-label').text()).toBe('vCPU')
    expect(cpuRow.find('.pool-values').text()).toContain('1.5')
    expect(cpuRow.find('.pool-values').text()).toContain('of 8')
  })

  it('shows memory row in pool section when maxMem > 0', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMLiveMetrics.mockResolvedValue({
      name: 'my-vm',
      status: 'running',
      cpu_percent: 50,
      mem_bytes: 0,
      swap_bytes: 0,
      disk_bytes: 0,
      disk_logical_bytes: 0,
      disk_capacity_bytes: 0,
      mem_capacity_bytes: 4 * 1024 * 1024 * 1024,
      cpus: 2,
      net_rx_bytes: 0,
      net_tx_bytes: 0,
    })
    mockFetchVMsLive.mockResolvedValue({
      vms: [
        { name: 'my-vm', status: 'running', cpu_percent: 50, cpus: 2, mem_bytes: 0, mem_capacity_bytes: 4 * 1024 * 1024 * 1024, disk_bytes: 0, disk_logical_bytes: 0, disk_capacity_bytes: 0, net_rx_bytes: 0, net_tx_bytes: 0 },
      ],
      pool: { cpu_used: 0.5, cpu_max: 4, mem_used_bytes: 4 * 1024 * 1024 * 1024, mem_max_bytes: 16 * 1024 * 1024 * 1024 },
    })
    const wrapper = await mountVMDetail()
    const poolSection = wrapper.find('.pool-section')
    const labels = poolSection.findAll('.pool-label').map(l => l.text())
    expect(labels).toContain('Memory')
  })

  it('hides memory row in pool section when maxMem is 0', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMLiveMetrics.mockResolvedValue({
      name: 'my-vm',
      status: 'running',
      cpu_percent: 50,
      mem_bytes: 0,
      swap_bytes: 0,
      disk_bytes: 0,
      disk_logical_bytes: 0,
      disk_capacity_bytes: 0,
      mem_capacity_bytes: 4 * 1024 * 1024 * 1024,
      cpus: 2,
      net_rx_bytes: 0,
      net_tx_bytes: 0,
    })
    mockFetchVMsLive.mockResolvedValue({
      vms: [
        { name: 'my-vm', status: 'running', cpu_percent: 50, cpus: 2, mem_bytes: 0, mem_capacity_bytes: 4 * 1024 * 1024 * 1024, disk_bytes: 0, disk_logical_bytes: 0, disk_capacity_bytes: 0, net_rx_bytes: 0, net_tx_bytes: 0 },
      ],
      pool: { cpu_used: 0.5, cpu_max: 4, mem_used_bytes: 4 * 1024 * 1024 * 1024, mem_max_bytes: 0 },
    })
    const wrapper = await mountVMDetail()
    const poolSection = wrapper.find('.pool-section')
    const labels = poolSection.findAll('.pool-label').map(l => l.text())
    expect(labels).toContain('vCPU')
    expect(labels).not.toContain('Memory')
  })

  it('shows legend with VM name in pool section', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMLiveMetrics.mockResolvedValue({
      name: 'my-vm',
      status: 'running',
      cpu_percent: 50,
      mem_bytes: 0,
      swap_bytes: 0,
      disk_bytes: 0,
      disk_logical_bytes: 0,
      disk_capacity_bytes: 0,
      mem_capacity_bytes: 0,
      cpus: 2,
      net_rx_bytes: 0,
      net_tx_bytes: 0,
    })
    mockFetchVMsLive.mockResolvedValue({
      vms: [
        { name: 'my-vm', status: 'running', cpu_percent: 50, cpus: 2, mem_bytes: 0, mem_capacity_bytes: 0, disk_bytes: 0, disk_logical_bytes: 0, disk_capacity_bytes: 0, net_rx_bytes: 0, net_tx_bytes: 0 },
      ],
      pool: { cpu_used: 0.5, cpu_max: 4, mem_used_bytes: 0, mem_max_bytes: 0 },
    })
    const wrapper = await mountVMDetail()
    const legend = wrapper.find('.pool-legend')
    expect(legend.exists()).toBe(true)
    expect(legend.text()).toContain('my-vm')
    expect(legend.text()).toContain('other VMs')
  })

  it('hides pool section when fetchVMsLive returns cpu_max = 0 (unlimited plan)', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMLiveMetrics.mockResolvedValue({
      name: 'my-vm',
      status: 'running',
      cpu_percent: 50,
      mem_bytes: 0,
      swap_bytes: 0,
      disk_bytes: 0,
      disk_logical_bytes: 0,
      disk_capacity_bytes: 0,
      mem_capacity_bytes: 0,
      cpus: 2,
      net_rx_bytes: 0,
      net_tx_bytes: 0,
    })
    // Default beforeEach mock already returns cpu_max: 0
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.pool-section').exists()).toBe(false)
  })

  it('hides pool section for stopped VMs (fetchProvisionedSpecs not called)', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ status: 'stopped' })],
    }))
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.pool-section').exists()).toBe(false)
    expect(mockFetchVMLiveMetrics).not.toHaveBeenCalled()
    expect(mockFetchVMsLive).not.toHaveBeenCalled()
  })

  it('hides pool section when backend returns empty vms (metrics not validated)', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMLiveMetrics.mockResolvedValue({
      name: 'my-vm',
      status: 'running',
      cpu_percent: 50,
      mem_bytes: 0,
      swap_bytes: 0,
      disk_bytes: 0,
      disk_logical_bytes: 0,
      disk_capacity_bytes: 0,
      mem_capacity_bytes: 0,
      cpus: 2,
      net_rx_bytes: 0,
      net_tx_bytes: 0,
    })
    mockFetchVMsLive.mockResolvedValue({
      vms: [],
      pool: { cpu_used: 0, cpu_max: 0, mem_used_bytes: 0, mem_max_bytes: 0 },
    })
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.pool-section').exists()).toBe(false)
  })



  // --- Charts placeholder still hidden ---

  it('does not render chart placeholder sections', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.section-placeholder').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Usage History')
  })
})
