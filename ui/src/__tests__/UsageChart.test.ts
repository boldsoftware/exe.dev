import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import UsageChart from '../components/UsageChart.vue'
import * as client from '../api/client'

// Stub the PrimeVue Chart component — we test data flow, not Chart.js rendering
const ChartStub = {
  name: 'Chart',
  props: ['type', 'data', 'options'],
  template: '<div class="chart-stub" :data-type="type"></div>',
}

// Sample compute-usage data (chart-ready from backend)
const sampleComputeUsageData: client.VMComputeUsagePoint[] = [
  {
    timestamp: '2024-01-01T00:00:00Z',
    cpu_cores: 0,
    cpu_nominal: 2,
    memory_bytes: 1073741824,
    disk_used_bytes: 5368709120,
    disk_capacity_bytes: 10737418240,
    net_rx_bytes_per_sec: 0,
    net_tx_bytes_per_sec: 0,
  },
  {
    timestamp: '2024-01-01T01:00:00Z',
    cpu_cores: 0.35,
    cpu_nominal: 2,
    memory_bytes: 1200000000,
    disk_used_bytes: 5500000000,
    disk_capacity_bytes: 10737418240,
    net_rx_bytes_per_sec: 278,
    net_tx_bytes_per_sec: 150,
  },
  {
    timestamp: '2024-01-01T02:00:00Z',
    cpu_cores: 0.72,
    cpu_nominal: 2,
    memory_bytes: 1300000000,
    disk_used_bytes: 5600000000,
    disk_capacity_bytes: 10737418240,
    net_rx_bytes_per_sec: 500,
    net_tx_bytes_per_sec: 200,
  },
]

const mountChart = (overrides: Record<string, unknown> = {}) => {
  return mount(UsageChart, {
    props: {
      vmName: 'test-vm',
      vmStatus: 'running',
      ...overrides,
    },
    global: {
      stubs: { Chart: ChartStub },
    },
  })
}

describe('UsageChart', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('renders all metric tabs', () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue([])
    const wrapper = mountChart()
    const tabs = wrapper.findAll('.metric-tab')
    expect(tabs).toHaveLength(3)
    expect(tabs.map((t) => t.text())).toEqual(['CPU', 'Disk', 'Network'])
  })

  it('renders time range selector buttons', () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue([])
    const wrapper = mountChart()
    const tabs = wrapper.findAll('.time-tab')
    expect(tabs).toHaveLength(3)
    expect(tabs.map((t) => t.text())).toEqual(['24h', '7d', '30d'])
  })

  it('fetches data with default hours=24', async () => {
    const fetchSpy = vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue(sampleComputeUsageData)
    mountChart()
    await flushPromises()
    expect(fetchSpy).toHaveBeenCalledWith('test-vm', 24)
  })

  it('fetches data with selected hours on time range change', async () => {
    const fetchSpy = vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue(sampleComputeUsageData)
    const wrapper = mountChart()
    await flushPromises()
    fetchSpy.mockClear()

    await wrapper.findAll('.time-tab')[1].trigger('click')
    await flushPromises()
    expect(fetchSpy).toHaveBeenCalledWith('test-vm', 168)
  })

  it('shows empty state when no data', async () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue([])
    const wrapper = mountChart()
    await flushPromises()
    expect(wrapper.find('.chart-empty').exists()).toBe(true)
    expect(wrapper.find('.chart-empty').text()).toContain('No data available')
  })

  it('shows error state on fetch failure', async () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockRejectedValue(new Error('Network error'))
    const wrapper = mountChart()
    await flushPromises()
    expect(wrapper.find('.chart-error').exists()).toBe(true)
    expect(wrapper.find('.chart-error').text()).toContain('Network error')
  })

  it('shows loading spinner while fetching', async () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockReturnValue(new Promise(() => {}))
    const wrapper = mountChart()
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.chart-loading').exists()).toBe(true)
    expect(wrapper.find('.spinner').exists()).toBe(true)
    expect(wrapper.find('.chart-stub').exists()).toBe(false)
  })

  it('renders Chart component with data', async () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue(sampleComputeUsageData)
    const wrapper = mountChart()
    await flushPromises()
    expect(wrapper.find('.chart-stub').exists()).toBe(true)
    expect(wrapper.find('.chart-stub').attributes('data-type')).toBe('line')
  })

  it('passes CPU data as cores with provisioned line', async () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue(sampleComputeUsageData)
    const wrapper = mountChart()
    await flushPromises()

    const chartComp = wrapper.findComponent(ChartStub)
    const data = chartComp.props('data')
    expect(data.datasets).toHaveLength(2)
    expect(data.datasets[0].label).toBe('Used')
    expect(data.datasets[0].fill).toBe(true)
    expect(data.datasets[0].data).toEqual([0, 0.35, 0.72])
    expect(data.datasets[1].label).toBe('Provisioned')
    expect(data.datasets[1].borderDash).toEqual([5, 5])
    expect(data.datasets[1].data).toEqual([2, 2, 2])
  })

  it('passes Network data as two datasets (rx/tx) without fill', async () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue(sampleComputeUsageData)
    const wrapper = mountChart()
    await flushPromises()

    await wrapper.findAll('.metric-tab')[2].trigger('click')
    await flushPromises()

    const chartComp = wrapper.findComponent(ChartStub)
    const data = chartComp.props('data')
    expect(data.datasets).toHaveLength(2)
    expect(data.datasets[0].label).toBe('rx')
    expect(data.datasets[0].fill).toBe(false)
    expect(data.datasets[0].data).toEqual([0, 278, 500])
    expect(data.datasets[1].label).toBe('tx')
    expect(data.datasets[1].fill).toBe(false)
    expect(data.datasets[1].data).toEqual([0, 150, 200])
  })

  it('passes Disk data as provisioned capacity dataset', async () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue(sampleComputeUsageData)
    const wrapper = mountChart()
    await flushPromises()

    await wrapper.findAll('.metric-tab')[1].trigger('click')
    await flushPromises()

    const chartComp = wrapper.findComponent(ChartStub)
    const data = chartComp.props('data')
    expect(data.datasets).toHaveLength(1)
    expect(data.datasets[0].label).toBe('Provisioned')
    expect(data.datasets[0].fill).toBe(true)
  })

  it('loads data once on mount without polling', async () => {
    const fetchSpy = vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue(sampleComputeUsageData)
    mountChart()
    await flushPromises()
    expect(fetchSpy).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(120000)
    await flushPromises()
    expect(fetchSpy).toHaveBeenCalledTimes(1)
  })

  it('switches metric tabs correctly', async () => {
    vi.spyOn(client, 'fetchVMComputeUsage').mockResolvedValue(sampleComputeUsageData)
    const wrapper = mountChart()
    await flushPromises()

    let tabs = wrapper.findAll('.metric-tab')
    expect(tabs[0].classes()).toContain('active')
    expect(tabs[1].classes()).not.toContain('active')

    await tabs[1].trigger('click')
    tabs = wrapper.findAll('.metric-tab')
    expect(tabs[0].classes()).not.toContain('active')
    expect(tabs[1].classes()).toContain('active')
  })
})
