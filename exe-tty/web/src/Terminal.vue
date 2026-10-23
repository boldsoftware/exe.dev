<template>
  <div class="term-app" :class="{ 'kb-mode': keyboardMode }">
    <!-- Title + session tabs share the top row on desktop. The title is
         hidden on mobile (precious vertical space); there the tabs scroll
         horizontally on their own.
         On mobile we default to a collapsed view that only shows the
         current tab plus a tab counter to expand; the full list eats a lot
         of space on phones. Tabs can be drag-reordered via pointer events.
         Drag-and-drop is wired up via window-level listeners (see onMounted)
         rather than on an element, so drops over teleported PrimeVue
         dialogs/toasts are also caught and can't navigate the page away. -->
    <div
      class="tab-bar"
      :class="{ collapsed: tabsCollapsed }"
      role="tablist"
      aria-label="Terminal sessions"
    >
      <div class="term-title">{{ pageTitle }}</div>
      <button
        class="tab-toggle"
        type="button"
        :title="tabToggleTitle"
        :aria-label="`Sessions (${sessionCount})`"
        :aria-expanded="!tabsCollapsed"
        @click="tabsCollapsed = !tabsCollapsed"
      >
        <span class="tab-counter" aria-hidden="true">{{ sessionCountLabel }}</span>
      </button>
      <span
        v-if="tabsCollapsed || keyboardMode"
        class="tab-toggle-name"
        dir="auto"
        :title="tabLabel(currentName).tooltip"
        ><span class="title-head">{{ tabLabel(currentName).head }}</span
        ><span class="title-tail">{{ tabLabel(currentName).tail }}</span></span
      >
      <!-- Announce session switches, not every title update (Claude Code
           rewrites its title about once a second). -->
      <span v-if="tabsCollapsed || keyboardMode" class="sr-only" aria-live="polite">{{
        currentName
      }}</span>
      <template v-if="!tabsCollapsed">
        <button
          v-for="name in sessions"
          :key="name"
          class="tab"
          :class="{
            active: name === currentName,
            dragging: name === draggingName,
            'drop-target': name === dropTargetName,
          }"
          role="tab"
          :aria-selected="name === currentName ? 'true' : 'false'"
          :data-session="name"
          :title="tabLabel(name).tooltip"
          draggable="true"
          @click="switchTo(name)"
          @pointerdown="onTabPointerDown($event, name)"
          @dragstart="onTabDragStart($event, name)"
          @dragover.prevent="onTabDragOver($event, name)"
          @drop.prevent="onTabDrop($event, name)"
          @dragend="onTabDragEnd"
        >
          <span class="tab-name" dir="auto"
            ><span class="title-head">{{ tabLabel(name).head }}</span
            ><span class="title-tail">{{ tabLabel(name).tail }}</span></span
          >
          <span
            class="tab-close"
            :class="{ closing: closing.has(name) }"
            role="button"
            :aria-label="`Close session ${name}`"
            :aria-busy="closing.has(name) ? 'true' : 'false'"
            :title="`Close session &quot;${name}&quot;`"
            @click.stop="closeSession(name)"
            ><i
              class="pi"
              :class="closing.has(name) ? 'pi-spinner pi-spin' : 'pi-times'"
              aria-hidden="true"
            ></i
          ></span>
        </button>
      </template>
      <button
        class="tab tab-icon tab-new"
        type="button"
        title="Open a new terminal session"
        aria-label="Open a new terminal session"
        @click="openNew"
      >
        <i class="pi pi-plus" aria-hidden="true"></i>
      </button>
      <SettingsMenu
        v-model:theme-mode="themeMode"
        v-model:renderer-kind="rendererKind"
        v-model:diagnostics="diag.enabled.value"
        :renderer-busy="rendererSwitching"
        :actions="settingsActions"
        @action="onSettingsAction"
      />
    </div>

    <!-- Control bar (mobile-friendly, always rendered).
         One row by default: these three stacked bars used to eat 105px (16%)
         of a 390x844 iPhone, and 32% of what was left with the keyboard up.
         The keys people reach for constantly (modifiers, Esc, Tab, ^C, the
         arrows, focus) stay on that row; the rest move behind a disclosure
         so they cost nothing until asked for. -->
    <div class="control-bar" role="toolbar" aria-label="Terminal controls">
      <div class="ctl-row">
        <button
          class="ctl mod"
          :class="{ active: ctrlActive }"
          :aria-pressed="ctrlActive"
          title="Ctrl modifier"
          aria-label="Ctrl modifier"
          @click.prevent="toggleCtrl"
        >
          ^
        </button>
        <button
          class="ctl mod"
          :class="{ active: altActive }"
          :aria-pressed="altActive"
          title="Alt modifier"
          aria-label="Alt modifier"
          @click.prevent="toggleAlt"
        >
          ⌥
        </button>
        <button class="ctl" title="Escape" aria-label="Escape" @click.prevent="sendKey('Escape')">
          Esc
        </button>
        <button class="ctl" title="Tab" aria-label="Tab" @click.prevent="sendKey('Tab')">⇥</button>
        <button
          class="ctl"
          title="Ctrl-C (interrupt)"
          aria-label="Control C"
          @click.prevent="sendCtrl('c')"
        >
          ^C
        </button>
        <button class="ctl sm" title="Up" aria-label="Arrow up" @click.prevent="sendKey('ArrowUp')">
          <i class="pi pi-arrow-up" aria-hidden="true"></i>
        </button>
        <button
          class="ctl sm"
          title="Down"
          aria-label="Arrow down"
          @click.prevent="sendKey('ArrowDown')"
        >
          <i class="pi pi-arrow-down" aria-hidden="true"></i>
        </button>
        <button
          class="ctl sm"
          title="Left"
          aria-label="Arrow left"
          @click.prevent="sendKey('ArrowLeft')"
        >
          <i class="pi pi-arrow-left" aria-hidden="true"></i>
        </button>
        <button
          class="ctl sm"
          title="Right"
          aria-label="Arrow right"
          @click.prevent="sendKey('ArrowRight')"
        >
          <i class="pi pi-arrow-right" aria-hidden="true"></i>
        </button>
        <button
          class="ctl"
          :class="{ active: keyboardMode }"
          :aria-pressed="keyboardMode"
          title="Focus terminal / show keyboard"
          aria-label="Focus terminal"
          @click.prevent="toggleKb"
        >
          ⌨
        </button>
        <button
          class="ctl ctl-more"
          :class="{ active: ctlExpanded }"
          type="button"
          :aria-expanded="ctlExpanded"
          aria-controls="term-ctl-more"
          :title="ctlExpanded ? 'Fewer keys' : 'More keys'"
          :aria-label="ctlExpanded ? 'Fewer keys' : 'More keys'"
          @click.prevent="ctlExpanded = !ctlExpanded"
        >
          <i
            class="pi"
            :class="ctlExpanded ? 'pi-chevron-up' : 'pi-ellipsis-h'"
            aria-hidden="true"
          ></i>
        </button>
      </div>
      <div v-show="ctlExpanded" id="term-ctl-more" class="ctl-row ctl-row-more">
        <button
          class="ctl"
          title="Ctrl-D (EOF)"
          aria-label="Control D"
          @click.prevent="sendCtrl('d')"
        >
          ^D
        </button>
        <button
          class="ctl"
          title="Ctrl-Z (suspend)"
          aria-label="Control Z"
          @click.prevent="sendCtrl('z')"
        >
          ^Z
        </button>
        <button
          class="ctl"
          title="Ctrl-R (reverse search)"
          aria-label="Control R"
          @click.prevent="sendCtrl('r')"
        >
          ^R
        </button>
        <button
          class="ctl"
          title="Ctrl-L (clear)"
          aria-label="Control L"
          @click.prevent="sendCtrl('l')"
        >
          ^L
        </button>
        <button class="ctl" title="Home" aria-label="Home" @click.prevent="sendKey('Home')">
          Home
        </button>
        <button class="ctl" title="End" aria-label="End" @click.prevent="sendKey('End')">
          End
        </button>
        <button class="ctl" title="Page up" aria-label="Page up" @click.prevent="sendKey('PageUp')">
          PgUp
        </button>
        <button
          class="ctl"
          title="Page down"
          aria-label="Page down"
          @click.prevent="sendKey('PageDown')"
        >
          PgDn
        </button>
        <button
          class="ctl"
          title="Paste from clipboard"
          aria-label="Paste from clipboard"
          @click.prevent="pasteClip"
        >
          <i class="pi pi-clipboard" aria-hidden="true"></i>
        </button>
      </div>
    </div>

    <!-- Embedder-supplied strip (e.g. an announcement). Fixed-position content
         that should keep the status pill clear of it marks its root with
         .term-bottom-bar. -->
    <slot name="banner" />

    <!-- Status pill -->
    <div
      class="status"
      :class="statusType"
      v-show="statusVisible"
      :role="'status'"
      aria-live="polite"
      @click="statusClickable && reconnect()"
    >
      <span class="dot"></span>
      <span>{{ statusText }}</span>
    </div>

    <!-- Hidden file input backing the upload button and the dialog's
         "Choose files" control. -->
    <input ref="fileInput" type="file" multiple class="upload-input" @change="onFilePicked" />

    <!-- Recently-uploaded files banner: shows where each file landed on the
         machine, with a per-file Copy button. Newest first; dismissable. -->
    <div v-if="uploadedFiles.length" class="upload-result" role="status" aria-live="polite">
      <div class="upload-result-head">
        <span class="upload-result-title">
          {{ uploadedFiles.length }} file{{ uploadedFiles.length === 1 ? "" : "s" }} uploaded
        </span>
        <button
          class="upload-dismiss"
          type="button"
          title="Dismiss"
          aria-label="Dismiss"
          @click="uploadedFiles = []"
        >
          <i class="pi pi-times" aria-hidden="true"></i>
        </button>
      </div>
      <ul class="upload-result-list">
        <li v-for="f in uploadedFiles" :key="f.path" class="upload-result-item">
          <span class="upload-result-text"
            ><i class="pi pi-check" aria-hidden="true"></i> <code>{{ f.path }}</code></span
          >
          <button
            class="upload-copy"
            type="button"
            :title="copiedPath === f.path ? 'Copied' : 'Copy path'"
            :aria-label="copiedPath === f.path ? 'Copied' : 'Copy path'"
            @click="copyUploadedPath(f.path)"
          >
            <i
              class="pi"
              :class="copiedPath === f.path ? 'pi-check' : 'pi-copy'"
              aria-hidden="true"
            ></i>
            {{ copiedPath === f.path ? "Copied" : "Copy" }}
          </button>
        </li>
      </ul>
    </div>

    <!-- The xterm host element. We mount a single Terminal instance and
         tear it down / rebuild it when the user switches sessions, so the
         outer Vue app and PrimeVue runtime stay loaded. -->
    <div class="term-stage">
      <div
        ref="termEl"
        class="term"
        role="region"
        aria-label="Terminal output"
        @contextmenu.capture="onTermContextMenu"
      ></div>
      <TerminalDiagnostics :diag="diag" />
    </div>

    <!-- Upload dialog: choose the destination directory on the server, queue up
         one or more files (via the button or by dropping onto the zone), and
         watch them upload. The directory is remembered for next time. -->
    <Dialog
      v-model:visible="uploadDialog"
      modal
      :header="'Upload to ' + (boxName || 'this machine')"
      :style="{ width: '30rem' }"
      class="upload-dialog"
    >
      <label class="upload-dir-label" for="upload-dir">Destination directory</label>
      <input
        id="upload-dir"
        v-model="uploadDir"
        class="upload-dir-input"
        type="text"
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        placeholder="/tmp"
        @keyup.enter="pickUpload"
      />
      <p class="upload-dir-hint">
        Defaults to <code>/tmp</code>. Use <code>~</code> for your home directory, a relative path
        (relative to home), or an absolute path. Created if missing.
      </p>

      <!-- Drop zone / file chooser. -->
      <div
        class="upload-zone"
        :class="{ 'is-over': dialogDragOver }"
        role="button"
        tabindex="0"
        @click="pickUpload"
        @keydown.enter.prevent="pickUpload"
        @keydown.space.prevent="pickUpload"
        @dragenter.prevent.stop="dialogDragOver = true"
        @dragover.prevent.stop="dialogDragOver = true"
        @dragleave.prevent.stop="dialogDragOver = false"
        @drop.prevent.stop="onZoneDrop"
      >
        <span class="upload-zone-icon"><i class="pi pi-upload" aria-hidden="true"></i></span>
        <span class="upload-zone-text"> <strong>Click to choose</strong> or drop files here </span>
      </div>

      <!-- Per-file queue with live status. -->
      <ul v-if="queue.length" class="upload-queue">
        <li v-for="item in queue" :key="item.id" class="upload-queue-item" :class="item.status">
          <span class="uq-icon" aria-hidden="true">
            <i
              class="pi"
              :class="{
                'pi-check': item.status === 'done',
                'pi-exclamation-triangle': item.status === 'error',
                'pi-spin pi-spinner': item.status === 'uploading',
                'pi-clock': item.status === 'queued',
              }"
            ></i>
          </span>
          <span class="uq-body">
            <span class="uq-name" :title="item.file.name">{{ item.file.name }}</span>
            <span class="uq-meta">
              <template v-if="item.status === 'error'">{{ item.error }}</template>
              <template v-else-if="item.status === 'done'"
                >{{ formatBytes(item.file.size) }} · done</template
              >
              <template v-else-if="item.status === 'uploading'"
                >{{ formatBytes(item.file.size) }} · uploading…</template
              >
              <template v-else>{{ formatBytes(item.file.size) }} · queued</template>
            </span>
            <span
              class="uq-bar"
              role="progressbar"
              :aria-valuenow="item.progress"
              aria-valuemin="0"
              aria-valuemax="100"
              :aria-label="`Upload progress for ${item.file.name}`"
              ><span class="uq-bar-fill" :style="{ width: item.progress + '%' }"></span
            ></span>
          </span>
          <button
            v-if="item.status === 'queued'"
            class="uq-remove"
            type="button"
            title="Remove"
            aria-label="Remove from queue"
            @click="removeFromQueue(item.id)"
          >
            <i class="pi pi-times" aria-hidden="true"></i>
          </button>
        </li>
      </ul>

      <template #footer>
        <Button label="Done" severity="secondary" text @click="uploadDialog = false" />
        <Button label="Upload" :disabled="!hasQueuedUploads" @click="startUploads" />
      </template>
    </Dialog>

    <!-- Full-window drag overlay: appears while a file is dragged anywhere
         over the page so the whole window becomes a drop target. -->
    <div v-show="windowDragOver && !uploadDialog" class="upload-overlay" aria-hidden="true">
      <div class="upload-overlay-card">
        <span class="upload-overlay-icon"><i class="pi pi-upload" aria-hidden="true"></i></span>
        <span class="upload-overlay-title">Drop to upload</span>
      </div>
    </div>

    <ContextMenu ref="terminalContextMenu" :model="terminalContextItems" />
    <Toast position="bottom-center" />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick } from "vue";
