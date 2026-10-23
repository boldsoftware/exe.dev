<!--
  Settings menu (hamburger) for Terminal.vue and any other page that embeds a
  terminal. It is the single home for the terminal's display settings at every
  viewport width:

    - Optional session actions (e.g. Refresh sessions, Upload files), supplied
      by the parent via the `actions` prop.
    - Theme (Auto / Light / Dark)
    - Terminal renderer (ghostty-web / xterm.js)
    - Diagnostics pane on/off

  The active option in each group shows a leading checkmark (and a highlighted
  row) so the live theme/renderer are obvious at a glance.

  State is owned by the parent and flows through v-model bindings (themeMode,
  rendererKind, diagnostics) so the menu stays in sync with whatever the parent
  persists / rebuilds.

  Implementation notes:
  - The panel is TELEPORTED to <body> and positioned `fixed` from the trigger's
    bounding rect. Both host placements live inside overflow:auto scrollers, so
    an absolutely-positioned child would be clipped (z-index can't escape
    clipping). Teleport sidesteps that.
  - DISCLOSURE pattern (aria-haspopup + aria-expanded on the trigger, plain
    buttons with aria-pressed for options), not role="menu" -- we don't
    implement roving focus, so menu semantics would over-promise to AT.
  - Closes on outside pointerdown, Escape (focus returns to trigger),
    selecting a theme/renderer, or scroll/resize (the fixed panel would float
    away from the trigger).
-->
<template>
  <div ref="rootEl" class="settings-menu">
    <button
      ref="triggerEl"
      type="button"
      class="settings-trigger"
      :aria-expanded="open"
      aria-haspopup="true"
      aria-label="Settings"
      title="Settings"
      @click="toggle"
    >
      <i class="pi pi-bars" aria-hidden="true"></i>
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        ref="panelEl"
        class="settings-panel"
        :style="panelStyle"
        aria-label="Settings"
      >
        <div
          v-if="actions && actions.length"
          class="settings-group"
          role="group"
          aria-label="Session"
        >
          <div class="settings-label">Session</div>
          <div class="settings-options">
            <button
              v-for="a in actions"
              :key="a.id"
              type="button"
              class="settings-option"
              :disabled="a.disabled"
              @click="runAction(a)"
            >
              <span class="settings-check" aria-hidden="true"></span>
              <i
                class="pi settings-leadicon"
                :class="a.busy ? 'pi-spin pi-spinner' : a.icon"
                aria-hidden="true"
              ></i>
              <span class="settings-optlabel">{{
                a.busy && a.busyLabel ? a.busyLabel : a.label
              }}</span>
            </button>
          </div>
        </div>

        <div class="settings-group" role="group" aria-label="Theme">
          <div class="settings-label">Theme</div>
          <div class="settings-options">
            <button
              v-for="opt in themeOptions"
              :key="opt.value"
              type="button"
              class="settings-option"
              :class="{ active: themeMode === opt.value }"
              :aria-pressed="themeMode === opt.value"
              @click="selectTheme(opt.value)"
            >
              <span class="settings-check" aria-hidden="true">
                <i v-if="themeMode === opt.value" class="pi pi-check"></i>
              </span>
              <i class="pi settings-leadicon" :class="opt.icon" aria-hidden="true"></i>
              <span class="settings-optlabel">{{ opt.title }}</span>
            </button>
          </div>
        </div>

        <div class="settings-group" role="group" aria-label="Terminal renderer">
          <div class="settings-label">Renderer</div>
          <div class="settings-options">
            <button
              v-for="opt in rendererOptions"
              :key="opt.value"
              type="button"
              class="settings-option"
              :class="{ active: rendererKind === opt.value }"
              :aria-pressed="rendererKind === opt.value"
              :disabled="rendererBusy && rendererKind !== opt.value"
              @click="selectRenderer(opt.value)"
            >
              <span class="settings-check" aria-hidden="true">
                <i v-if="rendererKind === opt.value" class="pi pi-check"></i>
              </span>
              <span class="settings-glyph" aria-hidden="true">{{ opt.glyph }}</span>
              <span class="settings-optlabel">{{ opt.title }}</span>
              <span
                v-if="rendererBusy && rendererKind === opt.value"
                class="settings-busy"
                aria-hidden="true"
              >
                <i class="pi pi-spin pi-spinner"></i>
              </span>
            </button>
          </div>
        </div>

        <div class="settings-group" role="group" aria-label="Diagnostics">
          <div class="settings-label">Debug</div>
          <div class="settings-options">
            <button
              type="button"
              class="settings-option"
              :class="{ active: diagnostics }"
              :aria-pressed="diagnostics"
              @click="toggleDiagnostics"
            >
              <span class="settings-check" aria-hidden="true">
                <i v-if="diagnostics" class="pi pi-check"></i>
              </span>
              <i class="pi pi-search settings-leadicon" aria-hidden="true"></i>
              <span class="settings-optlabel">Diagnostics pane</span>
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, nextTick, onBeforeUnmount, type CSSProperties } from "vue";
import { themeOptions, type ThemeMode } from "../lib/themeMode";
import type { RendererKind } from "../lib/terminalRenderer";

