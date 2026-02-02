// Web Worker for @pierre/diffs syntax highlighting
// This offloads tokenization to background threads for better performance
// Note: This file is built as IIFE and runs in a Worker context
//
// We import and reference to prevent tree-shaking - the worker.js file
// sets up self.addEventListener("message", ...) which is a side effect
import * as diffsWorker from "@pierre/diffs/worker/worker.js";

// Prevent tree-shaking by referencing the import
// The worker module registers message handlers as a side effect
(globalThis as unknown as { __diffsWorker: unknown }).__diffsWorker = diffsWorker;