import {
  loadTerminalBackend,
  termNameFor,
  getRendererKind,
  setRendererKind,
  isMacPlatform,
  linkHandler,
  terminalKeepsGestureLocal,
  terminalReportsMouse,
  type TerminalLike,
  type FitAddonLike,
  type RendererKind,
} from "./lib/terminalRenderer";
import { createTerminalDiagnostics } from "./lib/terminalDiagnostics";
import { createTerminalFlightRecorder } from "./lib/terminalFlightRecorder";
import { splitInput } from "./lib/terminalInput";
import { attachTerminalTouch } from "./lib/terminalTouch";
import { computeViewportClamp, KEYBOARD_SETTLE_MS } from "./lib/terminalViewport";
import Dialog from "primevue/dialog";
import Button from "primevue/button";
import ContextMenu from "primevue/contextmenu";
import Toast from "primevue/toast";
import type { MenuItem } from "primevue/menuitem";
import { useToast } from "primevue/usetoast";
import SettingsMenu, { type SettingsAction } from "./components/SettingsMenu.vue";
import TerminalDiagnostics from "./components/TerminalDiagnostics.vue";
import { uploadFile } from "./lib/terminalUpload";
import { formatBytes } from "./lib/formatBytes";
import {
  applyDocumentTheme,
  effectiveTheme,
  loadThemeMode,
  saveThemeMode,
  watchSystemTheme,
  type ThemeMode,
  type ThemeStore,
} from "./lib/themeMode";
import {
  beginDeferredClipboardWrite,
  isPlainOsc52CopyKey,
  isTerminalCopyShortcut,
  Osc52ClipboardParser,
  TerminalCopyHintDetector,
  type DeferredClipboardWrite,
} from "./lib/terminalClipboard";
import {
  cleanTerminalTitle,
  SessionTitleStore,
  splitTitleTail,
  TerminalTitleParser,
} from "./lib/terminalTitle";

const props = defineProps<{
  // Where the light/dark/auto choice lives; defaults to localStorage.
  themeStore?: ThemeStore;
}>();

const toast = useToast();
type ContextMenuRef = { show(event: Event): void };
const terminalContextMenu = ref<ContextMenuRef | null>(null);
const contextCopyText = ref("");
const terminalContextItems = computed<MenuItem[]>(() => [
  {
    label: "Copy",
    icon: "pi pi-copy",
    disabled: !contextCopyText.value,
    command: () => void copyContextSelection(),
  },
  {
    label: "Paste",
    icon: "pi pi-clipboard",
    command: () => void pasteClip(),
  },
]);

// 100 color names for new sessions. Keep the list stable: session names
// outlive page loads, and a renamed color would orphan nothing but would
// read as a different session.
const colorNames = [
  "amber",
  "aqua",
  "azure",
  "beige",
  "bisque",
  "black",
  "blue",
  "bronze",
  "brown",
  "coral",
  "cream",
  "crimson",
  "cyan",
  "denim",
  "ebony",
  "emerald",
  "fuchsia",
  "gold",
  "gray",
  "green",
  "indigo",
  "ivory",
  "jade",
  "khaki",
  "lavender",
  "lemon",
  "lilac",
  "lime",
  "linen",
  "magenta",
  "maroon",
  "mauve",
  "mint",
  "navy",
  "ochre",
  "olive",
  "orange",
  "orchid",
  "peach",
  "pearl",
  "periwinkle",
  "pink",
  "plum",
  "purple",
  "red",
  "rose",
  "ruby",
  "rust",
  "saffron",
  "sage",
  "salmon",
  "sand",
  "sapphire",
  "scarlet",
  "sepia",
  "silver",
  "slate",
  "snow",
  "tan",
  "teal",
  "thistle",
  "tomato",
  "turquoise",
  "umber",
  "vanilla",
  "violet",
  "wheat",
  "white",
  "wine",
  "yellow",
  "amethyst",
  "apricot",
  "ash",
  "auburn",
  "buff",
  "cardinal",
  "carmine",
  "cerise",
  "charcoal",
  "cherry",
  "chestnut",
  "chocolate",
  "cinnamon",
  "claret",
  "cobalt",
  "copper",
  "cornflower",
  "eggplant",
  "flax",
  "garnet",
  "ginger",
  "goldenrod",
  "gunmetal",
  "hazel",
  "honeydew",
  "ice",
  "iron",
  "jasmine",
  "jet",
  "mango",
];
function randomColorName(): string {
  return colorNames[Math.floor(Math.random() * colorNames.length)];
}

const themes = {
  light: {
    background: "#ffffff",
    foreground: "#24292f",
    cursor: "#0969da",
    selectionBackground: "#0969da40",
    selectionForeground: "#24292f",
    black: "#24292f",
    red: "#cf222e",
    green: "#116329",
    yellow: "#4d2d00",
    blue: "#0969da",
    magenta: "#8250df",
    cyan: "#1b7c83",
    white: "#6e7681",
    brightBlack: "#656d76",
    brightRed: "#a40e26",
    brightGreen: "#1a7f37",
    brightYellow: "#633c01",
    brightBlue: "#218bff",
    brightMagenta: "#a475f9",
    brightCyan: "#3192aa",
    brightWhite: "#8c959f",
  },
  dark: {
    background: "#0d1117",
    foreground: "#e6edf3",
    cursor: "#58a6ff",
    selectionBackground: "#58a6ff4d",
    black: "#484f58",
    red: "#ff7b72",
    green: "#3fb950",
    yellow: "#d29922",
    blue: "#58a6ff",
    magenta: "#bc8cff",
    cyan: "#39c5cf",
    white: "#b1bac4",
    brightBlack: "#6e7681",
    brightRed: "#ffa198",
    brightGreen: "#56d364",
    brightYellow: "#e3b341",
    brightBlue: "#79c0ff",
    brightMagenta: "#d2a8ff",
    brightCyan: "#56d4dd",
    brightWhite: "#f0f6fc",
  },
} as const;

function effectiveThemeMode(): "light" | "dark" {
  return effectiveTheme(themeMode.value);
}

// The first DNS label names the machine when the host has at least three,
// e.g. "machine.term.example.com" -> "machine"; localhost and IPs get none.
function boxNameFromHost(): string | null {
  const host = location.hostname;
  if (/^[\d.]+$/.test(host) || host.includes(":")) return null;
  const parts = host.split(".");
  return parts.length >= 3 ? parts[0] : null;
}

// Session name comes from the URL ?name=... query param. When it's absent we
// prefer adopting an existing session over minting a new one (see
// adoptOrMintSession); this returns "" in that case so the caller can wait for
// the session list before committing to a name.
function sessionFromURL(): string {
  return new URL(location.href).searchParams.get("name") ?? "";
}

// Point the URL at `name` without reloading, so a refresh lands on the same
// session.
function setSessionInURL(name: string) {
  const u = new URL(location.href);
  u.searchParams.set("name", name);
  history.replaceState({}, "", u.pathname + "?" + u.searchParams.toString());
}

// Mint a session name not already taken by `existing`.
function mintSessionName(existing: Set<string>): string {
  let name = randomColorName();
  for (let i = 0; i < 20 && existing.has(name); i++) name = randomColorName();
  return name;
}

function terminalIdFromURL(): string {
  return new URL(location.href).searchParams.get("t") || "1";
}

// Optional ?d=<dir>: start shells in <dir> instead of $HOME. The server only
// applies it when a socket creates a session (reattaching keeps the shell
// where it is), so it is forwarded on every socket: the first attach, + tabs,
// and restarts after the shell exits. Tab switches keep it in the URL.
const workingDir = new URL(location.href).searchParams.get("d") ?? "";

// ---- Reactive state ----
const termEl = ref<HTMLElement | null>(null);
const boxName = boxNameFromHost();
const currentName = ref<string>(sessionFromURL());
const sessions = ref<string[]>([]);
// The session-list toggle is a tab counter. The list is empty until the first
// /terminal/sessions poll, but there is always the session on screen, so it
// never reads 0. The box caps at 99+; the title and aria-label carry the exact
// number.
const sessionCount = computed(() => Math.max(sessions.value.length, 1));
const sessionCountLabel = computed(() =>
  sessionCount.value > 99 ? "99+" : String(sessionCount.value),
);
const tabToggleTitle = computed(() =>
  tabsCollapsed.value
    ? `Show all sessions (${sessionCount.value})`
    : `Hide session list (${sessionCount.value})`,
);
const hasSeenCurrent = ref(false);

const pageTitle = computed(() => boxName || "Terminal");

const ctrlActive = ref(false);
const altActive = ref(false);
const keyboardMode = ref(false);

// Namespace storage keys by hostname so prefs for one machine don't bleed
// into another's terminal.
const storageScope = `xterm:${location.hostname}`;
const tabsCollapsedKey = `${storageScope}:tabsCollapsed`;
const ctlExpandedKey = `${storageScope}:ctlExpanded`;
const tabOrderKey = `${storageScope}:tabOrder`;
const lastActiveKey = `${storageScope}:lastActive`;
const themeModeKey = `${storageScope}:themeMode`;

// Window titles programs set via OSC 0/2 (the shell prompt's user@host: dir,
// Claude Code's current task, ...), by session. A tab shows its session's
// title when there is one, else the session name, like the iOS app. Only the
// attached session's output reaches this page, so other tabs show the last
// title seen for them, remembered across reloads and shared with other
// browser tabs on this host.
const titleStore = new SessionTitleStore(`${storageScope}:title:`);
const sessionTitles = ref(titleStore.load());
// Long titles lose their middle: the label's head gets a CSS ellipsis and
// this many trailing cells always show. The tooltip has the whole thing.
const TAB_TITLE_TAIL = 8;
type TabLabel = { head: string; tail: string; tooltip: string };
const tabLabels = computed(() => {
  const labels = new Map<string, TabLabel>();
  for (const name of new Set([...sessions.value, currentName.value])) {
    const title = sessionTitles.value.get(name);
    labels.set(
      name,
      title
        ? { ...splitTitleTail(title, TAB_TITLE_TAIL), tooltip: `${title}\nSession: ${name}` }
        : { head: name, tail: "", tooltip: name },
    );
  }
  return labels;
});
function tabLabel(name: string): TabLabel {
  return tabLabels.value.get(name) ?? { head: name, tail: "", tooltip: name };
}
function setSessionTitle(name: string, raw: string) {
  const title = cleanTerminalTitle(raw);
  if ((sessionTitles.value.get(name) ?? null) === title) return;
  if (title === null) sessionTitles.value.delete(name);
  else sessionTitles.value.set(name, title);
  titleStore.set(name, title);
}
function forgetSessionTitle(name: string) {
  setSessionTitle(name, "");
}
function onTitleStorage(ev: StorageEvent) {
  if (ev.storageArea !== localStorage) return;
  const change = titleStore.changeFrom(ev);
  if (!change) return;
  if ("cleared" in change) sessionTitles.value = titleStore.load();
  else if (change.title === null) sessionTitles.value.delete(change.name);
  else sessionTitles.value.set(change.name, change.title);
}
// The browser tab follows the attached session: "<title> · <box>".
watch(
  () => [sessionTitles.value.get(currentName.value), pageTitle.value] as const,
  ([title, page]) => {
    document.title = title ? `${title} · ${page}` : page;
  },
  { immediate: true },
);

// Explicit theme preference. PrimeVue is configured with
// `darkModeSelector: '.app-dark'` for this entrypoint; we drive that
// class (and a matching `.app-light` for the light-mode token overrides)
// via the shared themeMode helpers. Persisted per origin by default; an
// embedder can supply its own store (props.themeStore).
const themeStore: ThemeStore = props.themeStore ?? {
  load: () => loadThemeMode(themeModeKey),
  save: (m) => saveThemeMode(themeModeKey, m),
};
const themeMode = ref<ThemeMode>(themeStore.load());
applyDocumentTheme(effectiveThemeMode());
watch(themeMode, (m) => {
  themeStore.save(m);
  applyDocumentTheme(effectiveThemeMode());
  applyTermTheme();
});

// Terminal renderer (ghostty-web vs xterm.js). Owned here so the SettingsMenu
// toggle and the terminalRenderer persistence stay in sync. Switching tears
// the canvas down and remounts with the other backend (and reconnects so the
// session server resends a screen snapshot).
const rendererKind = ref<RendererKind>(getRendererKind());
const rendererSwitching = ref(false);
watch(rendererKind, async (kind) => {
  if (term && kind === termKind) return;
  setRendererKind(kind);
  rendererSwitching.value = true;
  try {
    await rebuildTerminal();
  } finally {
    rendererSwitching.value = false;
  }
});

// Diagnostics store (keydown / composition / onData / PTY-latency log). Off by
// default; toggled from the SettingsMenu. Lives here so it survives terminal
// rebuilds (renderer/theme switches) within the same session.
const diag = createTerminalDiagnostics();

// Session actions surfaced inside the hamburger SettingsMenu (the old toolbar
// refresh/upload icons now live there). The upload row mirrors the button's
// busy/disabled state while files are in flight.
const settingsActions = computed<SettingsAction[]>(() => [
  { id: "refresh", label: "Refresh sessions", icon: "pi-refresh" },
  {
    id: "upload",
    label: "Upload files",
    icon: "pi-paperclip",
    disabled: uploadsInFlight.value > 0,
    busy: uploadsInFlight.value > 0,
    busyLabel: "Uploading…",
  },
]);
function onSettingsAction(id: string) {
  if (id === "refresh") refreshSessions();
  else if (id === "upload") openUploadDialog();
}

// Default the session list to collapsed on small touch screens; users can flip
// it open with the tab counter and the preference sticks per host via localStorage.
// Keyed off pointer capability as well as width: an iPad is wider than the 768px
// phone breakpoint but has just as little room for an expanded list. Bounded at
// 1180px to match the CSS -- this choice is persisted, so defaulting a 1440px
// touchscreen laptop to collapsed would stick after a single visit.
function defaultTabsCollapsed(): boolean {
  try {
    const v = localStorage.getItem(tabsCollapsedKey);
    if (v === "1") return true;
    if (v === "0") return false;
  } catch {
    /* ignore (Safari private mode etc.) */
  }
  return window.matchMedia("(max-width: 768px), (any-pointer: coarse) and (max-width: 1180px)")
    .matches;
}
const tabsCollapsed = ref(defaultTabsCollapsed());
watch(tabsCollapsed, (v) => {
  try {
    localStorage.setItem(tabsCollapsedKey, v ? "1" : "0");
  } catch {
    /* ignore */
  }
  nextTick(() => fitTerminal());
});

// The second control row (^D/^Z/^R/^L, Home/End, PgUp/PgDn, paste) is behind
// a disclosure so the default is a single row. Collapsed by default -- the
// whole point is to give the vertical space back to the terminal -- and the
// choice sticks per host.
const ctlExpanded = ref(
  (() => {
    try {
      return localStorage.getItem(ctlExpandedKey) === "1";
    } catch {
      return false;
    }
  })(),
);
watch(ctlExpanded, (v) => {
  try {
    localStorage.setItem(ctlExpandedKey, v ? "1" : "0");
  } catch {
    /* ignore */
  }
  nextTick(() => fitTerminal());
});

// User-tweaked tab order. Persisted across reloads so a re-arrangement
// survives the refresh cycle. Server returns sessions in mtime order; we
// reconcile against this list in refreshSessions().
function loadTabOrder(): string[] {
  try {
    const raw = localStorage.getItem(tabOrderKey);
    if (raw) {
      const arr = JSON.parse(raw);
      if (Array.isArray(arr)) return arr.filter((s) => typeof s === "string");
    }
  } catch {
    /* ignore */
  }
  return [];
}
function saveTabOrder() {
  try {
    localStorage.setItem(tabOrderKey, JSON.stringify(sessions.value));
  } catch {
    /* ignore */
  }
}

// The session this page was last attached to. Tab *order* is a separate,
// purely visual thing (drag-and-drop), so it can't stand in for this: dragging
// a tab must not change which session a bare URL resumes.
function loadLastActive(): string {
  try {
    return localStorage.getItem(lastActiveKey) || "";
  } catch {
    return "";
  }
}
function saveLastActive(name: string) {
  try {
    localStorage.setItem(lastActiveKey, name);
  } catch {
    /* ignore */
  }
}
const draggingName = ref<string | null>(null);
const dropTargetName = ref<string | null>(null);

