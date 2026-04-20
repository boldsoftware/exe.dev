import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import LiveMetrics from '../components/LiveMetrics.vue'
import type { VMLiveMetrics } from '../api/client'

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    fetchVMLiveMetrics: vi.fn(),
  }
})

import { fetchVMLiveMetrics } from '../api/client'
const mockFetch = vi.mocked(fetchVMLiveMetrics)

function makeMetrics(overrides: Partial<VMLiveMetrics> = {}): VMLiveMetrics {
  return {
    name: 'test-vm',
    status: 'running',
    cpu_percent: 23.4,
    mem_bytes: 2 * 1024 * 1024 * 1024,          // 2 GiB
    swap_bytes: 0,
    disk_bytes: 3 * 1024 * 1024 * 1024,          // 3 GiB compressed
    disk_logical_bytes: 5 * 1024 * 1024 * 1024,  // 5 GiB logical (matches df -h)
    disk_capacity_bytes: 25 * 1024 * 1024 * 1024, // 25 GiB
    mem_capacity_bytes: 4 * 1024 * 1024 * 1024,  // 4 GiB allocated
    cpus: 4,
    net_rx_bytes: 1.5 * 1024 * 1024 * 1024,      // 1.5 GiB
    net_tx_bytes: 256 * 1024 * 1024,             // 256 MiB
    ...overrides,
  }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('LiveMetrics', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders all 5 metric cards', async () => {
    mockFetch.mockResolvedValue(makeMetrics())
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const cards = wrapper.findAll('.metric-card')
    expect(cards).toHaveLength(5)
    const labels = cards.map(c => c.find('.mt').text())
    expect(labels).toEqual(['CPU', 'Memory', 'Disk', 'Net ↓', 'Net ↑'])
  })

  it('displays CPU percentage', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ cpu_percent: 75.3 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const cpuCard = wrapper.findAll('.metric-card')[0]
    expect(cpuCard.find('.mv').text()).toBe('75.3%')
  })

  it('shows vCPU count in CPU subtitle', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ cpus: 4 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const cpuCard = wrapper.findAll('.metric-card')[0]
    expect(cpuCard.find('.ms').text()).toBe('of 4 vCPUs')
  })

  it('shows singular vCPU for 1 core', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ cpus: 1 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const cpuCard = wrapper.findAll('.metric-card')[0]
    expect(cpuCard.find('.ms').text()).toBe('of 1 vCPU')
  })

  it('falls back to generic CPU subtitle when cpus is 0', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ cpus: 0 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const cpuCard = wrapper.findAll('.metric-card')[0]
    expect(cpuCard.find('.ms').text()).toBe('of CPU capacity')
  })

  it('shows allocated memory capacity (no progress bar)', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ mem_capacity_bytes: 4 * 1024 * 1024 * 1024 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const memCard = wrapper.findAll('.metric-card')[1]
    // Should show capacity, not cgroup memory.current
    expect(memCard.find('.mv').text()).toBe('4.0 GB')
    expect(memCard.find('.ms').text()).toBe('allocated')
    // No progress bar for memory
    expect(memCard.find('.mb-fill').exists()).toBe(false)
  })

  it('shows dash when no memory capacity', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ mem_capacity_bytes: 0 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const memCard = wrapper.findAll('.metric-card')[1]
    expect(memCard.find('.mv').text()).toBe('—')
  })

  it('displays logical disk usage with capacity subtitle', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ disk_bytes: 3 * 1024 * 1024 * 1024, disk_logical_bytes: 5 * 1024 * 1024 * 1024, disk_capacity_bytes: 25 * 1024 * 1024 * 1024 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const diskCard = wrapper.findAll('.metric-card')[2]
    // Should show logical bytes (5 GiB), not compressed (3 GiB)
    expect(diskCard.find('.mv').text()).toBe('5.0 GB')
    expect(diskCard.find('.ms').text()).toContain('25.0 GB capacity')
  })

  it('falls back to compressed disk_bytes when disk_logical_bytes is 0', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ disk_bytes: 3 * 1024 * 1024 * 1024, disk_logical_bytes: 0, disk_capacity_bytes: 25 * 1024 * 1024 * 1024 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const diskCard = wrapper.findAll('.metric-card')[2]
    expect(diskCard.find('.mv').text()).toBe('3.0 GB')
  })

  it('shows dash on first poll for network (no rate yet)', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ net_rx_bytes: 1.5 * 1024 * 1024 * 1024, net_tx_bytes: 256 * 1024 * 1024 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const rxCard = wrapper.findAll('.metric-card')[3]
    const txCard = wrapper.findAll('.metric-card')[4]
    // First poll: no rate yet, show dash
    expect(rxCard.find('.mv').text()).toBe('—')
    expect(txCard.find('.mv').text()).toBe('—')
    // No subtitle until rate is available
    expect(rxCard.find('.ms').text()).toBe('')
    expect(txCard.find('.ms').text()).toBe('')
  })

  it('shows network rate after second poll', async () => {
    // First poll
    mockFetch.mockResolvedValue(makeMetrics({ net_rx_bytes: 1_000_000, net_tx_bytes: 500_000 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()

    // Advance time and trigger second poll
    mockFetch.mockResolvedValue(makeMetrics({ net_rx_bytes: 2_000_000, net_tx_bytes: 1_000_000 }))
    vi.advanceTimersByTime(5000)
    await flushPromises()

    const rxCard = wrapper.findAll('.metric-card')[3]
    // Should show a rate now (Mbps/Kbps/bps), not raw bytes
    expect(rxCard.find('.mv').text()).toMatch(/bps/)
  })

  it('shows rate labels in subtitle after second poll', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ net_rx_bytes: 1_000_000, net_tx_bytes: 500_000 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()

    // Second poll
    mockFetch.mockResolvedValue(makeMetrics({ net_rx_bytes: 2_000_000, net_tx_bytes: 1_000_000 }))
    vi.advanceTimersByTime(5000)
    await flushPromises()

    const rxCard = wrapper.findAll('.metric-card')[3]
    const txCard = wrapper.findAll('.metric-card')[4]
    expect(rxCard.find('.ms').text()).toBe('receive rate')
    expect(txCard.find('.ms').text()).toBe('send rate')
  })

  it('does not poll when VM is stopped', async () => {
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'stopped' } })
    await flushPromises()
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it('starts polling when status changes to running', async () => {
    mockFetch.mockResolvedValue(makeMetrics())
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'stopped' } })
    await flushPromises()
    expect(mockFetch).not.toHaveBeenCalled()

    await wrapper.setProps({ vmStatus: 'running' })
    await flushPromises()
    expect(mockFetch).toHaveBeenCalledWith('test-vm')
  })

  it('stops polling when status changes to stopped', async () => {
    mockFetch.mockResolvedValue(makeMetrics())
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    expect(mockFetch).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ vmStatus: 'stopped' })
    vi.advanceTimersByTime(10000)
    await flushPromises()
    // Should not have polled again after stopping
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  it('resets and re-polls when vmName changes', async () => {
    mockFetch.mockResolvedValue(makeMetrics())
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    expect(mockFetch).toHaveBeenCalledWith('test-vm')

    mockFetch.mockResolvedValue(makeMetrics({ name: 'other-vm' }))
    await wrapper.setProps({ vmName: 'other-vm' })
    await flushPromises()
    expect(mockFetch).toHaveBeenCalledWith('other-vm')
  })

  it('shows error when fetch fails and no prior data', async () => {
    mockFetch.mockRejectedValue(new Error('Network error'))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    expect(wrapper.find('.metrics-error').exists()).toBe(true)
    expect(wrapper.text()).toContain('Unable to load metrics')
  })

  it('keeps existing data on transient fetch failure', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ cpu_percent: 50.0 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    expect(wrapper.findAll('.metric-card')[0].find('.mv').text()).toBe('50.0%')

    // Second poll fails
    mockFetch.mockRejectedValue(new Error('Timeout'))
    vi.advanceTimersByTime(5000)
    await flushPromises()

    // Data should still be showing
    expect(wrapper.find('.metrics-error').exists()).toBe(false)
    expect(wrapper.findAll('.metric-card')[0].find('.mv').text()).toBe('50.0%')
  })

  it('shows static refresh interval text', async () => {
    mockFetch.mockResolvedValue(makeMetrics())
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    expect(wrapper.text()).toContain('refreshes every 5s')
  })

  it('polls at 5-second intervals', async () => {
    mockFetch.mockResolvedValue(makeMetrics())
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    expect(mockFetch).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(5000)
    await flushPromises()
    expect(mockFetch).toHaveBeenCalledTimes(2)

    vi.advanceTimersByTime(5000)
    await flushPromises()
    expect(mockFetch).toHaveBeenCalledTimes(3)
  })

  it('handles zero values gracefully', async () => {
    mockFetch.mockResolvedValue(makeMetrics({
      cpu_percent: 0,
      mem_bytes: 0,
      mem_capacity_bytes: 0,
      cpus: 0,
      disk_bytes: 0,
      disk_logical_bytes: 0,
      disk_capacity_bytes: 0,
      net_rx_bytes: 0,
      net_tx_bytes: 0,
    }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const cpuCard = wrapper.findAll('.metric-card')[0]
    expect(cpuCard.find('.mv').text()).toBe('0.0%')
    // Should not throw or show NaN
    expect(wrapper.text()).not.toContain('NaN')
    expect(wrapper.text()).not.toContain('undefined')
  })

  it('caps CPU bar at 100%', async () => {
    mockFetch.mockResolvedValue(makeMetrics({ cpu_percent: 250 }))
    const wrapper = mount(LiveMetrics, { props: { vmName: 'test-vm', vmStatus: 'running' } })
    await flushPromises()
    const cpuBar = wrapper.findAll('.metric-card')[0].find('.mb-fill')
    // Bar width should be capped at 100%, but the display should show the real value
    expect(cpuBar.attributes('style')).toContain('width: 100%')
    expect(wrapper.findAll('.metric-card')[0].find('.mv').text()).toBe('250.0%')
  })
})
