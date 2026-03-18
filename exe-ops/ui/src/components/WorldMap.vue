<template>
  <div class="world-map-container">
    <!-- Zoom out button -->
    <button
      v-if="zoomedRegion"
      class="zoom-out-btn"
      @click="zoomOut"
    >
      <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
        <path d="M6.5 12a5.5 5.5 0 1 0 0-11 5.5 5.5 0 0 0 0 11zM13 6.5a6.5 6.5 0 1 1-13 0 6.5 6.5 0 0 1 13 0z"/>
        <path d="M10.344 11.742a.5.5 0 0 1 .028-.03l3.861 3.861a.5.5 0 0 0 .708-.708l-3.861-3.861a.5.5 0 0 1 .03-.028 6.5 6.5 0 1 0-.766.766zM3.5 7h6a.5.5 0 0 1 0 1h-6a.5.5 0 0 1 0-1z"/>
      </svg>
      Zoom out
    </button>

    <svg
      :viewBox="currentViewBox"
      preserveAspectRatio="xMidYMid meet"
      class="world-map"
      :class="{ zooming: isAnimating }"
      @click.self="handleBackgroundClick"
    >
      <!-- Ocean background -->
      <rect x="-180" y="-72" width="360" height="132" class="ocean" @click="handleBackgroundClick" />

      <!-- Graticule grid lines -->
      <g class="graticule">
        <line v-for="lon in longitudes" :key="'lon'+lon"
          :x1="lon" y1="-72" :x2="lon" y2="60" />
        <line v-for="lat in latitudes" :key="'lat'+lat"
          x1="-180" :y1="lat" x2="180" :y2="lat" />
        <!-- Equator (slightly more visible) -->
        <line x1="-180" y1="0" x2="180" y2="0" class="equator" />
        <!-- Prime meridian -->
        <line x1="0" y1="-72" x2="0" y2="60" class="prime-meridian" />
      </g>

      <!-- Landmass fills -->
      <path
        v-for="(d, i) in LAND_PATHS"
        :key="'land-'+i"
        :d="d"
        class="landmass"
      />

      <!-- Country borders -->
      <path
        v-for="(d, i) in BORDER_PATHS"
        :key="'border-'+i"
        :d="d"
        class="country-border"
      />

      <!-- Region dots (overview mode) -->
      <g
        v-for="r in regions"
        :key="r.code"
        class="region-dot"
        :class="{
          selected: selectedRegion === r.code,
          hidden: zoomedRegion !== null && zoomedRegion !== r.code,
          'zoom-origin': zoomedRegion === r.code,
        }"
        @click.stop="handleRegionClick(r.code)"
        style="cursor: pointer;"
      >
        <title>{{ r.code.toUpperCase() }} ({{ r.info.display }}) — {{ r.total }} server{{ r.total !== 1 ? 's' : '' }} ({{ r.online }} online)</title>

        <!-- Pulse ring for online servers -->
        <circle
          v-if="r.online > 0 && zoomedRegion !== r.code"
          :cx="r.info.x"
          :cy="r.info.y"
          r="5"
          :class="['pulse-ring', 'pulse-' + dotStatus(r)]"
        />

        <!-- Drop shadow -->
        <circle
          v-if="zoomedRegion !== r.code"
          :cx="r.info.x"
          :cy="r.info.y"
          :r="dotRadius(r.total) + 1.5"
          class="dot-shadow"
        />

        <!-- Main dot -->
        <circle
          v-if="zoomedRegion !== r.code"
          :cx="r.info.x"
          :cy="r.info.y"
          :r="dotRadius(r.total)"
          :class="'dot-' + dotStatus(r)"
        />

        <!-- Count label -->
        <text
          v-if="zoomedRegion !== r.code"
          :x="r.info.x"
          :y="r.info.y + 0.8"
          class="dot-label"
        >{{ r.total }}</text>
      </g>

      <!-- Individual server dots (zoomed mode) -->
      <g v-if="zoomedRegion" class="server-dots">
        <g
          v-for="(s, i) in zoomedServers"
          :key="s.name"
          class="server-dot"
          @click.stop="router.push({ name: 'server-details', params: { name: s.name } })"
          style="cursor: pointer;"
        >
          <title>{{ s.name }} — {{ serverOnline(s) ? 'online' : 'offline' }}</title>

          <!-- Pulse ring for online servers -->
          <circle
            v-if="serverOnline(s)"
            :cx="serverPosition(i).x"
            :cy="serverPosition(i).y"
            r="2"
            class="pulse-ring"
            :class="'pulse-' + serverStatus(s)"
          />

          <!-- Drop shadow -->
          <circle
            :cx="serverPosition(i).x"
            :cy="serverPosition(i).y"
            r="2.5"
            class="dot-shadow"
          />

          <!-- Main dot -->
          <circle
            :cx="serverPosition(i).x"
            :cy="serverPosition(i).y"
            r="1.8"
            :class="'dot-' + serverStatus(s)"
          />

          <!-- Server name label -->
          <text
            :x="serverPosition(i).x"
            :y="serverPosition(i).y + labelOffset(i).y"
            class="server-label"
            :text-anchor="labelOffset(i).anchor"
          >{{ s.name }}</text>
        </g>

        <!-- Region label -->
        <text
          v-if="zoomedRegionInfo"
          :x="zoomedRegionInfo.x"
          :y="zoomedRegionInfo.y - 12"
          class="region-label"
        >{{ zoomedRegion.toUpperCase() }}</text>
      </g>
    </svg>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import type { ServerSummary } from '../api/client'