// Sessions whose close request is in flight. Closing is immediate now (no
// confirmation), and the request can take a round-trip to the box, so the tab
// sits there for a few hundred ms looking unclicked. While a name is in here its
// × shows a spinner and ignores further clicks, so an impatient second click
// can't fire a second close.
const closing = ref(new Set<string>());
// Sessions this page has closed. The /terminal/sessions poll lags a close by a
// round-trip, so a listing in flight when the close landed can still mention
// them; they're filtered out of the reconciled list until the server agrees they
// are gone. Reopening a name (which mints a fresh session server-side) clears it.
const closedNames = new Set<string>();

const statusType = ref<"connecting" | "connected" | "error" | "disconnected">("connecting");
const statusText = ref("Connecting...");
const statusVisible = ref(true);
const statusClickable = ref(false);
let hideStatusTimer: ReturnType<typeof setTimeout> | null = null;

// ---- xterm + websocket bookkeeping ----
let term: TerminalLike | null = null;
const osc52Clipboard = new Osc52ClipboardParser(64 * 1024);
const titleParser = new TerminalTitleParser();
const terminalCopyHint = new TerminalCopyHintDetector();
const OSC52_COPY_INTENT_MS = 3000;
const OSC52_COPY_HINT_MS = 2 * 60 * 1000;
let osc52CopyHintExpiresAt = 0;
type Osc52CopyIntent = {
  expiresAt: number;
  deferredWrite: DeferredClipboardWrite | null;
  timeout: ReturnType<typeof setTimeout> | null;
};
// The primary-button gesture a mouse-tracking app is currently receiving:
// where it started, and whether it already started a clipboard write (a
// multi-click press does; a drag waits for its release).
let trackedPress: { x: number; y: number; armed: boolean } | null = null;
// Whether the previous primary press on the terminal went to the app. The
// browser's click count includes clicks the emulator kept for itself, which
// the app never saw, so a multi-click counts only after a tracked click.
let lastPressTracked = false;
// Motion below this is a click, not a drag; roughly half a cell.
const MOUSE_DRAG_THRESHOLD_PX = 4;
let osc52CopyIntent: Osc52CopyIntent | null = null;
let fit: FitAddonLike | null = null;
let termKind: RendererKind = "xterm";
// Disposer for the diagnostics DOM instrumentation on the current canvas.
let diagDispose: (() => void) | null = null;
// Disposer for the touch-gesture handling (tap-to-focus vs scroll drag).
// Re-attached on every mount so it binds to the live backend's DOM.
let touchDispose: (() => void) | null = null;
// Bumped on teardown and before each mount so an in-flight mountTerminal() whose
// backend load is superseded by a newer rebuild (renderer switch racing an OS
// theme flip, etc.) bails instead of mounting a stale/second terminal.
let mountGeneration = 0;
let ws: WebSocket | null = null;
let connected = false;
let retryCount = 0;
const maxRetries = 8;
// Application-private WebSocket close code (RFC 6455 4000-4999) the terminal
// server sends when the remote shell itself exits (the user typed exit / hit
// Ctrl-D and the session ended). It is deliberately distinct from a
// generic close (server shutdown, dropped link), where we SHOULD reconnect and
// reattach to the still-alive session. On this code we stop instead of
// reconnecting into a fresh shell. See API.md.
const CLOSE_SHELL_EXITED = 4001;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
// Retry state for the initial session lookup (see startSession).
let adoptRetryTimer: ReturnType<typeof setTimeout> | null = null;
let adoptRetryDelayMs = 2000;
// The first successful mount owns startup, even if it supersedes onMounted's
// renderer load after an early tab or engine change.
let sessionStarted = false;
// Set when the user picks a session (+ or a tab) before the initial lookup has
// resolved, so the currentName watcher knows to do the connect itself.
let userPickedDuringAdoption = false;
// Exponential backoff capped at 8s so a laptop coming back from sleep
// doesn't churn on dozens of fast retries before we get a working socket.
function reconnectDelayMs(attempt: number): number {
  return Math.min(8000, 500 * Math.pow(2, Math.max(0, attempt - 1)));
}
let resizeObs: ResizeObserver | null = null;
let themeMqTeardown: (() => void) | null = null;
let destroyed = false;
// Set once the remote shell exits (server close code CLOSE_SHELL_EXITED): the
// session is gone, so every *automatic* reconnect path (focus,
// visibilitychange, online, pageshow, the dead-socket probe) must stand down
// instead of silently spawning a fresh shell. A deliberate user action --
// clicking the status, switching/opening a tab -- clears it via connect().
// Wall-clock of the most recent ws.onmessage; used to detect "OPEN but
// dead" sockets after a laptop wake/network change. probeConnection()
// sends a "ping" and re-checks lastTrafficAt a moment later; if nothing
// came back (the server's "pong" counts) we treat the socket as zombied
// and force reconnect.
let lastTrafficAt = 0;
const flight = createTerminalFlightRecorder("terminal", () => ({
  host: termEl.value,
  term,
  renderer: termKind,
  ws,
  connected,
  status: statusType.value,
  statusVisible: statusVisible.value,
}));

const keySeqs: Record<string, string> = {
  Escape: "\x1b",
  Tab: "\t",
  ArrowUp: "\x1b[A",
  ArrowDown: "\x1b[B",
  ArrowRight: "\x1b[C",
  ArrowLeft: "\x1b[D",
  Home: "\x1b[H",
  End: "\x1b[F",
  PageUp: "\x1b[5~",
  PageDown: "\x1b[6~",
  Delete: "\x1b[3~",
};

function setStatus(
  type: typeof statusType.value,
  text: string,
  opts?: { hideAfter?: number; clickable?: boolean },
) {
  statusType.value = type;
  statusText.value = text;
  statusVisible.value = true;
  statusClickable.value = !!opts?.clickable;
  if (hideStatusTimer) {
    clearTimeout(hideStatusTimer);
    hideStatusTimer = null;
  }
  if (opts?.hideAfter) {
    hideStatusTimer = setTimeout(() => {
      statusVisible.value = false;
    }, opts.hideAfter);
  }
}

function wsSendJSON(msg: object) {
  if (connected && ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(msg));
    return true;
  }
  return false;
}

function wsSendInput(data: string) {
  if (!data) return;
  diag.logInputSent(data);
  for (const piece of splitInput(data)) {
    if (!wsSendJSON({ type: "input", data: piece })) return;
    flight.input(piece.length);
  }
}

function sendResize() {
  if (!term) return;
  // Only tell the PTY when the grid actually changed. fitTerminal() runs on
  // every ResizeObserver tick, and during an iOS keyboard animation the
  // container height changes on each of ~20 frames while cols/rows stay put
  // for most of them. Each redundant SIGWINCH makes a full-screen TUI repaint,
  // which is what stacked half-drawn copies of top/vim down the screen.
  const key = `${term.cols}x${term.rows}`;
  if (key === lastSentSize) return;
  lastSentSize = key;
  flight.record("resize-request", { cols: term.cols, rows: term.rows });
  wsSendJSON({ type: "resize", cols: term.cols, rows: term.rows, term: termNameFor(termKind) });
}

// Grid size ("COLSxROWS") of the last resize we sent, so sendResize() can skip
// no-ops. Cleared whenever a socket opens: a fresh PTY needs to be told its
// size even if the grid happens to match what the previous socket knew.
let lastSentSize = "";

function base64ToBytes(s: string): Uint8Array {
  const U8 = Uint8Array as unknown as { fromBase64?: (s: string) => Uint8Array };
  if (typeof U8.fromBase64 === "function") return U8.fromBase64(s);
  const bin = atob(s);
  return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}

let sessionEnded = false;

function cancelOsc52CopyIntent() {
  if (!osc52CopyIntent) return;
  if (osc52CopyIntent.timeout) clearTimeout(osc52CopyIntent.timeout);
  osc52CopyIntent.deferredWrite?.cancel();
  osc52CopyIntent = null;
}

function resetTerminalClipboardState() {
  osc52Clipboard.reset();
  terminalCopyHint.reset();
  osc52CopyHintExpiresAt = 0;
  dropTrackedPress();
  cancelOsc52CopyIntent();
}

function noteTerminalCopyHint(bytes: Uint8Array) {
  if (terminalCopyHint.push(bytes)) {
    osc52CopyHintExpiresAt = Date.now() + OSC52_COPY_HINT_MS;
  }
}

function clearTerminalCopyHint() {
  osc52CopyHintExpiresAt = 0;
}

function armOsc52CopyIntent() {
  cancelOsc52CopyIntent();
  const intent: Osc52CopyIntent = {
    expiresAt: Date.now() + OSC52_COPY_INTENT_MS,
    // This call is deliberately synchronous in the keydown/mouse handler.
    // Safari requires clipboard.write() to begin while user activation is
    // live; the promised ClipboardItem data is resolved when OSC 52 arrives.
    deferredWrite: beginDeferredClipboardWrite(),
    timeout: null,
  };
  intent.timeout = setTimeout(() => {
    if (osc52CopyIntent === intent) cancelOsc52CopyIntent();
  }, OSC52_COPY_INTENT_MS);
  osc52CopyIntent = intent;
}

function takeOsc52CopyIntent() {
  const intent = osc52CopyIntent;
  osc52CopyIntent = null;
  if (intent?.timeout) clearTimeout(intent.timeout);
  return intent;
}

function connect() {
  if (destroyed || !term || !currentName.value) return;
  resetTerminalClipboardState();
  // A (re)connect is always a fresh intent to attach, so clear the
  // shell-exited latch; the automatic callers below already bail out while it
  // is set, so reaching here means the user asked for a new session.
  sessionEnded = false;
  setStatus("connecting", "Connecting to terminal...");
  // Cancel any pending reconnect timer so a stale timeout from the previous
  // attempt doesn't open a second socket later.
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  if (ws) {
    try {
      ws.close();
    } catch {
      /* ignore */
    }
  }
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const tid = terminalIdFromURL();
  // The session this socket attaches to; titles in its output belong to it.
  const session = currentName.value;
  const q = new URLSearchParams({ name: session });
  if (workingDir) q.set("d", workingDir);
  // Start a new session at our size, rather than drawing it at 80x24 first.
  if (term) {
    q.set("cols", String(term.cols));
    q.set("rows", String(term.rows));
  }
  const url = `${proto}//${location.host}/terminal/ws/${tid}?${q}`;
  const sock = new WebSocket(url);
  ws = sock;
  titleParser.reset();
  flight.socket();

  sock.onopen = () => {
    if (sock !== ws) return;
    connected = true;
    flight.record("socket-open", { readyState: sock.readyState });
    retryCount = 0;
    setStatus("connected", "Connected", { hideAfter: 2000 });
    // A new PTY has no idea how big we are, so this resize must go out even if
    // the grid matches what the previous socket was told.
    lastSentSize = "";
    sendResize();
    // The session only exists after the first ws connect; refresh shortly after
    // so this session's tab appears in the bar. Guard against the user having
    // already switched to a different tab in the meantime.
    setTimeout(() => {
      if (sock === ws) refreshSessions();
    }, 500);
  };
  sock.onmessage = (ev) => {
    if (sock !== ws) return;
    lastTrafficAt = Date.now();
    try {
      const msg = JSON.parse(ev.data);
      if (msg.type === "output" && msg.data) {
        const bytes = base64ToBytes(msg.data);
        diag.logOutput(bytes.length);
        flight.output(bytes);
        noteTerminalCopyHint(bytes);
        if (!osc52CopyIntent) {
          // OSC 52 is relevant only while satisfying an explicit copy key.
          // Avoid parsing arbitrary terminal output (including huge hostile
          // payloads) when there is no write we could honor.
          osc52Clipboard.reset();
        }
        for (const text of osc52CopyIntent ? osc52Clipboard.push(bytes) : []) {
          // Browser terminals cannot safely expose OSC 52 as an ambient
          // clipboard capability. Honor one write only in reply to something
          // the user just did that the application could be answering: its
          // plain `c` copy shortcut, or a selection gesture (drag, double or
          // triple click) it received via mouse tracking -- Claude Code copies
          // its own selection this way. The intent is consumed immediately.
          // Enter, paste, plain clicks, gestures the emulator kept for its own
          // selection, and locally-handled Ctrl/Cmd-C never authorize
          // terminal output.
          const intent = takeOsc52CopyIntent();
          const authorized = !!intent && document.hasFocus() && Date.now() <= intent.expiresAt;
          if (!authorized) {
            intent?.deferredWrite?.cancel();
            continue;
          }
          if (intent.deferredWrite) {
            void intent.deferredWrite.resolve(text).then((copied) => {
              // Chromium can grant clipboard permission without transient user
              // activation; use the ordinary path only if the delayed write
              // itself failed. Safari succeeds through the delayed item.
              if (!copied) void copyTerminalText(text);
            });
          } else {
            void copyTerminalText(text);
          }
        }
        term?.write(bytes);
        // Only the last title in a frame matters.
        const titles = titleParser.push(bytes);
        if (titles.length > 0) setSessionTitle(session, titles[titles.length - 1]);
      } else if (msg.type === "pong") {
        flight.pong();
      }
    } catch {
      // ignore malformed frames
    }
  };
  sock.onerror = () => {
    flight.record("socket-error", { readyState: sock.readyState });
  };
  sock.onclose = (ev) => {
    if (sock !== ws) return;
    connected = false;
    flight.record("socket-close", { code: ev.code, readyState: sock.readyState });
    if (destroyed) return;
    // The remote shell exited (Ctrl-D / logout): the session is gone, so
    // reconnecting would just spawn a fresh shell. Stop and tell the user,
    // matching what ssh(1) does when the remote shell exits.
    if (ev.code === CLOSE_SHELL_EXITED) {
      ws = null;
      sessionEnded = true;
      // Starting a new one reuses the name, but it's a different shell.
      forgetSessionTitle(session);
      setStatus("disconnected", "Session ended. Click to start a new one.", { clickable: true });
      return;
    }
    retryCount++;
    if (retryCount <= maxRetries) {
      const delay = reconnectDelayMs(retryCount);
      flight.record("reconnect-scheduled", { delayMs: delay });
      setStatus("error", `Connection lost. Reconnecting... (${retryCount}/${maxRetries})`);
      reconnectTimer = setTimeout(() => {
        connect();
      }, delay);
    } else {
      setStatus("disconnected", "Disconnected. Click to reconnect.", { clickable: true });
    }
  };
}

// kickReconnect immediately retries if we're not currently connected,
// resetting any in-flight backoff. Wired to events that signal the
// network/UI just came back online (visibilitychange, online, pageshow,
// window focus). Avoids the "laptop wakes up, terminal sits there for
// 8s before reconnecting" experience.
function kickReconnect() {
  if (destroyed || sessionEnded) return;
  if (ws && ws.readyState === WebSocket.OPEN) {
    // Already connected; just make sure the terminal is sized correctly
    // (a fresh layout pass after wake can have changed the viewport).
    fitTerminal();
    return;
  }
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  retryCount = 0;
  flight.record("reconnect-requested");
  connect();
}

function reconnect() {
  retryCount = 0;
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  connect();
}

