import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { initializeTheme } from "./services/theme";
import { initializeNotifications } from "./services/notifications";

// Apply theme before render to avoid flash
initializeTheme();

// Initialize notification system (includes favicon)
initializeNotifications();

// Render main app
const rootContainer = document.getElementById("root");
if (!rootContainer) throw new Error("Root container not found");

const root = createRoot(rootContainer);
root.render(<App />);
