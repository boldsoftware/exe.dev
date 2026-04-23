<template>
  <div class="integrations-page">
    <nav class="breadcrumbs" aria-label="Breadcrumb">
      <router-link to="/" class="breadcrumb-link">Home</router-link>
      <span class="breadcrumb-sep">›</span>
      <span class="breadcrumb-current">Integrations</span>
    </nav>

    <div v-if="loading" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>

    <div v-else-if="loadError" class="error-state">
      <p>Failed to load integrations: {{ loadError }}</p>
      <button class="btn btn-secondary" @click="loadIntegrations">Retry</button>
    </div>

    <template v-else-if="data">
      <!-- Page header -->
      <div class="page-title">Integrations</div>
      <p class="page-subtitle">Connect your VMs to external services. <a href="/docs/integrations" target="_blank" rel="noopener noreferrer">Learn more →</a></p>

      <div v-if="inlineMessage" class="inline-msg" :class="inlineMessageIsError ? 'inline-error' : 'inline-success'">
        {{ inlineMessage }}
        <button class="inline-msg-dismiss" @click="inlineMessage = ''">&times;</button>
      </div>

      <!-- Integration Grid -->
      <div class="integration-grid">
        <button class="grid-btn" @click="openGitHubSetup">
          <i class="pi pi-github grid-btn-icon"></i>
          <span class="grid-btn-label">GitHub</span>
          <span v-if="data.githubAccounts.length > 0" class="grid-btn-badge">{{ data.githubAccounts.length }}</span>
        </button>
        <button class="grid-btn" @click="openAddHTTPProxy">
          <i class="pi pi-globe grid-btn-icon"></i>
          <span class="grid-btn-label">HTTP Proxy</span>
        </button>
        <button class="grid-btn" @click="openAddReflection">
          <span class="reflection-icon grid-btn-icon" aria-hidden="true">
            <span class="reflection-frame">
              <img src="/exy.png" alt="" class="reflection-fish" />
              <span class="reflection-shimmer"></span>
            </span>
          </span>
          <span class="grid-btn-label">Reflection</span>
        </button>
        <button v-for="sp in serviceProxies" :key="sp.id" class="grid-btn" @click="openServiceProxy(sp.id)">
          <span class="grid-btn-icon grid-btn-icon-text" v-html="sp.icon"></span>
          <span class="grid-btn-label">{{ sp.label }}</span>
        </button>
      </div>

      <!-- Active Integrations Table -->
      <section v-if="allActiveIntegrations.length > 0" class="card">
        <div class="card-header-row">
          <h2 class="card-title">Active Integrations</h2>
        </div>
        <table class="integrations-table">
          <thead>
            <tr>
              <th class="col-name">Name</th>
              <th class="col-detail">Details</th>
              <th class="col-attach">Attached to</th>
              <th class="col-actions"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in allActiveIntegrations" :key="row.name">
              <td class="col-name">
                <div class="table-name-cell">
                  <span v-if="row.iconSvg" class="table-icon" v-html="row.iconSvg"></span>
                  <i v-else :class="row.iconClass" class="table-icon-pi"></i>
                  <div class="table-name-info">
                    <span class="table-name">{{ row.displayName }}</span>
                    <span v-if="row.isTeam" class="badge badge-team">team</span>
                  </div>
                </div>
              </td>
              <td class="col-detail">
                <div class="table-detail">
                  <template v-if="row.type === 'github'">
                    <span v-if="row.repositories.length > 0" class="text-muted">{{ row.repositories.join(', ') }}</span>
                    <div v-if="row.repositories.length > 0" class="usage-rows">
                      <div class="usage-row">
                        <code>git clone {{ integrationScheme }}://{{ row.name }}.{{ row.isTeam ? 'team' : 'int' }}.{{ boxHost }}/{{ row.repositories[0] }}.git</code>
                        <CopyButton :text="`git clone ${integrationScheme}://${row.name}.${row.isTeam ? 'team' : 'int'}.${boxHost}/${row.repositories[0]}.git`" title="Copy" />
                      </div>
                    </div>
                  </template>
                  <template v-else-if="row.type === 'reflection'">
                    <div class="table-detail-badges">
                      <span v-for="f in row.reflectionFields" :key="f" class="badge badge-blue">{{ f }}</span>
                    </div>
                    <div class="usage-rows">
                      <div class="usage-row">
                        <code>curl {{ integrationScheme }}://{{ row.name }}.{{ row.isTeam ? 'team' : 'int' }}.{{ boxHost }}/</code>
                        <CopyButton :text="`curl ${integrationScheme}://${row.name}.${row.isTeam ? 'team' : 'int'}.${boxHost}/`" title="Copy" />
                      </div>
                    </div>
                  </template>
                  <template v-else>
                    <div class="table-detail-badges">
                      <span class="text-muted">{{ row.target }}</span>
                      <span v-if="row.peerVM" class="badge badge-green">peer: {{ row.peerVM }}</span>
                      <span v-if="row.hasHeader && !row.peerVM" class="badge badge-blue">header</span>
                      <span v-if="row.hasBasicAuth" class="badge badge-yellow">auth</span>
                    </div>
                    <div class="usage-rows">
                      <div class="usage-row">
                        <code>{{ integrationScheme }}://{{ row.name }}.{{ row.isTeam ? 'team' : 'int' }}.{{ boxHost }}/</code>
                        <CopyButton :text="`${integrationScheme}://${row.name}.${row.isTeam ? 'team' : 'int'}.${boxHost}/`" title="Copy" />
                      </div>
                    </div>
                  </template>
                  <div v-if="row.comment" class="integration-comment text-muted">{{ row.comment }}</div>
                </div>
              </td>
              <td class="col-attach">
                <div class="integration-attachments">
                  <span v-for="a in row.attachments" :key="a" class="attachment-tag">
                    {{ a }}
                    <button class="attachment-tag-remove" @click="detachSpec(row.name, a)" :title="'Detach from ' + a">&times;</button>
                  </span>
                  <button class="attachment-tag-add" @click="attachViaCommand(row.name)" title="Attach">
                    {{ row.attachments.length > 0 ? '+' : '+ attach' }}
                  </button>
                </div>
              </td>
              <td class="col-actions">
                <button class="btn btn-danger" @click="removeIntegration(row.name)">Remove</button>
              </td>
            </tr>
          </tbody>
        </table>
      </section>



      <!-- Push Notifications -->
      <section v-if="data.hasPushTokens" class="card">
        <h2 class="card-title">
          <i class="pi pi-bell"></i> Notifications
        </h2>
        <p class="section-desc">The <strong>notify</strong> integration is built-in and attached to all your VMs. It sends push notifications to your devices.</p>
        <div class="integration-row">
          <div class="integration-info">
            <span class="integration-name">notify</span>
            <span class="text-muted">push notifications to device</span>
            <div class="text-muted" style="font-size: 11px; margin-top: 2px;">attached to auto:all · built-in</div>
          </div>
        </div>
        <div class="section-desc" style="margin-top: 12px;">
          Usage from a VM:
          <div style="margin-top: 4px;">
            <code style="font-size: 11px; display: block; padding: 8px; background: var(--surface-ground); border-radius: 4px; overflow-x: auto;">
              curl -X POST {{ integrationScheme }}://notify.int.{{ boxHost }}/ -H 'Content-Type: application/json' -d '{"title":"Done","body":"Task finished"}'
            </code>
          </div>
        </div>
      </section>
    </template>

    <!-- GitHub Setup Modal -->
    <div v-if="ghSetupModal.visible" class="modal-overlay" @click.self="ghSetupModal.visible = false">
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="GitHub Integration">
        <div class="modal-header">
          <h3><i class="pi pi-github" style="margin-right: 6px;"></i> GitHub</h3>
          <button class="modal-close" aria-label="Close" @click="ghSetupModal.visible = false">&times;</button>
        </div>
        <div class="modal-body">
          <p class="section-desc" style="margin-bottom: 8px;">Link your GitHub accounts to connect VMs to git repos. <a href="/docs/integrations-github" target="_blank" rel="noopener noreferrer">Learn more →</a></p>

          <div v-if="!data?.githubEnabled" class="empty-msg">GitHub integration is not available on this server.</div>

          <template v-else>
            <div class="subsection-title">Connected Accounts</div>
            <div v-if="data.githubAccounts.length === 0" class="gh-install-section">
              <div style="margin-bottom: 12px;">
                <a class="btn btn-primary" :href="'https://github.com/apps/' + data.githubAppSlug + '/installations/new'" target="_blank" rel="noopener noreferrer">
                  <i class="pi pi-github"></i> Install the exe.dev app
                </a>
              </div>
              <div class="gh-install-hint">
                If the app is already installed on the accounts you need,
                <a class="btn btn-secondary" href="/github/setup">link to installed apps</a>
              </div>
            </div>

            <div v-for="acct in data.githubAccounts" :key="acct.installationID" class="gh-account-row">
              <div class="gh-account-info">
                <span class="gh-login">{{ acct.githubLogin }}</span>
                <span v-if="acct.githubLogin !== acct.targetLogin" class="text-muted">
                  installed on <strong>{{ acct.targetLogin }}</strong>
                </span>
                <span v-if="verifyResults[acct.installationID]" class="gh-verify-result" :class="verifyResults[acct.installationID].isError ? 'gh-verify-error' : 'gh-verify-ok'">
                  {{ verifyResults[acct.installationID].message }}
                </span>
              </div>
              <div class="gh-account-actions">
                <template v-if="!unlinkingAccounts.has(acct.installationID)">
                  <button class="btn btn-secondary" :disabled="verifyingAccounts.has(acct.installationID)" @click="verifyGitHub(acct.installationID)">
                    <i v-if="verifyingAccounts.has(acct.installationID)" class="pi pi-spin pi-spinner" style="font-size: 10px;"></i>
                    {{ verifyingAccounts.has(acct.installationID) ? 'Verifying...' : 'Verify' }}
                  </button>
                  <button class="btn btn-danger" @click="unlinkGitHub(acct.installationID)">Unlink</button>
                </template>
                <template v-else>
                  <span class="text-muted" style="font-size: 11px; margin-right: 8px;">Confirm unlink?</span>
                  <button class="btn btn-danger" @click="confirmUnlinkGitHub(acct.installationID)">Yes</button>
                  <button class="btn btn-secondary" @click="cancelUnlinkGitHub(acct.installationID)">Cancel</button>
                </template>
              </div>
            </div>

            <div v-if="data.githubAppSlug && data.githubAccounts.length > 0" style="margin-top: 8px; display: flex; gap: 8px; flex-wrap: wrap;">
              <a class="btn btn-secondary" :href="'https://github.com/apps/' + data.githubAppSlug + '/installations/new'" target="_blank" rel="noopener noreferrer">
                Install on another account
              </a>
              <a class="btn btn-secondary" href="/github/setup">
                Link another account
              </a>
              <a class="btn btn-secondary" :href="'https://github.com/apps/' + data.githubAppSlug" target="_blank" rel="noopener noreferrer">
                Configure on GitHub
              </a>
            </div>
          </template>
        </div>
        <div class="modal-footer">
          <button v-if="(data?.githubAccounts ?? []).length > 0" class="btn btn-primary" @click="ghSetupModal.visible = false; openAddGitHubRepo()">
            <i class="pi pi-plus" style="font-size: 11px;"></i> Add Repository Integration
          </button>
          <button class="btn btn-secondary" @click="ghSetupModal.visible = false">Close</button>
        </div>
      </div>
    </div>

    <!-- Service Proxy Modal (Stripe, ClickHouse, etc.) -->
    <div v-if="svcModal.visible" class="modal-overlay" @click.self="closeSvcModal">
      <div class="modal-panel" role="dialog" aria-modal="true" :aria-label="svcModal.title">
        <div class="modal-header">
          <h3>{{ svcModal.title }}</h3>
          <button class="modal-close" aria-label="Close" @click="closeSvcModal">&times;</button>
        </div>
        <div class="modal-body">
          <div class="form-row">
            <label>Name</label>
            <input v-model="svcModal.name" class="form-input" :placeholder="svcModal.namePlaceholder" />
          </div>
          <div v-if="data?.hasTeam" class="form-row">
            <div class="form-row-check">
              <input type="checkbox" v-model="svcModal.team" id="svc-team-check" />
              <label for="svc-team-check">Team integration</label>
              <span class="text-muted">— shared with all team members</span>
            </div>
          </div>
          <div class="form-row">
            <label>Comment <span class="text-muted">(optional)</span></label>
            <input v-model="svcModal.comment" class="form-input" placeholder="notes about this integration" />
          </div>
          <div v-if="svcModal.showTarget" class="form-row">
            <label>{{ svcModal.targetLabel }}</label>
            <input v-model="svcModal.target" class="form-input" :placeholder="svcModal.targetPlaceholder" />
          </div>
          <div v-for="(field, i) in svcModal.fields" :key="i" class="form-row">
            <label>{{ field.label }}</label>
            <input v-model="field.value" :type="field.secret ? 'password' : 'text'" class="form-input" :placeholder="field.placeholder" :autocomplete="field.secret ? 'new-password' : 'off'" />
            <div v-if="field.error" class="field-error">{{ field.error }}</div>
          </div>
          <div class="form-row">
            <label>Attach to</label>
            <div class="multi-select" ref="svcAttachRef">
              <div class="multi-select-tags" v-if="svcModal.attachments.length > 0">
                <span v-for="a in svcModal.attachments" :key="a" class="multi-select-tag" :class="{ 'multi-select-tag-default': a === `tag:${svcEffectiveName}` }">
                  {{ a }}
                  <span v-if="a.startsWith('tag:')" class="tag-default-hint">{{ tagChipHint(a.slice(4)) }}</span>
                  <button class="multi-select-tag-remove" @click="removeSvcAttachment(a)">&times;</button>
                </span>
              </div>
              <input
                ref="svcAttachInputRef"
                v-model="svcAttachSearch"
                class="form-input"
                :placeholder="svcModal.team ? 'Search or create a tag...' : 'Search VMs, tags, or create a tag...'"
                @focus="svcAttachOpen = true"
                @input="svcAttachOpen = true"
                @blur="delayClose(() => svcAttachOpen = false)"
              />
              <div v-if="svcAttachOpen && (filteredSvcAttachOptions.length > 0 || creatableSvcAttachOption || svcModal.team)" class="attach-dropdown">
                <div
                  v-for="opt in filteredSvcAttachOptions"
                  :key="opt.value"
                  class="attach-option"
                  @mousedown.prevent="addSvcAttachment(opt.value)"
                >
                  <span>{{ opt.label }}</span>
                  <span v-if="opt.sublabel" class="attach-option-context">{{ opt.sublabel }}</span>
                </div>
                <div
                  v-if="creatableSvcAttachOption"
                  class="attach-option attach-option-create"
                  @mousedown.prevent="addSvcAttachment(creatableSvcAttachOption.value)"
                >
                  <span>+ {{ creatableSvcAttachOption.label }}</span>
                </div>
                <div
                  v-if="svcModal.team && filteredSvcAttachOptions.length === 0 && !creatableSvcAttachOption"
                  class="attach-hint"
                >
                  {{ teamAttachHint(svcModal.attachments) }}
                </div>
              </div>
            </div>
          </div>
          <div v-if="svcFirstTag" class="form-row">
            <label>Tag additional VMs with <span class="tag-hash">#{{ svcFirstTag }}</span></label>
            <div class="multi-select" ref="svcTagVMRef">
              <div class="multi-select-tags" v-if="svcModal.tagVMs.length > 0">
                <span v-for="vm in svcModal.tagVMs" :key="vm" class="multi-select-tag">
                  {{ vm }}
                  <button class="multi-select-tag-remove" @click="removeSvcTagVM(vm)">&times;</button>
                </span>
              </div>
              <input
                ref="svcTagVMInputRef"
                v-model="svcTagVMSearch"
                class="form-input"
                placeholder="Select VMs..."
                @focus="svcTagVMOpen = true"
                @input="svcTagVMOpen = true"
                @blur="delayClose(() => svcTagVMOpen = false)"
              />
              <div v-if="svcTagVMOpen && filteredSvcTagVMOptions.length > 0" class="attach-dropdown">
                <div
                  v-for="opt in filteredSvcTagVMOptions"
                  :key="opt.name"
                  class="attach-option"
                  @mousedown.prevent="addSvcTagVM(opt.name)"
                >
                  <span>{{ opt.name }}</span>
                  <span v-if="opt.tags.length > 0" class="attach-option-context">{{ opt.tags.join(', ') }}</span>
                </div>
              </div>
            </div>
          </div>
          <div class="cmd-preview">
            <code v-for="(cmd, i) in svcDisplayCommands" :key="i">{{ cmd }}</code>
            <code v-if="svcDisplayCommands.length === 0">Fill in required fields to preview command</code>
          </div>
          <div v-if="svcModal.result" class="cmd-result" :class="svcModal.result.success ? 'success' : 'error'">
            {{ svcModal.result.output || svcModal.result.error }}
          </div>
        </div>
        <div class="modal-footer">
          <button v-if="svcModal.result?.success" class="btn btn-primary" @click="closeSvcModal">Done</button>
          <template v-else>
            <button class="btn btn-secondary" @click="closeSvcModal">Cancel</button>
            <button class="btn btn-primary" :disabled="svcBuiltCommands.length === 0 || svcModal.running" @click="runSvcCommand">
              {{ svcModal.running ? 'Running...' : 'Run' }}
            </button>
          </template>
        </div>
      </div>
    </div>

    <!-- Command Modal (for remove, detach, attach-single) -->
    <CommandModal
      :visible="modal.visible"
      :title="modal.title"
      :description="modal.description"
      :command="modal.command"
      :command-prefix="modal.commandPrefix"
      :input-placeholder="modal.inputPlaceholder"
      :danger="modal.danger"
      @close="modal.visible = false"
      @success="reload"
    />

    <!-- Add GitHub Repo Modal -->
    <div v-if="ghModal.visible" class="modal-overlay" @click.self="closeGhModal">
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="Add Repository Integration">
        <div class="modal-header">
          <h3>Add Repository Integration</h3>
          <button class="modal-close" aria-label="Close" @click="closeGhModal">&times;</button>
        </div>
        <div class="modal-body">
          <div class="form-row">
            <label>Repository</label>
            <div class="repo-combobox" ref="repoComboboxRef">
              <input
                v-model="repoSearch"
                class="form-input"
                :placeholder="loadingRepos ? 'Loading repos...' : 'Search repositories...'"
                :disabled="loadingRepos"
                @focus="repoDropdownOpen = true"
                @input="repoDropdownOpen = true"
                @blur="delayClose(() => repoDropdownOpen = false)"
              />
              <div v-if="repoDropdownOpen && !loadingRepos && filteredRepos.length > 0" class="repo-dropdown">
                <div
                  v-for="repo in filteredRepos"
                  :key="repo.full_name"
                  class="repo-option"
                  :class="{ 'repo-option-selected': ghModal.repo === repo.full_name }"
                  @mousedown.prevent="selectRepo(repo)"
                >
                  <span class="repo-option-name">{{ repo.full_name }}</span>
                  <span v-if="repo.description" class="repo-option-sub">{{ repo.description }}</span>
                </div>
              </div>
              <div v-if="repoDropdownOpen && !loadingRepos && repoSearch && filteredRepos.length === 0" class="repo-dropdown">
                <div class="repo-option repo-option-empty">No matching repositories</div>
              </div>
            </div>
          </div>
          <div class="form-row">
            <label>Name</label>
            <input v-model="ghModal.name" class="form-input" :placeholder="ghModal.repo ? ghModal.repo.replace(/\//g, '-') : 'integration-name'" />
          </div>
          <div v-if="data?.hasTeam" class="form-row">
            <div class="form-row-check">
              <input type="checkbox" v-model="ghModal.team" id="gh-team-check" />
              <label for="gh-team-check">Team integration</label>
              <span class="text-muted">— shared with all team members</span>
            </div>
          </div>
          <div class="form-row">
            <label>Comment <span class="text-muted">(optional)</span></label>
            <input v-model="ghModal.comment" class="form-input" placeholder="notes about this integration" />
          </div>
          <div class="form-row">
            <label>Attach to</label>
            <div class="multi-select" ref="ghAttachRef">
              <div class="multi-select-tags" v-if="ghModal.attachments.length > 0">
                <span v-for="a in ghModal.attachments" :key="a" class="multi-select-tag" :class="{ 'multi-select-tag-default': a === `tag:${ghEffectiveName}` }">
                  {{ a }}
                  <span v-if="a.startsWith('tag:')" class="tag-default-hint">{{ tagChipHint(a.slice(4)) }}</span>
                  <button class="multi-select-tag-remove" @click="removeGhAttachment(a)">&times;</button>
                </span>
              </div>
              <input
                ref="ghAttachInputRef"
                v-model="ghAttachSearch"
                class="form-input"
                :placeholder="ghModal.team ? 'Search or create a tag...' : 'Search VMs, tags, or create a tag...'"
                @focus="ghAttachOpen = true"
                @input="ghAttachOpen = true"
                @blur="delayClose(() => ghAttachOpen = false)"
              />
              <div v-if="ghAttachOpen && (filteredGhAttachOptions.length > 0 || creatableGhAttachOption || ghModal.team)" class="attach-dropdown">
                <div
                  v-for="opt in filteredGhAttachOptions"
                  :key="opt.value"
                  class="attach-option"
                  @mousedown.prevent="addGhAttachment(opt.value)"
                >
                  <span>{{ opt.label }}</span>
                  <span v-if="opt.sublabel" class="attach-option-context">{{ opt.sublabel }}</span>
                </div>
                <div
                  v-if="creatableGhAttachOption"
                  class="attach-option attach-option-create"
                  @mousedown.prevent="addGhAttachment(creatableGhAttachOption.value)"
                >
                  <span>+ {{ creatableGhAttachOption.label }}</span>
                </div>
                <div
                  v-if="ghModal.team && filteredGhAttachOptions.length === 0 && !creatableGhAttachOption"
                  class="attach-hint"
                >
                  {{ teamAttachHint(ghModal.attachments) }}
                </div>
              </div>
            </div>
          </div>
          <div v-if="ghFirstTag" class="form-row">
            <label>Tag additional VMs with <span class="tag-hash">#{{ ghFirstTag }}</span></label>
            <div class="multi-select" ref="ghTagVMRef">
              <div class="multi-select-tags" v-if="ghModal.tagVMs.length > 0">
                <span v-for="vm in ghModal.tagVMs" :key="vm" class="multi-select-tag">
                  {{ vm }}
                  <button class="multi-select-tag-remove" @click="removeGhTagVM(vm)">&times;</button>
                </span>
              </div>
              <input
                ref="ghTagVMInputRef"
                v-model="ghTagVMSearch"
                class="form-input"
                placeholder="Select VMs..."
                @focus="ghTagVMOpen = true"
                @input="ghTagVMOpen = true"
                @blur="delayClose(() => ghTagVMOpen = false)"
              />
              <div v-if="ghTagVMOpen && filteredGhTagVMOptions.length > 0" class="attach-dropdown">
                <div
                  v-for="opt in filteredGhTagVMOptions"
                  :key="opt.name"
                  class="attach-option"
                  @mousedown.prevent="addGhTagVM(opt.name)"
                >
                  <span>{{ opt.name }}</span>
                  <span v-if="opt.tags.length > 0" class="attach-option-context">{{ opt.tags.join(', ') }}</span>
                </div>
              </div>
            </div>
          </div>
          <div class="cmd-preview">
            <code v-for="(cmd, i) in ghBuiltCommands" :key="i">{{ cmd }}</code>
            <code v-if="ghBuiltCommands.length === 0">Select a repository to preview command</code>
          </div>
          <div v-if="ghModal.result" class="cmd-result" :class="ghModal.result.success ? 'success' : 'error'">
            {{ ghModal.result.output || ghModal.result.error }}
          </div>
        </div>
        <div class="modal-footer">
          <button v-if="ghModal.result?.success" class="btn btn-primary" @click="closeGhModal">Done</button>
          <template v-else>
            <button class="btn btn-secondary" @click="closeGhModal">Cancel</button>
            <button class="btn btn-primary" :disabled="ghBuiltCommands.length === 0 || ghModal.running" @click="runGhCommand">
              {{ ghModal.running ? 'Running...' : 'Run' }}
            </button>
          </template>
        </div>
      </div>
    </div>

    <!-- Add HTTP Proxy Modal -->
    <div v-if="proxyModal.visible" class="modal-overlay" @click.self="closeProxyModal">
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="Add HTTP Proxy Integration">
        <div class="modal-header">
          <h3>Add HTTP Proxy Integration</h3>
          <button class="modal-close" aria-label="Close" @click="closeProxyModal">&times;</button>
        </div>
        <div class="modal-body">
          <div class="form-row">
            <label>Name</label>
            <input ref="proxyNameInputRef" v-model="proxyModal.name" class="form-input" placeholder="my-api" />
          </div>
          <div v-if="data?.hasTeam" class="form-row">
            <div class="form-row-check">
              <input type="checkbox" v-model="proxyModal.team" id="proxy-team-check" />
              <label for="proxy-team-check">Team integration</label>
              <span class="text-muted">— shared with all team members</span>
            </div>
          </div>
          <div class="form-row">
            <label>Comment <span class="text-muted">(optional)</span></label>
            <input v-model="proxyModal.comment" class="form-input" placeholder="notes about this integration" />
          </div>
          <div class="form-row">
            <label>Target URL</label>
            <div class="target-url-wrapper" ref="proxyTargetRef">
              <input
                v-model="proxyModal.target"
                class="form-input"
                placeholder="https://api.example.com"
                @focus="proxyTargetOpen = true"
                @input="proxyTargetOpen = true"
              />
              <div v-if="proxyTargetOpen && filteredTargetVMs.length > 0" class="attach-dropdown">
                <div
                  v-for="vm in filteredTargetVMs"
                  :key="vm.name"
                  class="attach-option"
                  @mousedown.prevent="selectTargetVM(vm.name)"
                >
                  <span>https://{{ vm.name }}.{{ boxHost }}/</span>
                  <span class="attach-option-context">{{ vm.name }}</span>
                </div>
              </div>
            </div>
            <div v-if="detectedPeerVM" class="peer-hint">
              <label class="peer-check">
                <input type="checkbox" v-model="proxyModal.usePeer" />
                Use peer auth (auto-generate API key for <strong>{{ detectedPeerVM }}</strong>)
              </label>
            </div>
          </div>
          <div v-if="!proxyModal.usePeer" class="form-row">
            <label>Auth method</label>
            <div class="radio-group">
              <label class="radio-label"><input type="radio" v-model="proxyModal.authMethod" value="none" /> None</label>
              <label class="radio-label"><input type="radio" v-model="proxyModal.authMethod" value="basic" /> HTTP Basic Auth</label>
              <label class="radio-label"><input type="radio" v-model="proxyModal.authMethod" value="bearer" /> Bearer Token</label>
              <label class="radio-label"><input type="radio" v-model="proxyModal.authMethod" value="header" /> Custom Header</label>
            </div>
          </div>
          <div v-if="proxyModal.authMethod === 'basic'" class="form-row">
            <label>Username</label>
            <input v-model="proxyModal.basicUser" class="form-input" placeholder="username" autocomplete="off" />
          </div>
          <div v-if="proxyModal.authMethod === 'basic'" class="form-row">
            <label>Password</label>
            <input v-model="proxyModal.basicPass" type="text" class="form-input" placeholder="password" autocomplete="off" />
          </div>
          <div v-if="proxyModal.authMethod === 'bearer'" class="form-row">
            <label>Token</label>
            <input v-model="proxyModal.bearer" type="password" autocomplete="new-password" class="form-input" placeholder="your-bearer-token" />
          </div>
          <div v-if="proxyModal.authMethod === 'header'" class="form-row">
            <label>Header</label>
            <input v-model="proxyModal.header" class="form-input" placeholder="Authorization: Bearer ..." />
          </div>
          <div class="form-row">
            <label>Attach to</label>
            <div class="multi-select" ref="proxyAttachRef">
              <div class="multi-select-tags" v-if="proxyModal.attachments.length > 0">
                <span v-for="a in proxyModal.attachments" :key="a" class="multi-select-tag" :class="{ 'multi-select-tag-default': a === `tag:${proxyModal.name.trim()}` }">
                  {{ a }}
                  <span v-if="a.startsWith('tag:')" class="tag-default-hint">{{ tagChipHint(a.slice(4)) }}</span>
                  <button class="multi-select-tag-remove" @click="removeProxyAttachment(a)">&times;</button>
                </span>
              </div>
              <input
                ref="proxyAttachInputRef"
                v-model="proxyAttachSearch"
                class="form-input"
                :placeholder="proxyModal.team ? 'Search or create a tag...' : 'Search VMs, tags, or create a tag...'"
                @focus="proxyAttachOpen = true"
                @input="proxyAttachOpen = true"
                @blur="delayClose(() => proxyAttachOpen = false)"
              />
              <div v-if="proxyAttachOpen && (filteredProxyAttachOptions.length > 0 || creatableProxyAttachOption || proxyModal.team)" class="attach-dropdown">
                <div
                  v-for="opt in filteredProxyAttachOptions"
                  :key="opt.value"
                  class="attach-option"
                  @mousedown.prevent="addProxyAttachment(opt.value)"
                >
                  <span>{{ opt.label }}</span>
                  <span v-if="opt.sublabel" class="attach-option-context">{{ opt.sublabel }}</span>
                </div>
                <div
                  v-if="creatableProxyAttachOption"
                  class="attach-option attach-option-create"
                  @mousedown.prevent="addProxyAttachment(creatableProxyAttachOption.value)"
                >
                  <span>+ {{ creatableProxyAttachOption.label }}</span>
                </div>
                <div
                  v-if="proxyModal.team && filteredProxyAttachOptions.length === 0 && !creatableProxyAttachOption"
                  class="attach-hint"
                >
                  {{ teamAttachHint(proxyModal.attachments) }}
                </div>
              </div>
            </div>
          </div>
          <div v-if="proxyFirstTag" class="form-row">
            <label>Tag additional VMs with <span class="tag-hash">#{{ proxyFirstTag }}</span></label>
            <div class="multi-select" ref="proxyTagVMRef">
              <div class="multi-select-tags" v-if="proxyModal.tagVMs.length > 0">
                <span v-for="vm in proxyModal.tagVMs" :key="vm" class="multi-select-tag">
                  {{ vm }}
                  <button class="multi-select-tag-remove" @click="removeProxyTagVM(vm)">&times;</button>
                </span>
              </div>
              <input
                ref="proxyTagVMInputRef"
                v-model="proxyTagVMSearch"
                class="form-input"
                placeholder="Select VMs..."
                @focus="proxyTagVMOpen = true"
                @input="proxyTagVMOpen = true"
                @blur="delayClose(() => proxyTagVMOpen = false)"
              />
              <div v-if="proxyTagVMOpen && filteredProxyTagVMOptions.length > 0" class="attach-dropdown">
                <div
                  v-for="opt in filteredProxyTagVMOptions"
                  :key="opt.name"
                  class="attach-option"
                  @mousedown.prevent="addProxyTagVM(opt.name)"
                >
                  <span>{{ opt.name }}</span>
                  <span v-if="opt.tags.length > 0" class="attach-option-context">{{ opt.tags.join(', ') }}</span>
                </div>
              </div>
            </div>
          </div>
          <div class="cmd-preview">
            <code v-for="(cmd, i) in proxyDisplayCommands" :key="i">{{ cmd }}</code>
            <code v-if="proxyDisplayCommands.length === 0">Fill in name and target to preview command</code>
          </div>
          <div v-if="proxyModal.result" class="cmd-result" :class="proxyModal.result.success ? 'success' : 'error'">
            {{ proxyModal.result.output || proxyModal.result.error }}
          </div>
        </div>
        <div class="modal-footer">
          <button v-if="proxyModal.result?.success" class="btn btn-primary" @click="closeProxyModal">Done</button>
          <template v-else>
            <button class="btn btn-secondary" @click="closeProxyModal">Cancel</button>
            <button class="btn btn-primary" :disabled="proxyBuiltCommands.length === 0 || proxyModal.running" @click="runProxyCommand">
              {{ proxyModal.running ? 'Running...' : 'Run' }}
            </button>
          </template>
        </div>
      </div>
    </div>

    <!-- Reflection Integration Modal -->
    <div v-if="reflectionModal.visible" class="modal-overlay" @click.self="closeReflectionModal">
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="Add Reflection Integration">
        <div class="modal-header">
          <h3>Add Reflection Integration</h3>
          <button class="modal-close" aria-label="Close" @click="closeReflectionModal">&times;</button>
        </div>
        <div class="modal-body">
          <p class="section-desc">
            The reflection integration exposes information about the VM itself back to the VM —
            its owner's email, the list of integrations attached to it, and its tags. Useful for
            agents that need to know what they have access to.
          </p>
          <div class="form-row">
            <label>Name</label>
            <input v-model="reflectionModal.name" class="form-input" placeholder="reflection" />
          </div>
          <div v-if="data?.hasTeam" class="form-row">
            <div class="form-row-check">
              <input type="checkbox" v-model="reflectionModal.team" id="reflect-team-check" />
              <label for="reflect-team-check">Team integration</label>
              <span class="text-muted">— shared with all team members</span>
            </div>
          </div>
          <div class="form-row">
            <label>Comment <span class="text-muted">(optional)</span></label>
            <input v-model="reflectionModal.comment" class="form-input" placeholder="notes about this integration" />
          </div>
          <div class="form-row">
            <label>Fields to expose</label>
            <div class="radio-group">
              <label class="radio-label"><input type="checkbox" v-model="reflectionModal.fieldEmail" /> email <span class="text-muted">— owner's email address</span></label>
              <label class="radio-label"><input type="checkbox" v-model="reflectionModal.fieldIntegrations" /> integrations <span class="text-muted">— attached integrations</span></label>
              <label class="radio-label"><input type="checkbox" v-model="reflectionModal.fieldTags" /> tags <span class="text-muted">— VM tags</span></label>
            </div>
          </div>
          <div class="form-row">
            <label>Attach to</label>
            <div class="multi-select" ref="reflectionAttachRef">
              <div class="multi-select-tags" v-if="reflectionModal.attachments.length > 0">
                <span v-for="a in reflectionModal.attachments" :key="a" class="multi-select-tag" :class="{ 'multi-select-tag-default': a === `tag:${reflectionEffectiveName}` }">
                  {{ a }}
                  <span v-if="a.startsWith('tag:')" class="tag-default-hint">{{ tagChipHint(a.slice(4)) }}</span>
                  <button class="multi-select-tag-remove" @click="removeReflectionAttachment(a)">&times;</button>
                </span>
              </div>
              <input
                ref="reflectionAttachInputRef"
                v-model="reflectionAttachSearch"
                class="form-input"
                placeholder="Search VMs, tags..."
                @focus="reflectionAttachOpen = true"
                @input="reflectionAttachOpen = true"
                @blur="delayClose(() => reflectionAttachOpen = false)"
              />
              <div v-if="reflectionAttachOpen && filteredReflectionAttachOptions.length > 0" class="attach-dropdown">
                <div
                  v-for="opt in filteredReflectionAttachOptions"
                  :key="opt.value"
                  class="attach-option"
                  @mousedown.prevent="addReflectionAttachment(opt.value)"
                >
                  <span>{{ opt.label }}</span>
                  <span v-if="opt.sublabel" class="attach-option-context">{{ opt.sublabel }}</span>
                </div>
              </div>
            </div>
          </div>
          <div class="cmd-preview">
            <code v-for="(cmd, i) in reflectionBuiltCommands" :key="i">{{ cmd }}</code>
            <code v-if="reflectionBuiltCommands.length === 0">Fill in name to preview command</code>
          </div>
          <div v-if="reflectionModal.result" class="cmd-result" :class="reflectionModal.result.success ? 'success' : 'error'">
            {{ reflectionModal.result.output || reflectionModal.result.error }}
          </div>
        </div>
        <div class="modal-footer">
          <button v-if="reflectionModal.result?.success" class="btn btn-primary" @click="closeReflectionModal">Done</button>
          <template v-else>
            <button class="btn btn-secondary" @click="closeReflectionModal">Cancel</button>
            <button class="btn btn-primary" :disabled="reflectionBuiltCommands.length === 0 || reflectionModal.running" @click="runReflectionCommand">
              {{ reflectionModal.running ? 'Running...' : 'Run' }}
            </button>
          </template>
        </div>
      </div>
    </div>

    <!-- Attach Modal (for managing attachments on existing integrations) -->
    <div v-if="attachModal.visible" class="modal-overlay" @click.self="closeAttachModal">
      <div class="modal-panel modal-panel-narrow" role="dialog" aria-modal="true" aria-label="Attach Integration">
        <div class="modal-header">
          <h3>Attach integration '{{ attachModal.name }}'</h3>
          <button class="modal-close" aria-label="Close" @click="closeAttachModal">&times;</button>
        </div>
        <div class="modal-body">
          <div class="form-row">
            <label>Attached to</label>
            <div class="multi-select" ref="attachModalRef">
              <div class="multi-select-tags" v-if="attachModal.currentAttachments.length > 0">
                <span v-for="a in attachModal.currentAttachments" :key="a" class="multi-select-tag" :class="{ 'multi-select-tag-removing': attachModal.removing === a }">
                  {{ a }}
                  <button class="multi-select-tag-remove" :disabled="attachModal.removing === a" @click="detachFromModal(a)">&times;</button>
                </span>
              </div>
              <div v-if="attachModal.currentAttachments.length === 0" class="attach-empty">Not attached to anything</div>
              <input
                ref="attachModalInputRef"
                v-model="attachModalSearch"
                class="form-input"
                :placeholder="attachModal.isTeam ? 'Search or create a tag...' : 'Search VMs, tags, or create a tag to attach...'"
                @focus="attachModalOpen = true"
                @input="attachModalOpen = true"
                @blur="delayClose(() => attachModalOpen = false)"
              />
              <div v-if="attachModalOpen && (filteredAttachModalOptions.length > 0 || creatableAttachModalOption || attachModal.isTeam)" class="attach-dropdown">
                <div
                  v-for="opt in filteredAttachModalOptions"
                  :key="opt.value"
                  class="attach-option"
                  @mousedown.prevent="attachFromModal(opt.value)"
                >
                  <span>{{ opt.label }}</span>
                  <span v-if="opt.sublabel" class="attach-option-context">{{ opt.sublabel }}</span>
                </div>
                <div
                  v-if="creatableAttachModalOption"
                  class="attach-option attach-option-create"
                  @mousedown.prevent="attachFromModal(creatableAttachModalOption.value)"
                >
                  <span>+ {{ creatableAttachModalOption.label }}</span>
                </div>
                <div
                  v-if="attachModal.isTeam && filteredAttachModalOptions.length === 0 && !creatableAttachModalOption"
                  class="attach-hint"
                >
                  {{ teamAttachHint(attachModal.currentAttachments) }}
                </div>
              </div>
            </div>
          </div>
          <div v-if="attachModal.error" class="cmd-result error">
            {{ attachModal.error }}
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="closeAttachModal">Done</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { fetchIntegrations, shellQuote, runCommand, type IntegrationsData, type IntegrationInfo } from '../api/client'
import CommandModal from '../components/CommandModal.vue'
import CopyButton from '../components/CopyButton.vue'

