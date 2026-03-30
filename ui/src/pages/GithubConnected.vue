<template>
  <div class="page">
    <div class="swim-container">
      <img :src="'/github-mark.svg'" alt="GitHub" class="swimmer github-mark light-only">
      <img :src="'/github-mark-white.svg'" alt="GitHub" class="swimmer github-mark dark-only">
      <img :src="'/exy.png'" alt="Exy" class="swimmer exy">
    </div>
    <main class="page-content mt-8">
      <p class="heading mb-6">CONNECTED</p>
      <p class="subtitle">{{ page.gitHubLogin }} · return to your terminal</p>
    </main>
  </div>
</template>

<script setup lang="ts">
import { pageData } from './simple'

interface PageData {
  gitHubLogin: string
}

const page = pageData<PageData>()
</script>

<style scoped>
.page {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 48px;
  overflow: hidden;
}

.page-content {
  width: 100%;
  max-width: 42rem;
  text-align: center;
}

.heading {
  font-size: 3.75rem;
  font-weight: 600;
  letter-spacing: 0.1em;
  line-height: 1;
}

.subtitle {
  color: var(--text-color-secondary);
}

.mt-8 { margin-top: 2rem; }
.mb-6 { margin-bottom: 1.5rem; }

/* Swimming animation */
.swim-container {
  position: relative;
  width: 100%;
  max-width: 480px;
  height: 140px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.swimmer {
  position: absolute;
  height: 100px;
  width: auto;
  animation-duration: 1.8s;
  animation-timing-function: cubic-bezier(0.25, 0.46, 0.45, 0.94);
  animation-fill-mode: forwards;
}

.swimmer.github-mark {
  left: 0;
  height: 80px;
  animation: swim-from-left 1.8s cubic-bezier(0.25, 0.46, 0.45, 0.94) forwards,
             bob-left 3s ease-in-out 1.8s infinite;
}

.swimmer.exy {
  right: 0;
  height: 100px;
  animation: swim-from-right 1.8s cubic-bezier(0.25, 0.46, 0.45, 0.94) forwards,
             bob-right 3s ease-in-out 1.8s infinite;
}

/* Dark/light mode image switching */
.light-only { display: block; }
.dark-only { display: none; }

@media (prefers-color-scheme: dark) {
  .dark-only { display: block; }
  .light-only { display: none; }
}

@keyframes swim-from-left {
  0%   { transform: translateX(-120vw) translateY(0px); }
  20%  { transform: translateX(-80vw) translateY(-6px); }
  40%  { transform: translateX(-40vw) translateY(4px); }
  60%  { transform: translateX(-10vw) translateY(-4px); }
  80%  { transform: translateX(10px) translateY(3px); }
  100% { transform: translateX(30px) translateY(0px); }
}

@keyframes swim-from-right {
  0%   { transform: translateX(120vw) translateY(0px); }
  20%  { transform: translateX(80vw) translateY(5px); }
  40%  { transform: translateX(40vw) translateY(-5px); }
  60%  { transform: translateX(10vw) translateY(4px); }
  80%  { transform: translateX(-10px) translateY(-3px); }
  100% { transform: translateX(-30px) translateY(0px); }
}

@keyframes bob-left {
  0%, 100% { transform: translateX(30px) translateY(0px); }
  50%      { transform: translateX(30px) translateY(-6px); }
}

@keyframes bob-right {
  0%, 100% { transform: translateX(-30px) translateY(0px); }
  50%      { transform: translateX(-30px) translateY(-6px); }
}

@media (max-width: 640px) {
  .page { padding: 24px; }
  .heading { font-size: 2.25rem; }
}
</style>
