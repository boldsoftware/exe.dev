<template>
  <div class="profile-page">
    <nav class="breadcrumbs" aria-label="Breadcrumb">
      <router-link to="/" class="breadcrumb-link">Home</router-link>
      <span class="breadcrumb-sep">›</span>
      <span class="breadcrumb-current">Profile</span>
    </nav>

    <div v-if="loading" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>

    <div v-else-if="loadError" class="error-state">
      <p>Failed to load profile: {{ loadError }}</p>
      <button class="btn btn-secondary" @click="loadProfile">Retry</button>
    </div>

    <template v-else-if="data">
      <!-- What is exe? section for basic users -->
      <section v-if="data.basicUser" class="card">
        <h2 class="card-title">What is exe?</h2>
        <p class="section-help">
          exe.dev is a hosting service. You've logged into a site
          hosted by exe.dev that uses "Login with exe" to manage
          authentication. See our <a href="/docs">docs</a> for
          more information.
        </p>
      </section>

      <!-- User Info -->
      <section class="card">
        <h2 class="card-title">Account</h2>
        <div class="info-grid">
          <div class="info-row">
            <span class="info-label">Support</span>
            <span class="info-value">
              <button class="btn btn-secondary" @click="openSupportModal">E-mail support</button>
              &ensp;<a href="mailto:support@exe.dev">support@exe.dev</a>
            </span>
          </div>
          <div class="info-row">
            <span class="info-label">Email</span>
            <span class="info-value">{{ data.user.email }}</span>
          </div>
          <div class="info-row">
            <span class="info-label">Region</span>
            <span class="info-value">
              {{ data.user.region.toUpperCase() }}<template v-if="data.user.regionDisplay"> ({{ data.user.regionDisplay }})</template>
              &ensp;<button class="btn btn-secondary" @click="openRegionModal">Change</button>
            </span>
          </div>
          <div class="info-row">
            <span class="info-label">Newsletter</span>
            <label class="newsletter-label">
              <input type="checkbox" :checked="data.user.newsletterSubscribed" @change="toggleNewsletter" />
              <span>Subscribe to updates</span>
              <span v-if="newsletterStatus" class="newsletter-status">{{ newsletterStatus }}</span>
            </label>
          </div>
          <div v-if="data.inviteCount > 0 || data.canRequestInvites" class="info-row">
            <span class="info-label">Invites</span>
            <span class="info-value">
              {{ data.inviteCount }} invite{{ data.inviteCount !== 1 ? 's' : '' }} available.
              <form v-if="data.inviteCount > 0" method="POST" action="/invite" style="display: inline;">
                <button type="submit" class="btn btn-secondary">Allocate</button>
              </form>
              <a v-else-if="data.canRequestInvites" href="/invite/request" class="btn btn-secondary">Request more</a>
            </span>
          </div>
        </div>
      </section>

      <!-- Billing -->
      <section class="card">
        <h2 class="card-title">Billing</h2>
        <div class="billing-card-inner">
          <!-- Skip Billing Notice -->
          <template v-if="data.credits.skipBilling">
            <div class="info-row">
              <span class="info-label">Status</span>
              <span class="info-value">Not configured (no <code>STRIPE_SECRET_KEY</code> env var exported)</span>
            </div>
          </template>

          <!-- Full Billing UI -->
          <template v-else>
            <!-- Plan Section -->
            <div class="billing-plan-section">
              <div class="billing-plan-row">
                <div class="billing-plan-info">
                  <div class="billing-plan-name-row">
                    <h3 class="plan-name">{{ data.credits.planName || 'Individual' }} Plan<span v-if="data.planCapacity && data.planCapacity.tierName"> ({{ data.planCapacity.tierName }})</span></h3>
                    <!-- TODO: uncomment when billing status states are implemented
                    <Tag v-if="data.credits.selfServeBilling" value="ACTIVE" class="active-tag" />
                    -->
                    <span v-if="data.trial && data.trial.expired" class="trial-expired">Expired</span>
                    <span v-else-if="data.trial" class="trial-expiry">Expires in {{ data.trial.daysLeft === 1 ? '1 day' : data.trial.daysLeft + ' days' }}</span>
                  </div>
                  <div v-if="data.planCapacity && data.planCapacity.poolSize" class="billing-plan-desc">{{ data.planCapacity.poolSize }}</div>
                  <div v-if="data.planCapacity" class="billing-plan-desc billing-plan-limits">{{ data.planCapacity.maxVMs }} VMs &middot; {{ data.planCapacity.defaultDiskGB }} GB disk<sup>+</sup> &middot; {{ data.planCapacity.bandwidthGB }} GB transfer<sup>+</sup></div>
                  <div v-if="data.planCapacity && data.planCapacity.monthlyPriceCents" class="billing-plan-price">${{ data.planCapacity.monthlyPriceCents / 100 }}<span class="billing-plan-interval">/month</span></div>
                  <div v-if="data.billingPeriodEnd" class="billing-plan-renewal">Your subscription will auto renew on {{ formatRenewalDate(data.billingPeriodEnd) }}.</div>
                </div>
                <div class="billing-plan-action">
                  <a v-if="canManageBilling && (data.credits.selfServeBilling || data.trial || data.basicUser)" href="/billing/update?source=profile" :class="['btn', data.credits.selfServeBilling ? 'btn-secondary' : 'btn-upgrade']">{{ data.credits.selfServeBilling ? 'Manage plan' : 'Upgrade' }}</a>
                  <a href="/pricing" target="_blank" rel="noopener noreferrer" class="billing-pricing-link">View Pricing &#x2197;</a>
                </div>
              </div>



              <div v-if="data.teamInfo && !data.teamInfo.isBillingOwner && data.teamInfo.billingAdmins && data.teamInfo.billingAdmins.length > 0" class="billing-managed-by">
                Your plan is managed by your team billing admins: {{ data.teamInfo.billingAdmins.join(', ') }}
              </div>

              <!-- Resource Usage (live) -->
              <template v-if="liveMetrics && data.planCapacity && data.planCapacity.maxCPUs > 0">
                <div class="billing-divider"></div>
                <div class="resource-usage-header">Resource Usage (live)</div>

                <div class="resource-meters">
                  <div v-if="liveMetrics.is_sudoer" class="resource-meter">
                    <span class="meter-label">vCPU</span>
                    <div class="meter-bar-wrap">
                      <div class="meter-bar">
                        <div
                          class="meter-fill"
                          :class="meterColor(liveMetrics.pool.cpu_max > 0 ? (liveMetrics.pool.cpu_used / liveMetrics.pool.cpu_max) * 100 : 0)"
                          :style="{ width: liveMetrics.pool.cpu_max > 0 ? Math.min((liveMetrics.pool.cpu_used / liveMetrics.pool.cpu_max) * 100, 100) + '%' : '0%' }"
                        ></div>
                      </div>
                      <div class="meter-values">
                        <span class="meter-used">{{ Math.min(liveMetrics.pool.cpu_used, liveMetrics.pool.cpu_max).toFixed(1) }} cores</span>
                        <span>{{ liveMetrics.pool.cpu_max }} cores</span>
                      </div>
                    </div>
                  </div>
                  <div v-if="liveMetrics.is_sudoer && liveMetrics.pool.mem_max_bytes > 0" class="resource-meter">
                    <span class="meter-label">Memory</span>
                    <div class="meter-bar-wrap">
                      <div class="meter-bar">
                        <div
                          class="meter-fill"
                          :class="meterColor((liveMetrics.pool.mem_used_bytes / liveMetrics.pool.mem_max_bytes) * 100)"
                          :style="{ width: Math.min((liveMetrics.pool.mem_used_bytes / liveMetrics.pool.mem_max_bytes) * 100, 100) + '%' }"
                        ></div>
                      </div>
                      <div class="meter-values">
                        <span class="meter-used">{{ formatBytes(Math.min(liveMetrics.pool.mem_used_bytes, liveMetrics.pool.mem_max_bytes)) }}</span>
                        <span>{{ formatBytes(liveMetrics.pool.mem_max_bytes) }}</span>
                      </div>
                    </div>
                  </div>
                  <div v-if="data.planCapacity && data.planCapacity.bandwidthGB > 0" class="resource-meter">
                    <span class="meter-label">Bandwidth</span>
                    <div class="meter-bar-wrap">
                      <div class="meter-bar">
                        <div
                          class="meter-fill"
                          :class="meterColor((totalBandwidthBytes / (data.planCapacity.bandwidthGB * 1024 * 1024 * 1024)) * 100)"
                          :style="{ width: Math.min((totalBandwidthBytes / (data.planCapacity.bandwidthGB * 1024 * 1024 * 1024)) * 100, 100) + '%' }"
                        ></div>
                      </div>
                      <div class="meter-values">
                        <span class="meter-used">{{ formatBytes(totalBandwidthBytes) }} used</span>
                        <span>{{ data.planCapacity.bandwidthGB }} GB included</span>
                      </div>
                    </div>
                  </div>
                  <div v-if="totalDiskIncludedBytes > 0" class="resource-meter">
                    <span class="meter-label">Disk</span>
                    <div class="meter-bar-wrap">
                      <div class="meter-bar">
                        <div
                          class="meter-fill"
                          :class="totalDiskProvisionedBytes > totalDiskIncludedBytes ? 'yellow' : 'green'"
                          :style="{ width: Math.min((totalDiskProvisionedBytes / totalDiskIncludedBytes) * 100, 100) + '%' }"
                        ></div>
                      </div>
                      <div class="meter-values">
                        <span class="meter-used">{{ formatBytes(totalDiskProvisionedBytes) }} provisioned</span>
                        <span>{{ formatBytes(totalDiskIncludedBytes) }} included</span>
                      </div>
                    </div>
                  </div>
                </div>

                <!-- Per-VM breakdown (sudoer only) -->
                <div v-if="liveMetrics.is_sudoer && liveMetrics.vms.length > 0" class="vm-breakdown">
                  <div class="vm-breakdown-header">Per-VM Breakdown</div>
                  <table class="vm-breakdown-table">
                    <thead>
                      <tr>
                        <th>VM</th>
                        <th>vCPU</th>
                        <th>Memory</th>
                        <th>Disk</th>
                        <th>Bandwidth</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="vm in liveMetrics.vms" :key="vm.name">
                        <td>
                          <div class="vm-name-cell">
                            <span class="status-dot" :class="vm.status === 'running' ? 'running' : 'stopped'"></span>
                            <router-link v-if="vm.status === 'running'" :to="'/vm/' + vm.name" class="vm-link">{{ vm.name }}</router-link>
                            <span v-else class="text-muted">{{ vm.name }}</span>
                          </div>
                        </td>
                        <td>
                          <template v-if="vm.status === 'running'">
                            <div class="cpu-cell">
                              {{ Math.min(vm.cpu_percent / 100, liveMetrics.pool.cpu_max > 0 ? liveMetrics.pool.cpu_max : Infinity).toFixed(1) }}
                              <div class="cpu-mini-bar">
                                <div class="cpu-mini-fill" :class="meterColor(liveMetrics.pool.cpu_max > 0 ? (vm.cpu_percent / 100 / liveMetrics.pool.cpu_max) * 100 : 0)" :style="{ width: cpuMiniWidth(vm.cpu_percent, liveMetrics.pool.cpu_max) }"></div>
                              </div>
                            </div>
                          </template>
                          <span v-else class="text-muted">&mdash;</span>
                        </td>
                        <td>
                          <template v-if="vm.status === 'running'">{{ formatBytes(vm.mem_bytes) }}</template>
                          <span v-else class="text-muted">&mdash;</span>
                        </td>
                        <td>{{ formatBytes(vm.disk_capacity_bytes) }}</td>
                        <td>{{ formatBytes(vmBandwidth(vm.name)) }}</td>
                      </tr>
                      <tr v-if="liveMetrics.vms.length > 1" class="totals-row">
                        <td>Total</td>
                        <td>{{ Math.min(liveMetrics.pool.cpu_used, liveMetrics.pool.cpu_max).toFixed(1) }} / {{ liveMetrics.pool.cpu_max }}</td>
                        <td>{{ formatBytes(Math.min(liveMetrics.pool.mem_used_bytes, liveMetrics.pool.mem_max_bytes)) }} / {{ formatBytes(liveMetrics.pool.mem_max_bytes) }}</td>
                        <td>{{ formatBytes(totalDiskProvisionedBytes) }}</td>
                        <td>{{ formatBytes(totalBandwidthBytes) }}</td>
                      </tr>
                    </tbody>
                  </table>
                </div>

                <!-- Upsell -->
                <div v-if="data.planCapacity.nextTier" class="billing-upsell">
                  <span class="billing-upsell-text">Need more power? Upgrade to <strong>{{ data.planCapacity.nextTier.poolSize }}</strong> for ${{ data.planCapacity.nextTier.monthlyPriceCents / 100 }}/mo.</span>
                  <a href="/billing/update?source=upgrade" class="billing-upsell-link">Upgrade</a>
                </div>
              </template>
            </div>

            <!-- Payment Section -->
            <div v-if="data.credits.selfServeBilling && data.credits.paymentMethod" class="billing-divider-section">
              <div class="billing-section-header">Payment Method</div>
              <div class="payment-method-callout">
                <div class="pm-left">
                  <img :src="paymentIconUrl(data.credits.paymentMethod)" :alt="paymentBrandName(data.credits.paymentMethod)" class="pm-icon-img" />
                  <span class="pm-label">{{ paymentBrandName(data.credits.paymentMethod) }}
                    <template v-if="data.credits.paymentMethod.last4"> •••• {{ data.credits.paymentMethod.last4 }}</template>
                    <template v-else-if="data.credits.paymentMethod.email"> {{ data.credits.paymentMethod.email }}</template>
                  </span>
                </div>
                <span v-if="data.credits.paymentMethodManagedByTeam" class="pm-managed-badge">Managed by team</span>
                <a v-else href="/billing/update?source=profile" class="btn btn-secondary">Update</a>
              </div>
            </div>

            <!-- Credit Balance Section -->
            <div v-if="data.credits.creditBalanceUSD > 0" class="billing-divider-section">
              <div class="credit-balance-line">
                <div>
                  <span class="credit-balance-label">Credit Balance</span>
                  <span class="credit-balance-sub">Will be applied on your next invoice</span>
                </div>
                <span class="credit-balance-amount">${{ data.credits.creditBalanceUSD.toFixed(2) }}</span>
              </div>
            </div>

            <!-- Invoices Section -->
            <div v-if="canManageBilling && data.credits.invoices && data.credits.invoices.length > 0" class="billing-divider-section">
              <div class="billing-section-header-row">
                <span class="billing-section-header">Invoices</span>
                <a href="/billing/update?source=profile" class="view-all-link">View all in Stripe &#x2197;</a>
              </div>
              <ul class="invoice-list">
                <li v-for="(inv, idx) in data.credits.invoices" :key="idx" class="invoice-item">
                  <div class="invoice-info">
                    <span class="invoice-desc">{{ inv.planName || inv.description }}</span>
                    <span class="invoice-period">{{ inv.periodStart }} – {{ inv.periodEnd }}</span>
                  </div>
                  <div class="invoice-right">
                    <template v-if="inv.status !== 'upcoming' && parseFloat(inv.creditGenerated) > 0">
                      <span class="invoice-amount invoice-amount-zero">${{ inv.amount }}</span>
                      <span class="invoice-credit-generated">+${{ inv.creditGenerated }} credit</span>
                    </template>
                    <template v-else-if="inv.status !== 'upcoming' && parseFloat(inv.creditApplied) > 0">
                      <span class="invoice-amount">${{ inv.amount }}</span>
                      <span class="invoice-credit">−${{ inv.creditApplied }} credit applied</span>
                    </template>
                    <template v-else>
                      <span class="invoice-amount">${{ inv.status === 'upcoming' ? inv.subtotal : inv.amount }}</span>
                    </template>
                  </div>
                  <div class="invoice-status-col">
                    <a v-if="inv.hostedInvoiceURL" :href="inv.hostedInvoiceURL" target="_blank" rel="noopener noreferrer" class="invoice-link">
                      <span :class="['invoice-badge', 'invoice-badge-' + inv.status]">{{ inv.status === 'paid' ? 'Paid' : inv.status === 'upcoming' ? 'Upcoming' : inv.status === 'open' ? 'Open' : inv.status }} &#x2197;</span>
                    </a>
                    <span v-else :class="['invoice-badge', 'invoice-badge-' + inv.status]">{{ inv.status === 'paid' ? 'Paid' : inv.status === 'upcoming' ? 'Upcoming' : inv.status === 'open' ? 'Open' : inv.status }}</span>
                  </div>
                </li>

              </ul>
            </div>

          </template>
        </div>
      </section>

      <!-- Shelley -->
      <section v-if="!data.credits.skipBilling" class="card">
        <h2 class="card-title">Shelley</h2>
        <div class="billing-card-inner">
          <template v-if="!data.credits.skipBilling">

            <!-- Team credits banner -->
            <Message v-if="data.teamInfo && !pooledCreditsBannerDismissed" severity="info" :closable="true" @close="dismissPooledCreditsBanner" class="team-credits-banner">
              <template #default>
                <div class="team-credits-banner-content">
                  <strong>Pooled team credits coming soon</strong>
                  <span>Credits will be shared across your team and managed by your billing owner. For now, each member has their own balance.</span>
                </div>
              </template>
            </Message>

            <!-- Monthly Usage Progress (promoted above credits for visibility) -->
            <div v-if="data.credits.hasShelleyFreeCreditPct" class="usage-section">
              <div class="usage-header">
                <span class="usage-label">Monthly Usage</span>
                <span class="usage-pct">${{ Math.round(data.credits.monthlyUsedUSD) }} / ${{ Math.round(data.credits.shelleyCreditsMax) }}</span>
              </div>
              <ProgressBar 
                :value="Math.min(data.credits.monthlyUsedPct, 100)" 
                :severity="usageBarSeverity"
                :show-value="false"
              />
              <div class="usage-footer">
                <span>Resets {{ data.credits.monthlyCreditsResetAt }}</span>
              </div>
            </div>

            <!-- Shelley Credits Section (side-by-side on all screens) -->
            <div v-if="data.credits.hasShelleyFreeCreditPct" class="credits-grid">
              <!-- Monthly Allowance -->
              <div class="credit-card">
                <div class="credit-card-title">ALLOWANCE</div>
                <div class="credit-card-amount" :class="{ 'credit-depleted': Math.round(data.credits.shelleyCreditsMax - data.credits.monthlyUsedUSD) <= 0 }">${{ Math.round(data.credits.shelleyCreditsMax - data.credits.monthlyUsedUSD) }}</div>
                <div class="credit-card-detail">remaining of ${{ Math.round(data.credits.shelleyCreditsMax) }}</div>
              </div>

              <!-- Extra Credits -->
              <div class="credit-card">
                <div class="credit-card-title">EXTRA CREDITS</div>
                <div class="credit-card-amount">${{ Math.round(data.credits.ledgerBalanceUSD) }}</div>
                <div class="credit-card-detail">no expiry</div>
              </div>
            </div>

            <!-- Buy Credits -->
            <div v-if="data.credits.hasShelleyFreeCreditPct" class="buy-section">
              <div class="buy-label">
                Top up extra credits · <a href="/docs/pricing#shelley-tokens" class="learn-more-link">How credits work</a>
              </div>
              <form method="POST" action="/credits/buy" class="buy-form">
                <div class="buy-amounts">
                  <button type="button" @click="selectedAmount = 5" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 5 }]">$5</button>
                  <button type="button" @click="selectedAmount = 10" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 10 }]">$10</button>
                  <button type="button" @click="selectedAmount = 25" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 25 }]">$25</button>
                  <button type="button" @click="selectedAmount = 50" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 50 }]">$50</button>
                  <button type="button" @click="selectedAmount = 100" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 100 }]">$100</button>
                </div>
                <input type="hidden" name="dollars" :value="selectedAmount" />
                <button type="submit" class="buy-btn">Buy ${{ selectedAmount }}</button>
              </form>
            </div>


            <!-- Transaction History -->
            <div v-if="transactionHistory.length > 0" class="transaction-section">
              <div class="transaction-header">
                <span class="tx-section-title">Transaction History</span>
                <button
                  v-if="receiptsAvailable"
                  class="btn btn-secondary btn-small tx-download-btn"
                  :disabled="downloadingReceipts"
                  @click="downloadReceipts"
                ><i class="pi pi-download"></i> {{ downloadingReceipts ? 'Downloading...' : 'Download all' }}</button>
              </div>
              <ul class="tx-list">
                <li v-for="tx in transactionHistory" :key="tx.type + tx.date + tx.amount" class="tx-item">
                  <div :class="['tx-icon', tx.type === 'Purchase' ? 'tx-icon-purchase' : 'tx-icon-gift']">
                    <i :class="tx.type === 'Purchase' ? 'pi pi-credit-card' : 'pi pi-gift'"></i>
                  </div>
                  <div class="tx-info">
                    <span class="tx-type">{{ tx.type === 'Purchase' ? 'Credit Purchase' : 'Gift' }}<template v-if="tx.type === 'Gift' && tx.details"> &middot; {{ tx.details }}</template></span>
                    <span class="tx-date">{{ tx.date || '\u2014' }}</span>
                  </div>
                  <div class="tx-right">
                    <span class="tx-amount">{{ tx.amount }}</span>
                    <a
                      v-if="tx.type === 'Purchase' && tx.receiptURL"
                      :href="tx.receiptURL"
                      target="_blank"
                      rel="noopener noreferrer"
                      class="tx-receipt"
                    >Receipt &#x2197;</a>
                  </div>
                </li>
              </ul>
            </div>

            <!-- LLM Usage -->
            <div v-if="llmPeriodLabel" class="llm-usage-section">
              <div class="llm-usage-header">
                <span class="llm-usage-title">LLM Usage</span>
                <div class="llm-period-nav">
                  <button class="llm-period-btn" @click="llmPeriodPrev" title="Previous period">‹</button>
                  <span class="llm-usage-period">{{ llmPeriodLabel }}</span>
                  <button class="llm-period-btn" :class="{ disabled: isCurrentPeriod }" :disabled="isCurrentPeriod" @click="llmPeriodNext" title="Next period">›</button>
                </div>
              </div>
              <template v-if="llmLoading">
                <div class="llm-empty">Loading…</div>
              </template>
              <template v-else-if="!llmUsage || llmUsage.totalCount === 0">
                <div class="llm-empty">No usage this period</div>
              </template>
              <template v-else>
                <div v-for="dayGroup in llmUsage.days" :key="dayGroup.day" class="llm-day-group">
                  <div class="llm-day-header" @click="toggleDay(dayGroup.day)">
                    <span class="llm-day-label">
                      <span class="llm-day-chevron">{{ expandedDays.has(dayGroup.day) ? '▾' : '▸' }}</span>
                      {{ formatDay(dayGroup.day) }}
                    </span>
                    <span class="llm-day-stats">{{ dayGroup.cost }}</span>
                  </div>
                  <template v-if="expandedDays.has(dayGroup.day)">
                    <div v-for="(e, i) in dayGroup.entries" :key="i" class="llm-usage-row">
                      <div class="llm-usage-left">
                        <span class="llm-usage-model">{{ e.model }}</span>
                        <span class="llm-usage-box">{{ e.box }}</span>
                      </div>
                      <span class="llm-usage-stats">{{ e.cost }}</span>
                    </div>
                  </template>
                </div>
                <div class="llm-usage-total">
                  <span>Total</span>
                  <span>{{ llmUsage.totalCost }}</span>
                </div>
              </template>
            </div>

          </template>
        </div>
      </section>

      <!-- Pending Team Invites -->
      <section v-if="data.pendingTeamInvites.length > 0" class="card">
        <h2 class="card-title">Team Invitations</h2>
        <div v-for="invite in data.pendingTeamInvites" :key="invite.token" class="invite-row">
          <div>
            <strong>{{ invite.teamName }}</strong> invited you to join their team
            <span class="text-muted">by {{ invite.invitedBy }}</span>
            <div v-if="invite.vmCount > 0" class="invite-warning">
              · Accepting will make your {{ invite.vmCount }} existing VM{{ invite.vmCount !== 1 ? 's' : '' }} visible to team admins
            </div>
          </div>
          <div class="invite-actions">
            <button class="btn btn-primary" @click="acceptInvite(invite.token)">Accept</button>
            <button class="btn btn-secondary" @click="declineInvite(invite.token)">Decline</button>
          </div>
        </div>
      </section>

      <!-- Create Team (when user has no team and can create one) -->
      <section v-if="!data.teamInfo && data.canEnableTeam" class="card">
        <h2 class="card-title">Teams</h2>
        <p class="section-desc">Teams lets you manage shared billing, invite members, SSH into team members' VMs, and share VMs across your organization.</p>
        <p class="section-desc text-muted">You'll become the billing owner. Your existing VMs will become part of the team.</p>
        <div class="create-team-row">
          <input
            v-model="teamName"
            type="text"
            class="form-input"
            placeholder="Team name"
            @keydown.enter="createTeam"
          />
          <button class="btn btn-primary" :disabled="creatingTeam" @click="createTeam">
            {{ creatingTeam ? 'Creating...' : 'Create Team' }}
          </button>
        </div>
        <div v-if="teamError" class="field-error">{{ teamError }}</div>
      </section>

      <!-- Team Info -->
      <section v-if="data.teamInfo" class="card">
        <h2 class="card-title">Team</h2>
        <div class="info-grid">
          <div class="info-row">
            <span class="info-label">Name</span>
            <span class="info-value">{{ data.teamInfo.displayName }}</span>
          </div>
          <div class="info-row">
            <span class="info-label">Role</span>
            <span class="info-value">{{ data.teamInfo.role }}</span>
          </div>
          <div class="info-row">
            <span class="info-label">VMs</span>
            <span class="info-value">{{ data.teamInfo.boxCount }} / {{ data.teamInfo.maxBoxes }}</span>
          </div>
        </div>
        <div v-if="data.teamInfo.members.length > 0" class="member-list">
          <h3 class="subsection-title">Members</h3>
          <div v-for="m in data.teamInfo.members" :key="m.email" class="member-row">
            <span>{{ m.email }}</span>
            <span class="text-muted">{{ m.role }}</span>
            <span v-if="data.teamInfo.isAdmin && m.email !== data.user.email" class="member-actions">
              <button
                v-if="canChangeRole(m)"
                class="btn btn-secondary"
                @click="changeTeamMemberRole(m.email, m.role)"
              >Change Role</button>
              <button
                class="btn btn-danger"
                @click="removeTeamMember(m.email)"
              >Remove</button>
            </span>
          </div>
        </div>
        <div v-if="data.teamInfo.isAdmin" class="team-admin-actions">
          <button class="btn btn-secondary" @click="inviteTeamMember">Invite Member</button>
          <button class="btn btn-secondary" @click="transferVM">Transfer VM</button>
        </div>
        <div v-if="data.teamInfo.isBillingOwner && data.teamInfo.onlyMember" class="danger-zone">
          <div class="danger-zone-title">Danger Zone</div>
          <p class="danger-zone-text">
            Disabling your team will remove all team shares, cancel pending invites, and delete team auth/SSO configuration. Your VMs will remain on your personal account.
          </p>
          <button class="btn btn-danger" @click="disableTeam">Disable Team</button>
        </div>
      </section>

      <!-- SSH Keys -->
      <section class="card">
        <h2 class="card-title">SSH Keys</h2>
        <p class="section-help">SSH keys allow you to connect to exe.dev via <code>ssh exe.dev</code></p>
        <div v-if="data.sshKeys.length === 0" class="empty-msg">No SSH keys registered.</div>
        <div v-for="key in data.sshKeys" :key="key.fingerprint" class="ssh-key-row">
          <div class="ssh-key-info">
            <div class="ssh-key-name">
              {{ key.comment }}
              <span v-if="key.apiKeyHint" class="badge badge-muted">used by generated api key {{ key.apiKeyHint }}…</span>
              <span v-if="key.integrationId" class="badge badge-muted">managed by integration</span>
            </div>
            <div class="ssh-key-fp">{{ key.publicKey }}</div>
          </div>
          <div class="ssh-key-actions">
            <button class="btn btn-secondary" @click="renameSSHKey(key.comment, key.fingerprint)">Rename</button>
            <button v-if="!key.integrationId" class="btn btn-danger" @click="removeSSHKey(key.publicKey)">Remove</button>
            <span v-else class="text-muted" style="font-size: 11px;">remove via integration</span>
          </div>
        </div>
        <div style="margin-top: 12px;">
          <button class="btn btn-secondary" @click="addSSHKey">Add SSH Key</button>
        </div>
      </section>

      <!-- API Keys -->
      <section class="card">
        <h2 class="card-title">API Keys</h2>
        <p class="section-help">API keys let you access exe.dev and your VMs programmatically. <a href="/docs/cli-ssh-key#generate-api-key">Docs</a></p>
        <div style="margin-top: 12px;">
          <button class="btn btn-secondary" @click="openCreateAPIKey">Create API Key</button>
        </div>
      </section>

      <!-- Passkeys -->
      <section class="card">
        <h2 class="card-title">Passkeys</h2>
        <p class="section-help">Passkeys allow you to log into exe.dev on the web without going through e-mail.</p>
        <div v-if="!passkeySupported" class="text-muted">Passkeys are not supported on this device or browser.</div>
        <template v-else>
          <div v-if="data.passkeys.length === 0" class="empty-msg">No passkeys registered yet.</div>
          <div v-for="pk in data.passkeys" :key="pk.id" class="passkey-row">
            <div>
              <div class="passkey-name">{{ pk.name }}</div>
              <div class="text-muted" style="font-size: 11px;">Added {{ pk.createdAt }} · Last used {{ pk.lastUsedAt }}</div>
            </div>
            <div class="passkey-actions">
              <template v-if="!deletingPasskeys.has(pk.id)">
                <button class="btn btn-danger" @click="deletingPasskeys.add(pk.id)">Delete</button>
              </template>
              <template v-else>
                <span class="text-muted" style="font-size: 11px; margin-right: 4px;">Delete this passkey?</span>
                <button class="btn btn-danger" @click="confirmDeletePasskey(pk.id)">Yes</button>
                <button class="btn btn-secondary" @click="deletingPasskeys.delete(pk.id)">Cancel</button>
              </template>
            </div>
          </div>
          <div class="add-passkey-form">
            <input v-model="passkeyName" type="text" class="passkey-name-input" placeholder="Passkey name (e.g., MacBook, iPhone)" />
            <button class="btn btn-secondary" @click="addPasskey">Add Passkey</button>
          </div>
          <div v-if="passkeyError" class="passkey-error">{{ passkeyError }}</div>
        </template>
      </section>

      <!-- Site Sessions -->
      <section v-if="data.siteSessions.length > 0" class="card">
        <h2 class="card-title">Active Sessions</h2>
        <table class="mini-table">
          <thead><tr><th>Site</th><th>Last Used</th></tr></thead>
          <tbody>
            <tr v-for="s in data.siteSessions" :key="s.domain">
              <td><a :href="s.url" target="_blank" rel="noopener noreferrer">{{ s.domain }}</a></td>
              <td>{{ s.lastUsedAt || 'Never' }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- Shared VMs -->
      <section v-if="data.sharedBoxes.length > 0" class="card">
        <h2 class="card-title">Sites Shared With You</h2>
        <table class="mini-table">
          <thead><tr><th>VM</th><th>Owner</th></tr></thead>
          <tbody>
            <tr v-for="b in data.sharedBoxes" :key="b.name">
              <td><a :href="b.proxyURL" target="_blank" rel="noopener noreferrer">{{ b.name }}</a></td>
              <td>{{ b.ownerEmail }}</td>
            </tr>
          </tbody>
        </table>
      </section>
    </template>

    <!-- Support Modal -->
    <div v-if="showSupportModal" class="modal-overlay" @click.self="closeSupportModal">
      <div class="modal-box support-modal">
        <div class="modal-title">Contact support</div>
        <p class="section-help">We'd love to hear from you!</p>
        <div class="form-group">
          <label>Subject</label>
          <input v-model="support.subject" class="form-input" :disabled="support.sending" />
        </div>
        <div class="form-group">
          <label>Message</label>
          <textarea v-model="support.body" class="form-input" rows="6" :disabled="support.sending" @paste="onSupportPaste"></textarea>
        </div>
        <div class="form-group">
          <label>Attachments</label>
          <input ref="supportFileInput" type="file" multiple @change="onSupportFilesChange" :disabled="support.sending" />
          <div v-if="support.files.length > 0" class="support-files">
            <div v-for="(f, i) in support.files" :key="i" class="support-file">
              <span>{{ f.name }} <span class="text-muted">({{ formatAttachBytes(f.size) }})</span></span>
              <button class="btn btn-secondary btn-xs" @click="removeSupportFile(i)" :disabled="support.sending">Remove</button>
            </div>
          </div>
        </div>
        <p v-if="support.error" class="error-text">{{ support.error }}</p>
        <p v-if="support.success" class="support-success">Message sent.</p>
        <div class="modal-actions">
          <button class="btn btn-primary" :disabled="!canSendSupport" @click="sendSupportEmail">
            {{ support.sending ? 'Sending…' : 'Send' }}
          </button>
          <button class="btn btn-secondary" :disabled="support.sending" @click="closeSupportModal">Cancel</button>
        </div>
      </div>
    </div>

    <!-- Region Change Modal -->
    <div v-if="showRegionModal" class="modal-overlay" @click.self="showRegionModal = false">
      <div class="modal-box">
        <div class="modal-title">Change Region</div>
        <div class="form-group">
          <select v-model="selectedRegion" class="form-input" :disabled="regionSaving">
            <option v-for="r in data?.availableRegions ?? []" :key="r.code" :value="r.code">
              {{ r.code.toUpperCase() }} — {{ r.display }}
            </option>
          </select>
        </div>
        <p v-if="regionError" class="error-text">{{ regionError }}</p>
        <div class="modal-actions">
          <button class="btn btn-primary" :disabled="regionSaving || selectedRegion === data?.user.region" @click="saveRegion">
            {{ regionSaving ? 'Saving…' : 'Save' }}
          </button>
          <button class="btn btn-secondary" :disabled="regionSaving" @click="showRegionModal = false">Cancel</button>
        </div>
      </div>
    </div>

    <!-- Command Modal -->
    <CommandModal
      :visible="modal.visible"
      :title="modal.title"
      :description="modal.description"
      :command="modal.command"
      :command-prefix="modal.commandPrefix"
      :input-placeholder="modal.inputPlaceholder"
      :default-value="modal.defaultValue"
      :danger="modal.danger"
      :choices="modal.choices"
      :default-choice="modal.defaultChoice"
      @close="modal.visible = false"
      @success="reload"
    />

    <!-- Create API Key Modal -->
    <div v-if="apiKeyModal.visible" class="modal-overlay" @click.self="closeApiKeyModal">
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="Create API Key">
        <div class="modal-header">
          <h3>Create API Key</h3>
          <button class="modal-close" aria-label="Close" @click="closeApiKeyModal">&times;</button>
        </div>
        <div class="modal-body">
          <template v-if="!apiKeyModal.result">
            <div class="form-row">
              <label>Label</label>
              <input v-model="apiKeyModal.label" class="form-input" placeholder="my-api-key" />
            </div>
            <div class="form-row">
              <label>Scope</label>
              <select v-model="apiKeyModal.vm" class="form-input">
                <option value="">exe.dev API</option>
                <option v-for="box in (data?.boxes || [])" :key="box.name" :value="box.name">{{ box.name }}</option>
              </select>
            </div>
            <div class="form-row">
              <label>Expiry</label>
              <select v-model="apiKeyModal.expiry" class="form-input">
                <option value="30d">30 days</option>
                <option value="90d">90 days</option>
                <option value="1y">1 year</option>
                <option value="never">Never</option>
              </select>
            </div>
            <div v-if="!apiKeyModal.vm" class="form-row">
              <label>Allowed commands <span class="text-muted">(leave unchecked for defaults)</span></label>
              <div class="cmd-checkboxes">
                <label v-for="cmd in availableCommands" :key="cmd.value" class="cmd-checkbox">
                  <input type="checkbox" :value="cmd.value" v-model="apiKeyModal.cmds" />
                  <code>{{ cmd.value }}</code>
                  <span v-if="cmd.isDefault" class="cmd-default-badge">default</span>
                </label>
              </div>
            </div>
            <div class="cmd-preview">
              <code>{{ apiKeyBuiltCommand }}</code>
            </div>
            <div v-if="apiKeyModal.error" class="cmd-result error">{{ apiKeyModal.error }}</div>
          </template>
          <template v-else>
            <div class="api-key-result">
              <div class="api-key-result-row">
                <span class="api-key-result-label">Label</span>
                <span>{{ apiKeyModal.result.label }}</span>
              </div>
              <div v-if="apiKeyModal.result.expires_at" class="api-key-result-row">
                <span class="api-key-result-label">Expires</span>
                <span>{{ formatExpiry(apiKeyModal.result.expires_at) }}</span>
              </div>
              <div v-else class="api-key-result-row">
                <span class="api-key-result-label">Expires</span>
                <span>Never</span>
              </div>
              <div class="api-key-token-area">
                <label class="api-key-token-label">Your API Key</label>
                <div class="api-key-token-box">
                  <code class="api-key-token-value">{{ apiKeyModal.result.token }}</code>
                  <CopyButton :text="apiKeyModal.result.token" title="Copy token" />
                </div>
              </div>
              <div class="api-key-warning">
                ⚠ This token will not be shown again. Copy it now.
              </div>
              <div class="api-key-usage">
                <label>Usage example</label>
                <div class="cmd-preview">
                  <code v-if="apiKeyModal.vm">curl -H "Authorization: Bearer {{ apiKeyModal.result.token }}" https://{{ apiKeyUsageHost }}/</code>
                  <code v-else>curl -X POST https://{{ apiKeyUsageHost }}/exec -H "Authorization: Bearer {{ apiKeyModal.result.token }}" -d 'whoami'</code>
                  <CopyButton :text="apiKeyUsageExample" title="Copy usage example" />
                </div>
              </div>
              <div class="api-key-revoke-hint">
                Revoke with: <code>ssh-key remove {{ apiKeyModal.result.label }}</code>
              </div>
            </div>
          </template>
        </div>
        <div class="modal-footer">
          <template v-if="apiKeyModal.result">
            <button class="btn btn-primary" @click="closeApiKeyModal">Done</button>
          </template>
          <template v-else>
            <button class="btn btn-secondary" @click="closeApiKeyModal">Cancel</button>
            <button class="btn btn-primary" :disabled="apiKeyModal.running" @click="runApiKeyCommand">
              {{ apiKeyModal.running ? 'Creating...' : 'Create' }}
            </button>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount } from 'vue'
