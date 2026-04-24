import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ResizeDiskModal from '../components/ResizeDiskModal.vue'

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    fetchVMLiveMetrics: vi.fn(),
    fetchProfile: vi.fn(),
    runCommand: vi.fn(),
  }
})

import { fetchVMLiveMetrics, fetchProfile, runCommand } from '../api/client'
const mockFetchVMLiveMetrics = vi.mocked(fetchVMLiveMetrics)
const mockFetchProfile = vi.mocked(fetchProfile)
const mockRunCommand = vi.mocked(runCommand)

const GIB = 1024 * 1024 * 1024

function baseProfile(overrides: any = {}) {
  return {
    user: { email: 'x@y', region: 'us', regionDisplay: 'US', newsletterSubscribed: false },
    sshKeys: [], passkeys: [], siteSessions: [], sharedBoxes: [],
    teamInfo: null, pendingTeamInvites: [], canEnableTeam: false,
    credits: { balance: '$0', purchases: [], monthlyIncluded: '$0', planName: '', nextBillingDate: null },
    planCapacity: {
      maxCPUs: 8, maxMemoryGB: 16, maxVMs: 5, defaultDiskGB: 30,
      maxDiskGB: 150, bandwidthGB: 100, tierName: 'small', poolSize: '',
      monthlyPriceCents: 0,
    },
    basicUser: false, showIntegrations: false,
    ...overrides,
  } as any
}

beforeEach(() => {
  vi.clearAllMocks()
  mockFetchVMLiveMetrics.mockResolvedValue({
    name: 'my-vm', status: 'running', cpu_percent: 0, mem_bytes: 0, swap_bytes: 0,
    disk_bytes: 0, disk_logical_bytes: 0,
    disk_capacity_bytes: 30 * GIB,
    mem_capacity_bytes: 0, cpus: 2, net_rx_bytes: 0, net_tx_bytes: 0,
  })
  mockFetchProfile.mockResolvedValue(baseProfile())
})

async function mountModal(props: any = {}) {
  const wrapper = mount(ResizeDiskModal, {
    props: { visible: true, boxName: 'my-vm', ...props },
  })
  await flushPromises()
  return wrapper
}

describe('ResizeDiskModal', () => {
  it('loads current size and shows a resize command', async () => {
    const wrapper = await mountModal()
    expect(wrapper.text()).toContain('Current size:')
    expect(wrapper.text()).toContain('30 GiB')
    const cmdBox = wrapper.find('.cmd-display code').text()
    expect(cmdBox).toMatch(/^resize my-vm --disk=\d+GiB$/)
    // Default target should be current+10, clamped inside slider range.
    expect(cmdBox).toContain('--disk=40GiB')
  })

  it('clamps slider to at most +250 GiB per operation', async () => {
    mockFetchProfile.mockResolvedValue(baseProfile({
      planCapacity: {
        maxCPUs: 8, maxMemoryGB: 16, maxVMs: 5, defaultDiskGB: 30,
        maxDiskGB: 1000, bandwidthGB: 100, tierName: 'big', poolSize: '',
        monthlyPriceCents: 0,
      },
    }))
    const wrapper = await mountModal()
    const slider = wrapper.find('input[type="range"]')
    expect((slider.element as HTMLInputElement).max).toBe('280') // 30 + 250
    expect(wrapper.text()).toContain('up to +250 GiB per operation')
  })

  it('shows an error when disk is already at plan max', async () => {
    mockFetchVMLiveMetrics.mockResolvedValue({
      name: 'my-vm', status: 'running', cpu_percent: 0, mem_bytes: 0, swap_bytes: 0,
      disk_bytes: 0, disk_logical_bytes: 0,
      disk_capacity_bytes: 150 * GIB,
      mem_capacity_bytes: 0, cpus: 2, net_rx_bytes: 0, net_tx_bytes: 0,
    })
    const wrapper = await mountModal()
    expect(wrapper.text()).toMatch(/already at your plan's maximum/i)
    expect(wrapper.find('input[type="range"]').exists()).toBe(false)
  })

  it('shows an error when the plan does not allow disk resize', async () => {
    mockFetchProfile.mockResolvedValue(baseProfile({
      planCapacity: {
        maxCPUs: 2, maxMemoryGB: 2, maxVMs: 1, defaultDiskGB: 30,
        maxDiskGB: 0, bandwidthGB: 10, tierName: 'basic', poolSize: '',
        monthlyPriceCents: 0,
      },
    }))
    const wrapper = await mountModal()
    expect(wrapper.text()).toMatch(/not available on your current plan/i)
  })

  it('runs the resize command and emits success', async () => {
    mockRunCommand.mockResolvedValue({ success: true, output: 'Disk grown: 30 GiB -> 40 GiB' })
    const wrapper = await mountModal()
    await wrapper.find('.btn-primary').trigger('click')
    await flushPromises()
    expect(mockRunCommand).toHaveBeenCalledWith('resize my-vm --disk=40GiB')
    expect(wrapper.emitted('success')).toBeTruthy()
    expect(wrapper.text()).toContain('Disk grown: 30 GiB -> 40 GiB')
  })

  it('emits close when cancel is clicked', async () => {
    const wrapper = await mountModal()
    await wrapper.find('.btn-secondary').trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
  })
})
