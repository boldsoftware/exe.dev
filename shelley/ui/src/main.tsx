import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { initializeTheme } from "./services/theme";

// Apply theme before render to avoid flash
initializeTheme();

// Render main app
const rootContainer = document.getElementById("root");
if (!rootContainer) throw new Error("Root container not found");

const root = createRoot(rootContainer);
root.render(<App />);
