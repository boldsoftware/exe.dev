<!--
  Diagnostics pane for the web terminal. Shows a live, color-coded log of
  keydown / composition / onData / PTY-output events (with round-trip latency)
  so the keyboard+composition+latency interaction can be inspected in the
  field without devtools. Toggled from the terminal SettingsMenu; the backing
  store lives in the parent view (createTerminalDiagnostics()).

  It's a fixed, dismissable side panel that overlays the terminal rather than
  reflowing it, so turning it on/off doesn't resize the PTY.
-->
<template>
  <div v-if="diag.enabled.value" class="term-diag" role="region" aria-label="Terminal diagnostics">
    <div class="term-diag-head">
      <span class="term-diag-title"
        ><i class="pi pi-search" aria-hidden="true"></i> Diagnostics</span
      >
      <span class="term-diag-count">{{ diag.entries.value.length }}</span>
      <span class="term-diag-spacer"></span>
      <button
        type="button"
        class="term-diag-btn"
        title="Clear"
        aria-label="Clear diagnostics"
        @click="diag.clear()"
      >
        <i class="pi pi-trash" aria-hidden="true"></i>
      </button>
      <button
        type="button"
        class="term-diag-btn"
        title="Close"
        aria-label="Close diagnostics"
        @click="diag.enabled.value = false"
      >
        <i class="pi pi-times" aria-hidden="true"></i>
      </button>
    </div>
    <div ref="scrollEl" class="term-diag-body">
      <div v-if="!diag.entries.value.length" class="term-diag-empty">
        Type in the terminal to capture keydown, composition, and PTY events.
      </div>
      <div v-for="e in diag.entries.value" :key="e.id" class="term-diag-row" :class="e.kind">
        <span class="term-diag-t">{{ fmtTime(e.t) }}</span>
        <span class="term-diag-kind">{{ e.kind }}</span>
        <span class="term-diag-msg">{{ e.msg }}</span>
        <span v-if="e.detail" class="term-diag-detail">{{ e.detail }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, nextTick } from "vue";
import type { TerminalDiagnostics } from "../lib/terminalDiagnostics";

const props = defineProps<{ diag: TerminalDiagnostics }>();

const scrollEl = ref<HTMLElement | null>(null);

function fmtTime(ms: number): string {
  return (ms / 1000).toFixed(3);
}

// Keep the newest entry in view as the log grows (only when already near the
// bottom, so a user scrolled up to inspect isn't yanked back down).
watch(
  () => props.diag.entries.value.length,
  async () => {
    const el = scrollEl.value;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    if (!nearBottom) return;
    await nextTick();
    el.scrollTop = el.scrollHeight;
  },
);
</script>

<style scoped>
/* stylelint-disable color-no-hex, declaration-property-value-disallowed-list -- legacy hardcoded colors; replace them with tokens.css variables, then delete this header */
.term-diag {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: min(420px, 92vw);
  z-index: 50;
  display: flex;
  flex-direction: column;
  background: var(--p-content-background, #141414);
  border-left: 1px solid var(--p-content-border-color, #000);
  box-shadow: -4px 0 16px rgba(0, 0, 0, 0.25);
  font-family: "JetBrains Mono", ui-monospace, Menlo, Consolas, monospace;
  font-size: 11px;
}
.term-diag-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--p-content-border-color, #000);
  background: var(--p-content-hover-background, #1d1d1d);
}
.term-diag-title {
  font-weight: 600;
  font-size: 12px;
  color: var(--p-text-color, #ddd);
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.term-diag-count {
  font-size: 10px;
  color: var(--p-text-muted-color, #888);
  background: var(--p-content-background, #000);
  border-radius: 8px;
  padding: 1px 7px;
}
.term-diag-spacer {
  flex: 1;
}
.term-diag-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  border-radius: 5px;
  background: transparent;
  color: var(--p-text-muted-color, #aaa);
  cursor: pointer;
}
.term-diag-btn:hover {
  background: var(--p-content-background, #2a2a2a);
  color: var(--p-text-color, #fff);
}
.term-diag-body {
  flex: 1;
  overflow-y: auto;
  padding: 4px 6px;
  color: var(--p-text-color, #cfcfcf);
}
.term-diag-empty {
  color: var(--p-text-muted-color, #888);
  padding: 10px 4px;
  line-height: 1.5;
}
.term-diag-row {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: baseline;
  padding: 1px 0;
  white-space: pre-wrap;
  word-break: break-all;
  line-height: 1.45;
}
.term-diag-t {
  color: var(--p-text-muted-color, #666);
  flex: 0 0 auto;
}
.term-diag-kind {
  flex: 0 0 76px;
  font-weight: 600;
}
.term-diag-msg {
  flex: 0 1 auto;
}
.term-diag-detail {
  color: var(--p-text-muted-color, #888);
  flex: 1 1 auto;
}

/* Per-kind accent colors (work on both light/dark via fixed hues). */
.term-diag-row.keydown .term-diag-kind {
  color: #3b9eff;
}
.term-diag-row.composition .term-diag-kind {
  color: #d99a00;
}
.term-diag-row.ondata .term-diag-kind {
  color: #2faa55;
}
.term-diag-row.input .term-diag-kind {
  color: #9b6dff;
}
.term-diag-row.output .term-diag-kind {
  color: #888;
}
.term-diag-row.info .term-diag-kind {
  color: #c58aff;
}
.term-diag-row.composition .term-diag-msg {
  color: #d99a00;
}
</style>