const loading = ref(true)
const loadError = ref('')
const data = ref<IntegrationsData | null>(null)
const githubRepos = ref<any[]>([])
const loadingRepos = ref(false)
const unlinkingAccounts = ref<Set<number>>(new Set())
const verifyResults = reactive<Record<number, { message: string; isError: boolean }>>({})
const verifyingAccounts = ref<Set<number>>(new Set())
const inlineMessage = ref('')
const inlineMessageIsError = ref(false)

// Repo combobox state
const repoSearch = ref('')
const repoDropdownOpen = ref(false)
const repoComboboxRef = ref<HTMLElement | null>(null)

// Attachment multi-select refs (for outside-click and focus)
const ghAttachRef = ref<HTMLElement | null>(null)
const proxyAttachRef = ref<HTMLElement | null>(null)
const ghAttachInputRef = ref<HTMLInputElement | null>(null)
const proxyAttachInputRef = ref<HTMLInputElement | null>(null)

function showError(msg: string) {
  inlineMessage.value = msg
  inlineMessageIsError.value = true
}

const integrationScheme = ref('https')
const boxHost = ref(window.location.hostname)

// GitHub setup modal
const ghSetupModal = reactive({ visible: false })
function openGitHubSetup() {
  ghSetupModal.visible = true
}

