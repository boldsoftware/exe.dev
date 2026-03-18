<script setup lang="ts">
import { computed, ref } from 'vue'

interface DataPoint {
  timestamp: string
  value: number
}

interface Series {
  name: string
  color: string
  data: DataPoint[]
}

const props = defineProps<{
  series: Series[]
  title?: string
  unit?: string
  maxValue?: number
  warningThreshold?: number
  criticalThreshold?: number
  periodMinutes?: number
  integerValues?: boolean
}>()

const hoveredPoint = ref<{
  seriesIndex: number
  pointIndex: number
  x: number
  y: number
  value: number
  timestamp: string
} | null>(null)

const chartWidth = 600
const chartHeight = 180
const padding = { top: 30, right: 16, bottom: 28, left: 42 }
const innerWidth = chartWidth - padding.left - padding.right
const innerHeight = chartHeight - padding.top - padding.bottom

const computedMaxValue = computed(() => {
  if (props.maxValue !== undefined) return props.maxValue
  let max = 0
  props.series.forEach(s => s.data.forEach(d => {
    if (d.value > max) max = d.value
  }))
  return Math.max(max * 1.1, 10)
})

const yTicks = computed(() => {
  const max = computedMaxValue.value
  if (max <= 0) return [0]
  // Target ~4 ticks. Pick a "nice" step from the 1-2-5 sequence.
  const rough = max / 4
  const mag = Math.pow(10, Math.floor(Math.log10(rough)))
  const residual = rough / mag
  const nice = residual <= 1.5 ? 1 : residual <= 3.5 ? 2 : residual <= 7.5 ? 5 : 10
  const step = nice * mag
  const ticks = []
  for (let i = 0; i <= max + step * 0.01; i += step) {
    ticks.push(parseFloat(i.toFixed(10)))
  }
  return ticks
})

const timeRange = computed(() => {
  const now = new Date()
  if (props.periodMinutes) {
    const start = new Date(now.getTime() - props.periodMinutes * 60 * 1000)
    return { start, end: now }
  }
  const timestamps: string[] = []
  props.series.forEach(s => s.data.forEach(d => timestamps.push(d.timestamp)))
  timestamps.sort()
  if (timestamps.length === 0) return { start: now, end: now }
  return { start: new Date(timestamps[0]), end: new Date(timestamps[timestamps.length - 1]) }
})

const xScale = computed(() => {
  const { start, end } = timeRange.value
  const range = end.getTime() - start.getTime()
  if (range <= 0) return (_ts: string) => innerWidth / 2
  return (timestamp: string) => {
    const t = new Date(timestamp).getTime()
    return ((t - start.getTime()) / range) * innerWidth
  }
})

const yScale = computed(() => {
  return (value: number) => innerHeight - (value / computedMaxValue.value) * innerHeight
})

function getPath(data: DataPoint[]): string {
  if (data.length === 0) return ''
  const sorted = [...data].sort((a, b) =>
    new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
  )
  return sorted.map((d, i) => {
    const x = xScale.value(d.timestamp)
    const y = Math.max(0, Math.min(innerHeight, yScale.value(d.value)))
    return `${i === 0 ? 'M' : 'L'} ${x} ${y}`
  }).join(' ')
}

function getAreaPath(data: DataPoint[]): string {
  if (data.length === 0) return ''
  const sorted = [...data].sort((a, b) =>
    new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
  )
  const points = sorted.map(d => ({
    x: xScale.value(d.timestamp),
    y: Math.max(0, Math.min(innerHeight, yScale.value(d.value)))
  }))
  const line = points.map((p, i) => `${i === 0 ? 'M' : 'L'} ${p.x} ${p.y}`).join(' ')
  return `${line} L ${points[points.length - 1].x} ${innerHeight} L ${points[0].x} ${innerHeight} Z`
}

