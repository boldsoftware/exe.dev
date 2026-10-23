// Entry for the standalone exe-tty page.
import { createApp } from "vue";
import PrimeVue from "primevue/config";
import Aura from "@primevue/themes/aura";
import ToastService from "primevue/toastservice";
import "@fontsource/jetbrains-mono/400.css";
import "@fontsource/jetbrains-mono/600.css";
import "primeicons/primeicons.css";
import "./tokens.css";
import { applyDocumentTheme, effectiveTheme, loadThemeMode } from "./lib/themeMode";
import Terminal from "./Terminal.vue";

// Apply the saved theme before mounting so there is no flash of the system
// theme; Terminal.vue keeps the class in sync afterwards (same key).
applyDocumentTheme(effectiveTheme(loadThemeMode(`xterm:${location.hostname}:themeMode`)));

const app = createApp(Terminal);
app.use(PrimeVue, { theme: { preset: Aura, options: { darkModeSelector: ".app-dark" } } });
app.use(ToastService);
app.mount("#app");