import { fetchProfile, fetchLLMUsage, fetchVMsLive, fetchVMUsage, runCommand, shellQuote, type ProfileData, type LLMUsageResponse, type VMsLiveResponse, type VMUsageEntry } from '../api/client'
import CommandModal from '../components/CommandModal.vue'
import CopyButton from '../components/CopyButton.vue'
import Tag from 'primevue/tag'
import Card from 'primevue/card'
import ProgressBar from 'primevue/progressbar'

import Message from 'primevue/message'

const loading = ref(true)
const loadError = ref('')
const data = ref<ProfileData | null>(null)
const llmUsage = ref<LLMUsageResponse | null>(null)
const llmLoading = ref(false)
const liveMetrics = ref<VMsLiveResponse | null>(null)
const billingUsage = ref<VMUsageEntry[]>([])
let liveMetricsTimer: ReturnType<typeof setInterval> | null = null
const expandedDays = ref<Set<string>>(new Set())
const passkeyName = ref('')
const deletingPasskeys = ref<Set<number>>(new Set())
const passkeyError = ref('')
const passkeySupported = ref(typeof window !== 'undefined' && window.PublicKeyCredential !== undefined)
const newsletterStatus = ref('')
const showRegionModal = ref(false)
const regionSaving = ref(false)
const regionError = ref('')
const selectedRegion = ref('')