// An imperative action surfaced in the menu's "Session" group (e.g. refresh the
// session list, open the upload dialog). The parent owns the behaviour and
// reacts to the `action` emit; the menu just renders the row.
export interface SettingsAction {
  id: string;
  label: string;
  icon: string; // primeicons class, e.g. 'pi-refresh'
  disabled?: boolean;
  busy?: boolean; // swaps the icon for a spinner and uses busyLabel if set
  busyLabel?: string;
  keepOpen?: boolean; // don't close the menu after running (default closes)
}

const props = defineProps<{
  themeMode: ThemeMode;
  rendererKind: RendererKind;
  diagnostics: boolean;
  rendererBusy?: boolean;
  actions?: SettingsAction[];
}>();
const emit = defineEmits<{
  (e: "update:themeMode", v: ThemeMode): void;
  (e: "update:rendererKind", v: RendererKind): void;
  (e: "update:diagnostics", v: boolean): void;
  (e: "action", id: string): void;
}>();

const rendererOptions: { value: RendererKind; glyph: string; title: string }[] = [
  { value: "ghostty", glyph: "\u{1F47B}", title: "ghostty-web" },
  { value: "xterm", glyph: "\u2715", title: "xterm.js" },
];

const open = ref(false);
const rootEl = ref<HTMLElement | null>(null);
const triggerEl = ref<HTMLButtonElement | null>(null);
const panelEl = ref<HTMLElement | null>(null);

// The teleported panel is position:fixed; anchor its top-right under the
// trigger's bottom-right so it lines up like a normal dropdown.
const panelStyle = ref<CSSProperties>({});
function positionPanel() {
  const t = triggerEl.value;
  if (!t) return;
  const r = t.getBoundingClientRect();
  panelStyle.value = {
    position: "fixed",
    top: `${Math.round(r.bottom + 6)}px`,
    right: `${Math.round(window.innerWidth - r.right)}px`,
  };
}

function toggle() {
  open.value ? close() : openMenu();
}
function openMenu() {
  positionPanel();
  open.value = true;
}
function close() {
  open.value = false;
}

function selectTheme(v: ThemeMode) {
  if (v !== props.themeMode) emit("update:themeMode", v);
  close();
}
function selectRenderer(v: RendererKind) {
  if (v !== props.rendererKind) emit("update:rendererKind", v);
  close();
}
// The diagnostics toggle deliberately does NOT close the menu, so it's easy
// to flip on and keep poking at other settings.
function toggleDiagnostics() {
  emit("update:diagnostics", !props.diagnostics);
}
function runAction(a: SettingsAction) {
  if (a.disabled) return;
  emit("action", a.id);
  if (!a.keepOpen) close();
}

function onDocPointerDown(e: PointerEvent) {
  const target = e.target;
  if (!(target instanceof Node)) return;
  if (rootEl.value?.contains(target)) return;
  if (panelEl.value?.contains(target)) return;
  close();
}
function onKeyDown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    close();
    triggerEl.value?.focus();
  }
}
function onReflow() {
  close();
}