import { LAND_PATHS, BORDER_PATHS, regionInfo } from './worldMapPaths'

const router = useRouter()

const props = defineProps<{
  servers: ServerSummary[]
  selectedRegion: string | null
}>()

const emit = defineEmits<{
  'select-region': [code: string | null]
}>()

// Graticule positions
const longitudes = [-150, -120, -90, -60, -30, 30, 60, 90, 120, 150]
const latitudes = [-60, -30, 30, 60]

// Full map viewBox
const FULL_VIEW = '-180 -72 360 132'

const zoomedRegion = ref<string | null>(null)
const currentViewBox = ref(FULL_VIEW)
const isAnimating = ref(false)

function normalizeRegion(region: string): string {
  return region.replace(/\d+$/, '')
}

function isOnline(s: ServerSummary): boolean {
  return Date.now() - new Date(s.last_seen).getTime() < 120_000
}

function serverOnline(s: ServerSummary): boolean {
  return isOnline(s)
}

function serverStatus(s: ServerSummary): string {
  return isOnline(s) ? 'online' : 'offline'
}

interface RegionGroup {
  code: string
  info: { x: number; y: number; display: string }
  total: number
  online: number
}

const regions = computed<RegionGroup[]>(() => {
  const map = new Map<string, { total: number; online: number }>()
  for (const s of props.servers) {
    const code = normalizeRegion(s.region)
    const entry = map.get(code) ?? { total: 0, online: 0 }
    entry.total++
    if (isOnline(s)) entry.online++
    map.set(code, entry)
  }
  return Array.from(map.entries()).map(([code, counts]) => ({
    code,
    info: regionInfo(code),
    ...counts,
  }))
})

const zoomedRegionInfo = computed(() => {
  if (!zoomedRegion.value) return null
  return regionInfo(zoomedRegion.value)
})

const zoomedServers = computed(() => {
  if (!zoomedRegion.value) return []
  return props.servers.filter(
    s => normalizeRegion(s.region) === zoomedRegion.value
  )
})

function serverPosition(index: number): { x: number; y: number } {
  const info = zoomedRegionInfo.value
  if (!info) return { x: 0, y: 0 }

  const count = zoomedServers.value.length
  if (count === 1) return { x: info.x, y: info.y }

  // Spread servers in concentric rings around the region center
  const ringCapacity = 8
  const ring = Math.floor(index / ringCapacity)
  const posInRing = index % ringCapacity
  const ringCount = Math.min(count - ring * ringCapacity, ringCapacity)
  const radius = 5 + ring * 5
  const angle = (2 * Math.PI * posInRing) / ringCount - Math.PI / 2

  return {
    x: info.x + radius * Math.cos(angle),
    y: info.y + radius * Math.sin(angle),
  }
}

