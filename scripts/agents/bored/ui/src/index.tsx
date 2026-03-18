import React from "react";
import { createRoot } from "react-dom/client";
import { WorkerPoolContextProvider } from "@pierre/diffs/react";
import App from "./App";

const root = createRoot(document.getElementById("root")!);

root.render(
  <WorkerPoolContextProvider
    poolOptions={{ workerFactory: () => new Worker("/diffs-worker.js") }}
    highlighterOptions={{ langs: [] }}
  >
    <App />
  </WorkerPoolContextProvider>
);
