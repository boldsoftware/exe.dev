<template>
  <button class="copy-btn" :class="{ copied }" :title="copied ? 'Copied!' : title" :aria-label="copied ? 'Copied!' : (title || 'Copy')" @click.stop="copy">
    <i :class="copied ? 'pi pi-check' : 'pi pi-copy'" style="font-size: 11px;"></i>
  </button>
</template>

<script setup lang="ts">
import { ref } from 'vue'

const props = defineProps<{
  text: string
  title?: string
}>()

const copied = ref(false)

async function copy() {
  try {
    await navigator.clipboard.writeText(props.text)
    copied.value = true
    setTimeout(() => { copied.value = false }, 1500)
  } catch {
    // fallback
    const ta = document.createElement('textarea')
    ta.value = props.text
    ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
    copied.value = true
    setTimeout(() => { copied.value = false }, 1500)
  }
}
</script>

<style scoped>
.copy-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  background: var(--btn-bg);
  border: 1px solid var(--btn-border);
  border-radius: 4px;
  cursor: pointer;
  color: var(--btn-text);
  transition: all 0.15s;
  box-sizing: border-box;
}

.copy-btn:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
}

.copy-btn.copied {
  color: var(--success-color);
  border-color: var(--success-color);
}
</style>