// Team creation
const teamName = ref('')
const teamError = ref('')
const creatingTeam = ref(false)

// Support form
const supportMaxMB = 25
const supportMaxBytes = supportMaxMB * 1024 * 1024
const supportFileInput = ref<HTMLInputElement | null>(null)
const showSupportModal = ref(false)
function openSupportModal() {
  support.error = ''
  support.success = false
  showSupportModal.value = true
}
function closeSupportModal() {
  if (support.sending) return
  showSupportModal.value = false
}
const support = reactive({
  subject: '',
  body: '',
  files: [] as File[],
  sending: false,
  error: '',
  success: false,
})
const supportTotalBytes = computed(() => support.files.reduce((a, f) => a + f.size, 0))
const canSendSupport = computed(() => !support.sending && support.subject.trim().length > 0 && support.body.trim().length > 0 && supportTotalBytes.value <= supportMaxBytes)

function formatAttachBytes(n: number): string {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / (1024 * 1024)).toFixed(1) + ' MB'
}

function onSupportFilesChange(ev: Event) {
  const input = ev.target as HTMLInputElement
  support.error = ''
  support.success = false
  support.files = input.files ? Array.from(input.files) : []
  if (supportTotalBytes.value > supportMaxBytes) {
    support.error = `Attachments exceed ${supportMaxMB} MB total.`
  }
}