// Detect WebSockets that are technically OPEN but silently dead (common
// after a laptop wakes from sleep on a different network). Send a "ping";
// the server answers "pong" without touching the PTY. If writes throw,
// readyState has flipped, or we don't see any traffic back within
// probeTimeoutMs, fall back once to the legacy resize probe (see below)
// before declaring the socket dead and reconnecting.
//
// This used to send a resize+1/resize pair to make the PTY repaint, but
// the PTY is SHARED across every attached client (phone, desktop, native
// apps), so merely focusing this window resized the terminal under
// everyone else — see github.com/boldsoftware/exe.dev issue #231. Liveness
// probing must never resize the PTY; only genuine local grid changes go
// through sendResize().
const probeTimeoutMs = 3000;
// One probe at a time: focus and visibilitychange often fire together
// (and focus spam is easy to produce), and overlapping probes would stack
// timers and interleave the ping stage of one probe with the legacy
// fallback stage of another. Cleared when a probe fully resolves (traffic
// seen, socket abandoned, or reconnect kicked).
let probeInFlight = false;
function probeConnection() {
  if (destroyed || sessionEnded || !ws) return;
  if (ws.readyState !== WebSocket.OPEN) {
    kickReconnect();
    return;
  }
  if (probeInFlight) return;
  const sock = ws;
  const before = lastTrafficAt;
  // Socket is OPEN but the server is no longer talking back on any
  // protocol we know. Force a fresh handshake.
  const giveUp = () => {
    probeInFlight = false;
    flight.record("probe-failed", { readyState: sock.readyState });
    try {
      sock.close();
    } catch {
      /* ignore */
    }
    kickReconnect();
  };
  try {
    flight.record("probe-ping");
    wsSendJSON({ type: "ping" });
  } catch {
    giveUp();
    return;
  }
  probeInFlight = true;
  setTimeout(() => {
    if (destroyed || ws !== sock) {
      probeInFlight = false;
      return;
    }
    if (lastTrafficAt !== before) {
      flight.record("probe-ok");
      probeInFlight = false;
      return;
    }
    // Fallback for servers that predate "ping"/"pong" (see API.md). Silence
    // after the ping means either the socket is dead OR we're talking to an
    // older server that ignores unknown frame types (e.g. mid-deploy) — on an
    // idle terminal there's no other traffic to tell those apart, and
    // reconnecting a healthy socket on every window focus would be worse.
    // So retry once with the legacy probe: a resize+1/resize pair, which
    // old servers answer with PTY repaint output and new servers ack with
    // pongs. Against an old server this does resize the shared PTY (the
    // very stomp issue #231 is about), but only when the pong never came —
    // i.e. exactly the servers whose behavior this was anyway.
    //
    // The pair deliberately bypasses sendResize()'s dedupe (we want the
    // redundant frames), so leave lastSentSize alone — it still describes
    // the size the PTY ends up at.
    try {
      if (term) {
        flight.record("probe-resize-fallback", { cols: term.cols, rows: term.rows });
        wsSendJSON({ type: "resize", cols: term.cols, rows: term.rows + 1 });
        wsSendJSON({ type: "resize", cols: term.cols, rows: term.rows });
      }
    } catch {
      giveUp();
      return;
    }
    setTimeout(() => {
      if (destroyed || ws !== sock) {
        probeInFlight = false;
        return;
      }
      if (lastTrafficAt === before) {
        giveUp();
        return;
      }
      flight.record("probe-ok");
      probeInFlight = false;
    }, probeTimeoutMs);
  }, probeTimeoutMs);
}

// Theme actually live in the current `term` instance. Used so a
// theme-mode change can detect whether the canvas needs a rebuild (the
// ghostty-web backend doesn't honor runtime theme updates as of 0.4.0).
let appliedTermTheme: "light" | "dark" | null = null;

function applyTermTheme() {
  if (!termEl.value) return;
  const t = effectiveThemeMode();
  // Keep the wrapper background in sync regardless of whether the
  // terminal instance is live yet (mountTerminal calls us right after
  // open()).
  termEl.value.style.setProperty("--xterm-bg", themes[t].background);
  if (!term) return;
  if (appliedTermTheme === t) return;
  // xterm.js honors options.theme at runtime and repaints. ghostty-web,
  // as of 0.4.0, logs a warning and leaves the already-rendered palette
  // alone (buildWasmConfig builds the palette at open() time). For
  // ghostty we tear the canvas down and remount; the session server
  // resends a screen snapshot on reconnect so the user sees only a
  // brief blink. For xterm.js we update in place.
  if (termKind === "ghostty") {
    void rebuildTerminal();
    return;
  }
  term.options.theme = themes[t];
  appliedTermTheme = t;
}

async function rebuildTerminal() {
  destroyTerminal();
  await nextTick();
  if (destroyed) return;
  const generation = await mountTerminal();
  // rebuildTerminal runs automatically on theme changes (incl. an OS
  // dark/light flip with no user interaction). After the shell has exited we
  // must NOT reconnect here -- that would silently spawn a fresh shell,
  // defeating the sessionEnded latch. Leave the remounted (idle) terminal and
  // the "Session ended" status as-is; a deliberate user action reconnects.
  if (sessionEnded) return;
  await connectMountedTerminal(generation);
}

function fitTerminal() {
  if (fitSettleTimer) {
    clearTimeout(fitSettleTimer);
    fitSettleTimer = null;
  }
  fitTerminalNow();
}

// Coalesce fits that arrive in a burst.
//
// The soft keyboard animates over ~300ms, and the CSS clamp (--vv-height)
// deliberately tracks every frame so the terminal never lags behind the
// keyboard. That means our ResizeObserver also fires every frame, and a fit
// per frame walked the grid down 37 -> 21 rows one row at a time, sending a
// SIGWINCH each step. A full-screen TUI repaints on every SIGWINCH, which is
// what stacked half-drawn copies of top/vim/Claude Code down the screen.
// Fitting once, after the size holds still, gets the same end state with one
// repaint.
let fitSettleTimer: ReturnType<typeof setTimeout> | null = null;
function scheduleFit() {
  if (fitSettleTimer) clearTimeout(fitSettleTimer);
  fitSettleTimer = setTimeout(() => {
    fitSettleTimer = null;
    fitTerminalNow();
  }, KEYBOARD_SETTLE_MS);
}

function fitTerminalNow() {
  if (!term || !fit) return;
  // If the wrapper has been transiently sized to 0 (e.g. iOS Safari
  // address-bar transition, or a parent display:none during a transition),
  // skip the fit. fit.fit() with a zero-sized container computes
  // cols/rows=0 which leaves the screen blank until the next resize
  // event, which is one of the regressions users saw against the legacy
  // page.
  const el = termEl.value;
  if (el && (el.clientWidth === 0 || el.clientHeight === 0)) return;
  try {
    const dims = fit.proposeDimensions();
    if (!dims || !dims.cols || !dims.rows) return;
    fit.fit();
    clampRowsToContainer();
  } catch {
    /* ignore */
  }
  sendResize();
}

// Guard against the bottom row being clipped after a resize.
//
// Both backends propose a row count from their own notion of a cell's
// height, but the actually-rendered rows can be a fraction of a pixel
// taller (xterm's DOM renderer rounds each row up to a whole pixel). When
// that rounding pushes total height past the container, `.term`'s
// overflow:hidden clips the last row — including the prompt. Measure the
// real rendered row height and shrink the terminal until every row fits.
function clampRowsToContainer() {
  if (!term) return;
  const el = termEl.value;
  if (!el) return;
  const rows = term.rows;
  if (!rows) return;
  const cs = getComputedStyle(el);
  const padTop = parseFloat(cs.paddingTop) || 0;
  const padBottom = parseFloat(cs.paddingBottom) || 0;
  const avail = el.clientHeight - padTop - padBottom;
  if (avail <= 0) return;
  // The element the backend sizes to rows * actual-cell-height: xterm.js
  // lays the rows out in `.xterm-rows`; ghostty-web paints a <canvas>.
  // (We avoid `.xterm-screen` because this component forces it to height
  // 100% via CSS, which would hide the very overflow we're measuring.)
  // Dividing its height by the row count gives the true per-row pixel cost
  // regardless of what the FitAddon assumed.
  const xtermRows = el.querySelector(".xterm-rows") as HTMLElement | null;
  const canvas = el.querySelector("canvas") as HTMLCanvasElement | null;
  const renderedH = (xtermRows ?? canvas)?.getBoundingClientRect().height ?? 0;
  if (renderedH <= 0) return;
  const rowPx = renderedH / rows;
  if (rowPx <= 0) return;
  // +1px epsilon so sub-pixel measurement noise doesn't needlessly drop a
  // row; only shrink when a row genuinely overflows.
  const target = Math.max(1, Math.floor((avail + 1) / rowPx));
  if (target < rows) {
    try {
      term.resize(term.cols, target);
    } catch {
      /* ignore */
    }
  }
}

async function mountTerminal(): Promise<number | null> {
  if (!termEl.value) return null;
  const gen = ++mountGeneration;
  // Force the kind the user picked (rendererKind is the source of truth here);
  // loadTerminalBackend() otherwise re-reads getRendererKind(), which honors a
  // ?renderer= query param and would make the in-app toggle a no-op.
  const backend = await loadTerminalBackend(rendererKind.value);
  // A newer rebuild started while we awaited the backend load; abandon this one.
  if (gen !== mountGeneration || destroyed || !termEl.value) return null;
  termKind = backend.kind;
  flight.record("renderer-mounted", { renderer: termKind });
  // Keep the toggle honest: getRendererKind() may request 'ghostty' but
  // loadTerminalBackend falls back to xterm.js if the WASM fails to load.
  // Persist the actual kind too, so a reload doesn't keep retrying the failed
  // backend (and the menu's checkmark matches what's really running).
  if (rendererKind.value !== backend.kind) {
    rendererKind.value = backend.kind;
    setRendererKind(backend.kind);
  }
  term = new backend.Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: "'JetBrains Mono', Consolas, 'Liberation Mono', Menlo, Courier, monospace",
    theme: themes[effectiveThemeMode()],
    // screenReaderMode: false (xterm.js default). Do NOT enable it: on macOS
    // Safari it stops xterm from preventDefault-ing printable keydowns, so
    // autocorrect/predictive text mutates the hidden textarea and re-emits the
    // input, doubling everything typed ("ls" -> "lsls"). Only re-enable behind
    // an explicit user opt-in if real screen-reader support is needed.
    linkHandler,
    // xterm.js only. Shift+drag selects locally while an app tracks the mouse
    // everywhere except macOS, where xterm.js reserves Option for that (the
    // iTerm2 convention) and this turns it on, at the price of Option+drag
    // column selection. A Shift+drag on macOS still goes to the app there.
    // ghostty-web gets Shift on every platform from
    // installGhosttySelectionPolicy. terminalKeepsGestureLocal mirrors this.
    macOptionClickForcesSelection: true,
  });
  fit = new backend.FitAddon();
  term.loadAddon(fit);
  term.loadAddon(new backend.WebLinksAddon());

  // Copy selected terminal text with the platform shortcut. In particular,
  // Ctrl-C remains an interrupt when nothing is selected, but behaves like a
  // normal copy command once the user has selected text. The same handler also
  // arms one OSC 52 write when the terminal has visibly advertised a plain
  // `c` copy shortcut (Claude Code's login screen is the motivating case).
  term.attachCustomKeyEventHandler((event) => {
    if (event.type !== "keydown") return true;
    cancelOsc52CopyIntent();
    const copyShortcut = isTerminalCopyShortcut(event);
    const plainCopyKey = isPlainOsc52CopyKey(event, ctrlActive.value, altActive.value);
    const changesCopyContext =
      event.key === "Enter" ||
      event.key === "Escape" ||
      event.ctrlKey ||
      event.metaKey ||
      event.altKey ||
      ctrlActive.value ||
      altActive.value;
    if (!plainCopyKey && changesCopyContext) clearTerminalCopyHint();
    if (plainCopyKey && Date.now() <= osc52CopyHintExpiresAt) armOsc52CopyIntent();

    if (copyShortcut && term?.hasSelection()) {
      const selectionTerm = term;
      const text = selectionTerm.getSelection();
      if (!text) return true;
      event.preventDefault();
      event.stopPropagation();
      // Clear now, before a permission prompt or slow clipboard provider can
      // keep swallowing later Ctrl-C presses or erase a newer selection.
      selectionTerm.clearSelection();
      void copyTerminalText(text).then(async () => {
        await nextTick();
        if (term === selectionTerm) selectionTerm.focus();
      });
      return false;
    }

    // When a sticky modifier is active, intercept the next a-z keypress and
    // send it as the appropriate ctrl-/alt- byte sequence.
    if (!ctrlActive.value && !altActive.value) return true;
    if (event.key.length === 1 && /[a-z]/i.test(event.key)) {
      event.preventDefault();
      event.stopPropagation();
      const data = ctrlActive.value
        ? String.fromCharCode(event.key.toLowerCase().charCodeAt(0) - 96)
        : "\x1b" + event.key;
      wsSendInput(data);
      resetModifiers();
      return false;
    }
    return true;
  });
  term.onData((data) => {
    diag.logOnData(data);
    wsSendInput(data);
  });
  term.open(termEl.value);
  // Instrument the element the renderer binds input listeners to so the
  // diagnostics pane can observe raw keydown/composition ordering. ghostty
  // binds to the host element; xterm.js routes input through a hidden
  // textarea under .xterm.
  if (diagDispose) {
    diagDispose();
    diagDispose = null;
  }
  const inputEl =
    (termKind === "xterm"
      ? (termEl.value.querySelector(".xterm-helper-textarea") as HTMLElement | null)
      : termEl.value) ?? termEl.value;
  diagDispose = diag.instrument(inputEl);
  // (Re)attach touch gesture handling: whether we have to scroll the terminal
  // ourselves depends on the backend that just mounted.
  attachTouchHandling();
  appliedTermTheme = effectiveThemeMode();
  applyTermTheme();
  fitTerminal();
  flight.record("terminal-mounted", { cols: term.cols, rows: term.rows });
  term.focus();
  return gen;
}

function destroyTerminal() {
  ++mountGeneration;
  flight.record("terminal-disposed");
  resetTerminalClipboardState();
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  connected = false;
  if (ws) {
    try {
      ws.close();
    } catch {
      /* ignore */
    }
    ws = null;
  }
  if (diagDispose) {
    diagDispose();
    diagDispose = null;
  }
  if (touchDispose) {
    touchDispose();
    touchDispose = null;
  }
  if (term) {
    try {
      term.dispose();
    } catch {
      /* ignore */
    }
    term = null;
  }
  appliedTermTheme = null;
  fit = null;
  lastSentSize = "";
  if (termEl.value) {
    termEl.value.innerHTML = "";
  }
}

function switchTo(name: string) {
  closedNames.delete(name);
  if (name === currentName.value) return;
  // An explicit choice supersedes any in-flight initial session lookup: cancel
  // its retry, and flag that this transition owns the connect (see the
  // currentName watcher) even though it starts from the empty initial name.
  if (adoptRetryTimer) {
    clearTimeout(adoptRetryTimer);
    adoptRetryTimer = null;
  }
  if (!currentName.value) userPickedDuringAdoption = true;
  // Update URL without reloading the page.
  const u = new URL(location.href);
  u.searchParams.set("name", name);
  history.pushState({}, "", u.pathname + "?" + u.searchParams.toString());
  // Reset per-tab connection bookkeeping synchronously so refreshSessions()
  // (which can fire from visibilitychange before the watcher runs) doesn't
  // see a stale hasSeenCurrent / retryCount from the previous tab.
  retryCount = 0;
  hasSeenCurrent.value = false;
  currentName.value = name;
}

function openNew() {
  const existing = new Set(sessions.value);
  const name = mintSessionName(existing);
  // Append immediately so the new tab appears at the end of the bar
  // without waiting for the next /terminal/sessions poll — otherwise the
  // tab pops in a moment later and feels laggy.
  if (!existing.has(name)) sessions.value = [...sessions.value, name];
  switchTo(name);
}