// Unified active integrations table
interface ActiveIntegrationRow {
  name: string
  displayName: string
  type: 'github' | 'http-proxy' | 'reflection'
  iconSvg: string
  iconClass: string
  target: string
  repositories: string[]
  attachments: string[]
  isTeam: boolean
  peerVM?: string
  hasHeader: boolean
  hasBasicAuth: boolean
  comment: string
  reflectionFields: string[]
}

function matchServiceProxy(ig: IntegrationInfo): ServiceProxyDef | null {
  if (ig.type !== 'http-proxy') return null
  for (const sp of serviceProxies) {
    const normalizedTarget = ig.target.replace(/\/$/, '')
    const normalizedSP = sp.targetURL.replace(/\/$/, '')
    if (ig.name === sp.defaultName && normalizedTarget === normalizedSP) return sp
  }
  return null
}

const allActiveIntegrations = computed((): ActiveIntegrationRow[] => {
  if (!data.value) return []
  const rows: ActiveIntegrationRow[] = []

  // GitHub repo integrations
  for (const ig of data.value.githubIntegrations) {
    rows.push({
      name: ig.name,
      displayName: ig.name,
      type: 'github',
      iconSvg: '',
      iconClass: 'pi pi-github',
      target: '',
      repositories: ig.repositories || [],
      attachments: ig.attachments || [],
      isTeam: ig.isTeam,
      peerVM: ig.peerVM,
      hasHeader: ig.hasHeader,
      hasBasicAuth: ig.hasBasicAuth,
      comment: ig.comment || '',
      reflectionFields: [],
    })
  }

  // Proxy integrations (with service matching)
  for (const ig of data.value.proxyIntegrations) {
    const sp = matchServiceProxy(ig)
    rows.push({
      name: ig.name,
      displayName: sp ? sp.label : ig.name,
      type: 'http-proxy',
      iconSvg: sp ? sp.icon : '',
      iconClass: sp ? '' : 'pi pi-globe',
      target: ig.target,
      repositories: ig.repositories || [],
      attachments: ig.attachments || [],
      isTeam: ig.isTeam,
      peerVM: ig.peerVM,
      hasHeader: ig.hasHeader,
      hasBasicAuth: ig.hasBasicAuth,
      comment: ig.comment || '',
      reflectionFields: [],
    })
  }

  // Reflection integrations
  for (const ig of (data.value.reflectionIntegrations || [])) {
    rows.push({
      name: ig.name,
      displayName: ig.name,
      type: 'reflection',
      iconSvg: '',
      iconClass: 'pi pi-eye',
      target: '',
      repositories: [],
      attachments: ig.attachments || [],
      isTeam: ig.isTeam,
      hasHeader: false,
      hasBasicAuth: false,
      comment: ig.comment || '',
      reflectionFields: ig.reflectionFields || [],
    })
  }

  return rows
})