function labelOffset(index: number): { y: number; anchor: string } {
  const count = zoomedServers.value.length
  if (count === 1) return { y: 3.5, anchor: 'middle' }

  const ringCapacity = 8
  const posInRing = index % ringCapacity
  const ringCount = Math.min(count - Math.floor(index / ringCapacity) * ringCapacity, ringCapacity)
  const angle = (2 * Math.PI * posInRing) / ringCount - Math.PI / 2

  // Place label on the outward side of the dot relative to center
  const dy = Math.sin(angle)
  const dx = Math.cos(angle)

  let y: number
  if (dy > 0.3) y = 3.5
  else if (dy < -0.3) y = -3
  else y = 0.5

  let anchor: string
  if (dx > 0.3) anchor = 'start'
  else if (dx < -0.3) anchor = 'end'
  else anchor = 'middle'

  return { y, anchor }
}

function dotStatus(r: RegionGroup): string {
  if (r.online === 0) return 'offline'
  if (r.online === r.total) return 'online'
  if (r.online / r.total > 0.5) return 'warn'
  return 'critical'
}

function dotRadius(total: number): number {
  if (total <= 1) return 3
  if (total <= 5) return 4
  return 5
}

function animateViewBox(target: string, duration: number) {
  const parse = (vb: string) => vb.split(' ').map(Number)
  const from = parse(currentViewBox.value)
  const to = parse(target)
  const start = performance.now()
  isAnimating.value = true

  function step(now: number) {
    const t = Math.min((now - start) / duration, 1)
    // ease-in-out cubic
    const ease = t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2
    const vb = from.map((f, i) => f + (to[i] - f) * ease)
    currentViewBox.value = vb.join(' ')
    if (t < 1) {
      requestAnimationFrame(step)
    } else {
      currentViewBox.value = target
      isAnimating.value = false
    }
  }
  requestAnimationFrame(step)
}

function zoomToRegion(code: string) {
  const info = regionInfo(code)
  // Zoom to a ~60x40 area centered on the region
  const w = 60
  const h = 40
  const target = `${info.x - w / 2} ${info.y - h / 2} ${w} ${h}`
  zoomedRegion.value = code
  animateViewBox(target, 400)
  emit('select-region', code)
}

function zoomOut() {
  zoomedRegion.value = null
  animateViewBox(FULL_VIEW, 400)
  emit('select-region', null)
}

function handleRegionClick(code: string) {
  if (zoomedRegion.value === code) {
    // Already zoomed into this region, toggle selection
    emit('select-region', props.selectedRegion === code ? null : code)
    return
  }
  zoomToRegion(code)
}

function handleBackgroundClick() {
  if (zoomedRegion.value) {
    zoomOut()
  } else {
    emit('select-region', null)
  }
}
</script>

<style scoped>
.world-map-container {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 0.75rem 1rem;
  margin-bottom: 1.5rem;
  overflow: hidden;
  position: relative;
}

.world-map {
  width: 100%;
  aspect-ratio: 360 / 132;
  display: block;
}

/* Zoom out button */
.zoom-out-btn {
  position: absolute;
  top: 0.75rem;
  left: 1rem;
  display: flex;
  align-items: center;
  gap: 0.375rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  color: var(--text-color-muted);
  font-size: 0.7rem;
  font-family: inherit;
  padding: 0.25rem 0.5rem;
  border-radius: 3px;
  cursor: pointer;
  z-index: 2;
  transition: color 0.15s, border-color 0.15s;
}

.zoom-out-btn:hover {
  color: var(--primary-color);
  border-color: var(--primary-color);
}

/* Ocean */
.ocean {
  fill: #080c10;
}

/* Graticule grid */
.graticule line {
  stroke: rgba(72, 209, 204, 0.04);
  stroke-width: 0.25;
}

.graticule .equator,
.graticule .prime-meridian {
  stroke: rgba(72, 209, 204, 0.07);
  stroke-width: 0.3;
}

/* Land fill */
.landmass {
  fill: #1c2128;
  stroke: #30363d;
  stroke-width: 0.25;
  stroke-linejoin: round;
}

/* Country borders */
.country-border {
  fill: none;
  stroke: #080c10;
  stroke-width: 0.2;
  stroke-linejoin: round;
  stroke-linecap: round;
}