// Pick the session to attach to when the URL didn't name one. Opening the
// terminal is "show me this box's shell", not "give me another shell", so
// reuse the most recent existing session (the server lists oldest first)
// rather than stranding a new one on the box on every visit. The + button,
// closing the last tab, and ?d= still mint new sessions explicitly.
//
// Returns "" when the session list couldn't be read. Minting on a failed list
// would strand a session the moment the box is briefly unreachable, and on an
// expired cookie it would also bake a random ?name= into the URL that the auth
// round-trip preserves — so the caller reloads/retries instead.
async function adoptOrMintSession(): Promise<string> {
  let existing: string[] = [];
  try {
    const resp = await fetch("/terminal/sessions", {
      credentials: "include",
      redirect: "manual",
      headers: { Accept: "application/json" },
    });
    if (resp.type === "opaqueredirect" || resp.status === 401 || resp.status === 403) {
      // Cookie expired: bounce through the auth flow, same as refreshSessions().
      location.reload();
      return "";
    }
    if (!resp.ok) return "";
    const data = await resp.json();
    if (!Array.isArray(data.sessions)) return "";
    existing = data.sessions
      .map((s: { name?: unknown }) => s?.name)
      .filter((n: unknown): n is string => typeof n === "string" && n !== "");
  } catch {
    return "";
  }
  const alive = new Set(existing);
  // An existing session's shell is already somewhere else, so ?d= can only be
  // honoured by a fresh one.
  if (workingDir) return mintSessionName(alive);
  // Prefer the tab the user was last on if it's still alive, so returning to
  // a bare URL feels like resuming rather than jumping to whatever is newest.
  const remembered = loadLastActive();
  if (remembered && alive.has(remembered)) return remembered;
  if (existing.length > 0) return existing[existing.length - 1];
  return mintSessionName(alive);
}

async function refreshSessions() {
  try {
    const resp = await fetch("/terminal/sessions", {
      credentials: "include",
      redirect: "manual",
      headers: { Accept: "application/json" },
    });
    if (resp.type === "opaqueredirect" || resp.status === 401 || resp.status === 403) {
      // Cookie expired: bounce through the auth flow.
      location.reload();
      return;
    }
    if (!resp.ok) throw new Error("list failed: " + resp.status);
    const data = await resp.json();
    const raw: Array<{ name: string }> = Array.isArray(data.sessions) ? data.sessions : [];
    // Server returns sessions in creation order (oldest first). We layer
    // three sources on top of that:
    //   1. The current in-memory order (so live drag-rearrangements stay
    //      put across polls).
    //   2. The user's saved tab order from localStorage (so they survive
    //      a reload).
    //   3. New sessions the server reports get appended at the end.
    const serverNames = raw.map((s) => s.name).filter((n) => !closedNames.has(n));
    const serverSet = new Set(serverNames);
    // Once the server stops reporting a closed name, stop filtering it: the name
    // is free to be reused for a brand-new session.
    for (const n of closedNames) {
      if (!raw.some((s) => s.name === n)) closedNames.delete(n);
    }
    const preferred: string[] = [];
    const seen = new Set<string>();
    const pushIfKnown = (n: string) => {
      if (!seen.has(n) && serverSet.has(n)) {
        preferred.push(n);
        seen.add(n);
      }
    };
    for (const n of sessions.value) pushIfKnown(n);
    for (const n of loadTabOrder()) pushIfKnown(n);
    for (const n of serverNames) pushIfKnown(n);
    const kept = preferred;
    const hasCurrent = serverSet.has(currentName.value);
    if (currentName.value && !hasCurrent && !hasSeenCurrent.value) {
      // Server hasn't seen our socket yet (first connect race) — keep
      // the optimistic tab visible until it shows up.
      if (!kept.includes(currentName.value)) kept.push(currentName.value);
    } else if (hasCurrent) {
      hasSeenCurrent.value = true;
      // Forget titles of sessions that are gone. Only when the listing
      // includes the session we're attached to: a server may report a
      // failure as an empty list, which must not wipe every title.
      for (const n of titleStore.retain(new Set(kept))) sessionTitles.value.delete(n);
    }
    sessions.value = kept;
    saveTabOrder();
  } catch (err) {
    console.error("refresh sessions failed", err);
  }
}

// ---- Tab drag-to-reorder ----
function onTabPointerDown(_ev: PointerEvent, _name: string) {
  // Reserved for touch-drag support if we ever need it (HTML5 drag-and-
  // drop covers mouse, and mobile users get the simpler tab-counter toggle).
}
function onTabDragStart(ev: DragEvent, name: string) {
  draggingName.value = name;
  if (ev.dataTransfer) {
    ev.dataTransfer.effectAllowed = "move";
    // Firefox needs something in the dataTransfer or it suppresses the drag.
    try {
      ev.dataTransfer.setData("text/plain", name);
    } catch {
      /* ignore */
    }
  }
}
function onTabDragOver(ev: DragEvent, name: string) {
  if (!draggingName.value || draggingName.value === name) return;
  if (ev.dataTransfer) ev.dataTransfer.dropEffect = "move";
  dropTargetName.value = name;
}
function onTabDrop(_ev: DragEvent, name: string) {
  const src = draggingName.value;
  draggingName.value = null;
  dropTargetName.value = null;
  if (!src || src === name) return;
  const arr = sessions.value.slice();
  const from = arr.indexOf(src);
  const to = arr.indexOf(name);
  if (from < 0 || to < 0) return;
  arr.splice(from, 1);
  arr.splice(to, 0, src);
  sessions.value = arr;
  saveTabOrder();
}
function onTabDragEnd() {
  draggingName.value = null;
  dropTargetName.value = null;
}

async function closeSession(name: string) {
  if (closing.value.has(name)) return;
  closing.value = new Set(closing.value).add(name);
  const settle = () => {
    const next = new Set(closing.value);
    next.delete(name);
    closing.value = next;
  };
  try {
    const body = new URLSearchParams({ name }).toString();
    const resp = await fetch("/terminal/sessions/close", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
    });
    if (!resp.ok && resp.status !== 204) {
      const text = await resp.text();
      throw new Error(resp.status + (text ? " " + text : ""));
    }
  } catch (err) {
    console.error("close session failed", err);
    settle();
    toast.add({
      severity: "error",
      summary: "Could not close session",
      detail: (err as Error).message,
      life: 5000,
    });
    return;
  }
  settle();
  // Drop the closed name locally so nothing can pick it again: the
  // /terminal/sessions poll lags the close by a round-trip, so sessions.value
  // still lists it here. Without this, closing the attached tab could select the
  // session just closed as the "next" one and reconnect it.
  sessions.value = sessions.value.filter((n) => n !== name);
  closedNames.add(name);
  // A later session minted with the same name is a different shell.
  forgetSessionTitle(name);
  if (name === currentName.value) {
    // Closed the session we were attached to: pick another or open a fresh one.
    if (sessions.value.length > 0) {
      switchTo(sessions.value[0]);
    } else {
      // No tabs left: mint a new color and reconnect.
      openNew();
    }
  } else {
    saveTabOrder();
    await refreshSessions();
  }
}

// ---- Modifier / control handlers ----
function resetModifiers() {
  ctrlActive.value = false;
  altActive.value = false;
}
function toggleCtrl() {
  cancelOsc52CopyIntent();
  ctrlActive.value = !ctrlActive.value;
  term?.focus();
}
function toggleAlt() {
  cancelOsc52CopyIntent();
  altActive.value = !altActive.value;
  term?.focus();
}
function toggleKb() {
  cancelOsc52CopyIntent();
  // A real toggle, both ways. On mobile this is the guaranteed way *in*: a plain
  // tap on the terminal focuses it too, but a user who is mid-scroll (or who
  // long-presses for the paste menu) deliberately doesn't get a keyboard, so
  // they need an explicit control.
  //
  // It is also the only reliable way *out*. A terminal keyboard has no Done
  // key, and with the keyboard up we hide the session list to buy screen space,
  // so without a dismiss the user is stuck looking at a third of a screen.
  // The button renders as pressed (aria-pressed) while the keyboard is up, so
  // it has to actually un-press.
  //
  // iOS only opens the soft keyboard when .focus() is called synchronously
  // inside a user-gesture handler, so this happens in the click handler --
  // NOT in a setTimeout. keyboardMode is a derived signal (from
  // visualViewport) and updates on its own.
  if (keyboardMode.value) terminalInputEl()?.blur();
  else focusTerminalInput();
}
function sendKey(key: string) {
  cancelOsc52CopyIntent();
  clearTerminalCopyHint();
  let seq = keySeqs[key] || "";
  if (seq && (ctrlActive.value || altActive.value) && key.startsWith("Arrow")) {
    const mod = 1 + (altActive.value ? 2 : 0) + (ctrlActive.value ? 4 : 0);
    const code = { ArrowUp: "A", ArrowDown: "B", ArrowRight: "C", ArrowLeft: "D" }[
      key as "ArrowUp"
    ];
    if (code) seq = `\x1b[1;${mod}${code}`;
  }
  wsSendInput(seq);
  resetModifiers();
  term?.focus();
}
function sendCtrl(ch: string) {
  cancelOsc52CopyIntent();
  clearTerminalCopyHint();
  wsSendInput(String.fromCharCode(ch.toLowerCase().charCodeAt(0) - 96));
  resetModifiers();
  term?.focus();
}
async function pasteClip() {
  cancelOsc52CopyIntent();
  clearTerminalCopyHint();
  try {
    const text = await navigator.clipboard.readText();
    if (text) term?.paste(text);
  } catch {
    /* clipboard permission denied */
  }
  term?.focus();
}

async function copyTerminalText(text: string, notifyFailure = true): Promise<boolean> {
  if (!text) return false;
  const copied = await copyText(text);
  if (!copied && notifyFailure) {
    toast.add({
      severity: "error",
      summary: "Could not copy to the clipboard",
      life: 3000,
      closable: false,
    });
  }
  return copied;
}

async function copyContextSelection() {
  const text = contextCopyText.value;
  if (text) await copyTerminalText(text);
  term?.focus();
}

// ---- File upload ----
// Files are POSTed to the same-origin /terminal/upload endpoint, which writes
// them onto the machine the shell runs on. The user picks a
// destination directory (default /tmp, remembered in localStorage) and queues
// one or more files — via the toolbar button, the dialog's drop zone, or by
// dropping anywhere on the window. Each file uploads with a live progress bar;
// on success the server returns the absolute path it wrote, which we collect
// into a banner with per-file copy buttons.
const fileInput = ref<HTMLInputElement | null>(null);
const uploadsInFlight = ref(0);
const uploadedFiles = ref<{ path: string }[]>([]);
const copiedPath = ref("");
let copiedTimer: ReturnType<typeof setTimeout> | null = null;
const uploadDialog = ref(false);
const UPLOAD_DIR_KEY = "terminal.uploadDir";
const uploadDir = ref(loadUploadDir());

// Drag state. windowDragOver tracks a file being dragged anywhere over the
// page (drives the full-window overlay); dialogDragOver is the in-dialog zone.
// Rather than depth-count enter/leave events (which desync when children stop
// propagation), we keep the overlay up as long as dragover keeps firing and
// clear it shortly after the events stop (i.e. the pointer left or dropped).
const windowDragOver = ref(false);
const dialogDragOver = ref(false);
let windowDragTimer: ReturnType<typeof setTimeout> | null = null;

// Upload queue. Each picked/dropped file becomes an item we render with a
// status + progress bar. Items persist in the dialog until it's dismissed.
type QueueStatus = "queued" | "uploading" | "done" | "error";
interface QueueItem {
  id: number;
  file: File;
  // Destination captured at enqueue time so editing the directory mid-drain
  // doesn't silently retarget already-queued files.
  dir: string;
  status: QueueStatus;
  progress: number;
  error?: string;
}
const queue = ref<QueueItem[]>([]);
let queueSeq = 0;
let draining = false;

function loadUploadDir(): string {
  try {
    return localStorage.getItem(UPLOAD_DIR_KEY) ?? "/tmp";
  } catch {
    return "/tmp";
  }
}

function persistUploadDir() {
  try {
    localStorage.setItem(UPLOAD_DIR_KEY, uploadDir.value);
  } catch {
    /* ignore storage failures */
  }
}

function openUploadDialog() {
  uploadDialog.value = true;
}

function pickUpload() {
  // Persist the chosen directory for next time, then open the file picker.
  // Unlike before we keep the dialog open so the queue stays visible.
  persistUploadDir();
  fileInput.value?.click();
}

function onFilePicked(ev: Event) {
  const input = ev.target as HTMLInputElement;
  const files = input.files ? Array.from(input.files) : [];
  // Reset so picking the same file again re-fires change.
  input.value = "";
  enqueue(files);
}

// enqueue stages files in the upload queue (status 'queued') and opens the
// dialog so the user can review them. Uploading is an explicit step: the
// user presses the primary "Upload" button (startUploads) to begin.
function enqueue(files: File[]) {
  const fresh = files.filter(Boolean);
  if (fresh.length === 0) return;
  persistUploadDir();
  const dir = uploadDir.value;
  uploadDialog.value = true;
  for (const file of fresh) {
    queue.value.push({ id: ++queueSeq, file, dir, status: "queued", progress: 0 });
  }
}

// hasQueuedUploads enables the primary Upload button when there's something
// waiting to be sent.
const hasQueuedUploads = computed(() => queue.value.some((it) => it.status === "queued"));

// startUploads is the primary action: persist the directory and drain the
// queue. Each queued item captured its destination at enqueue time.
function startUploads() {
  persistUploadDir();
  void drainQueue();
}

function removeFromQueue(id: number) {
  queue.value = queue.value.filter((it) => it.id !== id || it.status !== "queued");
}

// When the dialog closes, drop finished (done/error) rows so the next session
// starts clean and we release references to the uploaded File blobs. Anything
// still queued or uploading is kept so it isn't silently abandoned.
watch(uploadDialog, (open) => {
  if (!open) {
    queue.value = queue.value.filter((it) => it.status === "queued" || it.status === "uploading");
  }
});

// drainQueue uploads queued items one at a time (serial keeps the server and the
// progress UI simple). Re-entrant-safe via the `draining` guard.
async function drainQueue() {
  if (draining) return;
  draining = true;
  try {
    for (;;) {
      const item = queue.value.find((it) => it.status === "queued");
      if (!item) break;
      await uploadOne(item);
    }
  } finally {
    draining = false;
  }
}

async function uploadOne(item: QueueItem) {
  item.status = "uploading";
  item.progress = 0;
  uploadsInFlight.value++;
  try {
    const { path } = await uploadFile(location.origin, item.file, item.dir, undefined, (frac) => {
      item.progress = Math.round(frac * 100);
    });
    item.progress = 100;
    item.status = "done";
    // Newest first; de-dup by path so re-uploading the same target replaces.
    uploadedFiles.value = [{ path }, ...uploadedFiles.value.filter((f) => f.path !== path)];
  } catch (err) {
    item.status = "error";
    item.error = err instanceof Error ? err.message : String(err);
    toast.add({
      severity: "error",
      summary: `Upload failed: ${item.file.name}`,
      detail: item.error,
      life: 6000,
    });
  } finally {
    uploadsInFlight.value--;
  }
}

async function copyUploadedPath(path: string) {
  if (!path) return;
  if (await copyText(path)) {
    copiedPath.value = path;
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => {
      if (copiedPath.value === path) copiedPath.value = "";
    }, 2000);
  }
}