// --- Service Proxy Definitions ---
interface ServiceProxyField {
  label: string
  placeholder: string
  secret: boolean
  value: string
  error: string
  authRole: 'username' | 'password' | 'bearer' | 'header'
  headerName?: string
}

interface ServiceProxyFieldDef {
  label: string
  placeholder: string
  secret: boolean
  authRole: 'username' | 'password' | 'bearer' | 'header'
  headerName?: string
  validate?: (v: string) => string
}

interface ServiceProxyDef {
  id: string
  label: string
  icon: string
  defaultName: string
  targetURL: string
  targetEditable: boolean
  targetLabel: string
  fields: ServiceProxyFieldDef[]
}

const serviceProxies: ServiceProxyDef[] = [
  {
    id: 'stripe',
    label: 'Stripe',
    icon: '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M13.976 9.15c-2.172-.806-3.356-1.426-3.356-2.409 0-.831.683-1.305 1.901-1.305 2.227 0 4.515.858 6.09 1.631l.89-5.494C18.252.975 15.697 0 12.165 0 9.667 0 7.589.654 6.104 1.872 4.56 3.147 3.757 4.992 3.757 7.218c0 4.039 2.467 5.76 6.476 7.219 2.585.92 3.445 1.574 3.445 2.583 0 .98-.84 1.545-2.354 1.545-1.875 0-4.965-.921-6.99-2.109l-.9 5.555C5.175 22.99 8.385 24 11.714 24c2.641 0 4.843-.624 6.328-1.813 1.664-1.305 2.525-3.236 2.525-5.732 0-4.128-2.524-5.851-6.591-7.305z"/></svg>',
    defaultName: 'stripe',
    targetURL: 'https://api.stripe.com',
    targetEditable: false,
    targetLabel: 'Target URL',
    fields: [
      {
        label: 'API Key',
        placeholder: 'sk_test_... or sk_live_...',
        secret: true,
        authRole: 'username',
        validate: (v: string) => {
          if (!v.trim()) return 'API key is required'
          if (!v.startsWith('sk_test_') && !v.startsWith('sk_live_') && !v.startsWith('rk_test_') && !v.startsWith('rk_live_')) {
            return 'Key should start with sk_test_, sk_live_, rk_test_, or rk_live_'
          }
          return ''
        },
      },
    ],
  },
  {
    id: 'clickhouse',
    label: 'ClickHouse',
    icon: '<svg viewBox="0 0 24 24" fill="currentColor"><rect x="1" y="2" width="3" height="20" rx="0.5"/><rect x="6" y="2" width="3" height="20" rx="0.5"/><rect x="11" y="2" width="3" height="20" rx="0.5"/><rect x="16" y="2" width="3" height="20" rx="0.5"/><rect x="20" y="9" width="3" height="6" rx="1.5" fill="#FADB14"/></svg>',
    defaultName: 'clickhouse',
    targetURL: 'https://api.clickhouse.cloud',
    targetEditable: true,
    targetLabel: 'API URL',
    fields: [
      { label: 'Key ID', placeholder: 'Key ID', secret: false, authRole: 'username' as const },
      { label: 'Key Secret', placeholder: 'Key Secret', secret: true, authRole: 'password' as const },
    ],
  },
  {
    id: 'postmark',
    label: 'Postmark',
    icon: '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 2L2 7v10l10 5 10-5V7L12 2zm0 2.18L19.35 7.5 12 10.82 4.65 7.5 12 4.18zM4 8.82l7 3.5V19.5l-7-3.5V8.82zm16 0v7.18l-7 3.5v-7.18l7-3.5z"/></svg>',
    defaultName: 'postmark',
    targetURL: 'https://api.postmarkapp.com',
    targetEditable: false,
    targetLabel: 'Target URL',
    fields: [
      { label: 'Server Token', placeholder: 'X-Postmark-Server-Token', secret: true, authRole: 'header' as const, headerName: 'X-Postmark-Server-Token' },
    ],
  },
]

// Simple command modal (for remove, detach, attach-single)
const modal = reactive({
  visible: false,
  title: '',
  description: '',
  command: '',
  commandPrefix: '',
  inputPlaceholder: '',
  danger: false,
})

// GitHub add modal
const ghModal = reactive({
  visible: false,
  repo: '',
  name: '',
  comment: '',
  team: false,
  attachments: [] as string[],
  tagVMs: [] as string[],
  running: false,
  result: null as { success: boolean; output: string; error: string } | null,
})

const ghAttachSearch = ref('')
const ghAttachOpen = ref(false)
const ghTagVMSearch = ref('')
const ghTagVMOpen = ref(false)
const ghTagVMRef = ref<HTMLElement | null>(null)
const ghTagVMInputRef = ref<HTMLInputElement | null>(null)

// HTTP Proxy add modal
const proxyModal = reactive({
  visible: false,
  name: '',
  target: '',
  comment: '',
  usePeer: false,
  authMethod: 'none',
  basicUser: '',
  basicPass: '',
  bearer: '',
  header: '',
  team: false,
  attachments: [] as string[],
  tagVMs: [] as string[],
  running: false,
  result: null as { success: boolean; output: string; error: string } | null,
})

const proxyTargetRef = ref<HTMLElement | null>(null)
const proxyNameInputRef = ref<HTMLInputElement | null>(null)
const proxyTargetOpen = ref(false)

const proxyAttachSearch = ref('')
const proxyAttachOpen = ref(false)
const proxyTagVMSearch = ref('')
const proxyTagVMOpen = ref(false)
const proxyTagVMRef = ref<HTMLElement | null>(null)
const proxyTagVMInputRef = ref<HTMLInputElement | null>(null)

// Service Proxy modal state
const svcModal = reactive({
  visible: false,
  serviceId: '',
  title: '',
  name: '',
  namePlaceholder: '',
  target: '',
  targetPlaceholder: '',
  targetLabel: 'Target URL',
  showTarget: false,
  team: false,
  comment: '',
  fields: [] as ServiceProxyField[],
  attachments: [] as string[],
  tagVMs: [] as string[],
  running: false,
  result: null as { success: boolean; output: string; error: string } | null,
})

// Reflection add modal
const reflectionModal = reactive({
  visible: false,
  name: 'reflection',
  comment: '',
  team: false,
  fieldEmail: true,
  fieldIntegrations: true,
  fieldTags: true,
  attachments: [] as string[],
  tagVMs: [] as string[],
  running: false,
  result: null as { success: boolean; output: string; error: string } | null,
})
const reflectionAttachRef = ref<HTMLElement | null>(null)
const reflectionAttachInputRef = ref<HTMLInputElement | null>(null)
const reflectionAttachSearch = ref('')
const reflectionAttachOpen = ref(false)
const reflectionTagVMRef = ref<HTMLElement | null>(null)
const reflectionTagVMInputRef = ref<HTMLInputElement | null>(null)
const reflectionTagVMSearch = ref('')
const reflectionTagVMOpen = ref(false)

const svcAttachRef = ref<HTMLElement | null>(null)
const svcAttachInputRef = ref<HTMLInputElement | null>(null)
const svcAttachSearch = ref('')
const svcAttachOpen = ref(false)
const svcTagVMRef = ref<HTMLElement | null>(null)
const svcTagVMInputRef = ref<HTMLInputElement | null>(null)
const svcTagVMSearch = ref('')
const svcTagVMOpen = ref(false)

// Attach modal (for managing attachments on existing integrations)
const attachModal = reactive({
  visible: false,
  name: '',
  isTeam: false,
  currentAttachments: [] as string[],
  removing: '' as string,
  error: '',
})

const attachModalSearch = ref('')
const attachModalOpen = ref(false)
const attachModalRef = ref<HTMLElement | null>(null)
const attachModalInputRef = ref<HTMLInputElement | null>(null)

// Helper: describe VMs for a tag in the dropdown
function tagVMLabel(tag: string): string {
  const vms = data.value?.tagVMs?.[tag] || []
  if (vms.length === 0) return 'new tag'
  if (vms.length <= 3) return vms.join(', ')
  return `${vms.length} VMs`
}

function tagChipHint(tag: string): string {
  const vms = data.value?.tagVMs?.[tag] || []
  if (vms.length === 0) return 'new tag'
  if (vms.length <= 2) return vms.join(', ')
  return `attached to ${vms.length} VMs`
}

