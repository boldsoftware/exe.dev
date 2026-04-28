export interface ServerVersion {
  version: string
  commit: string
  date: string
  environment?: string
}

export async function fetchServerVersion(): Promise<ServerVersion> {
  const resp = await fetch('/api/v1/version')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

// Deploy / Inventory types
export interface DeployProcess {
  hostname: string
  dns_name: string
  role: string      // exelet, exeprox, exed
  stage: string     // prod, staging
  region: string
  process: string   // exeletd, cgtop, exed, exeprox
  debug_url: string // link to debug page
  version: string   // git SHA (40 chars) or ""
  version_subject?: string  // commit subject line
  version_date?: string     // commit date (RFC 3339)
  version_url?: string      // github commit URL
  commits_behind: number    // -1 means unknown
  uptime_secs?: number      // process uptime in seconds (0 = unknown)
}

export interface DeployInventory {
  head_sha: string
  head_subject: string
  head_date: string
  processes: DeployProcess[]
}

export async function fetchDeployInventory(): Promise<DeployInventory> {
  const resp = await fetch('/api/v1/deploy/inventory')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export interface DeployStep {
  name: string
  status: string // pending, running, done, failed, cancelled
  started_at?: string
  done_at?: string
  output?: string
}

export interface DeployStatus {
  id: string
  stage: string
  role: string
  process: string
  host: string
  dns_name: string
  sha: string
  initiated_by?: string
  rollout_id?: string
  state: string // pending, running, done, failed, cancelled
  steps: DeployStep[]
  started_at: string
  done_at?: string
  error?: string
}

export interface DeployRequest {
  stage: string
  role: string
  process: string
  host: string
  dns_name: string
  sha: string
}

export interface DeployCommit {
  sha: string
  subject: string
  date: string
}

export async function fetchDeployCommits(from: string, to: string, limit?: number): Promise<DeployCommit[]> {
  const params = new URLSearchParams({ to })
  if (from) params.set('from', from)
  if (limit) params.set('limit', String(limit))
  const resp = await fetch(`/api/v1/deploy/commits?${params}`)
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchDeploys(since?: string): Promise<DeployStatus[]> {
  const params = since ? `?since=${encodeURIComponent(since)}` : '?since=24h'
  const resp = await fetch(`/api/v1/deploys${params}`)
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchDeployStatus(id: string): Promise<DeployStatus> {
  const resp = await fetch(`/api/v1/deploys/${encodeURIComponent(id)}`)
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function cancelDeploy(id: string): Promise<DeployStatus> {
  const resp = await fetch(`/api/v1/deploys/${encodeURIComponent(id)}/cancel`, {
    method: 'POST',
  })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(text || `HTTP ${resp.status}`)
  }
  return resp.json()
}

// Host metrics types
export interface HostMetrics {
  instance: string
  hostname: string
  stage: string
  role: string
  region: string
  up: boolean | null
  cpu_percent: number | null
  memory_pressure: number | null
  cpu_pressure: number | null
  io_pressure: number | null
  data_used_pct: number | null
  swap_used_pct: number | null
}

export async function fetchHosts(): Promise<HostMetrics[]> {
  const resp = await fetch('/api/v1/hosts')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

// Sparkline data: map from instance to pressure time-series.
// Each series is an array of [unix_timestamp, value] pairs.
export interface HostSparklineData {
  cpu_pressure?: [number, number][]
  memory_pressure?: [number, number][]
  io_pressure?: [number, number][]
}

export async function fetchHostSparklines(): Promise<Record<string, HostSparklineData>> {
  const resp = await fetch('/api/v1/hosts/sparklines')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function startDeploy(req: DeployRequest): Promise<DeployStatus> {
  const resp = await fetch('/api/v1/deploys', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(text || `HTTP ${resp.status}`)
  }
  return resp.json()
}

// Rollout types — phased multi-target deploy.

export interface RolloutTarget {
  stage: string
  role: string
  process: string
  host: string
  dns_name: string
  sha: string
  region: string
}

export interface RolloutRequest {
  targets: RolloutTarget[]
  batch_size?: number
  cooldown_secs?: number
  stop_on_failure: boolean
}

export interface WaveTarget {
  process: string
  host: string
  region: string
  stage: string
  deploy_id?: string
}

export interface RolloutWave {
  index: number
  region: string
  state: string // pending, running, done, failed, skipped
  targets: WaveTarget[]
  started_at?: string
  done_at?: string
}

export interface RolloutStatus {
  id: string
  process: string
  sha: string
  state: string // pending, running, cooldown, paused, done, failed, cancelled
  batch_size: number
  cooldown_secs: number
  stop_on_failure: boolean
  started_at: string
  done_at?: string
  cooldown_until?: string
  waves: RolloutWave[]
  current_wave: number
  total: number
  completed: number
  failed: number
  remaining: number
  pause_requested?: boolean
  initiated_by?: string
  error?: string
}

export async function startRollout(req: RolloutRequest): Promise<RolloutStatus> {
  const resp = await fetch('/api/v1/rollouts', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(text || `HTTP ${resp.status}`)
  }
  return resp.json()
}

export async function fetchRollout(id: string): Promise<RolloutStatus> {
  const resp = await fetch(`/api/v1/rollouts/${encodeURIComponent(id)}`)
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function cancelRollout(id: string): Promise<RolloutStatus> {
  const resp = await fetch(`/api/v1/rollouts/${encodeURIComponent(id)}/cancel`, {
    method: 'POST',
  })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(text || `HTTP ${resp.status}`)
  }
  return resp.json()
}

export async function pauseRollout(id: string): Promise<RolloutStatus> {
  const resp = await fetch(`/api/v1/rollouts/${encodeURIComponent(id)}/pause`, {
    method: 'POST',
  })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(text || `HTTP ${resp.status}`)
  }
  return resp.json()
}

export async function resumeRollout(id: string): Promise<RolloutStatus> {
  const resp = await fetch(`/api/v1/rollouts/${encodeURIComponent(id)}/resume`, {
    method: 'POST',
  })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(text || `HTTP ${resp.status}`)
  }
  return resp.json()
}

// Daemon health types
export interface DaemonMetric {
  name: string
  description: string
  grafana_expr?: string
  sparkline?: [number, number][]
  current: number | null
  floor_value: number | null
  unit: string
}

export interface DaemonHealth {
  daemon: string
  metrics: DaemonMetric[]
}

export async function fetchDaemonHealth(signal?: AbortSignal): Promise<DaemonHealth[]> {
  const resp = await fetch('/api/v1/daemons/health', { signal })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

// Per-instance daemon health: map of hostname -> DaemonHealth.
export type InstanceDaemonHealth = Record<string, DaemonHealth>

export async function fetchDaemonHealthInstances(signal?: AbortSignal): Promise<InstanceDaemonHealth> {
  const resp = await fetch('/api/v1/daemons/health/instances', { signal })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export interface DaemonMetricSummary {
  name: string
  current: number | null
  unit: string
}

export interface DaemonSummary {
  daemon: string
  metrics: DaemonMetricSummary[]
}

export async function fetchDaemonSummary(): Promise<DaemonSummary[]> {
  const resp = await fetch('/api/v1/daemons/summary')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

// Continuous Deployment types and functions
export interface ScheduledDeploy {
  sha: string
  deploy_id: string
  started_at: string
  state: string  // success, failed
}

export interface CDStatus {
  enabled: boolean
  deploying: boolean
  disabled_reason?: string
  next_deploy_at?: string
  last_deploy?: ScheduledDeploy
  window_open: boolean
}

export async function fetchCDStatus(): Promise<CDStatus> {
  const resp = await fetch('/api/v1/cd/status')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function enableCD(): Promise<CDStatus> {
  const resp = await fetch('/api/v1/cd/enable', { method: 'POST' })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(text || `HTTP ${resp.status}`)
  }
  return resp.json()
}

export async function disableCD(): Promise<CDStatus> {
  const resp = await fetch('/api/v1/cd/disable', { method: 'POST' })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(text || `HTTP ${resp.status}`)
  }
  return resp.json()
}
