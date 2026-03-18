import { createApp } from 'vue'
import PrimeVue from 'primevue/config'
import Aura from '@primevue/themes/aura'
import { definePreset } from '@primevue/themes'
import App from './App.vue'
import router from './router'
import 'primeicons/primeicons.css'

const Apollo = definePreset(Aura, {
  semantic: {
    primary: {
      50: '{teal.50}',
      100: '{teal.100}',
      200: '{teal.200}',
      300: '{teal.300}',
      400: '{teal.400}',
      500: '{teal.500}',
      600: '{teal.600}',
      700: '{teal.700}',
      800: '{teal.800}',
      900: '{teal.900}',
      950: '{teal.950}',
    },
    colorScheme: {
      dark: {
        surface: {
          0: '#d0f0e0',
          50: '#b0e0c8',
          100: '#7fbfa8',
          200: '#5a9a80',
          300: '#4a8a70',
          400: '#3a7a60',
          500: '#2a5a48',
          600: '#1f3f30',
          700: '#1a2f24',
          800: '#1a1a1a',
          900: '#0a0a0a',
          950: '#000000',
        },
      },
    },
  },
})

const app = createApp(App)
app.use(PrimeVue, {
  theme: {
    preset: Apollo,
    options: {
      darkModeSelector: '.dark-mode',
    },
  },
})
app.use(router)
app.mount('#app')
