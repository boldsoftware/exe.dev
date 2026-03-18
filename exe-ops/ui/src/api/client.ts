export interface ServerSummary {
  name: string
  hostname: string
  role: string
  region: string
  env: string
  instance: string
  tags: string[]
  last_seen: string
  agent_version?: string
  arch?: string
  upgrade_available?: boolean
  instances?: number
  capacity?: number
  cpu_percent: number
  mem_total: number
  mem_used: number
  mem_swap: number
  mem_swap_total: number
  disk_total: number
  disk_used: number
  net_send: number
  net_recv: number
  components?: Component[]
}

export interface Component {
  name: string
  version: string
  status: string
}

export interface ReportRow {
  timestamp: string
  cpu_percent: number
  mem_used: number
  disk_used: number
  net_send: number
  net_recv: number
  uptime_secs: number
}

export interface ZFSPool {
  name: string
  health: string
  used: number
  free: number
  frag_pct: number
  cap_pct: number
  read_errors: number
  write_errors: number
  cksum_errors: number
}

export interface ExeletCapacityRow {
  timestamp: string
  instances: number
  capacity: number
}

export interface ServerDetail extends ServerSummary {
  mem_free: number
  mem_swap: number
  mem_swap_total: number
  disk_free: number
  zfs_used?: number
  zfs_free?: number
  backup_zfs_used?: number
  backup_zfs_free?: number
  uptime_secs: number
  components?: Component[]
  updates?: string[]
  failed_units?: string[]
  first_seen: string
  history?: ReportRow[]
  load_avg_1: number
  load_avg_5: number
  load_avg_15: number
  zfs_pool_health?: string
  zfs_arc_size?: number
  zfs_arc_hit_rate?: number
  net_rx_errors: number
  net_rx_dropped: number
  net_tx_errors: number
  net_tx_dropped: number
  conntrack_count?: number
  conntrack_max?: number
  fd_allocated: number
  fd_max: number
  zfs_pools?: ZFSPool[]
  exelet_capacity?: ExeletCapacityRow[]
}

export interface FleetServer {
  name: string
  hostname: string
  role: string
  region: string
  env: string
  last_seen: string
  agent_version?: string
  cpu_percent: number
  mem_total: number
  mem_used: number
  disk_total: number
  disk_used: number
  conntrack_count?: number
  conntrack_max?: number
  fd_allocated: number
  fd_max: number
  components?: Component[]
  updates?: string[]
  failed_units?: string[]
  zfs_pools?: ZFSPool[]
  zfs_pool_health?: string
  zfs_arc_size?: number
  zfs_arc_hit_rate?: number
  net_rx_errors: number
  net_tx_errors: number
  net_rx_dropped: number
  net_tx_dropped: number
}

export interface ServerVersion {
  version: string
  commit: string
  date: string
}

export interface CustomAlertRule {
  id: number
  name: string
  metric: string
  operator: string
  threshold: number
  severity: 'warning' | 'critical'
  enabled: boolean
  created_at?: string
}

export interface ExeletCapacitySummary {
  total_instances: number
  total_capacity: number
}

export interface StatusEvent {
  name: string
  online: boolean
}

export interface ReportEvent {
  name: string
  cpu_percent: number
  mem_total: number
  mem_used: number
  disk_total: number
  disk_used: number
  net_send: number
  net_recv: number
}

