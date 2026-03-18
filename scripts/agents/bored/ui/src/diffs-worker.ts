// Web Worker for @pierre/diffs syntax highlighting
// Built as IIFE and runs in a Worker context
import * as diffsWorker from "@pierre/diffs/worker/worker.js";

// Prevent tree-shaking — the worker module registers message handlers as a side effect
(globalThis as unknown as { __diffsWorker: unknown }).__diffsWorker = diffsWorker;