// Tags known to the user — union of box tags (data.allTags, populated only
// from VMs the user owns) and any tag referenced by an existing integration
// attachment. Integration attachments can reference tags that aren't applied
// to any VM yet, so we surface those here too.
const allKnownTags = computed(() => {
  const set = new Set<string>(data.value?.allTags ?? [])
  for (const ig of data.value?.integrations ?? []) {
    for (const a of ig.attachments ?? []) {
      if (a.startsWith('tag:')) set.add(a.slice(4))
    }
  }
  return [...set].sort()
})

// All possible attachment options (for existing integration attach modal)
const allAttachOptions = computed(() => {
  if (!data.value) return [] as { value: string; label: string; sublabel: string }[]
  const opts: { value: string; label: string; sublabel: string }[] = [
    { value: 'auto:all', label: 'auto:all', sublabel: `${data.value.boxes.length} VMs` },
  ]
  for (const tag of allKnownTags.value) {
    opts.push({ value: `tag:${tag}`, label: `tag:${tag}`, sublabel: tagVMLabel(tag) })
  }
  for (const box of data.value.boxes) {
    opts.push({ value: `vm:${box.name}`, label: `vm:${box.name}`, sublabel: '' })
  }
  return opts
})

function filterAttachOptions(search: string, selected: string[]) {
  const q = search.toLowerCase().trim()
  return allAttachOptions.value
    .filter(o => !selected.includes(o.value))
    .filter(o => !q || o.value.toLowerCase().includes(q) || o.sublabel.toLowerCase().includes(q))
}

// Tag names must match this server-side regex (see ssh_tag_command.go).
const tagNameRe = /^[a-z][a-z0-9_-]*$/

// Returns a synthesized "Create tag '<name>'" option when the search string is
// a valid new tag name not already present in selected attachments or known
// tags. Used to let users create-and-attach tags directly from the dropdown.
function creatableTagOption(search: string, selected: string[]): { value: string; label: string } | null {
  const q = search.trim().replace(/^tag:/i, '')
  if (!q || !tagNameRe.test(q)) return null
  const value = `tag:${q}`
  if (selected.includes(value)) return null
  if (allKnownTags.value.includes(q)) return null
  return { value, label: `Create tag '${q}'` }
}

// Hint shown in attachment dropdowns when team mode is on and no options match.
// Considers tags already in the current modal's attachments list, so the hint
// doesn't claim "no tags" right after the user creates one in this session.
function teamAttachHint(currentAttachments: string[]): string {
  const hasAnyTag =
    allKnownTags.value.length > 0 ||
    currentAttachments.some(a => a.startsWith('tag:'))
  return hasAnyTag
    ? "Team integrations attach by tag — type a tag name."
    : "You have no tags yet. Team integrations attach by tag — type a tag name to create one."
}

const filteredAttachModalOptions = computed(() => {
  const opts = filterAttachOptions(attachModalSearch.value, attachModal.currentAttachments)
  return attachModal.isTeam ? opts.filter(o => o.value.startsWith('tag:')) : opts
})
const creatableAttachModalOption = computed(() => creatableTagOption(attachModalSearch.value, attachModal.currentAttachments))

// VM options for tagging (used in both add modals)
// Helper: get tags for a VM
function vmTags(vmName: string): string[] {
  if (!data.value?.tagVMs) return []
  const tags: string[] = []
  for (const [tag, vms] of Object.entries(data.value.tagVMs)) {
    if (vms?.includes(vmName)) tags.push(tag)
  }
  return tags
}

const allVMOptions = computed(() => {
  if (!data.value) return [] as { name: string; tags: string[] }[]
  return data.value.boxes.map(b => ({ name: b.name, tags: vmTags(b.name) }))
})

function filterVMOptions(search: string, selected: string[]) {
  const q = search.toLowerCase().trim()
  return allVMOptions.value
    .filter(vm => !selected.includes(vm.name))
    .filter(vm => !q || vm.name.toLowerCase().includes(q) || vm.tags.some(t => t.toLowerCase().includes(q)))
}

const filteredGhAttachOptions = computed(() => {
  const opts = filterAttachOptions(ghAttachSearch.value, ghModal.attachments)
  return ghModal.team ? opts.filter(o => o.value.startsWith('tag:')) : opts
})
const creatableGhAttachOption = computed(() => creatableTagOption(ghAttachSearch.value, ghModal.attachments))
// Target URL VM suggestions
const filteredTargetVMs = computed(() => {
  const boxes = data.value?.boxes || []
  const q = proxyModal.target.toLowerCase().trim()
  if (!q) return boxes.slice(0, 8)
  return boxes.filter(b =>
    b.name.toLowerCase().includes(q) ||
    `https://${b.name}.${boxHost.value}/`.toLowerCase().includes(q)
  ).slice(0, 8)
})

// Detect if the current target URL refers to one of the user's VMs
const detectedPeerVM = computed(() => {
  const raw = proxyModal.target.trim()
  if (!raw) return ''
  try {
    const url = new URL(raw)
    const host = boxHost.value
    if (host && url.hostname.endsWith('.' + host)) {
      const vmName = url.hostname.slice(0, -(host.length + 1))
      if (vmName && !vmName.includes('.')) {
        const boxes = data.value?.boxes || []
        if (boxes.some(b => b.name === vmName)) {
          return vmName
        }
      }
    }
  } catch {
    // not a valid URL yet
  }
  return ''
})

function selectTargetVM(vmName: string) {
  proxyModal.target = `https://${vmName}.${boxHost.value}/`
  proxyModal.usePeer = true
  proxyTargetOpen.value = false
}

const filteredProxyAttachOptions = computed(() => {
  const opts = filterAttachOptions(proxyAttachSearch.value, proxyModal.attachments)
  return proxyModal.team ? opts.filter(o => o.value.startsWith('tag:')) : opts
})
const creatableProxyAttachOption = computed(() => creatableTagOption(proxyAttachSearch.value, proxyModal.attachments))
const filteredGhTagVMOptions = computed(() => filterVMOptions(ghTagVMSearch.value, ghModal.tagVMs))
const filteredProxyTagVMOptions = computed(() => filterVMOptions(proxyTagVMSearch.value, proxyModal.tagVMs))

function fuzzyScore(query: string, text: string): number {
  if (!text) return -1
  const q = query.toLowerCase()
  const t = text.toLowerCase()
  if (t === q) return 1000
  if (t.startsWith(q)) return 500 + (q.length / t.length) * 100
  if (t.includes(q)) return 100 + (q.length / t.length) * 50
  // Fuzzy: all query chars must appear in order
  let qi = 0
  let score = 0
  let consecutive = 0
  for (let i = 0; i < t.length && qi < q.length; i++) {
    if (t[i] === q[qi]) {
      score += 1 + consecutive
      consecutive += 0.5
      qi++
    } else {
      consecutive = 0
    }
  }
  if (qi !== q.length) return -1
  return score
}

const filteredRepos = computed(() => {
  const q = repoSearch.value.trim()
  if (!q) return githubRepos.value.slice(0, 50)
  const scored: Array<{ repo: typeof githubRepos.value[number]; score: number }> = []
  for (const r of githubRepos.value) {
    const nameScore = fuzzyScore(q, r.full_name)
    const descScore = r.description ? fuzzyScore(q, r.description) : -1
    const score = Math.max(nameScore, descScore === -1 ? -1 : descScore * 0.5)
    if (score > 0) scored.push({ repo: r, score })
  }
  scored.sort((a, b) => b.score - a.score)
  return scored.slice(0, 50).map(s => s.repo)
})