/* Dot shadow */
.dot-shadow {
  fill: rgba(0, 0, 0, 0.4);
}

.dot-online {
  fill: #48d1cc;
}

.dot-warn {
  fill: #d89b00;
}

.dot-critical {
  fill: #da3633;
}

.dot-offline {
  fill: #6e7681;
}

.dot-label {
  fill: #fff;
  font-size: 3px;
  font-weight: 700;
  text-anchor: middle;
  dominant-baseline: middle;
  pointer-events: none;
}

/* Region label in zoomed view */
.region-label {
  fill: var(--primary-color, #48d1cc);
  font-size: 3px;
  font-weight: 600;
  text-anchor: middle;
  dominant-baseline: auto;
  pointer-events: none;
  text-transform: uppercase;
  letter-spacing: 0.4px;
}

/* Server name label */
.server-label {
  fill: var(--text-color-muted, #8b949e);
  font-size: 1.6px;
  dominant-baseline: auto;
  pointer-events: none;
}

@media (max-width: 1024px) {
  .region-label {
    font-size: 3.5px;
  }
  .server-label {
    font-size: 1.8px;
  }
}

@media (max-width: 768px) {
  .region-label {
    font-size: 4px;
  }
  .server-label {
    font-size: 2px;
  }
}

@media (max-width: 480px) {
  .region-label {
    font-size: 5px;
  }
  .server-label {
    font-size: 2.5px;
  }
}

.pulse-ring {
  fill: none;
  stroke-width: 0.6;
  opacity: 0;
  animation: pulse 2.5s ease-out infinite;
}

.pulse-online {
  stroke: #48d1cc;
}

.pulse-warn {
  stroke: #d89b00;
}

.pulse-critical {
  stroke: #da3633;
}

.region-dot.selected .dot-online,
.region-dot.selected .dot-warn,
.region-dot.selected .dot-critical,
.region-dot.selected .dot-offline {
  stroke: #fff;
  stroke-width: 1.2;
}

.region-dot.selected .dot-shadow {
  fill: rgba(72, 209, 204, 0.3);
}

.region-dot:hover .dot-online {
  fill: #7ee8e4;
}

.region-dot:hover .dot-warn {
  fill: #e8b230;
}

.region-dot:hover .dot-critical {
  fill: #e55a57;
}

.region-dot:hover .dot-offline {
  fill: #8b949e;
}

/* Hide non-zoomed regions during zoom */
.region-dot.hidden {
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.3s ease;
}

.region-dot.zoom-origin {
  pointer-events: none;
}

/* Server dot hover effects */
.server-dot:hover .dot-online {
  fill: #7ee8e4;
}

.server-dot:hover .dot-offline {
  fill: #8b949e;
}

.server-dot:hover .server-label {
  fill: #c9d1d9;
}

@keyframes pulse {
  0% {
    r: 3;
    opacity: 0.6;
  }
  100% {
    r: 10;
    opacity: 0;
  }
}


</style>

<style>
/* ── Light mode (unscoped to avoid scoping conflicts) ── */
.light-mode .ocean {
  fill: #a8b1bb;
}

.light-mode .landmass {
  fill: #2d333b;
  stroke: #444c56;
}

.light-mode .country-border {
  stroke: #1c2128;
}

.light-mode .graticule line {
  stroke: rgba(15, 150, 144, 0.08);
}

.light-mode .graticule .equator,
.light-mode .graticule .prime-meridian {
  stroke: rgba(15, 150, 144, 0.14);
}

.light-mode .dot-shadow {
  fill: rgba(0, 0, 0, 0.15);
}

.light-mode .dot-label {
  fill: #fff;
}

.light-mode .region-dot.selected .dot-online,
.light-mode .region-dot.selected .dot-warn,
.light-mode .region-dot.selected .dot-critical,
.light-mode .region-dot.selected .dot-offline {
  stroke: #1f2328;
}

.light-mode .region-dot.selected .dot-shadow {
  fill: rgba(15, 150, 144, 0.2);
}

.light-mode .server-dot:hover .server-label {
  fill: #1f2328;
}
</style>