function removeSupportFile(i: number) {
  support.files.splice(i, 1)
  if (supportFileInput.value) supportFileInput.value.value = ''
}

function onSupportPaste(ev: ClipboardEvent) {
  const cd = ev.clipboardData
  if (!cd) return
  // Collect any non-text items (e.g. pasted images/screenshots) as attachments.
  // Text (including text/html) is left alone so the default paste behavior runs.
  const attached: File[] = []
  for (const item of cd.items) {
    if (item.kind !== 'file') continue
    const f = item.getAsFile()
    if (f) attached.push(f)
  }
  if (attached.length === 0) return
  ev.preventDefault()
  let idx = support.files.length + 1
  for (let f of attached) {
    // Screenshots often come in as 'image.png' — give them a more useful name.
    if (/^image\.[a-z0-9]+$/i.test(f.name) || !f.name) {
      const ext = (f.type.split('/')[1] || 'bin').split(';')[0]
      const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19)
      f = new File([f], `pasted-${ts}-${idx}.${ext}`, { type: f.type })
    }
    support.files.push(f)
    idx++
  }
  support.error = ''
  support.success = false
  if (supportTotalBytes.value > supportMaxBytes) {
    support.error = `Attachments exceed ${supportMaxMB} MB total.`
  }
}

async function sendSupportEmail() {
  support.error = ''
  support.success = false
  if (supportTotalBytes.value > supportMaxBytes) {
    support.error = `Attachments exceed ${supportMaxMB} MB total.`
    return
  }
  support.sending = true
  try {
    const fd = new FormData()
    fd.append('subject', support.subject.trim())
    fd.append('body', support.body)
    for (const f of support.files) fd.append('attachments', f, f.name)
    const resp = await fetch('/api/profile/support', { method: 'POST', body: fd })
    if (!resp.ok) {
      const txt = await resp.text()
      throw new Error(txt || `HTTP ${resp.status}`)
    }
    support.success = true
    support.subject = ''
    support.body = ''
    support.files = []
    if (supportFileInput.value) supportFileInput.value.value = ''
    setTimeout(() => { showSupportModal.value = false; support.success = false }, 1200)
  } catch (e: any) {
    support.error = e?.message || 'Failed to send message'
  } finally {
    support.sending = false
  }
}

// Billing state
const selectedAmount = ref(25)
const pooledCreditsBannerDismissed = ref(localStorage.getItem('pooled-credits-banner-dismissed') === '1')

function dismissPooledCreditsBanner() {
  pooledCreditsBannerDismissed.value = true
  localStorage.setItem('pooled-credits-banner-dismissed', '1')
}

// Whether the current user can manage billing (not a team member, or is the billing owner)
const canManageBilling = computed(() => {
  if (!data.value) return false
  // No team = individual user, can always manage
  if (!data.value.teamInfo) return true
  // Team member: only billing owner can manage
  return data.value.teamInfo.isBillingOwner
})

function toggleDay(day: string) {
  const s = new Set(expandedDays.value)
  if (s.has(day)) s.delete(day)
  else s.add(day)
  expandedDays.value = s
}

function formatDay(day: string): string {
  const d = new Date(day + 'T00:00:00Z')
  return d.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric', timeZone: 'UTC' })
}

function fmtPeriodDate(s: string): string {
  return new Date(s).toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
}

const llmPeriodLabel = computed(() => {
  if (!llmUsage.value?.periodStart || !llmUsage.value?.periodEnd) {
    if (!data.value?.billingPeriodStart || !data.value?.billingPeriodEnd) return ''
    return `${fmtPeriodDate(data.value.billingPeriodStart)} – ${fmtPeriodDate(data.value.billingPeriodEnd)}`
  }
  return `${fmtPeriodDate(llmUsage.value.periodStart)} – ${fmtPeriodDate(llmUsage.value.periodEnd)}`
})

async function loadLLMUsage(date?: string) {
  llmLoading.value = true
  expandedDays.value = new Set()
  try {
    const resp = await fetchLLMUsage(date)
    llmUsage.value = resp
  } catch {
    llmUsage.value = null
  } finally {
    llmLoading.value = false
  }
}

const isCurrentPeriod = computed(() => {
  if (!llmUsage.value?.periodEnd) return true
  return new Date(llmUsage.value.periodEnd) >= new Date()
})

