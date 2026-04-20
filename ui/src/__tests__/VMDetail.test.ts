import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import VMDetail from '../views/VMDetail.vue'
import type { BoxInfo, DashboardData, VMUsageEntry, ProfileData, BoxLLMUsageResponse } from '../api/client'

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

function makeUsageEntry(overrides: Partial<VMUsageEntry> = {}): VMUsageEntry {
  return {
    vm_id: 'vm-123',
    vm_name: 'my-vm',
    disk_provisioned_bytes: 21474836480, // 20 GiB
    disk_avg_bytes: 10737418240,
    bandwidth_bytes: 1073741824, // 1 GiB
    cpu_seconds: 3600,
    io_read_bytes: 524288000,
    io_write_bytes: 524288000,
    days_with_data: 15,
    included_disk_bytes: 10737418240,
    included_bandwidth_bytes: 0,
    overage_disk_bytes: 0,
    overage_bandwidth_bytes: 0,
    display: {
      disk_provisioned: '20 GiB',
      bandwidth: '1.0 GiB',
      included_disk: '10 GiB',
      included_bandwidth: '0 B',
      overage_disk: '',
      overage_bandwidth: '',
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
    inviteCount: 0,
    canRequestInvites: false,
    boxes: [{ name: 'my-vm', status: 'running' }],
    availableRegions: [],
    billingPeriodStart: '2026-04-01T00:00:00Z',
    billingPeriodEnd: '2026-05-01T00:00:00Z',
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
    fetchVMUsage: vi.fn(),
    fetchBoxLLMUsage: vi.fn(),
    fetchProfile: vi.fn(),
    fetchVMLiveMetrics: vi.fn(),
  }
})