// Only keep global listeners alive while the panel is open.
watch(open, async (isOpen) => {
  if (isOpen) {
    document.addEventListener("pointerdown", onDocPointerDown, true);
    document.addEventListener("keydown", onKeyDown);
    window.addEventListener("resize", onReflow);
    window.addEventListener("scroll", onReflow, true);
    await nextTick();
    positionPanel();
    // The panel is teleported to the end of <body>; move focus into it so a
    // keyboard user doesn't have to tab through the whole page to reach the
    // options (and so Escape-to-restore has somewhere to return from).
    const first = panelEl.value?.querySelector<HTMLButtonElement>(".settings-option");
    first?.focus();
  } else {
    document.removeEventListener("pointerdown", onDocPointerDown, true);
    document.removeEventListener("keydown", onKeyDown);
    window.removeEventListener("resize", onReflow);
    window.removeEventListener("scroll", onReflow, true);
  }
});

onBeforeUnmount(() => {
  document.removeEventListener("pointerdown", onDocPointerDown, true);
  document.removeEventListener("keydown", onKeyDown);
  window.removeEventListener("resize", onReflow);
  window.removeEventListener("scroll", onReflow, true);
});
</script>

<style scoped>
.settings-menu {
  position: relative;
  display: inline-flex;
  flex: 0 0 auto;
}

.settings-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  border: 1px solid var(--p-content-border-color, var(--surface-border));
  border-radius: 6px;
  background: var(--p-content-background, var(--surface-card));
  color: var(--p-text-color, var(--text-color));
  cursor: pointer;
  font-family: inherit;
  line-height: 1;
}
.settings-trigger:hover {
  background: var(--p-content-hover-background, var(--surface-hover));
}
.settings-trigger:focus-visible {
  outline: 2px solid var(--p-primary-color, var(--text-color));
  outline-offset: 1px;
}
.settings-trigger .pi {
  font-size: 14px;
}
</style>

<!-- The panel is teleported to <body>, so its styles must NOT be scoped to the
     component subtree. Keep them global but namespaced under .settings-panel. -->
<style>
/* stylelint-disable declaration-property-value-disallowed-list -- legacy hardcoded colors; replace them with tokens.css variables, then delete this header */
.settings-panel {
  z-index: 1000;
  min-width: 200px;
  padding: 8px;
  border: 1px solid var(--p-content-border-color, var(--surface-border));
  border-radius: 8px;
  background: var(--p-content-background, var(--surface-card));
  box-shadow: 0 6px 24px rgba(0, 0, 0, 0.18);
  display: flex;
  flex-direction: column;
  gap: 10px;
  font-family: inherit;
}

.settings-panel .settings-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.settings-panel .settings-group + .settings-group {
  border-top: 1px solid var(--p-content-border-color, var(--surface-border));
  padding-top: 10px;
}

.settings-panel .settings-label {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--p-text-muted-color, var(--text-color-muted));
  padding: 0 4px;
}

.settings-panel .settings-options {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.settings-panel .settings-option {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--p-text-color, var(--text-color));
  font-family: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
  line-height: 1.2;
}
.settings-panel .settings-option:hover {
  background: var(--p-content-hover-background, var(--surface-hover));
}
.settings-panel .settings-option:focus-visible {
  outline: 2px solid var(--p-primary-color, var(--text-color));
  outline-offset: -2px;
}
.settings-panel .settings-option.active {
  background: var(--p-highlight-background, var(--surface-hover));
  color: var(--p-highlight-color, var(--text-color));
  font-weight: 600;
}
.settings-panel .settings-option:disabled {
  opacity: 0.5;
  cursor: default;
}
.settings-panel .settings-optlabel {
  flex: 1 1 auto;
}
.settings-panel .settings-leadicon {
  font-size: 14px;
  width: 16px;
  text-align: center;
}
.settings-panel .settings-glyph {
  font-size: 14px;
  width: 16px;
  text-align: center;
}

/* Fixed-width leading column so option labels line up whether or not the row
   is the active (checked) one. */
.settings-panel .settings-check {
  width: 16px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
}
.settings-panel .settings-check .pi {
  font-size: 13px;
  color: var(--p-primary-color, var(--text-color));
}
.settings-panel .settings-option.active .settings-check .pi {
  color: inherit;
}

.settings-panel .settings-busy {
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
}
.settings-panel .settings-busy .pi {
  font-size: 13px;
}
</style>