function llmPeriodPrev() {
  if (!llmUsage.value?.periodStart) return
  const d = new Date(llmUsage.value.periodStart)
  d.setUTCDate(d.getUTCDate() - 1)
  loadLLMUsage(d.toISOString().slice(0, 10))
}

function llmPeriodNext() {
  if (isCurrentPeriod.value || !llmUsage.value?.periodEnd) return
  loadLLMUsage(new Date(llmUsage.value.periodEnd).toISOString().slice(0, 10))
}

const usageBarSeverity = computed(() => {
  const pct = data.value?.credits.monthlyUsedPct ?? 0
  if (pct >= 90) return 'danger'
  if (pct >= 75) return 'warning'
  if (pct >= 50) return 'info'
  return 'success'
})

const transactionHistory = computed(() => {
  if (!data.value) return []
  
  // Merge purchases and gifts
  const purchases = (data.value.credits.purchases || []).map(p => ({
    type: 'Purchase',
    amount: `+$${p.amount}`,
    date: p.date,
    details: '',
    receiptURL: p.receiptURL,
    rawDate: new Date(p.date)
  }))
  
  const gifts = (data.value.credits.gifts || []).map(g => ({
    type: 'Gift',
    amount: `+$${g.amount}`,
    date: g.date || '',
    details: g.reason,
    receiptURL: '',
    rawDate: g.date ? new Date(g.date) : new Date(0)
  }))
  
  // Combine and sort by date (newest first)
  return [...purchases, ...gifts].sort((a, b) => b.rawDate.getTime() - a.rawDate.getTime())
})

const downloadingReceipts = ref(false)
const receiptsAvailable = computed(() =>
  (data.value?.credits?.purchases ?? []).some(p => p.receiptURL)
)

async function downloadReceipts() {
  downloadingReceipts.value = true
  try {
    const resp = await fetch('/api/receipts/download', { credentials: 'same-origin' })
    if (!resp.ok) throw new Error(await resp.text())
    const blob = await resp.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'receipts.zip'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  } catch (err: any) {
    console.error('receipt download failed:', err)
  } finally {
    downloadingReceipts.value = false
  }
}

// API Key modal
const apiKeyModal = reactive({
  visible: false,
  label: '',
  vm: '',
  expiry: '30d',
  cmds: [] as string[],
  running: false,
  error: '',
  result: null as { label: string; token: string; namespace: string; fingerprint: string; expires_at?: string } | null,
})

const defaultCmds = new Set(['help', 'ls', 'new', 'whoami', 'ssh-key list', 'share show'])
const availableCommands = [
  { value: 'ls' },
  { value: 'new' },
  { value: 'rm' },
  { value: 'restart' },
  { value: 'rename' },
  { value: 'whoami' },
  { value: 'help' },
  { value: 'cp' },
  { value: 'tag' },
  { value: 'ssh-key list' },
  { value: 'ssh-key add' },
  { value: 'ssh-key remove' },
  { value: 'ssh-key generate-api-key' },
  { value: 'share show' },
  { value: 'share add' },
  { value: 'share remove' },
  { value: 'share set-public' },
  { value: 'share set-private' },
  { value: 'integrations add' },
  { value: 'integrations remove' },
  { value: 'integrations attach' },
  { value: 'integrations detach' },
  { value: 'shelley prompt' },
].map(c => ({ ...c, isDefault: defaultCmds.has(c.value) }))

const apiKeyBuiltCommand = computed(() => {
  const parts = ['ssh-key generate-api-key']
  if (apiKeyModal.label.trim()) {
    parts.push(`--label=${shellQuote(apiKeyModal.label.trim())}`)
  }
  if (apiKeyModal.vm) {
    parts.push(`--vm=${shellQuote(apiKeyModal.vm)}`)
  }
  if (!apiKeyModal.vm && apiKeyModal.cmds.length > 0) {
    parts.push(`--cmds=${shellQuote(apiKeyModal.cmds.join(','))}`)
  }
  parts.push(`--exp=${apiKeyModal.expiry}`)
  parts.push('--json')
  return parts.join(' ')
})

function openCreateAPIKey() {
  apiKeyModal.visible = true
  apiKeyModal.label = ''
  apiKeyModal.vm = ''
  apiKeyModal.expiry = '30d'
  apiKeyModal.cmds = []
  apiKeyModal.running = false
  apiKeyModal.error = ''
  apiKeyModal.result = null
}

function closeApiKeyModal() {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  apiKeyModal.visible = false
  if (apiKeyModal.result) {
    reload()
  }
}

async function runApiKeyCommand() {
  apiKeyModal.running = true
  apiKeyModal.error = ''
  try {
    const resp = await runCommand(apiKeyBuiltCommand.value)
    if (resp.success && resp.output) {
      try {
        const parsed = JSON.parse(resp.output)
        apiKeyModal.result = parsed
      } catch {
        apiKeyModal.error = resp.output
      }
    } else {
      apiKeyModal.error = resp.error || resp.output || 'Command failed'
    }
  } catch (err: any) {
    apiKeyModal.error = err.message || 'Request failed'
  } finally {
    apiKeyModal.running = false
  }
}

const apiKeyUsageHost = computed(() => {
  if (!apiKeyModal.result) return ''
  // namespace is like "v0@exe.dev" or "v0@vmname.exe.xyz"
  const ns = apiKeyModal.result.namespace
  const at = ns.indexOf('@')
  return at >= 0 ? ns.slice(at + 1) : ns
})

const apiKeyUsageExample = computed(() => {
  if (!apiKeyModal.result) return ''
  const host = apiKeyUsageHost.value
  const token = apiKeyModal.result.token
  if (apiKeyModal.vm) {
    return `curl -H "Authorization: Bearer ${token}" https://${host}/`
  }
  return `curl -X POST https://${host}/exec -H "Authorization: Bearer ${token}" -d 'whoami'`
})

function formatRenewalDate(dateStr: string): string {
  if (!dateStr) return ''
  const d = new Date(dateStr)
  return d.toLocaleDateString('en-US', { month: 'long', day: 'numeric', year: 'numeric' })
}

function paymentBrandName(pm: { type: string; brand?: string }): string {
  if (pm.brand) {
    const names: Record<string, string> = {
      visa: 'Visa', mastercard: 'Mastercard', amex: 'American Express',
      discover: 'Discover', diners: 'Diners Club', jcb: 'JCB',
      unionpay: 'UnionPay', maestro: 'Maestro',
    }
    return names[pm.brand.toLowerCase()] ?? (pm.brand.charAt(0).toUpperCase() + pm.brand.slice(1))
  }
  if (pm.type === 'link') return 'Link'
  if (pm.type === 'paypal') return 'PayPal'
  return pm.type.charAt(0).toUpperCase() + pm.type.slice(1)
}

function paymentIconUrl(pm: { type: string; brand?: string }): string {
  const brand = pm.brand?.toLowerCase()
  const base = '/payment-icons'
  const supported = ['visa','mastercard','amex','discover','diners','jcb','maestro','unionpay','paypal']
  if (brand && supported.includes(brand)) return `${base}/${brand}.svg`
  if (pm.type === 'paypal') return `${base}/paypal.svg`
  if (pm.type === 'link') return `${base}/link.svg`
  return `${base}/default.svg`
}

function formatExpiry(iso: string | undefined): string {
  if (!iso) return 'Never'
  const d = new Date(iso)
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}


// Escape key handler
function openRegionModal() {
  selectedRegion.value = data.value?.user.region ?? ''
  regionError.value = ''
  showRegionModal.value = true
}

async function saveRegion() {
  if (!data.value || regionSaving.value) return
  regionSaving.value = true
  regionError.value = ''
  try {
    const resp = await fetch('/api/profile/region', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ region: selectedRegion.value }),
    })
    if (!resp.ok) {
      const msg = await resp.text()
      regionError.value = msg || 'Failed to update region'
      return
    }
    const result = await resp.json()
    data.value.user.region = result.region
    data.value.user.regionDisplay = result.regionDisplay
    showRegionModal.value = false
  } finally {
    regionSaving.value = false
  }
}

function onEscapeKey(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (apiKeyModal.visible) { closeApiKeyModal(); return }
  if (showRegionModal.value) { showRegionModal.value = false; return }
  if (showSupportModal.value) { closeSupportModal(); return }
}

const modal = reactive({
  visible: false,
  title: '',
  description: '',
  command: '',
  commandPrefix: '',
  inputPlaceholder: '',
  defaultValue: '',
  danger: false,
  choices: [] as { value: string; label?: string; hint?: string; disabled?: boolean; disabledReason?: string }[],
  defaultChoice: '',
})

async function reload() {
  try {
    data.value = await fetchProfile()
  } catch (err) {
    console.error('Failed to load profile:', err)
  }
}

async function loadProfile() {
  loading.value = true
  loadError.value = ''
  try {
    data.value = await fetchProfile()
    // Fetch LLM usage and billing usage non-blocking.
    if (data.value.billingPeriodStart && data.value.billingPeriodEnd) {
      loadLLMUsage()
      loadBillingUsage()
    }
    // Start live metrics polling.
    loadLiveMetrics()
  } catch (err: any) {
    console.error('Failed to load profile:', err)
    loadError.value = err.message || 'Failed to load data'
  } finally {
    loading.value = false
  }
}

async function loadLiveMetrics() {
  try {
    liveMetrics.value = await fetchVMsLive()
  } catch {
    // Silently ignore — metrics are best-effort.
  }
}

async function loadBillingUsage() {
  if (!data.value?.billingPeriodStart || !data.value?.billingPeriodEnd) return
  try {
    const resp = await fetchVMUsage(data.value.billingPeriodStart, data.value.billingPeriodEnd)
    billingUsage.value = resp.metrics || []
  } catch {
    // Silently ignore.
  }
}

const totalBandwidthBytes = computed(() => {
  return billingUsage.value.reduce((sum, vm) => sum + vm.bandwidth_bytes, 0)
})

const totalDiskProvisionedBytes = computed(() => {
  if (!liveMetrics.value) return 0
  return liveMetrics.value.vms.reduce((sum, vm) => sum + vm.disk_capacity_bytes, 0)
})

const totalDiskIncludedBytes = computed(() => {
  if (!data.value?.planCapacity || !liveMetrics.value) return 0
  const vmCount = liveMetrics.value.vms.length || 1
  return data.value.planCapacity.defaultDiskGB * 1024 * 1024 * 1024 * vmCount
})

function vmBandwidth(vmName: string): number {
  const entry = billingUsage.value.find(e => e.vm_name === vmName)
  return entry ? entry.bandwidth_bytes : 0
}

function startLiveMetricsPolling() {
  if (liveMetricsTimer) return
  liveMetricsTimer = setInterval(loadLiveMetrics, 10_000)
}

function stopLiveMetricsPolling() {
  if (liveMetricsTimer) {
    clearInterval(liveMetricsTimer)
    liveMetricsTimer = null
  }
}

function meterColor(pct: number): string {
  if (pct >= 90) return 'red'
  if (pct >= 70) return 'yellow'
  return 'green'
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const gb = bytes / (1024 * 1024 * 1024)
  if (gb >= 1) return gb.toFixed(1) + ' GB'
  const mb = bytes / (1024 * 1024)
  return mb.toFixed(0) + ' MB'
}

function cpuMiniWidth(cpuPercent: number, poolMax: number): string {
  if (poolMax === 0) return '0%'
  const pct = Math.min((cpuPercent / 100 / poolMax) * 100, 100)
  return pct + '%'
}

