import React from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import LogPanel from './components/LogPanel';

// Render main app
const rootContainer = document.getElementById('root');
if (!rootContainer) throw new Error('Root container not found');

const root = createRoot(rootContainer);
root.render(<App />);

// Initialize log panel for desktop
const logContent = document.getElementById('log-content');
if (logContent) {
  const logRoot = createRoot(logContent);
  logRoot.render(<LogPanel />);
}