export async function fetchExeletCapacitySummary(env?: string, region?: string): Promise<ExeletCapacitySummary> {
  const params = new URLSearchParams()
  if (env) params.set('env', env)
  if (region) params.set('region', region)
  const qs = params.toString()
  const resp = await fetch(`/api/v1/exelet-capacity-summary${qs ? '?' + qs : ''}`)
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchFleet(): Promise<FleetServer[]> {
  const resp = await fetch('/api/v1/fleet')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchServers(): Promise<ServerSummary[]> {
  const resp = await fetch('/api/v1/servers')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchServer(name: string): Promise<ServerDetail> {
  const resp = await fetch(`/api/v1/servers/${encodeURIComponent(name)}`)
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchServerVersion(): Promise<ServerVersion> {
  const resp = await fetch('/api/v1/version')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function deleteServer(name: string): Promise<void> {
  const resp = await fetch(`/api/v1/servers/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
}

export async function resetNetCounters(name: string): Promise<void> {
  const resp = await fetch(`/api/v1/servers/${encodeURIComponent(name)}/reset-net-counters`, {
    method: 'POST',
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
}

export async function triggerUpgrade(name: string): Promise<void> {
  const resp = await fetch(`/api/v1/servers/${encodeURIComponent(name)}/upgrade`, {
    method: 'POST',
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
}

export async function fetchCustomAlerts(): Promise<CustomAlertRule[]> {
  const resp = await fetch('/api/v1/custom-alerts')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function createCustomAlert(rule: Omit<CustomAlertRule, 'id' | 'created_at'>): Promise<CustomAlertRule> {
  const resp = await fetch('/api/v1/custom-alerts', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(rule),
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function updateCustomAlert(rule: CustomAlertRule): Promise<CustomAlertRule> {
  const resp = await fetch(`/api/v1/custom-alerts/${rule.id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(rule),
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function deleteCustomAlert(id: number): Promise<void> {
  const resp = await fetch(`/api/v1/custom-alerts/${id}`, {
    method: 'DELETE',
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
}

// Chat / AI Agent types and functions

export interface Conversation {
  id: string
  title: string
  created_at: string
  updated_at: string
}

export interface ChatMessage {
  id: number
  conversation_id: string
  role: string
  content: string
  created_at: string
}

export interface ChatConfig {
  provider: string
  model: string
}

export async function fetchChatConfig(): Promise<ChatConfig> {
  const resp = await fetch('/api/v1/chat/config')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchConversations(): Promise<Conversation[]> {
  const resp = await fetch('/api/v1/chat/conversations')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchChatMessages(conversationId: string): Promise<ChatMessage[]> {
  const resp = await fetch(`/api/v1/chat/messages?conversation_id=${encodeURIComponent(conversationId)}`)
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function renameConversation(id: string, title: string): Promise<void> {
  const resp = await fetch(`/api/v1/chat/conversations/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
}

export async function deleteConversation(id: string): Promise<void> {
  const resp = await fetch(`/api/v1/chat/conversations/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
}

export interface ChatStreamCallbacks {
  onConversation: (id: string) => void
  onDelta: (text: string) => void
  onTitle: (title: string) => void
  onDone: () => void
  onError: (error: string) => void
}

export function sendChatMessage(
  message: string,
  conversationId: string | null,
  callbacks: ChatStreamCallbacks,
): AbortController {
  const controller = new AbortController()
  const body: Record<string, string> = { message }
  if (conversationId) {
    body.conversation_id = conversationId
  }

  fetch('/api/v1/chat/send', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: controller.signal,
  }).then(async (resp) => {
    if (!resp.ok) {
      const text = await resp.text()
      callbacks.onError(text || `HTTP ${resp.status}`)
      return
    }
    const reader = resp.body?.getReader()
    if (!reader) {
      callbacks.onError('No response body')
      return
    }
    const decoder = new TextDecoder()
    let buffer = ''
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''
      let eventType = ''
      for (const line of lines) {
        if (line.startsWith('event: ')) {
          eventType = line.slice(7)
        } else if (line.startsWith('data: ')) {
          const data = line.slice(6)
          try {
            const parsed = JSON.parse(data)
            switch (eventType) {
              case 'conversation':
                callbacks.onConversation(parsed.id)
                break
              case 'delta':
                callbacks.onDelta(parsed.text)
                break
              case 'title':
                callbacks.onTitle(parsed.title)
                break
              case 'done':
                callbacks.onDone()
                break
              case 'error':
                callbacks.onError(parsed.error || 'Unknown error')
                break
            }
          } catch {
            // ignore parse errors
          }
        }
      }
    }
  }).catch((err) => {
    if (err.name !== 'AbortError') {
      callbacks.onError(err.message)
    }
  })

  return controller
}