onMounted(() => {
  document.addEventListener('keydown', onEscapeKey)
  loadProfile()
  startLiveMetricsPolling()
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onEscapeKey)
  stopLiveMetricsPolling()
})

function openModal(opts: Partial<typeof modal>) {
  Object.assign(modal, {
    visible: true,
    title: '',
    description: '',
    command: '',
    commandPrefix: '',
    inputPlaceholder: '',
    defaultValue: '',
    danger: false,
    choices: [] as { value: string; label?: string; hint?: string; disabled?: boolean; disabledReason?: string }[],
    defaultChoice: '',
    ...opts,
  })
}

function addSSHKey() {
  openModal({
    title: 'Add SSH Key',
    description: 'Generate a key:<pre>ssh-keygen -t ed25519 -C "my-key" -f ~/.ssh/id_exe</pre>Then paste the contents of <code>~/.ssh/id_exe.pub</code> below.',
    commandPrefix: 'ssh-key add',
    inputPlaceholder: 'ssh-ed25519 AAAA... comment',
  })
}

function removeSSHKey(pubKey: string) {
  openModal({
    title: 'Remove SSH Key',
    command: `ssh-key remove ${shellQuote(pubKey)}`,
    description: 'Remove this SSH key from your account. You won\'t be able to authenticate with it anymore. API keys (if any) generated from this key will no longer work.',
    danger: true,
  })
}

function renameSSHKey(name: string, fingerprint: string) {
  openModal({
    title: 'Rename SSH Key',
    commandPrefix: `ssh-key rename ${shellQuote('SHA256:' + fingerprint)}`,
    inputPlaceholder: 'new name',
    defaultValue: name,
    description: 'Change the display name for this SSH key.',
  })
}

function removeTeamMember(email: string) {
  openModal({
    title: 'Remove Team Member',
    command: `team remove ${shellQuote(email)}`,
    description: 'Remove this member from your team. They will lose access to all team-shared VMs.',
    danger: true,
  })
}

// Only billing owners can promote/demote other billing owners.
function canChangeRole(m: { role: string }): boolean {
  const ti = data.value?.teamInfo
  if (!ti?.isAdmin) return false
  if (m.role === 'billing_owner') return !!ti.isBillingOwner
  return true
}

function roleChoices(currentRole: string) {
  const ti = data.value?.teamInfo
  const allRoles: { value: string; label: string; hint: string }[] = [
    { value: 'user', label: 'User', hint: 'Access to team-shared VMs' },
    { value: 'admin', label: 'Admin', hint: 'Can invite and manage members' },
    { value: 'billing_owner', label: 'Billing owner', hint: 'Owns billing and team settings' },
  ]
  return allRoles.map(r => {
    const c: { value: string; label: string; hint: string; disabled?: boolean; disabledReason?: string } = { ...r }
    if (r.value === currentRole) {
      c.disabled = true
      c.disabledReason = 'current'
    } else if (r.value === 'billing_owner' && !ti?.isBillingOwner) {
      c.disabled = true
      c.disabledReason = 'billing owners only'
    }
    return c
  })
}

function changeTeamMemberRole(email: string, currentRole: string) {
  const choices = roleChoices(currentRole)
  openModal({
    title: 'Change Team Member Role',
    commandPrefix: `team role ${shellQuote(email)}`,
    choices,
    description: `Change the role for <strong>${email}</strong> (currently <em>${currentRole}</em>).`,
  })
}

function inviteTeamMember() {
  // No 'current' role for a new invite — pass empty string so none are disabled
  // beyond the billing-owner permission check.
  openModal({
    title: 'Invite to Team',
    commandPrefix: 'team add',
    inputPlaceholder: 'user@example.com',
    choices: roleChoices(''),
    defaultChoice: 'user',
    description: 'Invite a user to join your team. They\'ll get access to team-shared VMs with the selected role.',
  })
}

function transferVM() {
  openModal({
    title: 'Transfer VM',
    commandPrefix: 'team transfer',
    inputPlaceholder: 'vm-name user@example.com',
    description: 'Transfer a VM to another team member. They will become the owner.',
  })
}

function disableTeam() {
  openModal({
    title: 'Disable Team',
    command: 'team disable',
    description: 'Disable your team. This will remove all team shares, cancel pending invites, and delete team auth/SSO configuration. Your VMs remain on your personal account.',
    danger: true,
  })
}

async function acceptInvite(token: string) {
  try {
    const resp = await fetch('/team/invite/accept', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ token }),
      redirect: 'follow',
    })
    if (resp.redirected) {
      window.location.href = resp.url
    } else {
      await reload()
    }
  } catch {
    await reload()
  }
}

async function declineInvite(token: string) {
  try {
    await fetch('/team/invite/decline', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ token }),
    })
  } catch { /* ignore */ }
  await reload()
}

// --- Team creation ---

async function createTeam() {
  const name = teamName.value.trim()
  if (!name) {
    teamError.value = 'Team name is required.'
    return
  }
  teamError.value = ''
  creatingTeam.value = true
  try {
    const resp = await fetch('/team/enable', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ name }),
    })
    const result = await resp.json()
    if (result.success) {
      await reload()
      teamName.value = ''
    } else {
      teamError.value = result.error || 'Failed to create team.'
    }
  } catch {
    teamError.value = 'Request failed.'
  } finally {
    creatingTeam.value = false
  }
}

// --- Passkey management ---

function base64URLEncode(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  const binary = Array.from(bytes, b => String.fromCharCode(b)).join('')
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '')
}

function base64URLDecode(str: string): ArrayBuffer {
  str = str.replace(/-/g, '+').replace(/_/g, '/')
  while (str.length % 4) str += '='
  const binary = atob(str)
  return Uint8Array.from(binary, c => c.charCodeAt(0)).buffer
}

function getDefaultPasskeyName(): string {
  const ua = navigator.userAgent
  if (/iPhone/.test(ua)) return 'iPhone'
  if (/iPad/.test(ua)) return 'iPad'
  if (/Macintosh/.test(ua)) return 'Mac'
  if (/Windows/.test(ua)) return 'Windows'
  if (/Android/.test(ua)) return 'Android'
  if (/Linux/.test(ua)) return 'Linux'
  return 'Passkey'
}

async function addPasskey() {
  passkeyError.value = ''
  const name = passkeyName.value.trim() || getDefaultPasskeyName()

  try {
    const startResp = await fetch('/passkey/register/start', { method: 'POST', credentials: 'same-origin' })
    if (!startResp.ok) throw new Error(await startResp.text() || 'Failed to start registration')
    const options = await startResp.json()

    options.publicKey.challenge = base64URLDecode(options.publicKey.challenge)
    options.publicKey.user.id = base64URLDecode(options.publicKey.user.id)
    if (options.publicKey.excludeCredentials) {
      options.publicKey.excludeCredentials = options.publicKey.excludeCredentials.map((c: any) => ({ ...c, id: base64URLDecode(c.id) }))
    }

    const credential = await navigator.credentials.create(options) as PublicKeyCredential
    if (!credential) throw new Error('No credential created')
    const response = credential.response as AuthenticatorAttestationResponse

    const body: any = {
      id: credential.id,
      rawId: base64URLEncode(credential.rawId),
      type: credential.type,
      response: {
        clientDataJSON: base64URLEncode(response.clientDataJSON),
        attestationObject: base64URLEncode(response.attestationObject),
      },
    }
    if (response.getTransports) {
      body.response.transports = response.getTransports()
    }

    const finishResp = await fetch('/passkey/register/finish?name=' + encodeURIComponent(name), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(body),
    })
    if (!finishResp.ok) throw new Error(await finishResp.text() || 'Failed to complete registration')

    passkeyName.value = ''
    await reload()
  } catch (err: any) {
    passkeyError.value = err.message || 'Failed to add passkey'
  }
}

async function confirmDeletePasskey(id: number) {
  try {
    await fetch('/passkey/delete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ id: String(id) }),
    })
  } catch { /* ignore */ }
  deletingPasskeys.value.delete(id)
  await reload()
}

// --- Newsletter ---

async function toggleNewsletter(event: Event) {
  const checked = (event.target as HTMLInputElement).checked
  newsletterStatus.value = 'Saving...'
  try {
    const resp = await fetch('/newsletter-subscribe', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      credentials: 'same-origin',
      body: new URLSearchParams({ subscribed: checked ? '1' : '0' }),
    })
    if (!resp.ok) throw new Error('Failed')
    newsletterStatus.value = checked ? 'Subscribed!' : 'Unsubscribed'
    if (data.value) data.value.user.newsletterSubscribed = checked
    setTimeout(() => { newsletterStatus.value = '' }, 2000)
  } catch {
    newsletterStatus.value = 'Error saving'
    setTimeout(() => { newsletterStatus.value = '' }, 2000)
  }
}
</script>

<style scoped>
.profile-page {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.loading-state {
  text-align: center;
  padding: 48px;
  color: var(--text-color-secondary);
}

.error-state {
  text-align: center;
  padding: 48px;
  color: var(--danger-text);
}

.error-state p {
  margin-bottom: 12px;
}

.card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 20px;
}

.card-title {
  font-size: 18px;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 12px;
}

.card-header-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.card-header-row .card-title {
  margin-bottom: 0;
}

.subsection-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-color-secondary);
  margin-bottom: 8px;
  margin-top: 16px;
}

.info-grid {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.info-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}

.info-label {
  color: var(--text-color-muted);
  min-width: 80px;
}

.info-value {
  color: var(--text-color);
}

.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-box {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 20px;
  max-width: 360px;
  width: 90%;
}

.modal-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 12px;
}

.modal-text {
  font-size: 13px;
  color: var(--text-color-secondary);
  margin-bottom: 8px;
}

.modal-actions {
  margin-top: 16px;
  display: flex;
  justify-content: flex-end;
}

.inline-link {
  font-size: 12px;
}

.text-muted {
  color: var(--text-color-muted);
  font-size: 12px;
}

/* Billing Section */
.billing-card-inner {
  background: var(--surface-ground);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 24px;
}

/* Plan Section */
.billing-plan-section {
  padding-bottom: 4px;
}
.billing-plan-row {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
}
.billing-plan-info {
  flex: 1;
  min-width: 0;
}
.billing-plan-name-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 4px;
}
.plan-name {
  font-size: 20px;
  font-weight: 600;
  margin: 0;
}
.active-tag {
  background: var(--text-color) !important;
  color: var(--surface-ground) !important;
}
.trial-expiry {
  font-size: 13px;
  color: var(--warning-text);
}
.trial-expired {
  font-size: 13px;
  color: var(--danger-text);
  font-weight: 600;
}
.billing-plan-desc {
  font-size: 14px;
  color: var(--text-color-secondary);
  margin-bottom: 2px;
}
.billing-plan-price {
  font-size: 28px;
  font-weight: 600;
  margin: 8px 0 4px;
}
.billing-plan-interval {
  font-size: 14px;
  font-weight: 400;
  color: var(--text-color-secondary);
}
.billing-plan-renewal {
  font-size: 13px;
  color: var(--text-color-secondary);
  margin-top: 2px;
}
.billing-plan-action {
  flex-shrink: 0;
  padding-top: 2px;
  display: flex;
  align-items: center;
  gap: 12px;
}
.billing-pricing-link {
  font-size: 13px;
  color: var(--text-color-secondary);
  text-decoration: none;
  white-space: nowrap;
}
.billing-pricing-link:hover {
  color: var(--text-color);
  text-decoration: underline;
}
.btn-upgrade {
  background: transparent !important;
  color: var(--warning-color) !important;
  border-color: var(--warning-color) !important;
}
.btn-upgrade:hover {
  background: var(--warning-bg) !important;
}