// GitHub modal: effective name for tag
const ghEffectiveName = computed(() => {
  return ghModal.name.trim() || (ghModal.repo ? ghModal.repo.replace(/\//g, '-') : 'integration-name')
})

// First tag from attachments (for "Tag additional VMs with #foo")
const ghFirstTag = computed(() => {
  const tag = ghModal.attachments.find(a => a.startsWith('tag:'))
  return tag ? tag.slice(4) : ''
})

const proxyFirstTag = computed(() => {
  const tag = proxyModal.attachments.find(a => a.startsWith('tag:'))
  return tag ? tag.slice(4) : ''
})

// Keep default tag:<name> attachment in sync with effective name
watch(ghEffectiveName, (newName, oldName) => {
  if (oldName && oldName !== 'integration-name') {
    const oldTag = `tag:${oldName}`
    const idx = ghModal.attachments.indexOf(oldTag)
    if (idx !== -1) {
      if (newName && newName !== 'integration-name') {
        ghModal.attachments[idx] = `tag:${newName}`
      } else {
        ghModal.attachments.splice(idx, 1)
      }
    }
  } else if (newName !== 'integration-name' && ghModal.attachments.length === 0) {
    ghModal.attachments.push(`tag:${newName}`)
  }
})

// GitHub modal command builder (returns array of commands)
const ghBuiltCommands = computed(() => {
  if (!ghModal.repo) return [] as string[]
  const name = ghEffectiveName.value
  let cmd = `integrations add github --name=${shellQuote(name)} --repository=${shellQuote(ghModal.repo)}`
  if (ghModal.team) cmd += ' --team'
  if (ghModal.comment.trim()) cmd += ` --comment=${shellQuote(ghModal.comment.trim())}`
  for (const a of ghModal.attachments) {
    cmd += ` --attach=${shellQuote(a)}`
  }
  const cmds: string[] = [cmd]
  const tagName = ghFirstTag.value
  if (tagName) {
    for (const vm of ghModal.tagVMs) {
      cmds.push(`tag ${shellQuote(vm)} ${shellQuote(tagName)}`)
    }
  }
  return cmds
})

// Auto-detect basic auth credentials in pasted target URL
// When team is toggled on, remove non-tag attachments
watch(() => proxyModal.team, (isTeam) => {
  if (isTeam) {
    proxyModal.attachments = proxyModal.attachments.filter(a => a.startsWith('tag:'))
  }
})
watch(() => ghModal.team, (isTeam) => {
  if (isTeam) {
    ghModal.attachments = ghModal.attachments.filter(a => a.startsWith('tag:'))
  }
})

watch(() => proxyModal.target, (newTarget) => {
  // Strip a duplicate scheme prefix that arises when the user pastes a full
  // URL into the pre-filled "https://" field. Match http(s):// followed by
  // another http(s):// and drop the first one.
  const dupScheme = /^https?:\/\/(https?:\/\/)/i
  if (dupScheme.test(newTarget)) {
    proxyModal.target = newTarget.replace(dupScheme, '$1')
    return
  }
  if (proxyModal.authMethod !== 'none' && proxyModal.authMethod !== 'basic') return
  try {
    const url = new URL(newTarget)
    if (url.username || url.password) {
      proxyModal.authMethod = 'basic'
      proxyModal.basicUser = decodeURIComponent(url.username)
      proxyModal.basicPass = decodeURIComponent(url.password)
      url.username = ''
      url.password = ''
      proxyModal.target = url.toString()
    }
  } catch {
    // Not a valid URL yet, ignore
  }
})

// Auto-toggle peer auth when target URL matches a user's VM
watch(detectedPeerVM, (vm) => {
  proxyModal.usePeer = !!vm
})

// Keep default tag:<name> attachment in sync with proxy name
watch(() => proxyModal.name.trim(), (newName, oldName) => {
  if (oldName) {
    const oldTag = `tag:${oldName}`
    const idx = proxyModal.attachments.indexOf(oldTag)
    if (idx !== -1) {
      if (newName) {
        proxyModal.attachments[idx] = `tag:${newName}`
      } else {
        proxyModal.attachments.splice(idx, 1)
      }
    }
  } else if (newName && proxyModal.attachments.length === 0) {
    proxyModal.attachments.push(`tag:${newName}`)
  }
})

// Proxy modal: build target URL with basic auth credentials embedded if needed
function proxyEffectiveTarget(): string {
  const raw = proxyModal.target.trim()
  if (!raw) return ''
  if (proxyModal.authMethod !== 'basic') return raw
  const user = proxyModal.basicUser.trim()
  const pass = proxyModal.basicPass
  if (!user && !pass) return raw
  try {
    const url = new URL(raw)
    url.username = user
    url.password = pass
    return url.toString()
  } catch {
    // If the URL is malformed, just show it as-is
    return raw
  }
}

// Proxy modal command builder (returns array of commands)
function buildProxyCommands(mask: boolean): string[] {
  if (!proxyModal.name.trim() || !proxyModal.target.trim()) return []
  const name = proxyModal.name.trim()
  const target = (mask && proxyModal.authMethod !== 'basic') ? proxyModal.target.trim() : proxyEffectiveTarget()
  let cmd = `integrations add http-proxy --name=${shellQuote(name)} --target=${shellQuote(target)}`
  if (proxyModal.team) cmd += ' --team'
  if (proxyModal.comment.trim()) cmd += ` --comment=${shellQuote(proxyModal.comment.trim())}`
  if (proxyModal.usePeer && detectedPeerVM.value) {
    cmd += ' --peer'
  } else if (proxyModal.authMethod === 'bearer' && proxyModal.bearer.trim()) {
    cmd += mask ? ' --bearer=\u2022\u2022\u2022\u2022' : ` --bearer=${shellQuote(proxyModal.bearer.trim())}`
  } else if (proxyModal.authMethod === 'header' && proxyModal.header.trim()) {
    cmd += mask ? ' --header=\u2022\u2022\u2022\u2022' : ` --header=${shellQuote(proxyModal.header.trim())}`
  }
  for (const a of proxyModal.attachments) {
    cmd += ` --attach=${shellQuote(a)}`
  }
  const cmds: string[] = [cmd]
  const tagName = proxyFirstTag.value
  if (tagName) {
    for (const vm of proxyModal.tagVMs) {
      cmds.push(`tag ${shellQuote(vm)} ${shellQuote(tagName)}`)
    }
  }
  return cmds
}

const proxyBuiltCommands = computed(() => buildProxyCommands(false))
const proxyDisplayCommands = computed(() => buildProxyCommands(true))



// GitHub attachment helpers
function addGhAttachment(opt: string) {
  if (!ghModal.attachments.includes(opt)) {
    ghModal.attachments.push(opt)
  }
  ghAttachSearch.value = ''
  nextTick(() => {
    ghAttachInputRef.value?.focus()
    ghAttachOpen.value = true
  })
}

function removeGhAttachment(opt: string) {
  ghModal.attachments = ghModal.attachments.filter(a => a !== opt)
}

// GitHub tag VM helpers
function addGhTagVM(vm: string) {
  if (!ghModal.tagVMs.includes(vm)) {
    ghModal.tagVMs.push(vm)
  }
  ghTagVMSearch.value = ''
  nextTick(() => {
    ghTagVMInputRef.value?.focus()
    ghTagVMOpen.value = true
  })
}

function removeGhTagVM(vm: string) {
  ghModal.tagVMs = ghModal.tagVMs.filter(v => v !== vm)
}

// Proxy attachment helpers
function addProxyAttachment(opt: string) {
  if (!proxyModal.attachments.includes(opt)) {
    proxyModal.attachments.push(opt)
  }
  proxyAttachSearch.value = ''
  nextTick(() => {
    proxyAttachInputRef.value?.focus()
    proxyAttachOpen.value = true
  })
}

function removeProxyAttachment(opt: string) {
  proxyModal.attachments = proxyModal.attachments.filter(a => a !== opt)
}

// Proxy tag VM helpers
function addProxyTagVM(vm: string) {
  if (!proxyModal.tagVMs.includes(vm)) {
    proxyModal.tagVMs.push(vm)
  }
  proxyTagVMSearch.value = ''
  nextTick(() => {
    proxyTagVMInputRef.value?.focus()
    proxyTagVMOpen.value = true
  })
}

function removeProxyTagVM(vm: string) {
  proxyModal.tagVMs = proxyModal.tagVMs.filter(v => v !== vm)
}

// --- Service Proxy Modal Logic ---

const svcEffectiveName = computed(() => {
  return svcModal.name.trim() || svcModal.namePlaceholder
})

const svcFirstTag = computed(() => {
  const tag = svcModal.attachments.find(a => a.startsWith('tag:'))
  return tag ? tag.slice(4) : ''
})

const filteredSvcAttachOptions = computed(() => {
  const opts = filterAttachOptions(svcAttachSearch.value, svcModal.attachments)
  return svcModal.team ? opts.filter(o => o.value.startsWith('tag:')) : opts
})
const creatableSvcAttachOption = computed(() => creatableTagOption(svcAttachSearch.value, svcModal.attachments))
const filteredSvcTagVMOptions = computed(() => filterVMOptions(svcTagVMSearch.value, svcModal.tagVMs))

watch(svcEffectiveName, (newName, oldName) => {
  if (oldName) {
    const oldTag = `tag:${oldName}`
    const idx = svcModal.attachments.indexOf(oldTag)
    if (idx !== -1) {
      if (newName) {
        svcModal.attachments[idx] = `tag:${newName}`
      } else {
        svcModal.attachments.splice(idx, 1)
      }
    }
  } else if (newName && svcModal.attachments.length === 0) {
    svcModal.attachments.push(`tag:${newName}`)
  }
})

watch(() => svcModal.team, (isTeam) => {
  if (isTeam) {
    svcModal.attachments = svcModal.attachments.filter(a => a.startsWith('tag:'))
  }
})

function openServiceProxy(serviceId: string) {
  const def = serviceProxies.find(s => s.id === serviceId)
  if (!def) return
  svcModal.visible = true
  svcModal.serviceId = serviceId
  svcModal.title = `Add ${def.label} Integration`
  svcModal.name = def.defaultName
  svcModal.namePlaceholder = def.defaultName
  svcModal.target = def.targetURL
  svcModal.targetPlaceholder = def.targetURL
  svcModal.targetLabel = def.targetLabel
  svcModal.showTarget = def.targetEditable
  svcModal.team = false
  svcModal.comment = ''
  svcModal.fields = def.fields.map(f => ({
    label: f.label, placeholder: f.placeholder, secret: f.secret,
    authRole: f.authRole, headerName: f.headerName, value: '', error: '',
  }))
  svcModal.attachments = [`tag:${def.defaultName}`]
  svcModal.tagVMs = []
  svcModal.running = false
  svcModal.result = null
  svcAttachSearch.value = ''
  svcTagVMSearch.value = ''
}

function closeSvcModal() {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  svcModal.visible = false
  if (svcModal.result?.success) reload()
}

function addSvcAttachment(opt: string) {
  if (!svcModal.attachments.includes(opt)) svcModal.attachments.push(opt)
  svcAttachSearch.value = ''
  nextTick(() => { svcAttachInputRef.value?.focus(); svcAttachOpen.value = true })
}
function removeSvcAttachment(opt: string) {
  svcModal.attachments = svcModal.attachments.filter(a => a !== opt)
}
function addSvcTagVM(vm: string) {
  if (!svcModal.tagVMs.includes(vm)) svcModal.tagVMs.push(vm)
  svcTagVMSearch.value = ''
  nextTick(() => { svcTagVMInputRef.value?.focus(); svcTagVMOpen.value = true })
}
function removeSvcTagVM(vm: string) {
  svcModal.tagVMs = svcModal.tagVMs.filter(v => v !== vm)
}

// --- Reflection modal ---
const reflectionEffectiveName = computed(() => reflectionModal.name.trim() || 'reflection')
const filteredReflectionAttachOptions = computed(() => {
  const opts = filterAttachOptions(reflectionAttachSearch.value, reflectionModal.attachments)
  return reflectionModal.team ? opts.filter(o => o.value.startsWith('tag:')) : opts
})
watch(reflectionEffectiveName, (newName, oldName) => {
  if (oldName) {
    const oldTag = `tag:${oldName}`
    const idx = reflectionModal.attachments.indexOf(oldTag)
    if (idx !== -1) {
      if (newName) reflectionModal.attachments[idx] = `tag:${newName}`
      else reflectionModal.attachments.splice(idx, 1)
    }
  } else if (newName && reflectionModal.attachments.length === 0) {
    reflectionModal.attachments.push(`tag:${newName}`)
  }
})
watch(() => reflectionModal.team, (isTeam) => {
  if (isTeam) reflectionModal.attachments = reflectionModal.attachments.filter(a => a.startsWith('tag:'))
})

function openAddReflection() {
  reflectionModal.visible = true
  reflectionModal.name = 'reflection'
  reflectionModal.comment = ''
  reflectionModal.team = false
  reflectionModal.fieldEmail = true
  reflectionModal.fieldIntegrations = true
  reflectionModal.fieldTags = true
  reflectionModal.attachments = ['tag:reflection']
  reflectionModal.tagVMs = []
  reflectionModal.running = false
  reflectionModal.result = null
  reflectionAttachSearch.value = ''
  reflectionTagVMSearch.value = ''
}
function closeReflectionModal() {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  reflectionModal.visible = false
  if (reflectionModal.result?.success) reload()
}
function addReflectionAttachment(opt: string) {
  if (!reflectionModal.attachments.includes(opt)) reflectionModal.attachments.push(opt)
  reflectionAttachSearch.value = ''
  nextTick(() => { reflectionAttachInputRef.value?.focus(); reflectionAttachOpen.value = true })
}
function removeReflectionAttachment(opt: string) {
  reflectionModal.attachments = reflectionModal.attachments.filter(a => a !== opt)
}

const reflectionSelectedFields = computed(() => {
  const fs: string[] = []
  if (reflectionModal.fieldEmail) fs.push('email')
  if (reflectionModal.fieldIntegrations) fs.push('integrations')
  if (reflectionModal.fieldTags) fs.push('tags')
  return fs
})

const reflectionBuiltCommands = computed(() => {
  const name = reflectionEffectiveName.value
  if (!name) return [] as string[]
  const fields = reflectionSelectedFields.value
  if (fields.length === 0) return [] as string[]
  const fieldsArg = fields.length === 3 ? 'all' : fields.join(',')
  let cmd = `integrations add reflection --name=${shellQuote(name)} --fields=${shellQuote(fieldsArg)}`
  if (reflectionModal.team) cmd += ' --team'
  if (reflectionModal.comment.trim()) cmd += ` --comment=${shellQuote(reflectionModal.comment.trim())}`
  for (const a of reflectionModal.attachments) cmd += ` --attach=${shellQuote(a)}`
  return [cmd]
})

async function runReflectionCommand() {
  const cmds = reflectionBuiltCommands.value
  if (cmds.length === 0) return
  reflectionModal.running = true
  reflectionModal.result = null
  try {
    const outputs: string[] = []
    for (const cmd of cmds) {
      const res = await runCommand(cmd)
      if (!res.success) {
        reflectionModal.result = { success: false, output: '', error: res.output || res.error || 'Command failed' }
        return
      }
      if (res.output) outputs.push(res.output)
    }
    reflectionModal.result = { success: true, output: outputs.join('\n') || 'Done', error: '' }
  } catch (err: any) {
    reflectionModal.result = { success: false, output: '', error: err.message || 'Network error' }
  } finally {
    reflectionModal.running = false
  }
}

function svcValidateFields(): boolean {
  const def = serviceProxies.find(s => s.id === svcModal.serviceId)
  if (!def) return false
  let valid = true
  for (let i = 0; i < svcModal.fields.length; i++) {
    const fieldDef = def.fields[i]
    const field = svcModal.fields[i]
    field.error = ''
    if (fieldDef.validate) {
      const err = fieldDef.validate(field.value)
      if (err) { field.error = err; valid = false }
    } else if (!field.value.trim()) {
      field.error = `${field.label} is required`; valid = false
    }
  }
  return valid
}

function svcEffectiveTarget(): string {
  const target = svcModal.showTarget ? svcModal.target.trim() : svcModal.targetPlaceholder
  if (!target) return ''
  const username = svcModal.fields.find(f => f.authRole === 'username')?.value.trim() || ''
  const password = svcModal.fields.find(f => f.authRole === 'password')?.value || ''
  if (!username && !password) return target
  try {
    const url = new URL(target)
    if (username) url.username = username
    if (password) url.password = password
    return url.toString()
  } catch { return target }
}

function buildSvcCommands(mask: boolean): string[] {
  const name = svcEffectiveName.value
  const target = svcModal.showTarget ? svcModal.target.trim() : svcModal.targetPlaceholder
  if (!name || !target) return []
  if (!svcModal.fields.every(f => f.value.trim())) return []
  const effectiveTarget = svcEffectiveTarget()
  const displayTarget = mask ? (svcModal.showTarget ? svcModal.target.trim() : svcModal.targetPlaceholder) : effectiveTarget
  let cmd = `integrations add http-proxy --name=${shellQuote(name)} --target=${shellQuote(displayTarget)}`
  if (svcModal.team) cmd += ' --team'
  if (svcModal.comment.trim()) cmd += ` --comment=${shellQuote(svcModal.comment.trim())}`
  for (const f of svcModal.fields) {
    if (f.authRole === 'header' && f.headerName && f.value.trim()) {
      cmd += mask ? ` --header=${shellQuote(`${f.headerName}: ••••`)}` : ` --header=${shellQuote(`${f.headerName}: ${f.value.trim()}`)}`
    } else if (f.authRole === 'bearer' && f.value.trim()) {
      cmd += mask ? ' --bearer=••••' : ` --bearer=${shellQuote(f.value.trim())}`
    }
  }
  for (const a of svcModal.attachments) cmd += ` --attach=${shellQuote(a)}`
  const cmds: string[] = [cmd]
  const tagName = svcFirstTag.value
  if (tagName) {
    for (const vm of svcModal.tagVMs) cmds.push(`tag ${shellQuote(vm)} ${shellQuote(tagName)}`)
  }
  return cmds
}

const svcBuiltCommands = computed(() => buildSvcCommands(false))
const svcDisplayCommands = computed(() => buildSvcCommands(true))

async function runSvcCommand() {
  if (!svcValidateFields()) return
  const cmds = svcBuiltCommands.value
  if (cmds.length === 0) return
  svcModal.running = true
  svcModal.result = null
  try {
    const outputs: string[] = []
    for (const cmd of cmds) {
      const res = await runCommand(cmd)
      if (!res.success) {
        svcModal.result = { success: false, output: '', error: res.output || res.error || 'Command failed' }
        return
      }
      if (res.output) outputs.push(res.output)
    }
    svcModal.result = { success: true, output: outputs.join('\n') || 'Done', error: '' }
  } catch (err: any) {
    svcModal.result = { success: false, output: '', error: err.message || 'Network error' }
  } finally {
    svcModal.running = false
  }
}

// Attach modal: immediately attach a spec
async function attachFromModal(opt: string) {
  attachModal.error = ''
  attachModalSearch.value = ''
  const cmd = `integrations attach ${shellQuote(attachModal.name)} ${shellQuote(opt)}`
  try {
    const res = await runCommand(cmd)
    if (res.success) {
      attachModal.currentAttachments.push(opt)
      await reload()
    } else {
      attachModal.error = res.output || res.error || 'Attach failed'
    }
  } catch (err: any) {
    attachModal.error = err.message || 'Network error'
  }
  nextTick(() => {
    attachModalInputRef.value?.focus()
    attachModalOpen.value = true
  })
}

// Attach modal: immediately detach a spec
async function detachFromModal(spec: string) {
  attachModal.error = ''
  attachModal.removing = spec
  const cmd = `integrations detach ${shellQuote(attachModal.name)} ${shellQuote(spec)}`
  try {
    const res = await runCommand(cmd)
    if (res.success) {
      attachModal.currentAttachments = attachModal.currentAttachments.filter(a => a !== spec)
      await reload()
    } else {
      attachModal.error = res.output || res.error || 'Detach failed'
    }
  } catch (err: any) {
    attachModal.error = err.message || 'Network error'
  } finally {
    attachModal.removing = ''
  }
}

// Close modals on Escape
// Close a dropdown after a short delay (allows mousedown on options to fire first)
function delayClose(fn: () => void) {
  setTimeout(fn, 150)
}

function onEscapeKey(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (attachModal.visible) { closeAttachModal(); return }
  if (ghSetupModal.visible) { ghSetupModal.visible = false; return }
  if (ghModal.visible) { closeGhModal(); return }
  if (proxyModal.visible) { closeProxyModal(); return }
  if (svcModal.visible) { closeSvcModal(); return }
}

// Close dropdowns on outside click
function onDocClick(e: MouseEvent) {
  if (repoComboboxRef.value && !repoComboboxRef.value.contains(e.target as Node)) {
    repoDropdownOpen.value = false
  }
  if (ghAttachRef.value && !ghAttachRef.value.contains(e.target as Node)) {
    ghAttachOpen.value = false
  }
  if (ghTagVMRef.value && !ghTagVMRef.value.contains(e.target as Node)) {
    ghTagVMOpen.value = false
  }
  if (proxyTargetRef.value && !proxyTargetRef.value.contains(e.target as Node)) {
    proxyTargetOpen.value = false
  }
  if (proxyAttachRef.value && !proxyAttachRef.value.contains(e.target as Node)) {
    proxyAttachOpen.value = false
  }
  if (proxyTagVMRef.value && !proxyTagVMRef.value.contains(e.target as Node)) {
    proxyTagVMOpen.value = false
  }
  if (attachModalRef.value && !attachModalRef.value.contains(e.target as Node)) {
    attachModalOpen.value = false
  }
  if (svcAttachRef.value && !svcAttachRef.value.contains(e.target as Node)) {
    svcAttachOpen.value = false
  }
  if (svcTagVMRef.value && !svcTagVMRef.value.contains(e.target as Node)) {
    svcTagVMOpen.value = false
  }
}

onMounted(() => {
  document.addEventListener('click', onDocClick)
  document.addEventListener('keydown', onEscapeKey)
  loadIntegrations()
})

onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onEscapeKey)
})

