// API client for exe.dev dashboard
// Uses the existing /cmd endpoint which proxies SSH commands
import { ref } from 'vue'

// Shared reactive auth state. Updated as a side effect of any API call.
// Starts true (optimistic) and flips to false on 401/403.
export const isAuthenticated = ref(true)

export interface CmdResult {
  success: boolean
  output?: string
  error?: string
}

export async function runCommand(command: string): Promise<CmdResult> {
  const resp = await fetch('/cmd', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ command }),
  })
  if (resp.status === 401 || resp.status === 403) {
    isAuthenticated.value = false
    window.location.href = '/auth'
    throw new Error('Session expired')
  }
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

// Shell-quote a string for safe command building
export function shellQuote(s: string): string {
  if (s.length > 0 && !/[^a-zA-Z0-9_./:@=-]/.test(s)) {
    return s
  }
  return "'" + s.replace(/'/g, "'\\''") + "'"
}

// --- Data types matching Go server structs ---

export interface BoxInfo {
  name: string
  status: string // running, stopped, creating, error
  image: string
  region: string
  createdAt: string
  updatedAt: string
  sshCommand: string
  proxyURL: string
  terminalURL: string
  shelleyURL: string
  vscodeURL: string
  proxyPort: number
  proxyShare: string // public, private
  routeKnown: boolean
  sharedUserCount: number
  shareLinkCount: number
  totalShareCount: number
  sharedEmails: string[]
  shareLinks: { token: string; url: string }[]
  displayTags: string[]
  hasCreationLog: boolean
  isTeamShared: boolean
  emoji: string
}

export interface SharedBoxInfo {
  name: string
  ownerEmail: string
  proxyURL: string
}

export interface TeamBoxInfo {
  name: string
  creatorEmail: string
  status: string
  proxyURL: string
  sshCommand: string
  displayTags: string[]
}

export interface TeamSharedBoxInfo {
  name: string
  ownerEmail: string
  status: string
  proxyURL: string
  sshCommand: string
  displayTags: string[]
}

export interface SSHKeyInfo {
  publicKey: string
  comment: string
  fingerprint: string
  addedAt: string | null
  lastUsedAt: string | null
  integrationId?: string | null
  apiKeyHint?: string | null
}

export interface PasskeyInfo {
  id: number
  name: string
  createdAt: string
  lastUsedAt: string
}

export interface SiteSessionInfo {
  domain: string
  url: string
  lastUsedAt: string | null
}

export interface IntegrationInfo {
  name: string
  type: string // github, http-proxy
  target: string
  hasHeader: boolean
  hasBasicAuth: boolean
  repositories: string[]
  attachments: string[]
  isTeam: boolean
  peerVM?: string
}

export interface GitHubAccountInfo {
  githubLogin: string
  targetLogin: string
  installationID: number
}

export interface TeamInfo {
  displayName: string
  role: string
  isAdmin: boolean
  isBillingOwner: boolean
  onlyMember: boolean
  members: TeamMemberInfo[]
  billingAdmins: string[]
  boxCount: number
  maxBoxes: number
}

export interface TeamMemberInfo {
  email: string
  role: string
  joinedAt: string | null
}

export interface PendingTeamInvite {
  token: string
  teamName: string
  invitedBy: string
  vmCount: number
}

export interface PaymentMethodInfo {
  type: string
  brand?: string
  last4?: string
  expMonth?: number
  expYear?: number
  email?: string
  displayLabel: string
}

export interface CreditInfo {
  planName: string
  selfServeBilling: boolean
  paidPlan: boolean
  skipBilling: boolean
  billingStatus: string
  shelleyCreditsAvailable: number
  shelleyCreditsMax: number
  extraCreditsUSD: number
  totalCreditsUSD: number
  totalRemainingPct: number
  monthlyAvailableUSD: number
  monthlyUsedUSD: number
  monthlyUsedPct: number
  usedCreditsUSD: number
  totalCapacityUSD: number
  usedBarPct: number
  hasShelleyFreeCreditPct: boolean
  monthlyCreditsResetAt: string
  ledgerBalanceUSD: number
  purchases: { amount: string; date: string; receiptURL: string }[]
  gifts: { amount: string; reason: string; date: string }[]
  invoices: { description: string; planName: string; periodStart: string; periodEnd: string; date: string; amount: string; subtotal: string; creditApplied: string; creditGenerated: string; status: string; hostedInvoiceURL: string; invoicePDF: string }[]
  creditBalanceUSD: number
  paymentMethod: PaymentMethodInfo | null
  paymentMethodManagedByTeam: boolean
}

export interface UserInfo {
  email: string
  region: string
  regionDisplay: string
  newsletterSubscribed: boolean
}

// Full page data as served by the Go backend
export interface TrialInfo {
  expiresAt: string
  daysLeft: number
  expired: boolean
}

export interface DashboardData {
  user: UserInfo
  boxes: BoxInfo[]
  sharedBoxes: SharedBoxInfo[]
  teamSharedBoxes: TeamSharedBoxInfo[]
  teamBoxes: TeamBoxInfo[]
  hasTeam: boolean
  inviteCount: number
  canRequestInvites: boolean
  sshCommand: string
  replHost: string
  showIntegrations: boolean
  billing: {
    periodStart: string
    periodEnd: string
  }
  trial?: TrialInfo
}

export interface RegionOption {
  code: string
  display: string
}

export interface PlanCapacity {
  maxCPUs: number
  maxMemoryGB: number
  maxVMs: number
  defaultDiskGB: number
  maxDiskGB: number
  bandwidthGB: number
  tierName: string
  poolSize: string
  monthlyPriceCents: number
  nextTier?: {
    poolSize: string
    monthlyPriceCents: number
  }
}

export interface ProfileData {
  user: UserInfo
  sshKeys: SSHKeyInfo[]
  passkeys: PasskeyInfo[]
  siteSessions: SiteSessionInfo[]
  sharedBoxes: SharedBoxInfo[]
  teamInfo: TeamInfo | null
  pendingTeamInvites: PendingTeamInvite[]
  canEnableTeam: boolean
  credits: CreditInfo
  planCapacity?: PlanCapacity
  basicUser: boolean
  showIntegrations: boolean
  inviteCount: number
  canRequestInvites: boolean
  boxes: { name: string; status: string }[]
  availableRegions: RegionOption[]
  billingPeriodStart: string
  billingPeriodEnd: string
  trial?: TrialInfo
}

export interface IntegrationsData {
  integrations: IntegrationInfo[]
  githubIntegrations: IntegrationInfo[]
  proxyIntegrations: IntegrationInfo[]
  githubAccounts: GitHubAccountInfo[]
  githubEnabled: boolean
  githubAppSlug: string
  hasPushTokens: boolean
  allTags: string[]
  tagVMs: Record<string, string[]>
  boxes: { name: string; status: string }[]
  integrationScheme: string
  boxHost: string
  hasTeam: boolean
}

// Fetch page data from API endpoints

async function fetchJSON<T>(url: string): Promise<T> {
  const resp = await fetch(url)
  if (resp.status === 401 || resp.status === 403) {
    isAuthenticated.value = false
    throw new Error('Session expired')
  }
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  isAuthenticated.value = true
  return resp.json()
}

export async function fetchDashboard(): Promise<DashboardData> {
  return fetchJSON('/api/dashboard')
}

// --- VM Usage types ---

export interface VMUsageEntry {
  vm_id: string
  vm_name: string
  disk_provisioned_bytes: number
  disk_avg_bytes: number
  bandwidth_bytes: number
  cpu_seconds: number
  io_read_bytes: number
  io_write_bytes: number
  days_with_data: number
  included_disk_bytes: number
  included_bandwidth_bytes: number
  overage_disk_bytes: number
  overage_bandwidth_bytes: number
  display: {
    disk_provisioned: string
    bandwidth: string
    included_disk: string
    included_bandwidth: string
    overage_disk: string
    overage_bandwidth: string
  }
}

export interface VMUsageResponse {
  period_start: string
  period_end: string
  metrics: VMUsageEntry[]
}

export async function fetchVMUsage(start: string, end: string): Promise<VMUsageResponse> {
  return fetchJSON(`/api/billing/usage/vms?start=${encodeURIComponent(start)}&end=${encodeURIComponent(end)}`)
}

// --- LLM Usage types ---

export interface LLMUsageDayEntry {
  box: string
  model: string
  provider: string
  cost: string
  requestCount: number
}

export interface LLMUsageDayGroup {
  day: string
  entries: LLMUsageDayEntry[]
  cost: string
  count: number
}

export interface LLMUsageResponse {
  days: LLMUsageDayGroup[]
  totalCost: string
  totalCount: number
  periodStart: string
  periodEnd: string
}

export async function fetchLLMUsage(date?: string): Promise<LLMUsageResponse> {
  const params = date ? `?date=${encodeURIComponent(date)}` : ''
  return fetchJSON(`/api/llm-usage${params}`)
}

export interface BoxLLMUsageResponse {
  models: { model: string; provider: string; cost: string }[]
  totalCost: string
  periodStart: string
  periodEnd: string
}

export async function fetchBoxLLMUsage(vmName: string): Promise<BoxLLMUsageResponse> {
  return fetchJSON(`/api/vm/${encodeURIComponent(vmName)}/llm-usage`)
}

export async function fetchProfile(): Promise<ProfileData> {
  return fetchJSON('/api/profile')
}

// --- Live VM Metrics ---

export interface VMLiveMetrics {
  name: string
  status: string
  cpu_percent: number
  mem_bytes: number
  swap_bytes: number
  disk_bytes: number
  disk_logical_bytes: number
  disk_capacity_bytes: number
  mem_capacity_bytes: number
  cpus: number
  net_rx_bytes: number
  net_tx_bytes: number
}

export async function fetchVMLiveMetrics(name: string): Promise<VMLiveMetrics> {
  return fetchJSON(`/api/vm/${encodeURIComponent(name)}/compute-usage/live`)
}

// --- All VMs Live Metrics + Pool Summary ---

export interface VMsLiveVM {
  name: string
  status: string
  cpu_percent: number
  cpus: number
  mem_bytes: number
  mem_capacity_bytes: number
  disk_bytes: number
  disk_logical_bytes: number
  disk_capacity_bytes: number
  net_rx_bytes: number
  net_tx_bytes: number
}

export interface VMsLivePool {
  cpu_used: number
  cpu_max: number
  mem_used_bytes: number
  mem_max_bytes: number
}

export interface VMsLiveResponse {
  vms: VMsLiveVM[]
  pool: VMsLivePool
}

export async function fetchVMsLive(): Promise<VMsLiveResponse> {
  return fetchJSON('/api/vms/live')
}

export async function fetchVMDetails(name: string): Promise<{ box: BoxInfo; billing: DashboardData['billing'] } | null> {
  const data = await fetchDashboard()
  const box = data.boxes.find(b => b.name === name) ?? null
  if (!box) return null
  return { box, billing: data.billing }
}

export async function fetchIntegrations(): Promise<IntegrationsData> {
  return fetchJSON('/api/integrations')
}

export async function checkHostname(hostname: string): Promise<{ valid: boolean; available: boolean; message: string }> {
  const resp = await fetch('/check-hostname', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ hostname }),
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function verifyGitHub(installationID: number): Promise<any> {
  const resp = await fetch(`/github/verify?installation_id=${installationID}`)
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function unlinkGitHub(installationID: number): Promise<any> {
  const resp = await fetch('/github/unlink', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ installation_id: installationID }),
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchGitHubRepos(): Promise<any> {
  const resp = await fetch('/github/repos')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

// --- Docs API ---

export interface DocsDocRef {
  slug: string
  path: string
  title: string
  description?: string
}

export interface DocsGroup {
  heading: string
  slug: string
  docs: DocsDocRef[]
}

export interface DocsListData {
  groups: DocsGroup[]
  defaultSlug: string
  isLoggedIn: boolean
}

export interface DocsEntryData {
  entry: {
    slug: string
    path: string
    title: string
    description: string
    content: string
    markdown: string
  }
  prev: DocsDocRef | null
  next: DocsDocRef | null
}

export async function fetchDocsList(): Promise<DocsListData> {
  const resp = await fetch('/api/docs')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export async function fetchDocsEntry(slug: string): Promise<DocsEntryData> {
  const resp = await fetch(`/api/docs/entry/${encodeURIComponent(slug)}`)
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

export interface DocsAllData {
  groups: DocsGroup[]
  content: string
  isLoggedIn: boolean
}

export async function fetchDocsAll(): Promise<DocsAllData> {
  const resp = await fetch('/api/docs/all')
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return resp.json()
}

