import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";

// Render main app
const rootContainer = document.getElementById("root");
if (!rootContainer) throw new Error("Root container not found");

const root = createRoot(rootContainer);
root.render(<App />);