async function reload() {
  try {
    data.value = await fetchIntegrations()
  } catch (err) {
    console.error('Failed to reload integrations:', err)
  }
}

async function loadIntegrations() {
  loading.value = true
  loadError.value = ''
  try {
    data.value = await fetchIntegrations()
    if (data.value.integrationScheme) integrationScheme.value = data.value.integrationScheme
    if (data.value.boxHost) boxHost.value = data.value.boxHost

    // After GitHub OAuth callback, auto-open the add-repo modal
    const params = new URLSearchParams(window.location.search)
    if (params.get('callout') === 'github-connected') {
      window.history.replaceState({}, '', window.location.pathname)
      if (data.value.githubAccounts.length > 0) {
        nextTick(() => openAddGitHubRepo())
      } else {
        showError('GitHub account was not linked. Try installing the GitHub app again.')
        nextTick(() => openGitHubSetup())
      }
    }
  } catch (err: any) {
    console.error('Failed to load integrations:', err)
    loadError.value = err.message || 'Failed to load data'
  } finally {
    loading.value = false
  }
}

function openModal(opts: Partial<typeof modal>) {
  Object.assign(modal, {
    visible: true,
    title: '',
    description: '',
    command: '',
    commandPrefix: '',
    inputPlaceholder: '',
    danger: false,
    ...opts,
  })
}

function removeIntegration(name: string) {
  openModal({
    title: 'Remove Integration',
    command: `integrations remove ${shellQuote(name)}`,
    description: 'Remove this integration and detach it from all VMs.',
    danger: true,
  })
}

function attachViaCommand(name: string) {
  // Find existing attachments for this integration
  const ig = [...(data.value?.githubIntegrations || []), ...(data.value?.proxyIntegrations || [])].find(i => i.name === name)
  attachModal.visible = true
  attachModal.name = name
  attachModal.isTeam = ig?.isTeam || false
  attachModal.currentAttachments = [...(ig?.attachments || [])]
  attachModal.removing = ''
  attachModal.error = ''
  attachModalSearch.value = ''
  nextTick(() => {
    attachModalInputRef.value?.focus()
    attachModalOpen.value = true
  })
}

function closeAttachModal() {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  attachModal.visible = false
}

function detachSpec(integrationName: string, spec: string) {
  openModal({
    title: 'Detach Integration',
    command: `integrations detach ${shellQuote(integrationName)} ${shellQuote(spec)}`,
    description: 'Detach this integration from the specified VM.',
  })
}

// GitHub add modal
async function openAddGitHubRepo() {
  ghModal.visible = true
  ghModal.repo = ''
  ghModal.name = ''
  ghModal.comment = ''
  ghModal.team = false
  ghModal.attachments = []
  ghModal.tagVMs = []
  ghModal.running = false
  ghModal.result = null
  repoSearch.value = ''
  ghAttachSearch.value = ''
  ghTagVMSearch.value = ''
  loadingRepos.value = true
  try {
    const resp = await fetch('/github/repos')
    const result = await resp.json()
    if (result.success) {
      githubRepos.value = result.repos || []
    } else {
      showError('Failed to load repos: ' + (result.error || 'unknown error'))
    }
  } catch (err: any) {
    showError('Failed to load repos: ' + err.message)
  } finally {
    loadingRepos.value = false
  }
}

function selectRepo(repo: any) {
  ghModal.repo = repo.full_name
  repoSearch.value = repo.full_name
  repoDropdownOpen.value = false
  // Don't auto-fill name — let placeholder show the default
}

function closeGhModal() {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  ghModal.visible = false
  if (ghModal.result?.success) reload()
}

async function runGhCommand() {
  const cmds = ghBuiltCommands.value
  if (cmds.length === 0) return
  ghModal.running = true
  ghModal.result = null
  try {
    const outputs: string[] = []
    for (const cmd of cmds) {
      const res = await runCommand(cmd)
      if (!res.success) {
        ghModal.result = { success: false, output: '', error: res.output || res.error || 'Command failed' }
        return
      }
      if (res.output) outputs.push(res.output)
    }
    ghModal.result = { success: true, output: outputs.join('\n') || 'Done', error: '' }
  } catch (err: any) {
    ghModal.result = { success: false, output: '', error: err.message || 'Network error' }
  } finally {
    ghModal.running = false
  }
}

// HTTP Proxy add modal
function openAddHTTPProxy() {
  proxyModal.visible = true
  proxyModal.name = ''
  proxyModal.target = 'https://'
  proxyModal.usePeer = false
  proxyModal.authMethod = 'none'
  proxyModal.basicUser = ''
  proxyModal.basicPass = ''
  proxyModal.bearer = ''
  proxyModal.header = ''
  proxyModal.comment = ''
  proxyModal.team = false
  proxyModal.attachments = []
  proxyModal.tagVMs = []
  proxyModal.running = false
  proxyModal.result = null
  proxyAttachSearch.value = ''
  proxyTagVMSearch.value = ''
  nextTick(() => proxyNameInputRef.value?.focus())
}

function closeProxyModal() {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  proxyModal.visible = false
  if (proxyModal.result?.success) reload()
}

async function runProxyCommand() {
  const cmds = proxyBuiltCommands.value
  if (cmds.length === 0) return
  proxyModal.running = true
  proxyModal.result = null
  try {
    const outputs: string[] = []
    for (const cmd of cmds) {
      const res = await runCommand(cmd)
      if (!res.success) {
        proxyModal.result = { success: false, output: '', error: res.output || res.error || 'Command failed' }
        return
      }
      if (res.output) outputs.push(res.output)
    }
    proxyModal.result = { success: true, output: outputs.join('\n') || 'Done', error: '' }
  } catch (err: any) {
    proxyModal.result = { success: false, output: '', error: err.message || 'Network error' }
  } finally {
    proxyModal.running = false
  }
}

function verifyGitHub(installationID: number) {
  delete verifyResults[installationID]
  verifyingAccounts.value.add(installationID)
  fetch(`/github/verify?installation_id=${installationID}`)
    .then(r => r.json())
    .then(result => {
      if (result.success) {
        const count = result.repo_count ?? 0
        verifyResults[installationID] = { message: `${count} repo${count !== 1 ? 's' : ''} accessible`, isError: false }
      } else {
        verifyResults[installationID] = { message: result.error || 'unknown error', isError: true }
      }
    })
    .catch(err => {
      verifyResults[installationID] = { message: err.message, isError: true }
    })
    .finally(() => {
      verifyingAccounts.value.delete(installationID)
    })
}

function unlinkGitHub(installationID: number) {
  unlinkingAccounts.value.add(installationID)
}

function cancelUnlinkGitHub(installationID: number) {
  unlinkingAccounts.value.delete(installationID)
}

function confirmUnlinkGitHub(installationID: number) {
  fetch('/github/unlink', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ installation_id: installationID }),
  })
    .then(r => r.json())
    .then(result => {
      if (result.success) {
        unlinkingAccounts.value.delete(installationID)
        reload()
      } else {
        showError('Unlink failed: ' + (result.error || 'unknown error'))
        unlinkingAccounts.value.delete(installationID)
      }
    })
    .catch(err => {
      showError('Unlink failed: ' + err.message)
      unlinkingAccounts.value.delete(installationID)
    })
}
</script>

<style scoped>
.integrations-page {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.page-title {
  font-size: 24px;
  font-weight: 700;
  color: var(--text-color);
  margin-bottom: -8px;
}

.page-subtitle {
  font-size: 14px;
  color: var(--text-color-muted);
  margin-bottom: 4px;
}

.page-subtitle a {
  color: var(--text-color-secondary);
}

.section-desc {
  font-size: 13px;
  color: var(--text-color-muted);
  margin-bottom: 12px;
  line-height: 1.5;
}

/* Integration grid */
.integration-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 10px;
}

.grid-btn {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 20px 12px;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.15s;
  position: relative;
  font-family: inherit;
  color: var(--text-color);
}

.grid-btn:hover {
  background: var(--surface-hover);
  border-color: var(--text-color-secondary);
}

.grid-btn-icon {
  font-size: 24px;
  line-height: 1;
  display: flex;
  align-items: center;
  justify-content: center;
}

.grid-btn-icon-text {
  font-size: inherit;
}

.grid-btn-icon-text :deep(svg) {
  width: 24px;
  height: 24px;
}