// ---- Window-wide drag-and-drop ----
// Listeners are attached to `window` (in onMounted) so they fire no matter
// where over the page the file is dragged — including over PrimeVue's modal
// dialog mask and toasts, which are teleported to <body> outside .term-app.
// Crucially, preventing default on dragover/drop everywhere stops the browser
// from navigating the tab to a stray-dropped file (which would kill the SPA).
//
// We only react to drags that carry files (ignore text/element drags such as
// the tab-reorder drag).
function dragHasFiles(ev: DragEvent): boolean {
  const types = ev.dataTransfer?.types;
  return !!types && Array.from(types).includes("Files");
}
// keepWindowDragAlive (re)arms a watchdog: while dragover keeps firing we stay
// "over"; once it stops for a beat the pointer has left, the drag was
// cancelled (ESC), or it dropped — so we clear the overlay. This sidesteps the
// classic enter/leave depth-count desync and the "no dragleave on ESC" bug.
function keepWindowDragAlive() {
  windowDragOver.value = true;
  if (windowDragTimer) clearTimeout(windowDragTimer);
  // Generous window: while held still, Chromium's dragover cadence can be a few
  // hundred ms, so a short timeout would make the overlay flicker on a paused
  // drag. 400ms is comfortably longer than that yet imperceptible on a drop.
  // (Edge case: a perfectly motionless drag held >400ms briefly hides the
  // overlay until the next pointer move; it self-corrects and the drop still
  // works. Worth it to avoid the alternative "stuck overlay" failure mode.)
  windowDragTimer = setTimeout(() => {
    windowDragOver.value = false;
  }, 400);
}
function clearWindowDrag() {
  if (windowDragTimer) {
    clearTimeout(windowDragTimer);
    windowDragTimer = null;
  }
  windowDragOver.value = false;
}
function onWindowDragOver(ev: DragEvent) {
  if (!dragHasFiles(ev)) return;
  // Suppress the browser's default "open the file" navigation everywhere.
  ev.preventDefault();
  if (ev.dataTransfer) ev.dataTransfer.dropEffect = "copy";
  keepWindowDragAlive();
}
function onWindowDrop(ev: DragEvent) {
  if (!dragHasFiles(ev)) {
    clearWindowDrag();
    return;
  }
  ev.preventDefault();
  clearWindowDrag();
  dialogDragOver.value = false;
  const files = ev.dataTransfer?.files ? Array.from(ev.dataTransfer.files) : [];
  enqueue(files);
}
// onZoneDrop handles drops on the dialog's own drop zone. The template binds
// it with `.stop` (load-bearing!): that's what prevents the window-level drop
// handler from ALSO firing and enqueuing each file twice. Keep the `.stop`.
function onZoneDrop(ev: DragEvent) {
  dialogDragOver.value = false;
  clearWindowDrag();
  const files = ev.dataTransfer?.files ? Array.from(ev.dataTransfer.files) : [];
  enqueue(files);
}

// copyText uses the async Clipboard API when available (HTTPS / secure
// contexts), with a hidden-textarea fallback for browsers/environments where
// it is absent or denied.
async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      /* fall through to legacy path */
    }
  }
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const ta = document.createElement("textarea");
  try {
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    ta.remove();
    active?.focus();
  }
}

// ---- Wiring ----
function onPopState() {
  // User used the back/forward button; sync to the new URL ?name=.
  const name = new URL(location.href).searchParams.get("name");
  if (name && name !== currentName.value) {
    currentName.value = name;
  }
}

function onVisibility() {
  if (document.hidden) {
    cancelOsc52CopyIntent();
    dropTrackedPress();
    return;
  }
  // Coming back to the tab: probe the socket and refit. If the socket is
  // OPEN but the server has already silently dropped us (common after a
  // laptop sleep or network change) the probe will trigger reconnect.
  // This mirrors what the legacy page got "for free" by reloading.
  probeConnection();
  kickReconnect();
  refreshSessions();
}

function onOnline() {
  kickReconnect();
}

function onPageShow(ev: PageTransitionEvent) {
  // Safari restores pages from bfcache without firing visibilitychange.
  if (ev.persisted) kickReconnect();
}

function onWindowFocus() {
  // Desktop: switching back to a window that's been minimized for a while
  // typically doesn't fire visibilitychange. Probe here too.
  probeConnection();
}

function onWindowBlur() {
  cancelOsc52CopyIntent();
  // A release while the window is unfocused is never delivered here.
  dropTrackedPress();
}

function onWindowPointerDown(ev: PointerEvent) {
  // A plain `c` authorizes only the application's immediate OSC 52 response.
  // Any later user gesture means that intent is stale.
  cancelOsc52CopyIntent();
  // Likewise a press anywhere ends the terminal's gesture, whether or not its
  // release was ever seen; a press on the terminal itself starts over from
  // its own mousedown, which follows this event.
  if (ev.button === 0 && !termEl.value?.contains(ev.target as Node | null)) dropTrackedPress();
}

function onWindowResize() {
  // Debounced: window resize is bursty (desktop drag-resize, and some browsers
  // fire it alongside the soft-keyboard animation), and a fit per event is how
  // the PTY got stormed with SIGWINCHs in the first place. The ResizeObserver
  // on .term covers the same ground, so a trailing fit is all this needs.
  scheduleFit();
}

// The element the active backend actually receives input on: xterm.js routes
// keystrokes through a hidden helper <textarea>; ghostty-web appends its own
// hidden <textarea> to the host. Focusing it (rather than the host) is what
// iOS needs to see to summon the soft keyboard.
function terminalInputEl(): HTMLElement | null {
  const host = termEl.value;
  if (!host) return null;
  return (
    (host.querySelector(".xterm-helper-textarea") as HTMLElement | null) ??
    (host.querySelector("textarea") as HTMLElement | null)
  );
}

// Focus the terminal's input element. This is the only path that may summon
// the mobile soft keyboard, so every caller must be a deliberate user action
// (tap, control-bar button) — never a scroll.
function focusTerminalInput() {
  const el = terminalInputEl();
  if (el) el.focus();
  else term?.focus();
}

// Pointerdown anywhere in the .term wrapper focuses the terminal. On iOS the
// hidden input element is impossible to tap precisely (it's 1px and sits
// behind the canvas), and missing it means the soft keyboard never appears.
//
// Touches are deliberately excluded: touch is handled by attachTerminalTouch,
// which distinguishes a tap (focus, keyboard) from a scroll drag (never
// focus). Focusing here on touchstart is what made scrolling on an iPhone pop
// the keyboard up and — because iOS auto-zooms to a focused editable element —
// zoom the page to the caret.
let lastTermPointerType = "";
let lastTermPointerAt = 0;
function onTermPointerDown(ev: PointerEvent) {
  lastTermPointerType = ev.pointerType;
  lastTermPointerAt = Date.now();
  if (ev.pointerType === "touch") return;
  // Don't preventDefault — we still want the backend's own pointerdown
  // handler (selection, scroll) to run.
  focusTerminalInput();
}

// macOS turns Control+primary into a context click: button stays 0 and only
// the contextmenu event says what it was, which is too late to stop the report.
function isContextClick(ev: MouseEvent): boolean {
  return ev.button === 2 || (isMacPlatform && ev.button === 0 && ev.ctrlKey);
}

// A mouse-tracking application (Claude Code, tmux with `mouse on`) receives
// the primary-button press, drag and release instead of the emulator
// selecting, and may answer a selection gesture with OSC 52 for the text it
// selected itself. That reply is honored the way a plain `c` is, but only for
// a gesture that looks like selecting: a multi-click press (answered on the
// press) or a drag (answered on the release). A plain click to focus or place
// the cursor never qualifies. The clipboard write must begin inside the
// gesture for Safari to allow it, so it starts here rather than when the
// reply arrives.
//
// Both handlers run in the capture phase, ahead of every emulator listener:
// the press on the host, so presses the emulator keeps for its own selection
// (Shift, or Option for xterm.js on macOS) and context clicks are seen before
// the emulator can stop them, and the release on the document, because
// xterm.js reports a drag released past the terminal's edge.
//
// Right-clicking a selection must also open the context menu with that
// selection intact. Both emulators would otherwise report the button to a
// mouse-tracking app and clear the selection as they do for any user input,
// leaving Copy disabled. Stop the press before either sees it, and then the
// matching release too (a lone release report clears the selection just the
// same, and on Windows contextmenu fires after the release). The release is
// matched by button, not modifiers, since Control may well come up before the
// button does, and is watched on the document so one landing outside the
// terminal still clears the latch. The contextmenu event itself is unaffected.
let swallowedContextButton: number | null = null;
function onTermMouseDownCapture(ev: MouseEvent) {
  if (!term) return;
  if (ev.button === 0) {
    const afterTrackedClick = lastPressTracked;
    dropTrackedPress();
    if (
      !isContextClick(ev) &&
      !terminalKeepsGestureLocal(termKind, ev) &&
      terminalReportsMouse(term)
    ) {
      lastPressTracked = true;
      trackedPress = { x: ev.clientX, y: ev.clientY, armed: ev.detail >= 2 && afterTrackedClick };
      if (trackedPress.armed) armOsc52CopyIntent();
    }
  }
  if (!isContextClick(ev) || !term.hasSelection() || !terminalReportsMouse(term)) return;
  ev.stopPropagation();
  swallowedContextButton = ev.button;
}
function onDocumentMouseUpCapture(ev: MouseEvent) {
  if (swallowedContextButton !== null && ev.button === swallowedContextButton) {
    swallowedContextButton = null;
    ev.stopPropagation();
    return;
  }
  if (ev.button !== 0) return;
  const press = trackedPress;
  trackedPress = null;
  // An armed press already started the one write Safari allows in flight;
  // starting another on the release would reject it (or the reply already
  // filled it in). Only a drag the app will hear the end of arms here:
  // ghostty-web's InputHandler listens on the host, so a release past the
  // terminal's edge is never reported to the app; xterm.js follows the drag.
  if (!press || !term || press.armed) return;
  const moved = Math.hypot(ev.clientX - press.x, ev.clientY - press.y);
  const reported = termKind === "xterm" || !!termEl.value?.contains(ev.target as Node | null);
  if (moved >= MOUSE_DRAG_THRESHOLD_PX && reported && terminalReportsMouse(term)) {
    armOsc52CopyIntent();
  }
}
function dropTrackedPress() {
  trackedPress = null;
  lastPressTracked = false;
}

// Desktop browsers disagree about whether a canvas-backed terminal selection
// is eligible for their native Copy menu. Use PrimeVue's context menu instead,
// capturing the selection before the menu click moves focus away from the
// terminal. Touch long-press is deliberately left to the native mobile menu,
// which is also the only way iOS Safari exposes Paste over the visible canvas.
function onTermContextMenu(ev: MouseEvent) {
  cancelOsc52CopyIntent();
  const sourceCapabilities = (
    ev as MouseEvent & { sourceCapabilities?: { firesTouchEvents?: boolean } | null }
  ).sourceCapabilities;
  const recentTouch =
    sourceCapabilities?.firesTouchEvents === true ||
    (lastTermPointerType === "touch" && Date.now() - lastTermPointerAt < 1500);
  if (recentTouch) return;
  lastTermPointerType = "";
  contextCopyText.value = term?.getSelection() ?? "";
  ev.preventDefault();
  ev.stopPropagation();
  terminalContextMenu.value?.show(ev);
}

function attachTouchHandling() {
  if (touchDispose) {
    touchDispose();
    touchDispose = null;
  }
  if (!termEl.value) return;
  touchDispose = attachTerminalTouch(termEl.value, {
    focus: focusTerminalInput,
    // ghostty-web paints a canvas and only scrolls on `wheel`, so touch
    // scrolling has to be synthesized. xterm.js 6 scrolls its virtual viewport
    // from a touch Gesture of its own; synthesizing there too would scroll
    // twice per drag.
    ownsScrolling: () => termKind === "ghostty",
    // Feed the drag back in as a wheel event rather than calling
    // scrollLines() directly. ghostty-web's wheel handler does more than
    // scroll: on the alternate screen (full-screen TUIs — vim, and the
    // Claude Code session this bug was reported against) it translates wheel
    // into arrow keys so the *application* scrolls, which is the behaviour a
    // user dragging through such a UI expects. Reusing it keeps one code path
    // for mouse and touch.
    scrollBy: (deltaY) => {
      const el = termEl.value;
      if (!el) return;
      el.dispatchEvent(
        new WheelEvent("wheel", {
          deltaY,
          deltaMode: 0, // DOM_DELTA_PIXEL: deltaY is CSS pixels of finger travel
          bubbles: true,
          cancelable: true,
        }),
      );
    },
  });
}

// iOS soft-keyboard awareness. window.innerHeight stays the same when
// the keyboard slides up; only visualViewport.height shrinks. We expose
// that height as a CSS custom property so .term-app can clamp itself
// and the terminal canvas stays fully visible above the keyboard.
let vvHandler: (() => void) | null = null;
let vvRaf = 0;

let vvLastKey = "";

function setupVisualViewportTracking() {
  const vv = window.visualViewport;
  if (!vv) return;
  const apply = (immediateFit = false) => {
    vvRaf = 0;
    // computeViewportClamp() distinguishes a soft keyboard from pinch-zoom:
    // both shrink visualViewport.height, but only the keyboard shrinks it in
    // *layout* pixels. Clamping while the user has merely zoomed in would
    // resize the PTY under them for no reason. It's pure and cheap, so compute
    // it up front — the scroll correction below needs to know.
    const { keyboardUp, clampPx } = computeViewportClamp({
      height: vv.height,
      scale: vv.scale,
      innerHeight: window.innerHeight,
    });
    // iOS scrolls the *layout* viewport to reveal the focused element when the
    // keyboard opens. Our chrome (tab bar, control keys) lives at the top of a
    // 100dvh flex column, so that scroll strands it off-screen with no way back
    // — the page has nowhere legitimate to scroll, yet iOS scrolled it anyway.
    // We clamp the height instead, which keeps the caret visible without
    // scrolling, so undo it.
    //
    // Two details:
    //  * Only while the keyboard is up. A pinch-zoomed page pans legitimately,
    //    and yanking it back to 0 would fight the user's own gesture.
    //  * BEFORE the same-geometry early return below, because iOS often
    //    delivers the reveal-scroll *after* the height has settled: a 'scroll'
    //    event whose height, scale and innerHeight are all unchanged. Bailing
    //    out on those would leave the chrome stranded.
    if (keyboardUp && window.scrollY !== 0) window.scrollTo(0, 0);
    // Round to avoid sub-pixel thrashing on iOS during keyboard animation.
    const key = `${Math.round(vv.height)}:${vv.scale.toFixed(2)}:${window.innerHeight}`;
    if (key === vvLastKey) return; // No-op; iOS fires plenty of redundant events.
    vvLastKey = key;
    if (clampPx != null) {
      document.documentElement.style.setProperty("--vv-height", clampPx + "px");
    } else {
      document.documentElement.style.removeProperty("--vv-height");
    }
    keyboardMode.value = keyboardUp;
    // Re-fit only once the viewport stops moving. Every intermediate frame
    // would otherwise become a PTY resize; see KEYBOARD_SETTLE_MS. The initial
    // sync is not an animation, so it fits straight away and the first paint is
    // correct rather than KEYBOARD_SETTLE_MS late.
    if (immediateFit) fitTerminal();
    else scheduleFit();
  };
  // iOS emits many resize/scroll events during keyboard animation; coalesce
  // into one update per animation frame.
  vvHandler = () => {
    if (vvRaf) return;
    vvRaf = requestAnimationFrame(() => apply());
  };
  vvLastKey = "";
  apply(true);
  vv.addEventListener("resize", vvHandler);
  vv.addEventListener("scroll", vvHandler);
}
function teardownVisualViewportTracking() {
  const vv = window.visualViewport;
  if (vv && vvHandler) {
    vv.removeEventListener("resize", vvHandler);
    vv.removeEventListener("scroll", vvHandler);
  }
  if (vvRaf) {
    cancelAnimationFrame(vvRaf);
    vvRaf = 0;
  }
  if (fitSettleTimer) {
    clearTimeout(fitSettleTimer);
    fitSettleTimer = null;
  }
  vvHandler = null;
  vvLastKey = "";
  document.documentElement.style.removeProperty("--vv-height");
}