import { fetchDashboard, fetchVMUsage, fetchBoxLLMUsage, fetchProfile, fetchVMLiveMetrics } from '../api/client'
const mockFetchDashboard = vi.mocked(fetchDashboard)
const mockFetchVMUsage = vi.mocked(fetchVMUsage)
const mockFetchBoxLLMUsage = vi.mocked(fetchBoxLLMUsage)
const mockFetchProfile = vi.mocked(fetchProfile)
const mockFetchVMLiveMetrics = vi.mocked(fetchVMLiveMetrics)

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
    // Default: usage and profile never resolve (test loading states separately)
    mockFetchVMUsage.mockReturnValue(new Promise(() => {}))
    mockFetchBoxLLMUsage.mockResolvedValue(makeBoxLLMUsage({ models: [], totalCost: '$0.00' }))
    mockFetchProfile.mockReturnValue(new Promise(() => {}))
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

  it('renders subtitle with region, image, and created date', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    const subtitle = wrapper.find('.vm-subtitle').text()
    expect(subtitle).toContain('us-west')
    expect(subtitle).toContain('ubuntu-22.04')
    expect(subtitle).toContain('2024-01-15')
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

  it('hides tags row when no tags', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.tags-row').exists()).toBe(false)
  })

  // --- Action buttons ---

  it('renders HTTPS and Terminal action pills', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    const pills = wrapper.findAll('.action-pill')
    const texts = pills.map(p => p.text())
    expect(texts.some(t => t.includes('HTTPS'))).toBe(true)
    expect(texts.some(t => t.includes('Terminal'))).toBe(true)
  })

  it('shows Shelley button only when shelleyURL is set', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ shelleyURL: 'https://my-vm.exe.cloud/shelley' })],
    }))
    const wrapper = await mountVMDetail()
    const pills = wrapper.findAll('.action-pill')
    expect(pills.some(p => p.text().includes('Shelley'))).toBe(true)
  })

  it('hides Shelley button when shelleyURL is empty', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    const pills = wrapper.findAll('.action-pill')
    expect(pills.some(p => p.text().includes('Shelley'))).toBe(false)
  })

  it('shows Editor button only when vscodeURL is set', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ vscodeURL: 'vscode://vscode-remote/ssh-remote+my-vm/home/exedev' })],
    }))
    const wrapper = await mountVMDetail()
    const pills = wrapper.findAll('.action-pill')
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

  // --- Junk drawer ---

  it('opens junk drawer on ellipsis button click', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.junk-drawer').exists()).toBe(false)
    await wrapper.find('.junk-btn').trigger('click')
    expect(wrapper.find('.junk-drawer').exists()).toBe(true)
  })

  it('hides Share with Team drawer item when hasTeam is false', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({ hasTeam: false }))
    const wrapper = await mountVMDetail()
    await wrapper.find('.junk-btn').trigger('click')
    const items = wrapper.findAll('.drawer-item').map(i => i.text())
    expect(items.some(t => t.includes('Team'))).toBe(false)
  })

  it('shows Share with Team drawer item when hasTeam is true', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({ hasTeam: true }))
    const wrapper = await mountVMDetail()
    await wrapper.find('.junk-btn').trigger('click')
    const items = wrapper.findAll('.drawer-item').map(i => i.text())
    expect(items.some(t => t.includes('Team'))).toBe(true)
  })

  // --- Billing: This Billing Period ---

  it('shows billing period loading spinner', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    // fetchVMUsage never resolves
    mockFetchVMUsage.mockReturnValue(new Promise(() => {}))
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.card-loading').exists()).toBe(true)
  })

  it('renders usage data in billing period card', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMUsage.mockResolvedValue({
      period_start: '2024-01-01',
      period_end: '2024-02-01',
      metrics: [makeUsageEntry()],
    })
    mockFetchProfile.mockResolvedValue(makeProfile())
    const wrapper = await mountVMDetail()
    const text = wrapper.text()
    expect(text).toContain('20 GiB') // disk_provisioned
    expect(text).toContain('1.0 GiB') // bandwidth
    expect(text).toContain('10 GiB') // included_disk
    expect(text).toContain('1.0h') // cpu_seconds = 3600
  })

  it('shows empty state when no usage entry for this VM', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMUsage.mockResolvedValue({
      period_start: '2024-01-01',
      period_end: '2024-02-01',
      metrics: [], // no entry for my-vm
    })
    mockFetchProfile.mockResolvedValue(makeProfile())
    const wrapper = await mountVMDetail()
    expect(wrapper.text()).toContain('No usage data for this period')
  })

  it('shows period label from billing dates', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      billing: { periodStart: '2024-01-01', periodEnd: '2024-02-01' },
    }))
    mockFetchVMUsage.mockResolvedValue({ period_start: '2024-01-01', period_end: '2024-02-01', metrics: [] })
    mockFetchProfile.mockResolvedValue(makeProfile())
    const wrapper = await mountVMDetail()
    expect(wrapper.text()).toContain('January 1')
    expect(wrapper.text()).toContain('February 1')
  })

  it('renders LLM usage with the API period label', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      billing: { periodStart: '2024-03-15', periodEnd: '2024-04-15' },
    }))
    mockFetchVMUsage.mockResolvedValue({ period_start: '2024-03-15', period_end: '2024-04-15', metrics: [] })
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
    mockFetchVMUsage.mockResolvedValue({ period_start: '2024-01-01', period_end: '2024-02-01', metrics: [] })
    mockFetchProfile.mockResolvedValue(makeProfile())
    mockFetchBoxLLMUsage.mockResolvedValue(makeBoxLLMUsage({ models: [], totalCost: '$0.00' }))
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.llm-usage-section').exists()).toBe(false)
  })

  it('logs when VM LLM usage fetch fails', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMUsage.mockResolvedValue({ period_start: '2024-01-01', period_end: '2024-02-01', metrics: [] })
    mockFetchProfile.mockResolvedValue(makeProfile())
    mockFetchBoxLLMUsage.mockRejectedValue(new Error('llm down'))
    await mountVMDetail()
    expect(consoleError).toHaveBeenCalledWith('Failed to load VM LLM usage:', expect.any(Error))
  })

  it('marks disk overage row with overage class', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMUsage.mockResolvedValue({
      period_start: '2024-01-01',
      period_end: '2024-02-01',
      metrics: [makeUsageEntry({
        display: {
          disk_provisioned: '20 GiB',
          bandwidth: '1.0 GiB',
          included_disk: '10 GiB',
          included_bandwidth: '0 B',
          overage_disk: '$2.00',
          overage_bandwidth: '',
        },
      })],
    })
    mockFetchProfile.mockResolvedValue(makeProfile())
    const wrapper = await mountVMDetail()
    const overageRow = wrapper.findAll('.card-row').find(r => r.classes('overage'))
    expect(overageRow).toBeDefined()
    expect(overageRow!.text()).toContain('$2.00')
  })

  // --- Editor modal ---

  it('opens editor modal when Editor button clicked', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ vscodeURL: 'vscode://vscode-remote/ssh-remote+my-vm/home/exedev' })],
    }))
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.modal-overlay').exists()).toBe(false)
    const editorBtn = wrapper.findAll('.action-pill').find(p => p.text().includes('Editor'))
    await editorBtn!.trigger('click')
    expect(wrapper.find('.modal-overlay').exists()).toBe(true)
    expect(wrapper.find('.modal-title').text()).toBe('Open in Editor')
  })

  it('closes editor modal when close button clicked', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ vscodeURL: 'vscode://vscode-remote/ssh-remote+my-vm/home/exedev' })],
    }))
    const wrapper = await mountVMDetail()
    const editorBtn = wrapper.findAll('.action-pill').find(p => p.text().includes('Editor'))
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
    const editorBtn = wrapper.findAll('.action-pill').find(p => p.text().includes('Editor'))
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
    const editorBtn = wrapper.findAll('.action-pill').find(p => p.text().includes('Editor'))
    await editorBtn!.trigger('click')
    const url = wrapper.find('.editor-url').text()
    expect(url).toContain('cursor://vscode-remote/ssh-remote+my-vm')
  })

  // --- Live Metrics ---

  it('shows live metrics section for running VMs', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    mockFetchVMLiveMetrics.mockResolvedValue({
      name: 'my-vm',
      status: 'running',
      cpu_percent: 42.5,
      mem_bytes: 1073741824,
      swap_bytes: 0,
      disk_bytes: 5368709120,
      disk_capacity_bytes: 10737418240,
      net_rx_bytes: 1048576,
      net_tx_bytes: 524288,
    })
    const wrapper = await mountVMDetail()
    expect(wrapper.text()).toContain('Live Metrics')
    expect(wrapper.text()).toContain('42.5%')
  })

  it('does not show live metrics for stopped VMs', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard({
      boxes: [makeBox({ status: 'stopped' })],
    }))
    const wrapper = await mountVMDetail()
    expect(wrapper.text()).not.toContain('Live Metrics')
  })

  // --- Charts placeholder still hidden ---

  it('does not render chart placeholder sections', async () => {
    mockFetchDashboard.mockResolvedValue(makeDashboard())
    const wrapper = await mountVMDetail()
    expect(wrapper.find('.section-placeholder').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Usage History')
  })
})