/* Plan limits (VMs, disk, transfer) */
.billing-plan-limits {
  font-size: 13px;
}
.billing-plan-limits sup {
  font-size: 9px;
}


/* Managed by billing admins */
.billing-managed-by {
  margin-top: 12px;
  font-size: 13px;
  color: var(--text-color-secondary);
}

/* Upsell */
.billing-upsell {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 16px;
  padding: 10px 14px;
  background: var(--surface-section, var(--surface-hover));
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  font-size: 13px;
}
.billing-upsell-text {
  color: var(--text-color-secondary);
}
.billing-upsell-text strong {
  color: var(--text-color);
}
.billing-upsell-link {
  font-size: 13px;
  font-weight: 500;
  color: var(--primary-color);
  text-decoration: none;
  white-space: nowrap;
}
.billing-upsell-link:hover {
  text-decoration: underline;
}

/* Resource Usage section */
.billing-divider {
  border-top: 1px solid var(--surface-border);
  margin: 16px 0;
}
.resource-usage-header {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 14px;
}
.resource-meters {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.resource-meter {
  display: flex;
  align-items: center;
  gap: 12px;
}
.meter-label {
  width: 80px;
  font-size: 12px;
  color: var(--text-color-secondary);
  flex-shrink: 0;
}
.meter-bar-wrap {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.meter-bar {
  height: 8px;
  background: var(--surface-border);
  border-radius: 4px;
  overflow: hidden;
}
.meter-fill {
  height: 100%;
  border-radius: 4px;
  transition: width 0.3s;
}
.meter-fill.green { background: var(--green-500, #22c55e); }
.meter-fill.yellow { background: var(--yellow-500, #eab308); }
.meter-fill.red { background: var(--red-500, #ef4444); }
.meter-values {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  color: var(--text-color-secondary);
}
.meter-used {
  color: var(--text-color);
  font-weight: 500;
}

/* Per-VM breakdown */
.vm-breakdown {
  margin-top: 16px;
}
.vm-breakdown-header {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 10px;
}
.vm-breakdown-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.vm-breakdown-table th {
  text-align: right;
  padding: 4px 8px;
  font-weight: 600;
  font-size: 11px;
  color: var(--text-color-secondary);
  border-bottom: 1px solid var(--surface-border);
}
.vm-breakdown-table th:first-child { text-align: left; }
.vm-breakdown-table td {
  padding: 6px 8px;
  text-align: right;
  border-bottom: 1px solid var(--surface-hover, #f1f5f9);
}
.vm-breakdown-table td:first-child { text-align: left; }
.vm-breakdown-table tr:last-child td { border-bottom: none; }
.vm-breakdown-table tr:hover td { background: var(--surface-hover, #f8fafc); }
.vm-name-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}
.status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
}
.status-dot.running { background: var(--green-500, #22c55e); }
.status-dot.stopped { background: var(--text-color-secondary); }
.vm-link {
  color: var(--text-color);
  text-decoration: none;
  font-weight: 500;
}
.vm-link:hover { color: var(--primary-color); }
.cpu-cell {
  display: flex;
  align-items: center;
  gap: 6px;
  justify-content: flex-end;
}
.cpu-mini-bar {
  width: 40px;
  height: 4px;
  background: var(--surface-border);
  border-radius: 2px;
  overflow: hidden;
}
.cpu-mini-fill {
  height: 100%;
  border-radius: 2px;
}
.cpu-mini-fill.green { background: var(--green-500, #22c55e); }
.cpu-mini-fill.yellow { background: var(--yellow-500, #eab308); }
.cpu-mini-fill.red { background: var(--red-500, #ef4444); }
.text-muted { color: var(--text-color-secondary); }
.totals-row td {
  font-weight: 600;
  border-top: 1px solid var(--surface-border) !important;
  color: var(--text-color);
}

/* Divider sections (Payment, Invoices) */
.billing-divider-section {
  border-top: 1px solid var(--surface-border);
  margin-top: 20px;
  padding-top: 20px;
}
.billing-section-header {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 12px;
}
.billing-section-header-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

/* Payment method */
.payment-method-callout {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.pm-left {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}
.pm-icon-img {
  width: 36px;
  height: 24px;
  object-fit: contain;
  border-radius: 4px;
  flex-shrink: 0;
}
.pm-label {
  font-size: 14px;
  color: var(--text-color);
}
.pm-managed-badge {
  font-size: 12px;
  color: var(--text-color-secondary);
  background: var(--surface-border);
  padding: 2px 8px;
  border-radius: 4px;
  white-space: nowrap;
}

/* Credit Cards Grid — always side-by-side */
.credits-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0;
  margin-bottom: 24px;
  border-top: 1px solid var(--surface-border);
  padding-top: 16px;
}

.credit-card {
  padding: 0 12px;
}

.credit-card:first-child {
  padding-left: 0;
  border-right: 1px solid var(--surface-border);
}

.credit-card:last-child {
  padding-right: 0;
}

.credit-card-title {
  font-size: 10px;
  font-weight: 600;
  color: var(--text-color-muted);
  letter-spacing: 0.8px;
  margin-bottom: 6px;
}

.credit-card-amount {
  font-size: 28px;
  font-weight: 600;
  line-height: 1;
  margin-bottom: 4px;
}

.credit-card-amount.credit-depleted {
  color: var(--text-color-muted);
}

.credit-card-detail {
  font-size: 11px;
  color: var(--text-color-muted);
  line-height: 1.5;
}

/* Usage Section */
.usage-section {
  margin-bottom: 16px;
}

.usage-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
  font-size: 12px;
}

.usage-label {
  font-weight: 600;
  font-size: 10px;
  letter-spacing: 0.8px;
  text-transform: uppercase;
  color: var(--text-color-muted);
}

.usage-pct {
  font-weight: 600;
  font-size: 12px;
}

.usage-footer {
  margin-top: 4px;
  font-size: 11px;
  color: var(--text-color-muted);
}

/* Buy Section */
.buy-section {
  padding: 20px;
  border-top: 1px solid var(--surface-border);
  margin: 0 -24px 24px;
  padding: 20px 24px;
}

.buy-label {
  font-size: 13px;
  margin-bottom: 12px;
}

.buy-form {
  display: flex;
  gap: 12px;
  align-items: center;
}

.buy-amounts {
  display: flex;
  gap: 8px;
  flex: 1;
}

.amount-btn {
  padding: 8px 16px;
  border: 1px solid var(--surface-border);
  background: transparent;
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  color: var(--text-color);
  transition: all 0.15s;
}

.amount-btn:hover {
  background: var(--surface-hover);
}

.amount-btn-selected {
  border-color: var(--text-color);
  background: transparent;
  color: var(--text-color);
}

.buy-btn {
  background: var(--text-color);
  color: var(--surface-ground);
  border: none;
  padding: 10px 24px;
  font-weight: 600;
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  transition: all 0.15s;
}

.buy-btn:hover {
  filter: brightness(0.9);
}

/* Invoices */
.view-all-link {
  font-size: 13px;
  color: var(--text-color-muted);
  text-decoration: none;
}
.view-all-link:hover {
  color: var(--text-color);
  text-decoration: underline;
}
.invoice-list {
  list-style: none;
  padding: 0;
  margin: 0;
}
.invoice-item {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 10px 0;
  border-bottom: 1px solid var(--surface-border);
}
.invoice-item:last-child {
  border-bottom: none;
}
.invoice-info {
  flex: 1;
  min-width: 0;
}
.invoice-desc {
  font-size: 14px;
  font-weight: 500;
  display: block;
}
.invoice-period {
  font-size: 12px;
  color: var(--text-color-muted);
  display: block;
}
.invoice-right {
  text-align: right;
  flex-shrink: 0;
  min-width: 70px;
}
.invoice-amount {
  font-size: 14px;
  font-weight: 600;
}
.invoice-amount-zero {
  font-weight: 400;
  color: var(--text-color-secondary);
  font-size: 13px;
}
.invoice-credit {
  display: block;
  font-size: 12px;
  color: var(--text-color-secondary);
  font-weight: 500;
  white-space: nowrap;
}
.invoice-credit-generated {
  display: block;
  font-size: 12px;
  color: var(--green-600, #16a34a);
  font-weight: 500;
  white-space: nowrap;
}
.credit-balance-line {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 0 12px;
}
.credit-balance-label {
  font-weight: 600;
  font-size: 14px;
  display: block;
}
.credit-balance-sub {
  font-size: 13px;
  color: var(--text-color-secondary);
  display: block;
}
.credit-balance-amount {
  font-size: 14px;
  font-weight: 700;
  color: var(--green-600, #16a34a);
}
.invoice-status-col {
  flex-shrink: 0;
  min-width: 70px;
  text-align: right;
}
.invoice-link {
  text-decoration: none;
}
.invoice-link .invoice-badge {
  text-decoration: underline;
  text-underline-offset: 2px;
}
.invoice-badge {
  font-size: 12px;
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-weight: 500;
}
.invoice-badge-paid {
  color: #4CAF50;
  background: #e8f5e9;
}
.invoice-badge-open {
  color: #f57c00;
  background: #fff3e0;
}
.invoice-badge-upcoming {
  color: #1976d2;
  background: #e3f2fd;
}

/* Transaction Section */
.transaction-section {
  border-top: 1px solid var(--surface-border);
  margin: 0 -24px 20px;
  padding: 20px 24px 0;
}

.transaction-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.tx-section-title {
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.8px;
  text-transform: uppercase;
  color: var(--text-color-muted);
}

.tx-download-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.tx-download-btn .pi {
  font-size: 12px;
}

.tx-list {
  list-style: none;
  padding: 0;
  margin: 0;
}

.tx-item {
  display: flex;
  align-items: center;
  padding: 10px 0;
  border-bottom: 1px solid var(--surface-border);
  gap: 12px;
}

.tx-item:last-child {
  border-bottom: none;
}

.tx-icon {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  flex-shrink: 0;
}

.tx-icon-purchase {
  background: #e8f5e9;
  color: #2e7d32;
}

.tx-icon-gift {
  background: #fce4ec;
  color: #c62828;
}

.tx-info {
  flex: 1;
  min-width: 0;
}

.tx-type {
  font-size: 13px;
  font-weight: 500;
  display: block;
}

.tx-date {
  font-size: 11px;
  color: var(--text-color-muted);
}

.tx-right {
  text-align: right;
  flex-shrink: 0;
}

.tx-amount {
  font-size: 14px;
  font-weight: 600;
  display: block;
}

.tx-receipt {
  font-size: 11px;
  color: var(--text-color-muted);
  text-decoration: none;
}

.tx-receipt:hover {
  color: var(--text-color);
  text-decoration: underline;
}

/* LLM Usage Section */
.llm-usage-section {
  margin-top: 0;
  padding-top: 20px;
}

.llm-usage-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.llm-usage-title {
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--text-color-secondary);
}

.llm-period-nav {
  display: flex;
  align-items: center;
  gap: 6px;
}

.llm-period-btn {
  background: none;
  border: none;
  padding: 0 4px;
  font-size: 16px;
  line-height: 1;
  color: var(--text-color-secondary);
  cursor: pointer;
  border-radius: 3px;
}

.llm-period-btn:hover:not(.disabled) {
  color: var(--text-color);
  background: var(--surface-hover);
}

.llm-period-btn.disabled {
  color: var(--text-color-muted);
  opacity: 0.4;
  cursor: default;
}

.llm-usage-period {
  font-size: 11px;
  color: var(--text-color-muted);
  min-width: 100px;
  text-align: center;
}

.llm-empty {
  font-size: 12px;
  color: var(--text-color-muted);
  padding: 8px 0;
}

.llm-day-group {
  margin-bottom: 8px;
}

.llm-day-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 0;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-color-secondary);
  border-bottom: 1px solid var(--surface-border);
  cursor: pointer;
  user-select: none;
}

.llm-day-header:hover {
  color: var(--text-color);
}

.llm-day-chevron {
  display: inline-block;
  width: 12px;
  font-size: 10px;
  color: var(--text-color-muted);
}

.llm-day-label {
  text-transform: uppercase;
  letter-spacing: 0.03em;
}

.llm-day-stats {
  font-weight: 500;
}

.llm-usage-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 5px 0 5px 12px;
  font-size: 13px;
}

.llm-usage-left {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.llm-usage-model {
  color: var(--text-color);
  font-family: var(--font-mono, 'JetBrains Mono', ui-monospace, monospace);
  font-size: 12px;
}

.llm-usage-box {
  color: var(--text-color-muted);
  font-size: 11px;
}

.llm-usage-stats {
  color: var(--text-color-secondary);
  font-size: 12px;
  white-space: nowrap;
  flex-shrink: 0;
}

.llm-usage-total {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 0 0;
  margin-top: 4px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-color);
}

/* Team credits banner */
.team-credits-banner {
  margin-bottom: 16px;
}

.team-credits-banner-content {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.team-credits-banner-content strong {
  font-size: 13px;
  font-weight: 600;
}

.team-credits-banner-content span {
  font-size: 12px;
  opacity: 0.85;
}

.learn-more-link {
  color: var(--text-color-muted);
  text-decoration: none;
  font-size: 12px;
}

.learn-more-link:hover {
  color: var(--text-color);
  text-decoration: underline;
}

@media (max-width: 768px) {
  .billing-card-inner {
    padding: 16px;
  }

  .billing-plan-row {
    flex-direction: column;
    gap: 12px;
  }

  .billing-upsell {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }

  .payment-method-callout {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }

  .meter-label {
    width: 60px;
    font-size: 11px;
  }

  .vm-breakdown-table {
    font-size: 11px;
  }

  .buy-section {
    margin: 0 -16px 24px;
    padding: 16px;
  }

  .buy-form {
    flex-direction: column;
    align-items: stretch;
  }
  
  .buy-amounts {
    width: 100%;
  }

  .amount-btn {
    flex: 1;
    padding: 8px 8px;
    min-width: 0;
  }

  .transaction-section {
    margin: 0 -16px 20px;
    padding: 16px 16px 0;
  }
}

/* ProgressBar severity colors */
:deep(.p-progressbar.p-progressbar-success .p-progressbar-value) {
  background: var(--success-color);
}

:deep(.p-progressbar.p-progressbar-info .p-progressbar-value) {
  background: var(--warning-color);
}

:deep(.p-progressbar.p-progressbar-warning .p-progressbar-value) {
  background: #f97316;
}

:deep(.p-progressbar.p-progressbar-danger .p-progressbar-value) {
  background: var(--danger-color);
}

/* SSH Keys */
.ssh-key-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--surface-subtle);
  gap: 12px;
}

.ssh-key-row:last-of-type {
  border-bottom: none;
}

.ssh-key-info {
  min-width: 0;
  flex: 1;
}

.ssh-key-name {
  font-weight: 500;
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  display: flex;
  align-items: center;
  gap: 6px;
}

.badge-muted {
  font-size: 10px;
  font-weight: 400;
  padding: 1px 6px;
  background: var(--surface-subtle);
  color: var(--text-color-muted);
  border-radius: 3px;
  white-space: nowrap;
}

.ssh-key-fp {
  font-size: 11px;
  color: var(--text-color-muted);
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ssh-key-actions {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
}

@media (max-width: 600px) {
  .ssh-key-row {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }

  .ssh-key-info {
    width: 100%;
  }

  .ssh-key-actions {
    align-self: flex-end;
  }
}


/* Passkeys */
.passkey-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--surface-subtle);
}

.passkey-name {
  font-weight: 500;
  font-size: 13px;
}

.passkey-actions {
  display: flex;
  align-items: center;
  gap: 4px;
}

.section-help {
  font-size: 12px;
  color: var(--text-color-muted);
  margin-bottom: 12px;
}

.add-passkey-form {
  display: flex;
  gap: 8px;
  margin-top: 12px;
  align-items: center;
}

.passkey-name-input {
  padding: 6px 10px;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  font-size: 12px;
  font-family: inherit;
  flex: 1;
  max-width: 300px;
  height: 30px;
  box-sizing: border-box;
}

.passkey-error {
  color: var(--danger-color);
  font-size: 12px;
  margin-top: 8px;
}

/* Newsletter */
.newsletter-label {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  font-size: 13px;
}

.newsletter-label input[type="checkbox"] {
  accent-color: var(--text-color);
  width: 16px;
  height: 16px;
}

.newsletter-status {
  font-size: 11px;
  color: var(--text-color-muted);
  margin-left: 4px;
}

/* Invite */
.invite-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px;
  background: var(--warning-bg);
  border: 1px solid var(--warning-color);
  border-radius: 6px;
}

.invite-actions {
  display: flex;
  gap: 8px;
}

/* Members */
.member-list {
  margin-top: 12px;
}

.member-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 6px 0;
  font-size: 13px;
  border-bottom: 1px solid var(--surface-subtle);
}

/* Table */
.mini-table {
  width: 100%;
  font-size: 12px;
  border-collapse: collapse;
}

.mini-table th {
  text-align: left;
  font-weight: 500;
  color: var(--text-color-muted);
  padding: 6px 8px;
  border-bottom: 1px solid var(--surface-border);
}

.mini-table td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--surface-subtle);
}

.empty-msg {
  color: var(--text-color-muted);
  font-size: 13px;
}

.section-desc {
  font-size: 14px;
  line-height: 1.6;
  margin: 0 0 12px;
}

.section-desc.text-muted {
  font-size: 13px;
  color: var(--text-color-muted);
  margin-bottom: 16px;
}

.create-team-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.form-input {
  flex: 1;
  width: 100%;
  padding: 6px 10px;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  font-family: inherit;
  font-size: 13px;
  background: var(--input-bg);
  color: var(--input-text);
  box-sizing: border-box;
}

.form-input:focus {
  border-color: var(--primary-color);
  outline: none;
}

select.form-input {
  cursor: pointer;
}

.field-error {
  color: var(--danger-color);
  font-size: 13px;
  margin-top: 8px;
}

.text-link {
  font-size: 12px;
}

/* Extra credits */


/* Buttons */
.btn {
  padding: 5px 12px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
  border: 1px solid transparent;
  transition: all 0.15s;
  height: 30px;
  box-sizing: border-box;
}

.btn-small {
  padding: 3px 8px;
  font-size: 11px;
}

.btn-primary {
  background: var(--text-color);
  color: var(--surface-ground);
}

.btn-primary:hover {
  filter: brightness(1.1);
}

.btn-secondary {
  background: var(--btn-bg);
  color: var(--btn-text);
  border-color: var(--btn-border);
}

.btn-secondary:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
}

.btn-danger {
  background: var(--btn-bg);
  color: var(--danger-color);
  border-color: var(--danger-border);
}

.btn-danger:hover {
  background: var(--danger-bg);
}

/* Team admin actions */
.team-admin-actions {
  margin-top: 16px;
  display: flex;
  gap: 8px;
}

/* Danger zone */
.danger-zone {
  margin-top: 24px;
  padding-top: 16px;
  border-top: 1px solid var(--surface-border);
}

.danger-zone-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--danger-color);
  margin-bottom: 8px;
}

.danger-zone-text {
  margin: 0 0 12px;
  font-size: 13px;
  color: var(--text-color-muted);
}

/* Invite warning */
.invite-warning {
  font-size: 12px;
  color: var(--text-color-muted);
  margin-top: 4px;
}

/* API Key Modal */
.modal-panel {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  width: 520px;
  max-width: 90vw;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.2);
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--surface-border);
}

.modal-header h3 {
  font-size: 14px;
  font-weight: 600;
  margin: 0;
}

.modal-close {
  background: none;
  border: none;
  font-size: 20px;
  cursor: pointer;
  color: var(--text-color-muted);
  padding: 0 4px;
}

.modal-body {
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 20px;
  border-top: 1px solid var(--surface-border);
}

.form-row {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.form-row label {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-color-secondary);
}

.cmd-preview {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  background: var(--surface-subtle);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 8px 12px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 12px;
  word-break: break-all;
  color: var(--text-color-secondary);
}

.cmd-preview code {
  flex: 1;
  min-width: 0;
}

.cmd-preview :deep(.copy-btn) {
  flex-shrink: 0;
}

.cmd-result {
  padding: 8px 12px;
  border-radius: 4px;
  font-size: 12px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  white-space: pre-wrap;
}

.cmd-result.error {
  background: var(--danger-bg);
  color: var(--danger-text);
  border: 1px solid var(--danger-border);
}

.api-key-result {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.api-key-result-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}

.api-key-result-label {
  color: var(--text-color-muted);
  min-width: 60px;
  font-size: 12px;
}

.api-key-token-area {
  margin-top: 4px;
}

.api-key-token-label {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-color-secondary);
  display: block;
  margin-bottom: 6px;
}

.api-key-token-box {
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--surface-subtle);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 10px 12px;
}

.api-key-token-value {
  flex: 1;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 13px;
  word-break: break-all;
  user-select: all;
}

.api-key-warning {
  font-size: 12px;
  color: var(--warning-text, #b45309);
  background: var(--warning-bg, #fef3c7);
  border: 1px solid var(--warning-border, #f59e0b);
  border-radius: 4px;
  padding: 8px 12px;
}

.api-key-usage {
  margin-top: 2px;
}

.api-key-usage label {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-color-secondary);
  display: block;
  margin-bottom: 4px;
}

.cmd-checkboxes {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 4px 16px;
  max-height: 180px;
  overflow-y: auto;
  padding: 8px;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  background: var(--input-bg);
}

.cmd-checkbox {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  cursor: pointer;
  padding: 2px 0;
}

.cmd-checkbox input[type="checkbox"] {
  accent-color: var(--text-color);
  width: 14px;
  height: 14px;
  flex-shrink: 0;
}

.cmd-checkbox code {
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 11px;
}

.cmd-default-badge {
  font-size: 9px;
  color: var(--text-color-muted);
  background: var(--surface-subtle);
  padding: 0 4px;
  border-radius: 3px;
  line-height: 16px;
}

.api-key-revoke-hint {
  font-size: 11px;
  color: var(--text-color-muted);
}

.api-key-revoke-hint code {
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  background: var(--surface-subtle);
  padding: 1px 4px;
  border-radius: 3px;
  font-size: 11px;
}
/* Breadcrumbs */
.breadcrumbs {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-color-muted);
  margin-bottom: 12px;
}

.breadcrumb-link {
  color: var(--text-color-secondary);
  text-decoration: none;
}

.breadcrumb-link:hover {
  color: var(--text-color);
  text-decoration: underline;
}

.breadcrumb-sep {
  color: var(--text-color-muted);
}

.breadcrumb-current {
  color: var(--text-color);
  font-weight: 500;
}

/* Support section */
.support-files {
  margin-top: 8px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.support-file {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 13px;
}
.support-success {
  color: var(--green-600, #15803d);
  font-size: 13px;
  margin-top: 8px;
}
.btn-xs {
  padding: 2px 8px;
  font-size: 11px;
}
</style>