// iOS Safari only shows the long-press "Paste" menu when the touch target is
// considered an editable region. xterm's hidden helper textarea handles
// that on desktop, but on mobile users tend to long-press the visible
// terminal area whose target isn't the textarea. Capture paste at the
// wrapper and route it through term.paste() so xterm's normal paste path
// (bracketed-paste wrapping when the running program has enabled it) still
// runs.
function onPaste(ev: ClipboardEvent) {
  clearTerminalCopyHint();
  // xterm.js attaches its own paste listeners to both its helper
  // <textarea> and its root element. When the user triggers a paste
  // from a focused terminal (right-click menu, Shift+Insert, Ctrl/Cmd+V
  // on desktop), the event targets the helper textarea and xterm
  // handles it natively. If we ALSO call term.paste(text) here, the
  // paste runs twice. preventDefault() doesn't help — it doesn't stop
  // xterm's listener from reading clipboardData.
  //
  // We only want this handler for the iOS Safari long-press case,
  // where the paste targets the bare wrapper div (which xterm doesn't
  // listen on). So: bail out if the event target is inside xterm's
  // own DOM.
  const target = ev.target as Node | null;
  if (target && term?.element && term.element.contains(target)) return;
  const text = ev.clipboardData?.getData("text/plain");
  if (text && term) {
    ev.preventDefault();
    term.paste(text);
  }
}

watch(currentName, async (name, prev) => {
  if (name) saveLastActive(name);
  // startSession() does the very first connect() itself (after resolving which
  // session to attach to), so skip the initial "" -> name transition when it
  // set the name. But when the *user* picks a session while that lookup is
  // still in flight, this watcher owns the connect — startSession() bails out
  // rather than clobbering their choice.
  if (!prev && !userPickedDuringAdoption) return;
  userPickedDuringAdoption = false;
  destroyTerminal();
  await nextTick();
  await connectMountedTerminal(await mountTerminal());
});

async function connectMountedTerminal(generation: number | null) {
  // A canceled load must not connect the current name on behalf of a newer
  // mount. Check again here in case that mount won before this caller resumed.
  if (generation === null || generation !== mountGeneration || destroyed) return;
  if (!sessionStarted) {
    sessionStarted = true;
    await startSession();
  } else if (currentName.value) {
    connect();
  }
}

// Resolve the session to attach to, then connect. Split out of onMounted so
// the page's listeners are all registered before this (possibly slow) lookup
// runs, and so a failed lookup can retry without re-running the rest of mount.
async function startSession() {
  if (currentName.value) {
    // The URL named a session (or the user picked one while we were waiting).
    connect();
    refreshSessions();
    return;
  }
  setStatus("connecting", "Finding your session...");
  const name = await adoptOrMintSession();
  if (destroyed) return;
  // The user can click + (or a tab) while the list request is in flight. That's
  // an explicit choice and it wins: adopting over it would discard the click
  // and strand the session it just created.
  if (currentName.value) return;
  if (!name) {
    // Couldn't read the list (offline, box unreachable, or an auth reload is
    // already under way). Minting here would strand a session, so retry
    // instead. Backs off to a slow poll rather than hammering the box.
    setStatus("disconnected", "Could not reach the server. Retrying...");
    adoptRetryTimer = setTimeout(() => {
      adoptRetryTimer = null;
      void startSession();
    }, adoptRetryDelayMs);
    adoptRetryDelayMs = Math.min(adoptRetryDelayMs * 2, 15000);
    return;
  }
  currentName.value = name;
  setSessionInURL(name);
  connect();
  refreshSessions();
}

onMounted(async () => {
  await nextTick();
  if (destroyed) return;

  themeMqTeardown = watchSystemTheme(
    () => themeMode.value,
    () => applyTermTheme(),
  );

  if (termEl.value) {
    resizeObs = new ResizeObserver(([entry]) => {
      if (entry) {
        flight.record("host-resize", {
          width: Math.round(entry.contentRect.width),
          height: Math.round(entry.contentRect.height),
        });
      }
      scheduleFit();
    });
    resizeObs.observe(termEl.value);
    // Mouse/pen only: tapping the terminal area focuses the hidden input so
    // the caret goes where the user clicked. Touch goes through
    // attachTerminalTouch (wired in mountTerminal) so a scroll drag can't
    // summon the soft keyboard.
    termEl.value.addEventListener("pointerdown", onTermPointerDown);
    termEl.value.addEventListener("mousedown", onTermMouseDownCapture, true);
    document.addEventListener("mouseup", onDocumentMouseUpCapture, true);
  }

  window.addEventListener("resize", onWindowResize);
  window.addEventListener("storage", onTitleStorage);
  window.addEventListener("popstate", onPopState);
  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("online", onOnline);
  window.addEventListener("pageshow", onPageShow);
  window.addEventListener("focus", onWindowFocus);
  window.addEventListener("blur", onWindowBlur);
  window.addEventListener("pointerdown", onWindowPointerDown, true);
  // Window-level drag-and-drop for file uploads. See the handlers' comments:
  // window scope (vs .term-app) ensures drops over teleported dialogs/toasts
  // are caught and never navigate the page away.
  window.addEventListener("dragover", onWindowDragOver);
  window.addEventListener("drop", onWindowDrop);
  // Capture phase so we get the paste even if xterm hasn't seen the touch.
  termEl.value?.addEventListener("paste", onPaste, true);

  // iOS Safari: when the soft keyboard appears, window.innerHeight does
  // NOT shrink — only visualViewport.height does. Without this listener,
  // the bottom rows of the terminal (including the prompt) end up hidden
  // underneath the keyboard, which is a UX disaster on phones. Mirror
  // the visible viewport height into a CSS custom property and use it
  // to clamp .term-app's height.
  setupVisualViewportTracking();

  // Wire listeners before either async startup step. Only the winning mount
  // starts adoption/connection; an early tab or renderer change can own it.
  await connectMountedTerminal(await mountTerminal());
});

onBeforeUnmount(() => {
  destroyed = true;
  flight.dispose();
  if (adoptRetryTimer) {
    clearTimeout(adoptRetryTimer);
    adoptRetryTimer = null;
  }
  window.removeEventListener("resize", onWindowResize);
  window.removeEventListener("storage", onTitleStorage);
  window.removeEventListener("popstate", onPopState);
  document.removeEventListener("visibilitychange", onVisibility);
  window.removeEventListener("online", onOnline);
  window.removeEventListener("pageshow", onPageShow);
  window.removeEventListener("focus", onWindowFocus);
  window.removeEventListener("blur", onWindowBlur);
  window.removeEventListener("pointerdown", onWindowPointerDown, true);
  window.removeEventListener("dragover", onWindowDragOver);
  window.removeEventListener("drop", onWindowDrop);
  if (themeMqTeardown) {
    themeMqTeardown();
    themeMqTeardown = null;
  }
  termEl.value?.removeEventListener("paste", onPaste, true);
  termEl.value?.removeEventListener("pointerdown", onTermPointerDown);
  termEl.value?.removeEventListener("mousedown", onTermMouseDownCapture, true);
  document.removeEventListener("mouseup", onDocumentMouseUpCapture, true);
  if (touchDispose) {
    touchDispose();
    touchDispose = null;
  }
  teardownVisualViewportTracking();
  resizeObs?.disconnect();
  if (windowDragTimer) {
    clearTimeout(windowDragTimer);
    windowDragTimer = null;
  }
  if (copiedTimer) {
    clearTimeout(copiedTimer);
    copiedTimer = null;
  }
  destroyTerminal();
});
</script>

<style scoped>
/* stylelint-disable color-no-hex, declaration-property-value-disallowed-list -- legacy hardcoded colors; replace them with tokens.css variables, then delete this header */
.term-app {
  /* One source of truth for the home-indicator inset. The ad bar grows by it
     and the status pill has to clear it, and those two rules living apart is
     how they drift: the bar gets taller on a notched iPhone while the pill
     stays put, and the pill ends up inside the bar. Naming it also lets a
     test stand in a nonzero value -- browsers on a desktop always report
     env(safe-area-inset-*) as 0, so the notched geometry is otherwise
     impossible to exercise. */
  --safe-bottom: env(safe-area-inset-bottom, 0px);
  display: flex;
  flex-direction: column;
  height: 100vh;
  height: 100dvh; /* iOS Safari: shrink with the dynamic URL bar */
  /* When the soft keyboard is up on iOS, --vv-height (set from
     visualViewport in JS) is smaller than 100dvh; clamp to it so the
     bottom of the terminal stays visible above the keyboard. */
  height: var(--vv-height, 100dvh);
  min-height: 0;
  padding: 16px;
  gap: 10px;
  box-sizing: border-box;
  overflow: hidden; /* keep iOS Safari from rubber-banding the whole page above the terminal */
  font-family:
    "JetBrains Mono", ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
}

.term-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--p-text-color);
  flex: 0 0 auto;
  margin-right: 4px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 40%;
}

.tab-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--p-content-border-color);
}

.tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px 4px 10px;
  border: 1px solid var(--p-content-border-color);
  border-radius: 6px;
  background: var(--surface-subtle);
  color: var(--p-text-muted-color);
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  user-select: none;
  max-width: 220px;
  line-height: 1.2;
}
.tab:hover {
  background: var(--p-content-hover-background);
  color: var(--p-text-color);
}
.tab.active {
  background: var(--p-content-background);
  color: var(--p-text-color);
  border-color: var(--p-primary-color);
  font-weight: 600;
}
/* Session tabs have a fixed width, so a title that changes with every prompt
   (or every Claude Code task) can't reflow the bar: a bar that wraps onto
   another line shrinks the terminal, which resizes the PTY under the program
   that just set the title. */
.tab-name {
  display: inline-flex;
  width: 20ch;
  overflow: hidden;
  white-space: pre;
}
/* Long titles keep both ends: the head takes the ellipsis, the tail stays. */
.title-head {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.title-tail {
  flex: none;
}
.tab-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  font-size: 14px;
  line-height: 1;
  color: inherit;
  cursor: pointer;
}
.tab-close:hover {
  background: var(--p-content-border-color);
  color: var(--p-text-color);
}
/* Close in flight: the request can take a round-trip to the box, so show that
   the click landed (pi-spin, which honours prefers-reduced-motion) instead of
   inviting a second click. Deliberately still hit-testable -- the click handler
   ignores repeats, whereas pointer-events: none would let a second click fall
   through to the tab button underneath and switch sessions instead. */
.tab-close.closing {
  cursor: default;
  opacity: 0.6;
}
/* Square icon-only action buttons (new / refresh / upload). Sized to match
   the height of the text tabs so the whole bar reads as one set of
   controls rather than a jumble of glyphs. */
.tab-icon {
  flex: 0 0 auto;
  width: 28px;
  height: 28px;
  padding: 0;
  justify-content: center;
  font-size: 13px;
}
.tab-new {
  border-style: dashed;
}
/* Push the settings (hamburger) menu to the far right of the tab bar; the
   refresh/upload actions it now hosts used to anchor this group here. */
.tab-bar :deep(.settings-menu) {
  margin-left: auto;
}
/* The session-list toggle is a tab counter, like a phone browser's tab
   switcher: the number of sessions in an outlined box. While the list is open
   the button stays pressed. */
.tab-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 28px;
  height: 28px;
  padding: 0 2px;
  box-sizing: border-box;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--p-text-muted-color);
  font-family: inherit;
  cursor: pointer;
  flex: 0 0 auto;
}
/* Touch browsers keep :hover after a tap, so hover only where there is a
   real pointer, and lighter than pressed so the two can't be confused. */
@media (hover: hover) {
  .tab-toggle:hover {
    background: var(--p-content-hover-background);
    color: var(--p-text-color);
  }
}
.tab-toggle[aria-expanded="true"] {
  background: var(--p-highlight-background);
  color: var(--p-highlight-color);
}
.tab-counter {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 18px;
  height: 18px;
  padding: 0 3px;
  box-sizing: border-box;
  border: 1px solid currentColor;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  line-height: 1;
}
.tab-toggle-name {
  display: inline-flex;
  min-width: 0;
  overflow: hidden;
  font-weight: 600;
  color: var(--p-text-color);
  font-size: 13px;
  max-width: 200px;
  white-space: pre;
  padding: 0 2px;
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}

.tab.dragging {
  opacity: 0.5;
}
.tab.drop-target {
  border-color: var(--p-primary-color);
  box-shadow: inset 2px 0 0 var(--p-primary-color);
}
.upload-dir-label {
  display: block;
  font-size: 13px;
  font-weight: 600;
  margin-bottom: 6px;
  color: var(--p-text-color);
}
.upload-dir-input {
  width: 100%;
  box-sizing: border-box;
  padding: 8px 10px;
  font-size: 14px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  color: var(--p-text-color);
  background: var(--p-content-background);
  border: 1px solid var(--p-content-border-color);
  border-radius: 6px;
}
.upload-dir-input:focus {
  outline: none;
  border-color: var(--p-primary-color);
}
.upload-dir-hint {
  margin: 8px 0 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--p-text-muted-color);
}
.upload-dir-hint code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--surface-subtle);
  padding: 1px 4px;
  border-radius: 4px;
}
.upload-input {
  display: none;
}
.upload-result {
  background: var(--surface-subtle);
  border-bottom: 1px solid var(--p-content-border-color);
  font-size: 13px;
  color: var(--p-text-color);
  padding: 6px 10px;
}
.upload-result-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.upload-result-title {
  flex: 1 1 auto;
  font-weight: 600;
  font-size: 12px;
  color: var(--p-text-muted-color);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.upload-result-list {
  list-style: none;
  margin: 4px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 30vh;
  overflow-y: auto;
}
.upload-result-item {
  display: flex;
  align-items: center;
  gap: 8px;
}
.upload-result-text {
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--p-primary-color);
}
.upload-result code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--p-content-background);
  padding: 1px 4px;
  border-radius: 4px;
  color: var(--p-text-color);
}
.upload-copy {
  flex: 0 0 auto;
  background: transparent;
  border: 1px solid var(--p-content-border-color);
  border-radius: 6px;
  color: var(--p-text-muted-color);
  cursor: pointer;
  padding: 3px 10px;
  font-size: 12px;
  font-family: inherit;
}
.upload-copy:hover {
  color: var(--p-text-color);
  background: var(--p-content-background);
}
.upload-dismiss {
  flex: 0 0 auto;
  background: transparent;
  border: none;
  color: var(--p-text-muted-color);
  cursor: pointer;
  font-size: 16px;
  line-height: 1;
  padding: 0 4px;
}
.upload-dismiss:hover {
  color: var(--p-text-color);
}

