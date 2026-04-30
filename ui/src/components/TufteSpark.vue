<template>
  <svg
    class="tufte-spark"
    :width="width"
    :height="height"
    :viewBox="`0 0 ${width} ${height}`"
  >
    <polyline :points="line" class="spark-line" :style="{ stroke: color }" />
    <circle
      v-if="endPt"
      :cx="endPt.x"
      :cy="endPt.y"
      r="2.5"
      :fill="color"
    />
  </svg>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    values: number[]
    color?: string
    scaleMax?: number
    width?: number
    height?: number
  }>(),
  {
    color: '#888',
    width: 80,
    height: 16,
  },
)

interface Pt {
  x: number
  y: number
}

const pad = 3 // room for end dot

const yMax = computed(() => {
  if (props.scaleMax != null && props.scaleMax > 0) return props.scaleMax
  const m = Math.max(...props.values, 0)
  return m > 0 ? m : 1
})

function toY(v: number): number {
  const drawH = props.height - pad * 2
  return pad + drawH - (Math.max(v, 0) / yMax.value) * drawH
}

function toX(i: number): number {
  const n = props.values.length
  if (n <= 1) return props.width / 2
  return (i / (n - 1)) * (props.width - pad) // leave room for end dot radius
}

const coords = computed<Pt[]>(() =>
  props.values.map((v, i) => ({ x: toX(i), y: toY(v) })),
)

const line = computed(() =>
  coords.value.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' '),
)

const endPt = computed<Pt | null>(() => {
  if (coords.value.length === 0) return null
  return coords.value[coords.value.length - 1]
})
</script>

<style scoped>
.tufte-spark {
  display: block;
  flex: 1 1 0;
  min-width: 20px;
  max-width: 80px;
  overflow: visible;
}
.spark-line {
  fill: none;
  stroke-width: 1.5;
}
</style>
