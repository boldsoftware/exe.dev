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
    window.location.href = '/login'
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

export interface SSHKeyInfo {
  publicKey: string
  comment: string
  fingerprint: string
  addedAt: string | null
  lastUsedAt: string | null
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
  gifts: { amount: string; reason: string }[]
}

export interface UserInfo {
  email: string
  region: string
  newsletterSubscribed: boolean
}

// Full page data as served by the Go backend
export interface DashboardData {
  user: UserInfo
  boxes: BoxInfo[]
  sharedBoxes: SharedBoxInfo[]
  teamBoxes: TeamBoxInfo[]
  inviteCount: number
  canRequestInvites: boolean
  sshCommand: string
  replHost: string
  showIntegrations: boolean
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
  basicUser: boolean
  showIntegrations: boolean
  inviteCount: number
  canRequestInvites: boolean
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
  boxes: { name: string; status: string }[]
  integrationScheme: string
  boxHost: string
}

// Fetch page data from API endpoints

async function fetchJSON<T>(url: string): Promise<T> {
  const resp = await fetch(url)
  if (resp.status === 401 || resp.status === 403) {
    isAuthenticated.value = false
    window.location.href = '/login'
    throw new Error('Session expired')
  }
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  isAuthenticated.value = true
  return resp.json()
}

export async function fetchDashboard(): Promise<DashboardData> {
  return fetchJSON('/api/dashboard')
}

export async function fetchProfile(): Promise<ProfileData> {
  return fetchJSON('/api/profile')
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