/* ---- Upload dialog: drop zone + queue ---- */
.upload-zone {
  margin-top: 12px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 18px;
  border: 2px dashed var(--p-content-border-color);
  border-radius: 10px;
  background: var(--surface-subtle);
  color: var(--p-text-muted-color);
  cursor: pointer;
  text-align: center;
  transition:
    border-color 0.12s ease,
    background 0.12s ease,
    color 0.12s ease;
}
.upload-zone:hover,
.upload-zone:focus-visible {
  border-color: var(--p-primary-color);
  color: var(--p-text-color);
  outline: none;
}
.upload-zone.is-over {
  border-color: var(--p-primary-color);
  background: color-mix(in srgb, var(--p-primary-color) 12%, transparent);
  color: var(--p-text-color);
}
/* Children don't intercept pointer events, so crossing onto the icon/text
   mid-drag doesn't fire a spurious dragleave that flickers the highlight. */
.upload-zone > * {
  pointer-events: none;
}
.upload-zone-icon {
  font-size: 22px;
  line-height: 1;
  font-weight: 700;
  color: var(--p-primary-color);
}
.upload-zone-text {
  font-size: 13px;
}
.upload-zone-text strong {
  color: var(--p-text-color);
}

.upload-queue {
  list-style: none;
  margin: 12px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 36vh;
  overflow-y: auto;
}
.upload-queue-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid var(--p-content-border-color);
  border-radius: 8px;
  background: var(--p-content-background);
}
.upload-queue-item.error {
  border-color: var(--p-red-400, #e5534b);
}
.uq-icon {
  flex: 0 0 auto;
  width: 18px;
  text-align: center;
  font-size: 13px;
  color: var(--p-text-muted-color);
}
.upload-queue-item.done .uq-icon {
  color: var(--p-green-500, #2da44e);
}
.upload-queue-item.error .uq-icon {
  color: var(--p-red-400, #e5534b);
}
.uq-body {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.uq-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--p-text-color);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.uq-meta {
  font-size: 11px;
  color: var(--p-text-muted-color);
}
.upload-queue-item.error .uq-meta {
  color: var(--p-red-400, #e5534b);
}
.uq-bar {
  height: 4px;
  border-radius: 999px;
  background: var(--surface-subtle);
  overflow: hidden;
}
.uq-bar-fill {
  display: block;
  height: 100%;
  width: 0;
  border-radius: 999px;
  background: var(--p-primary-color);
  transition: width 0.15s ease;
}
.upload-queue-item.done .uq-bar-fill {
  background: var(--p-green-500, #2da44e);
}
.upload-queue-item.error .uq-bar-fill {
  background: var(--p-red-400, #e5534b);
}
.uq-remove {
  flex: 0 0 auto;
  background: transparent;
  border: none;
  color: var(--p-text-muted-color);
  cursor: pointer;
  font-size: 16px;
  line-height: 1;
  padding: 0 2px;
}
.uq-remove:hover {
  color: var(--p-text-color);
}

/* ---- Full-window drag overlay ---- */
.upload-overlay {
  position: fixed;
  inset: 0;
  z-index: 2000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: color-mix(in srgb, var(--p-content-background) 70%, transparent);
  backdrop-filter: blur(2px);
  pointer-events: none;
}
.upload-overlay-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 32px 48px;
  border: 2px dashed var(--p-primary-color);
  border-radius: 16px;
  background: var(--p-content-background);
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.25);
}
.upload-overlay-icon {
  font-size: 34px;
  font-weight: 700;
  line-height: 1;
  color: var(--p-primary-color);
}
.upload-overlay-title {
  font-size: 18px;
  font-weight: 600;
  color: var(--p-text-color);
}

/* Control keys are for touch devices, not for narrow windows: they used to
   live entirely inside `@media (max-width: 768px)`, which meant they vanished
   in landscape (844x390) and on iPad -- precisely the short-viewport cases
   where reaching Esc or ^C without a hardware keyboard matters most. Show
   them whenever the primary input is touch, in any orientation or size. */
.control-bar {
  display: none;
  flex-direction: column;
  gap: 4px;
  padding: 2px 0;
  flex: 0 0 auto;
}
.ctl-row {
  display: flex;
  gap: 4px;
  align-items: center;
  flex-wrap: wrap;
}
.ctl {
  background: var(--surface-subtle);
  border: 1px solid var(--p-content-border-color);
  border-radius: 4px;
  padding: 4px 6px;
  font-size: 12px;
  font-family: inherit;
  color: var(--p-text-color);
  cursor: pointer;
  min-width: 28px;
  text-align: center;
  user-select: none;
  flex: 0 0 auto;
  -webkit-tap-highlight-color: transparent;
}
.ctl.sm {
  padding: 4px 5px;
  min-width: 24px;
}
.ctl:active {
  background: var(--p-content-hover-background);
}
/* Pressed state. The modifiers invert fully (they change what the NEXT key
   does, so they need to shout); other toggles just tint, but they must show
   something -- the keyboard key is a real on/off toggle and reports
   aria-pressed, so sighted users need the same signal screen readers get. */
.ctl.active {
  background: var(--p-content-hover-background);
  border-color: var(--p-primary-color);
}
.ctl.mod.active {
  background: var(--p-primary-color);
  color: var(--p-primary-contrast-color);
}
.ctl .pi {
  font-size: 12px;
  vertical-align: middle;
}

/* Touch sizing. Keyed off pointer *capability*, not viewport width, so it
   applies in landscape and on tablets too -- a phone in landscape is 844px
   wide, which any width-based query reads as "desktop".

   `any-pointer: coarse` rather than `pointer: coarse`: the latter describes only
   the *primary* pointer, so a touchscreen laptop or an iPad driven by a
   trackpad reports `pointer: fine` and would lose the keys even though a finger
   is available. `hover: none` has the opposite problem -- it matches any device
   with no hovering pointer, touchscreen or not.

   Must come after the base .ctl rules above: .ctl.sm outranks .ctl, so
   declaring these earlier let the arrows grow while the text keys stayed at
   their content width (in landscape the arrows ballooned to 150px each).

   ~32x34 per key, close to a key on the iOS system keyboard. Compacting the
   chrome must never come out of the tap targets: a mis-tap here sends the
   wrong control code to a live shell. */
@media (any-pointer: coarse) {
  .control-bar {
    display: flex;
  }
  .ctl-row {
    flex-wrap: nowrap;
    justify-content: space-between;
    gap: 3px;
  }
  /* min-height, not just padding: the icon-only keys have a shorter content
     box than the text ones and would otherwise come out a few px smaller.
     max-width keeps a wide screen from stretching eleven keys into slabs.
     min-width is deliberately absent: 11 keys * 26px + 10 gaps + bar padding
     needs 328px, which overflows a 320px iPhone SE and clipped the last key,
     and .term-app is overflow:hidden so it wasn't even scrollable. Let them
     shrink instead; the 34px height carries the tap target. */
  .ctl,
  .ctl.sm,
  .ctl-more {
    flex: 1 1 0;
    min-width: 0;
    max-width: 60px;
    min-height: 34px;
    padding: 6px 2px;
    font-size: 12px;
  }
}

/* Keep every primeicon in the tab/control bars rendering at a single,
   consistent size so the row of buttons looks uniform. */
.tab .pi {
  font-size: 13px;
  line-height: 1;
}
.tab-close .pi {
  font-size: 11px;
}

/* Stage wraps the terminal and the (absolutely-positioned) diagnostics pane
   so the pane overlays the terminal instead of reflowing/resizing the PTY. */
.term-stage {
  flex: 1 1 auto;
  min-height: 0;
  position: relative;
  display: flex;
}
.term {
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  border: 1px solid var(--p-content-border-color);
  border-radius: 6px;
  padding: 8px;
  position: relative;
  background: var(--xterm-bg, var(--p-content-background));
  overflow: hidden;
  /* ghostty-web marks this host contenteditable="true" so it can receive
     beforeinput/IME events, which makes iOS Safari treat it as a text field
     and auto-zoom on focus unless its font-size is >= 16px. Neither backend
     renders text with this size (xterm.js emits its own stylesheet, ghostty
     paints a canvas), so it is purely a zoom guard. */
  font-size: 16px;
  /* Tell the browser up front that it owns no panning here, so WebKit doesn't
     start its own overscroll/rubber-band before our first touchmove arrives.
     Neither backend has a natively-scrollable viewport (ghostty paints a
     canvas; xterm.js 6's viewport is virtual and it scrolls from JS), so there
     is no browser panning to preserve. pinch-zoom is deliberately still
     allowed — the user needs it, and it's what `none` would take away. */
  touch-action: pinch-zoom;
}
.term :deep(.xterm) {
  height: 100%;
}
.term :deep(.xterm-viewport) {
  background-color: var(--xterm-bg) !important;
  overflow-y: auto !important;
}
.term :deep(.xterm-screen) {
  height: 100% !important;
}

/* Stop iOS Safari from zooming the page when the terminal takes focus.
   Mobile Safari magnifies the viewport whenever focus lands on an editable
   element whose font-size is under 16px — and both backends receive input
   through a hidden 1px <textarea> that inherits the app's 13px body font.
   The result: every tap in the terminal zoomed the page to the caret. The
   textareas are clipped and fully transparent, so a 16px font costs nothing
   visually, and this beats `maximum-scale=1` in the viewport meta, which
   would also take real pinch-zoom away from the user. */
.term :deep(textarea),
.term :deep(.xterm-helper-textarea) {
  font-size: 16px !important;
}

.status {
  position: fixed;
  top: 14px;
  right: 14px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  z-index: 10;
  box-shadow: var(--p-overlay-popover-shadow, 0 1px 4px rgba(0, 0, 0, 0.1));
  background: var(--p-content-background);
  color: var(--p-text-color);
  border: 1px solid var(--p-content-border-color);
}
.status .dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--p-text-muted-color);
}
.status.connecting .dot {
  background: var(--p-yellow-500);
  animation: pulse 1.5s ease-in-out infinite alternate;
}
.status.connected .dot {
  background: var(--p-green-500);
}
.status.error .dot {
  background: var(--p-red-500);
  animation: pulse 1s ease-in-out infinite alternate;
}
.status.disconnected {
  cursor: pointer;
}
.status.disconnected .dot {
  background: var(--p-text-muted-color);
}

@keyframes pulse {
  from {
    opacity: 0.4;
  }
  to {
    opacity: 1;
  }
}

/* Compact chrome. Applies to phone-width screens *and* to any touch device
   regardless of width -- a phone in landscape is 844px wide and an iPad is
   wider still, and both were previously getting desktop chrome (16px padding,
   visible title, expanded tabs) on top of a screen with no room for it.

   Deliberately one block rather than a width block plus a height block: the
   earlier split had a later `padding: 0` shorthand silently clobbering the
   left/right safe-area insets set in the first, so a landscape notch overlapped
   the tabs -- the exact thing the padding was added to prevent.

   The touch arm is bounded at 1180px. Touch capability alone is the right gate
   for the control *keys* (a finger is a finger), but not for stripping the
   chrome: any touchscreen laptop reports a coarse pointer, and deleting the
   padding, title and borders on a 1440px window is wrong. 1180px keeps iPad Pro
   landscape (1194) on the desktop layout, where it has the room. */
@media (max-width: 768px), (any-pointer: coarse) and (max-width: 1180px) {
  /* Pad for the notch/home indicator. Individual sides only, never a `padding`
     shorthand, so nothing downstream can reset one of them by accident. */
  .term-app {
    /* viewport-fit=cover (see ui/vite.config.ts) makes the page go edge to edge,
       so the top inset has to be honoured too. Measured on an iPhone 17 Pro
       simulator (iOS 26.5), Safari reports all four insets as 0 in portrait --
       its own toolbars already cover the island and the home indicator -- so
       this is inert there. It earns its keep in the cases where the inset is
       non-zero: home-screen web-app mode, and landscape, where the notch
       intrudes on the side the tab bar occupies. */
    padding-top: env(safe-area-inset-top, 0);
    padding-left: env(safe-area-inset-left, 0);
    padding-right: env(safe-area-inset-right, 0);
    padding-bottom: env(safe-area-inset-bottom, 0);
    height: 100vh;
    height: 100dvh;
    height: var(--vv-height, 100dvh);
    gap: 2px;
  }
  /* Drop the title — the machine name is in window.title and showing it here just
     eats horizontal space the tabs need. */
  .term-title {
    display: none;
  }
  .tab-bar {
    padding: 3px 6px;
    flex-wrap: nowrap;
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
    scrollbar-width: none;
    gap: 4px;
  }
  .tab-bar::-webkit-scrollbar {
    display: none;
  }
  .tab {
    flex: 0 0 auto;
    padding: 2px 6px 2px 8px;
    font-size: 12px;
  }
  /* Phones fit three or four tabs across, like the iOS app. */
  .tab-name {
    width: 14ch;
  }
  .tab-icon {
    width: 24px;
    height: 24px;
  }
  .tab-toggle {
    min-width: 24px;
    height: 24px;
  }
  .control-bar {
    padding: 2px 6px;
  }
  .term {
    border-radius: 0;
    border-left: none;
    border-right: none;
    padding: 2px;
  }
  .status {
    top: auto;
    bottom: 12px;
    right: 12px;
  }

  /* The status pill lives in the bottom-right corner on mobile, just above
     where a banner bar sits. A "reconnecting"/"disconnected" pill is
     load-bearing -- it's the tap target that reconnects -- so the banner
     yields, not the status. The inset has to be repeated here because the bar's height grows
     by it: without it the pill sits *inside* the bar on any notched iPhone,
     and no test on a desktop browser can see that, since Chromium reports
     every env(safe-area-inset-*) as 0. */
  .term-app:has(.term-bottom-bar) .status {
    bottom: calc(76px + var(--safe-bottom));
  }

  /* Keyboard up: the visible viewport is about a third of the screen, so the
     chrome has to give ground -- but take it from the session list, not from
     the keys. Esc/^C and the arrows are exactly what you reach for with the
     keyboard covering the screen, so they keep their full tap target.

     Only the tabs and the new-session button collapse; the tab counter (next
     to the current session name), the settings menu and every control key
     stay, and the ⌨ key toggles the keyboard back off. Hiding the whole bar
     stranded the user with no way back and no idea which session they were in. */
  .term-app.kb-mode .tab-bar:not(.collapsed) .tab:not(.tab-icon),
  .term-app.kb-mode .tab-bar .tab-new {
    display: none;
  }
  .term-app.kb-mode .tab-bar {
    padding: 1px 6px;
  }
  .term-app.kb-mode .tab-icon {
    width: 22px;
    height: 22px;
  }
  .term-app.kb-mode .tab-toggle {
    min-width: 22px;
    height: 22px;
  }
  /* The rule above hides an open list, so don't show the counter pressed. */
  .term-app.kb-mode .tab-toggle[aria-expanded="true"] {
    background: transparent;
    color: var(--p-text-muted-color);
  }
}

/* Short viewport (phone landscape): vertical space is scarcest here, so shave
   the rows a little further. Scoped to touch so a merely short desktop window
   doesn't get phone chrome. */
@media (max-height: 500px) and (any-pointer: coarse) {
  .tab-bar {
    padding: 2px 6px;
  }
  .tab {
    font-size: 11px;
    padding: 2px 6px;
  }
  .tab-icon {
    width: 22px;
    height: 22px;
  }
  .tab-toggle {
    min-width: 22px;
    height: 22px;
  }
  .control-bar {
    padding: 1px 6px;
  }
}

/* Below this there is no room for the optional second row of keys: it is 38px
   of a ~190px visible viewport (landscape with the keyboard up), which starves
   the terminal down to a couple of rows. Since expansion is sticky per host, a
   user who expanded once in portrait would otherwise be stuck with a 1-row
   terminal every time they typed in landscape. The row comes back by itself
   when the keyboard closes; the state is untouched. */
@media (max-height: 420px) {
  .term-app.kb-mode .ctl-row-more {
    display: none;
  }
}
</style>