.grid-btn-label {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.grid-btn-badge {
  position: absolute;
  top: 6px;
  right: 6px;
  font-size: 10px;
  background: var(--primary-color, #14b8a6);
  color: white;
  border-radius: 50%;
  width: 18px;
  height: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 600;
}

.field-error {
  font-size: 11px;
  color: var(--danger-color, #ef4444);
  margin-top: 2px;
}

.section-desc a {
  color: var(--text-color-secondary);
}

.subsection-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-color-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin-bottom: 8px;
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

.inline-msg {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border-radius: 6px;
  font-size: 13px;
  margin-bottom: 16px;
}

.inline-error {
  background: var(--danger-bg);
  color: var(--danger-text);
  border: 1px solid var(--danger-border);
}

.inline-success {
  background: var(--success-bg);
  color: var(--success-text);
  border: 1px solid var(--success-border);
}

.inline-msg-dismiss {
  margin-left: auto;
  background: none;
  border: none;
  color: inherit;
  cursor: pointer;
  font-size: 16px;
  padding: 0 4px;
}

.card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 20px;
}

.card-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 12px;
  display: flex;
  align-items: center;
  gap: 8px;
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

.empty-msg {
  color: var(--text-color-muted);
  font-size: 13px;
}

.text-muted {
  color: var(--text-color-muted);
  font-size: 12px;
}

/* GitHub accounts */
.gh-install-section {
  padding: 8px 0;
}

.gh-install-hint {
  font-size: 13px;
  color: var(--text-color-muted);
}

.gh-verify-result {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 3px;
}

.gh-verify-ok {
  color: var(--success-text);
  background: var(--success-bg);
}

.gh-verify-error {
  color: var(--danger-text);
  background: var(--danger-bg);
}

.gh-account-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--surface-subtle);
}

.gh-account-info {
  display: flex;
  align-items: center;
  gap: 8px;
}

.gh-login {
  font-weight: 500;
  font-size: 13px;
}

.gh-account-actions {
  display: flex;
  gap: 4px;
}

/* Active Integrations Table */
.integrations-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.integrations-table th {
  text-align: left;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-color-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 0 8px 8px 8px;
  border-bottom: 1px solid var(--surface-border);
}

.integrations-table td {
  padding: 10px 8px;
  border-bottom: 1px solid var(--surface-subtle);
  vertical-align: top;
}

.integrations-table tbody tr:last-child td {
  border-bottom: none;
}

.col-name {
  white-space: nowrap;
}

.col-detail {
  min-width: 0;
}

.col-attach {
  min-width: 100px;
}

.col-actions {
  white-space: nowrap;
  text-align: right;
}

.table-name-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}

.table-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.table-icon :deep(svg) {
  width: 18px;
  height: 18px;
}

.table-icon-pi {
  font-size: 16px;
  flex-shrink: 0;
}

.table-name-info {
  display: flex;
  align-items: center;
  gap: 6px;
}

.table-name {
  font-weight: 600;
  font-size: 13px;
}

.table-detail {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.table-detail-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  align-items: center;
}

@media (max-width: 700px) {
  .integrations-table thead {
    display: none;
  }
  .integrations-table,
  .integrations-table tbody,
  .integrations-table tr,
  .integrations-table td {
    display: block;
  }
  .integrations-table tr {
    padding: 10px 0;
    border-bottom: 1px solid var(--surface-subtle);
  }
  .integrations-table td {
    border-bottom: none;
    padding: 2px 0;
  }
  .col-actions {
    text-align: left;
    padding-top: 6px !important;
  }
}

/* Integrations (legacy row layout, used in modals) */
.integration-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  padding: 10px 0;
  border-bottom: 1px solid var(--surface-subtle);
  gap: 12px;
}

.integration-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  flex: 1;
}

.integration-header {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.integration-attachments {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
}

.integration-name {
  font-weight: 500;
  font-size: 13px;
}

.integration-actions {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
}

.attachment-tag {
  font-size: 10px;
  padding: 1px 6px;
  background: var(--tag-bg);
  color: var(--text-color-secondary);
  border-radius: 3px;
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.badge {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
}

.badge-blue {
  background: var(--badge-share-bg);
  color: var(--badge-share-text);
}

.badge-yellow {
  background: var(--badge-public-bg);
  color: var(--badge-public-text);
}

.badge-team {
  background: #e0e7ff;
  color: #3730a3;
}

@media (prefers-color-scheme: dark) {
  .badge-team {
    background: #312e81;
    color: #c7d2fe;
  }
}

.form-row-check {
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.form-row-check input[type="checkbox"] {
  width: 14px;
  height: 14px;
  margin: 0;
  cursor: pointer;
  position: relative;
  top: 1px;
}

.form-row-check label {
  cursor: pointer;
  font-weight: normal;
}

.form-row-check .text-muted {
  margin-left: 2px;
}

.badge-green {
  background: #dcfce7;
  color: #166534;
}

.target-url-wrapper {
  position: relative;
}

.peer-hint {
  margin-top: 6px;
}

.peer-check {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-muted);
  cursor: pointer;
}

.peer-check input[type="checkbox"] {
  margin: 0;
  cursor: pointer;
}

.peer-check strong {
  color: var(--text-primary);
}

/* Modal overlay */
.modal-overlay {
  position: fixed;
  inset: 0;
  background: var(--surface-overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-panel {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  width: 520px;
  max-width: 90vw;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.2);
}

.modal-panel-narrow {
  width: 420px;
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

/* Form rows */
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

.form-input {
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

.radio-group {
  display: flex;
  flex-wrap: wrap;
  gap: 0.25rem 1rem;
}

.radio-label {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  font-size: 0.92rem;
  cursor: pointer;
  color: var(--text-color);
}

.radio-label input[type="radio"] {
  margin: 0;
  accent-color: var(--primary-color, #6366f1);
}

/* Repo combobox */
.repo-combobox {
  position: relative;
}

.repo-dropdown {
  position: absolute;
  top: 100%;
  left: 0;
  right: 0;
  max-height: 240px;
  overflow-y: auto;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  z-index: 100;
  margin-top: 2px;
}

.repo-option {
  padding: 8px 10px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.repo-option:hover {
  background: var(--surface-hover);
}

.repo-option-selected {
  background: var(--surface-subtle);
}

.repo-option-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-color);
}

.repo-option-sub {
  font-size: 11px;
  color: var(--text-color-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.repo-option-empty {
  color: var(--text-color-muted);
  font-size: 12px;
  cursor: default;
}

.repo-option-empty:hover {
  background: transparent;
}

/* Multi-select attachment picker */
.multi-select {
  position: relative;
}

.multi-select-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-bottom: 4px;
}

.multi-select-tag {
  font-size: 11px;
  padding: 2px 6px;
  background: var(--tag-bg);
  color: var(--text-color-secondary);
  border-radius: 3px;
  display: inline-flex;
  align-items: center;
  gap: 3px;
}

.multi-select-tag-remove {
  background: none;
  border: none;
  color: var(--text-color-secondary);
  cursor: pointer;
  padding: 0 1px;
  font-size: 13px;
  line-height: 1;
}

.multi-select-tag-remove:hover {
  color: var(--danger-color);
}

.multi-select-tag-removing {
  opacity: 0.5;
}

.multi-select-tag-default {
  border: 1px solid var(--primary-color, #14b8a6);
}

.tag-default-hint {
  font-size: 9px;
  text-transform: uppercase;
  letter-spacing: 0.03em;
  color: var(--primary-color, #14b8a6);
  opacity: 0.8;
}

.tag-hash {
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-weight: 600;
  color: var(--text-color);
}

.attach-empty {
  font-size: 12px;
  color: var(--text-color-muted);
  padding: 4px 0;
}

.attach-dropdown {
  position: absolute;
  top: 100%;
  left: 0;
  right: 0;
  max-height: 200px;
  overflow-y: auto;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  z-index: 100;
  margin-top: 2px;
}

.attach-option {
  padding: 6px 10px;
  cursor: pointer;
  font-size: 12px;
  color: var(--text-color);
}

.attach-option:hover {
  background: var(--surface-hover);
}

.attach-option {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.attach-option-context {
  font-size: 11px;
  color: var(--text-color-muted);
  margin-left: 8px;
  white-space: nowrap;
}

.attach-option-create {
  color: var(--text-color);
  font-style: italic;
}

.attach-hint {
  padding: 8px 10px;
  font-size: 11px;
  color: var(--text-color-muted);
  line-height: 1.4;
}


/* Command preview */
.cmd-preview {
  background: var(--surface-subtle);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 8px 12px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 12px;
  word-break: break-all;
  color: var(--text-color-secondary);
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.cmd-preview code {
  display: block;
}

.cmd-result {
  padding: 8px 12px;
  border-radius: 4px;
  font-size: 12px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  white-space: pre-wrap;
}

.cmd-result.success {
  background: var(--success-bg);
  color: var(--success-text);
  border: 1px solid var(--success-border);
}

.cmd-result.error {
  background: var(--danger-bg);
  color: var(--danger-text);
  border: 1px solid var(--danger-border);
}

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
  text-decoration: none;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.btn-primary {
  background: var(--text-color);
  color: var(--surface-ground);
}

.btn-primary:hover:not(:disabled) {
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
  text-decoration: none;
}

.btn-danger {
  background: var(--btn-bg);
  color: var(--danger-color);
  border-color: var(--danger-border);
}

.btn-danger:hover {
  background: var(--danger-bg);
}

/* Attachment tags with remove button */
.attachment-tag-remove {
  background: none;
  border: none;
  color: var(--text-color-secondary);
  cursor: pointer;
  padding: 0 2px;
  margin-left: 2px;
  font-size: 14px;
  line-height: 1;
}

.attachment-tag-remove:hover {
  color: var(--danger-color);
}

.attachment-tag-add {
  font-size: 10px;
  padding: 1px 6px;
  background: var(--surface-ground);
  color: var(--text-color-secondary);
  border: 1px dashed var(--surface-border);
  border-radius: 3px;
  cursor: pointer;
  transition: all 0.15s;
}

.attachment-tag-add:hover {
  background: var(--surface-hover);
  border-color: var(--text-color-secondary);
}

/* Usage rows */
.usage-rows {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.usage-row {
  font-size: 11px;
  color: var(--text-color-muted);
  display: flex;
  align-items: center;
  gap: 4px;
}

.usage-row code {
  background: var(--surface-ground);
  padding: 2px 6px;
  border-radius: 3px;
  font-family: var(--font-mono);
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
/* Reflection tile: exy.png in an ornate frame, shimmering like a reflecting pool */
.reflection-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.4em;
  height: 1.4em;
  line-height: 1;
}
.reflection-frame {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  height: 100%;
  border-radius: 45% 55% 48% 52% / 55% 48% 52% 45%;
  background:
    radial-gradient(circle at 30% 30%, #fff8e0 0%, #d4af37 30%, #8b6914 70%, #4a3810 100%);
  padding: 2px;
  box-shadow:
    inset 0 0 0 1px rgba(255, 230, 150, 0.9),
    inset 0 0 4px rgba(139, 105, 20, 0.6),
    0 0 3px rgba(212, 175, 55, 0.5);
}
.reflection-frame::before {
  /* inner ornate ring */
  content: "";
  position: absolute;
  inset: 2px;
  border-radius: inherit;
  border: 1px solid rgba(74, 56, 16, 0.7);
  pointer-events: none;
  z-index: 2;
}
.reflection-fish {
  width: 78%;
  height: 78%;
  object-fit: contain;
  border-radius: inherit;
  /* subtle vertical flip suggestion via gradient — keep fish upright, reflection is in the shimmer */
  filter: drop-shadow(0 1px 0 rgba(0, 0, 0, 0.25));
  animation: reflection-sway 6s ease-in-out infinite;
  position: relative;
  z-index: 1;
}
.reflection-shimmer {
  position: absolute;
  inset: 2px;
  border-radius: inherit;
  pointer-events: none;
  overflow: hidden;
  mix-blend-mode: screen;
  z-index: 3;
}
.reflection-shimmer::before {
  content: "";
  position: absolute;
  inset: -20%;
  background:
    repeating-linear-gradient(
      115deg,
      transparent 0px,
      transparent 8px,
      rgba(255, 255, 255, 0.25) 9px,
      transparent 10px,
      transparent 18px,
      rgba(180, 220, 255, 0.15) 19px,
      transparent 20px
    );
  animation: reflection-ripple 6s linear infinite;
}
.reflection-shimmer::after {
  content: "";
  position: absolute;
  inset: -50%;
  background: linear-gradient(
    100deg,
    transparent 40%,
    rgba(255, 255, 255, 0.3) 50%,
    transparent 60%
  );
  animation: reflection-sweep 7s ease-in-out infinite;
}
@keyframes reflection-ripple {
  from { transform: translateY(0) translateX(0); }
  to   { transform: translateY(-8px) translateX(-6px); }
}
@keyframes reflection-sweep {
  0%   { transform: translateX(-100%) rotate(0deg); opacity: 0; }
  20%  { opacity: 0.5; }
  50%  { transform: translateX(30%) rotate(0deg); opacity: 0.5; }
  80%  { opacity: 0; }
  100% { transform: translateX(100%) rotate(0deg); opacity: 0; }
}
@keyframes reflection-sway {
  0%,100% { transform: translateY(0) rotate(-0.5deg); }
  50%     { transform: translateY(-0.5px) rotate(0.5deg); }
}
@media (prefers-reduced-motion: reduce) {
  .reflection-fish,
  .reflection-shimmer::before,
  .reflection-shimmer::after { animation: none; }
}
.grid-btn:hover .reflection-shimmer::after { animation-duration: 3.5s; }
.grid-btn:hover .reflection-fish { animation-duration: 3s; }

</style>
