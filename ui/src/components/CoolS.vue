<template>
  <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 400" :width="size" :height="size">
    <circle cx="200" cy="200" r="200" :fill="bgColor" />
    <g transform="translate(117 10) scale(0.97)">
      <g stroke-linecap="round"><g transform="translate(13.3 97.5) rotate(0 1.4 42.2)"><path d="M1.28 0.48C1.15 14.67,-0.96 71.95,-1.42 86.14M-1.47-1.73C-0.61 11.51,4.65 66.62,4.21 81.75" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(87.6 97.2) rotate(0 1.2 42.4)"><path d="M-1.42 1.14C-1.89 15.33,-1.41 71.93,-1.52 85.6M3-0.71C3.35 12.53,3.95 66.59,4.06 80.91" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(156.3 91) rotate(0 0.7 42.1)"><path d="M-1.52 0.6C-1.62 14.26,-1.97 68.6,-2.04 83.12M2.86-1.55C3.77 12.32,3.09 71.53,3.26 85.73" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(157.7 230.3) rotate(0 0.6 42.9)"><path d="M-2.04-1.88C-2.11 12.64,-2.52 72.91,-1.93 87.72M2.05 3.27C3.01 17.02,3.68 70.97,3.43 84.18" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(12.6 226.7) rotate(0 0.2 44.3)"><path d="M-1.93 2.72C-1.33 17.52,1.37 73.57,1.54 86.96M2.23 1.72C2.77 15.92,1.05 69.12,0.14 83.02" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(82.8 226.6) rotate(0 -1.1 43.1)"><path d="M1.54 1.96C1.7 15.35,-0.76 69.37,-0.93 83.06M-1.07 0.56C-1.19 15.45,-3.69 71.28,-3.67 85.64" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(152.7 311.8) rotate(0 -32.3 34.6)"><path d="M-0.93-1.94C-12.26 9.08,-55.27 56.42,-66.46 68.08M3.76 3.18C-8.04 14.42,-56.04 59.98,-68.41 71.22" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(14.7 308.2) rotate(0 34.1 33.6)"><path d="M0.54-0.92C12.51 10.75,58.76 55.93,70.91 68.03M-2.62-3.88C8.97 8.35,55.58 59.22,68.08 71.13" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(11.3 178.5) rotate(0 35.7 23.4)"><path d="M-1.09-0.97C10.89 7.63,60.55 42.51,72.41 50.67M3.51-3.96C15.2 4,60.24 37.93,70.94 47.11" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(11.3 223.5) rotate(0 13.4 -10.2)"><path d="M1.41 2.67C6.27-1,23.83-19.1,28.07-23M-1.26 1.66C3.24-1.45,19.69-14.92,25.32-19.37" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(13.3 94.5) rotate(0 34.6 -42.2)"><path d="M-0.93 0C9.64-13.89,53.62-66.83,64.85-80.71M3.76-2.46C15.07-15.91,59.99-71.5,70.08-84.48" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(81.3 12.5) rotate(0 36.1 39.1)"><path d="M-2.15 2.29C10.41 14.58,61.78 62.2,74.43 73.73M1.88 1.07C14.1 13.81,60.32 65.18,71.89 77.21" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(88.3 177.5) rotate(0 31.2 22.9)"><path d="M-0.57-0.27C10.92 7.09,55.6 38.04,66.75 46.48M-4.32-2.89C6.87 4.52,51.07 40.67,63.83 48.74" stroke="#fff" stroke-width="14" fill="none"/></g></g>
      <g stroke-linecap="round"><g transform="translate(155.3 174.5) rotate(0 -10.7 13.4)"><path d="M-1.25-2.52C-5.27 2.41,-21.09 24.62,-24.67 29.33M3.26 2.28C0.21 6.4,-14.57 20.81,-19.18 25.04" stroke="#fff" stroke-width="14" fill="none"/></g></g>
    </g>
  </svg>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  name: string
  size?: number
}>()

function hashString(s: string): number {
  let hash = 0
  for (const c of s) {
    hash = ((hash << 5) - hash) + c.charCodeAt(0)
    hash = hash >>> 0 // convert to unsigned 32-bit
  }
  return hash
}

const bgColor = computed(() => {
  // Match the Shelley server's favicon hash key so the icon color is
  // consistent between the VMs list and the Shelley tab itself.
  // See generateFaviconSVG / serveIndexWithInit in shelley/server/handlers.go:
  // the server hashes `<hostname>:<listenPort>` where hostname is os.Hostname()
  // with ".exe.xyz" appended if it has no dots, and listenPort defaults to 9999.
  const key = `${props.name}.exe.xyz:9999`
  const h = hashString(key) % 360
  return `hsl(${h}, 70%, 55%)`
})

const size = computed(() => props.size ?? 14)
</script>
