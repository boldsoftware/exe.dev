export interface ServerVersion {
  version: string
  commit: string
  date: string
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
  status: string // pending, running, done, failed
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
  state: string // pending, running, done, failed
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

export async function fetchDeployCommits(from: string, to: string): Promise<DeployCommit[]> {
  const params = new URLSearchParams({ to })
  if (from) params.set('from', from)
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
}

export async function fetchHosts(): Promise<HostMetrics[]> {
  const resp = await fetch('/api/v1/hosts')
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
  state: string // pending, running, cooldown, done, failed, cancelled
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