function formatTime(timestamp: string): string {
  const date = new Date(timestamp)
  const diff = Date.now() - date.getTime()
  if (diff < 86400000) {
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  return date.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

function getXLabels(): { label: string; x: number }[] {
  const { start, end } = timeRange.value
  const range = end.getTime() - start.getTime()
  if (range <= 0) return []
  const count = 5
  const labels = []
  for (let i = 0; i <= count; i++) {
    const time = new Date(start.getTime() + (range * i) / count)
    labels.push({ label: formatTime(time.toISOString()), x: (i / count) * innerWidth })
  }
  return labels
}

function handleMouseMove(event: MouseEvent, seriesIndex: number) {
  const svg = (event.target as SVGElement).closest('svg')
  if (!svg) return
  const rect = svg.getBoundingClientRect()
  const scaleX = chartWidth / rect.width
  const x = (event.clientX - rect.left) * scaleX - padding.left

  const series = props.series[seriesIndex]
  let closestIndex = 0
  let closestDist = Infinity
  series.data.forEach((d, i) => {
    const dist = Math.abs(xScale.value(d.timestamp) - x)
    if (dist < closestDist) { closestDist = dist; closestIndex = i }
  })

  if (closestDist < 30 * scaleX) {
    const point = series.data[closestIndex]
    hoveredPoint.value = {
      seriesIndex, pointIndex: closestIndex,
      x: xScale.value(point.timestamp),
      y: yScale.value(point.value),
      value: point.value, timestamp: point.timestamp
    }
  } else {
    hoveredPoint.value = null
  }
}

const clampedHoverY = computed(() => {
  if (!hoveredPoint.value) return 0
  return Math.max(0, Math.min(innerHeight, hoveredPoint.value.y))
})

const tooltipX = computed(() => {
  if (!hoveredPoint.value) return 0
  const x = hoveredPoint.value.x
  return x > innerWidth - 95 ? x - 105 : x + 10
})

const tooltipY = computed(() => {
  if (!hoveredPoint.value) return 0
  const y = clampedHoverY.value
  return y < 45 ? y + 15 : y - 45
})

function formatValue(v: number): string {
  if (props.integerValues) return Math.round(v).toString()
  return v.toFixed(1)
}

function formatTooltipTime(timestamp: string): string {
  return new Date(timestamp).toLocaleString([], {
    month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit'
  })
}
</script>

<template>
  <div class="chart-wrapper">
    <div v-if="title" class="chart-header">
      <span class="chart-title">{{ title }}</span>
    </div>
    <div class="chart-body">
      <svg
        :viewBox="`0 0 ${chartWidth} ${chartHeight}`"
        class="chart-svg"
        @mouseleave="hoveredPoint = null"
      >
        <g :transform="`translate(${padding.left}, ${padding.top})`">
          <!-- Grid lines -->
          <line
            v-for="tick in yTicks" :key="tick"
            :x1="0" :y1="yScale(tick)" :x2="innerWidth" :y2="yScale(tick)"
            stroke="var(--surface-border)" stroke-opacity="0.4" stroke-dasharray="4,4"
          />

          <!-- Warning threshold -->
          <line v-if="warningThreshold !== undefined"
            :x1="0" :y1="yScale(warningThreshold)" :x2="innerWidth" :y2="yScale(warningThreshold)"
            stroke="var(--yellow-400)" stroke-opacity="0.4" stroke-dasharray="6,3"
          />

          <!-- Critical threshold -->
          <line v-if="criticalThreshold !== undefined"
            :x1="0" :y1="yScale(criticalThreshold)" :x2="innerWidth" :y2="yScale(criticalThreshold)"
            stroke="var(--red-400)" stroke-opacity="0.4" stroke-dasharray="6,3"
          />

          <!-- Area fills -->
          <path
            v-for="(s, i) in series" :key="`area-${i}`"
            :d="getAreaPath(s.data)" :fill="s.color" fill-opacity="0.08"
          />

          <!-- Lines + hit targets -->
          <g v-for="(s, i) in series" :key="`line-${i}`">
            <path :d="getPath(s.data)" stroke="transparent" stroke-width="20" fill="none"
              stroke-linecap="round" stroke-linejoin="round" class="hit-target"
              @mousemove="(e) => handleMouseMove(e, i)"
            />
            <path :d="getPath(s.data)" :stroke="s.color" stroke-width="1.5" fill="none"
              stroke-linecap="round" stroke-linejoin="round" class="line-path"
            />
          </g>

          <!-- Hover -->
          <g v-if="hoveredPoint">
            <line :x1="hoveredPoint.x" :y1="0" :x2="hoveredPoint.x" :y2="innerHeight"
              stroke="var(--text-color-muted)" stroke-opacity="0.4" stroke-dasharray="3,3"
            />
            <circle :cx="hoveredPoint.x" :cy="clampedHoverY" r="5"
              :fill="series[hoveredPoint.seriesIndex].color" stroke="var(--surface-card)" stroke-width="2"
            />
            <rect :x="tooltipX" :y="tooltipY" width="95" height="36" rx="3"
              fill="var(--surface-overlay)" stroke="var(--surface-border)" stroke-width="1"
            />
            <text :x="tooltipX + 47" :y="tooltipY + 14" text-anchor="middle"
              font-size="10" font-weight="600" :fill="series[hoveredPoint.seriesIndex].color"
            >{{ formatValue(hoveredPoint.value) }}{{ unit ?? '%' }}</text>
            <text :x="tooltipX + 47" :y="tooltipY + 28" text-anchor="middle"
              font-size="7" fill="var(--text-color-muted)"
            >{{ formatTooltipTime(hoveredPoint.timestamp) }}</text>
          </g>

          <!-- Y labels -->
          <text v-for="tick in yTicks" :key="`y-${tick}`"
            :x="-8" :y="yScale(tick)" text-anchor="end" dominant-baseline="middle"
            font-size="8" fill="var(--text-color-muted)"
          >{{ formatValue(tick) }}{{ unit ?? '%' }}</text>

          <!-- X labels -->
          <text v-for="(label, i) in getXLabels()" :key="`x-${i}`"
            :x="label.x" :y="innerHeight + 16" text-anchor="middle"
            font-size="8" fill="var(--text-color-muted)"
          >{{ label.label }}</text>
        </g>
      </svg>

      <!-- Legend -->
      <div v-if="series.length > 1" class="chart-legend">
        <div v-for="s in series" :key="s.name" class="legend-item">
          <span class="legend-dot" :style="{ background: s.color }"></span>
          <span>{{ s.name }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.chart-wrapper {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  overflow: hidden;
}

.chart-header {
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--surface-border);
}

.chart-title {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-color-secondary);
}

.chart-body {
  padding: 0.75rem 1rem 0.5rem;
}

.chart-svg {
  width: 100%;
  height: auto;
  display: block;
  font-family: 'JetBrains Mono', monospace;
}

.hit-target {
  cursor: crosshair;
}

.line-path {
  pointer-events: none;
}

.chart-legend {
  display: flex;
  gap: 1rem;
  padding-top: 0.5rem;
}

.legend-item {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  font-size: 0.7rem;
  color: var(--text-color-secondary);
}

.legend-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
</style>
