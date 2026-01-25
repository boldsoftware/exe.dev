import React, { useEffect, useRef, useState, useCallback } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

interface TerminalWidgetProps {
  command: string;
  cwd: string;
  onInsertIntoInput?: (text: string) => void;
  onClose?: () => void;
}

// Theme colors for xterm.js
function getTerminalTheme(isDark: boolean): Record<string, string> {
  if (isDark) {
    return {
      background: "#1a1b26",
      foreground: "#c0caf5",
      cursor: "#c0caf5",
      cursorAccent: "#1a1b26",
      selectionBackground: "#364a82",
      selectionForeground: "#c0caf5",
      black: "#32344a",
      red: "#f7768e",
      green: "#9ece6a",
      yellow: "#e0af68",
      blue: "#7aa2f7",
      magenta: "#ad8ee6",
      cyan: "#449dab",
      white: "#9699a8",
      brightBlack: "#444b6a",
      brightRed: "#ff7a93",
      brightGreen: "#b9f27c",
      brightYellow: "#ff9e64",
      brightBlue: "#7da6ff",
      brightMagenta: "#bb9af7",
      brightCyan: "#0db9d7",
      brightWhite: "#acb0d0",
    };
  }
  // Light theme
  return {
    background: "#f8f9fa",
    foreground: "#383a42",
    cursor: "#526eff",
    cursorAccent: "#f8f9fa",
    selectionBackground: "#bfceff",
    selectionForeground: "#383a42",
    black: "#383a42",
    red: "#e45649",
    green: "#50a14f",
    yellow: "#c18401",
    blue: "#4078f2",
    magenta: "#a626a4",
    cyan: "#0184bc",
    white: "#a0a1a7",
    brightBlack: "#4f525e",
    brightRed: "#e06c75",
    brightGreen: "#98c379",
    brightYellow: "#e5c07b",
    brightBlue: "#61afef",
    brightMagenta: "#c678dd",
    brightCyan: "#56b6c2",
    brightWhite: "#ffffff",
  };
}

// Reusable icon button component matching MessageActionBar style
function ActionButton({
  onClick,
  title,
  children,
  feedback,
}: {
  onClick: () => void;
  title: string;
  children: React.ReactNode;
  feedback?: boolean;
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        width: "24px",
        height: "24px",
        borderRadius: "4px",
        border: "none",
        background: feedback ? "var(--success-bg)" : "transparent",
        cursor: "pointer",
        color: feedback ? "var(--success-text)" : "var(--text-secondary)",
        transition: "background-color 0.15s, color 0.15s",
      }}
      onMouseEnter={(e) => {
        if (!feedback) {
          e.currentTarget.style.backgroundColor = "var(--bg-tertiary)";
        }
      }}
      onMouseLeave={(e) => {
        if (!feedback) {
          e.currentTarget.style.backgroundColor = "transparent";
        }
      }}
    >
      {children}
    </button>
  );
}

// SVG icons
const CopyIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
    <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
  </svg>
);

const CheckIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <polyline points="20 6 9 17 4 12" />
  </svg>
);

const InsertIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <path d="M12 3v12" />
    <path d="m8 11 4 4 4-4" />
    <path d="M4 21h16" />
  </svg>
);

const CloseIcon = () => (
  <svg
    width="14"
    height="14"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <line x1="18" y1="6" x2="6" y2="18" />
    <line x1="6" y1="6" x2="18" y2="18" />
  </svg>
);

export default function TerminalWidget({
  command,
  cwd,
  onInsertIntoInput,
  onClose,
}: TerminalWidgetProps) {
  const terminalRef = useRef<HTMLDivElement>(null);
  const xtermRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [status, setStatus] = useState<"connecting" | "running" | "exited" | "error">("connecting");
  const [exitCode, setExitCode] = useState<number | null>(null);
  const [height, setHeight] = useState(300);
  const [autoSized, setAutoSized] = useState(false);
  const isResizingRef = useRef(false);
  const startYRef = useRef(0);
  const startHeightRef = useRef(0);
  const lineCountRef = useRef(0);
  const [copyFeedback, setCopyFeedback] = useState<string | null>(null);

  // Detect dark mode
  const isDarkMode = () => {
    return document.documentElement.getAttribute("data-theme") === "dark";
  };

  const [isDark, setIsDark] = useState(isDarkMode);

  // Watch for theme changes
  useEffect(() => {
    const observer = new MutationObserver(() => {
      const newIsDark = isDarkMode();
      setIsDark(newIsDark);
      if (xtermRef.current) {
        xtermRef.current.options.theme = getTerminalTheme(newIsDark);
      }
    });

    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });

    return () => observer.disconnect();
  }, []);

  // Show copy feedback briefly
  const showFeedback = useCallback((type: string) => {
    setCopyFeedback(type);
    setTimeout(() => setCopyFeedback(null), 1500);
  }, []);

  // Copy screen content (visible area)
  const copyScreen = useCallback(() => {
    if (!xtermRef.current) return;
    const term = xtermRef.current;
    const lines: string[] = [];
    const buffer = term.buffer.active;
    const startRow = buffer.viewportY;
    for (let i = 0; i < term.rows; i++) {
      const line = buffer.getLine(startRow + i);
      if (line) {
        lines.push(line.translateToString(true));
      }
    }
    const text = lines.join("\n").trimEnd();
    navigator.clipboard.writeText(text);
    showFeedback("copyScreen");
  }, [showFeedback]);

  // Copy scrollback buffer (entire history)
  const copyScrollback = useCallback(() => {
    if (!xtermRef.current) return;
    const term = xtermRef.current;
    const lines: string[] = [];
    const buffer = term.buffer.active;
    for (let i = 0; i < buffer.length; i++) {
      const line = buffer.getLine(i);
      if (line) {
        lines.push(line.translateToString(true));
      }
    }
    const text = lines.join("\n").trimEnd();
    navigator.clipboard.writeText(text);
    showFeedback("copyAll");
  }, [showFeedback]);

  // Insert into input
  const handleInsertScreen = useCallback(() => {
    if (!xtermRef.current || !onInsertIntoInput) return;
    const term = xtermRef.current;
    const lines: string[] = [];
    const buffer = term.buffer.active;
    const startRow = buffer.viewportY;
    for (let i = 0; i < term.rows; i++) {
      const line = buffer.getLine(startRow + i);
      if (line) {
        lines.push(line.translateToString(true));
      }
    }
    const text = lines.join("\n").trimEnd();
    onInsertIntoInput(text);
    showFeedback("insertScreen");
  }, [onInsertIntoInput, showFeedback]);

  const handleInsertScrollback = useCallback(() => {
    if (!xtermRef.current || !onInsertIntoInput) return;
    const term = xtermRef.current;
    const lines: string[] = [];
    const buffer = term.buffer.active;
    for (let i = 0; i < buffer.length; i++) {
      const line = buffer.getLine(i);
      if (line) {
        lines.push(line.translateToString(true));
      }
    }
    const text = lines.join("\n").trimEnd();
    onInsertIntoInput(text);
    showFeedback("insertAll");
  }, [onInsertIntoInput, showFeedback]);

  // Close handler - kills the websocket/process
  const handleClose = useCallback(() => {
    if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
      wsRef.current.close();
    }
    if (onClose) {
      onClose();
    }
  }, [onClose]);

  // Resize handling
  const handleMouseDown = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      isResizingRef.current = true;
      startYRef.current = e.clientY;
      startHeightRef.current = height;

      const handleMouseMove = (e: MouseEvent) => {
        if (!isResizingRef.current) return;
        const delta = e.clientY - startYRef.current;
        const newHeight = Math.max(80, Math.min(800, startHeightRef.current + delta));
        setHeight(newHeight);
        setAutoSized(false); // User manually resized, disable auto-sizing
      };

      const handleMouseUp = () => {
        isResizingRef.current = false;
        document.removeEventListener("mousemove", handleMouseMove);
        document.removeEventListener("mouseup", handleMouseUp);
        // Refit terminal after resize
        if (fitAddonRef.current) {
          fitAddonRef.current.fit();
        }
      };

      document.addEventListener("mousemove", handleMouseMove);
      document.addEventListener("mouseup", handleMouseUp);
    },
    [height],
  );

  // Auto-size terminal based on content when process exits
  const autoSizeTerminal = useCallback(() => {
    if (!xtermRef.current || autoSized) return;

    const term = xtermRef.current;
    const buffer = term.buffer.active;

    // Count actual content lines (non-empty from the end)
    let contentLines = 0;
    for (let i = buffer.length - 1; i >= 0; i--) {
      const line = buffer.getLine(i);
      if (line && line.translateToString(true).trim()) {
        contentLines = i + 1;
        break;
      }
    }

    // Get actual cell dimensions from xterm
    // @ts-expect-error - accessing private _core for accurate measurements
    const core = term._core;
    const cellHeight = core?._renderService?.dimensions?.css?.cell?.height || 17;

    // Minimal padding for the terminal area
    const minHeight = 34; // ~2 lines minimum
    const maxHeight = 400;

    // Calculate exact height needed for content lines
    const neededHeight = Math.min(
      maxHeight,
      Math.max(minHeight, Math.ceil(contentLines * cellHeight) + 4),
    );

    setHeight(neededHeight);
    setAutoSized(true);

    // Refit after height change
    setTimeout(() => fitAddonRef.current?.fit(), 20);
  }, [autoSized]);

  useEffect(() => {
    if (!terminalRef.current) return;

    // Create terminal
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'Consolas, "Liberation Mono", Menlo, Courier, monospace',
      theme: getTerminalTheme(isDark),
      scrollback: 10000,
    });
    xtermRef.current = term;

    // Add fit addon
    const fitAddon = new FitAddon();
    fitAddonRef.current = fitAddon;
    term.loadAddon(fitAddon);

    // Add web links addon
    const webLinksAddon = new WebLinksAddon();
    term.loadAddon(webLinksAddon);

    // Open terminal in DOM
    term.open(terminalRef.current);
    fitAddon.fit();

    // Connect websocket
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const wsUrl = `${protocol}//${window.location.host}/api/exec-ws?cmd=${encodeURIComponent(command)}&cwd=${encodeURIComponent(cwd)}`;
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;

    ws.onopen = () => {
      // Send init message with terminal size
      ws.send(
        JSON.stringify({
          type: "init",
          cols: term.cols,
          rows: term.rows,
        }),
      );
      setStatus("running");
    };

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        if (msg.type === "output" && msg.data) {
          // Decode base64 data
          const decoded = atob(msg.data);
          term.write(decoded);
          // Track line count for auto-sizing
          lineCountRef.current = term.buffer.active.length;
        } else if (msg.type === "exit") {
          const code = parseInt(msg.data, 10) || 0;
          setExitCode(code);
          setStatus("exited");
        } else if (msg.type === "error") {
          term.write(`\r\n\x1b[31mError: ${msg.data}\x1b[0m\r\n`);
          setStatus("error");
        }
      } catch (err) {
        console.error("Failed to parse terminal message:", err);
      }
    };

    ws.onerror = (event) => {
      console.error("WebSocket error:", event);
    };

    ws.onclose = () => {
      setStatus((currentStatus) => {
        if (currentStatus === "exited") return currentStatus;
        return "exited";
      });
    };

    // Handle terminal input
    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "input", data }));
      }
    });

    // Handle resize
    const resizeObserver = new ResizeObserver(() => {
      fitAddon.fit();
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(
          JSON.stringify({
            type: "resize",
            cols: term.cols,
            rows: term.rows,
          }),
        );
      }
    });
    resizeObserver.observe(terminalRef.current);

    return () => {
      resizeObserver.disconnect();
      ws.close();
      term.dispose();
    };
  }, [command, cwd]); // Only recreate on command/cwd change, not on isDark change

  // Auto-size when process exits
  useEffect(() => {
    if (status === "exited" || status === "error") {
      // Small delay to ensure all output is written
      setTimeout(autoSizeTerminal, 100);
    }
  }, [status, autoSizeTerminal]);

  // Update theme when isDark changes without recreating terminal
  useEffect(() => {
    if (xtermRef.current) {
      xtermRef.current.options.theme = getTerminalTheme(isDark);
    }
  }, [isDark]);

  // Fit terminal when height changes
  useEffect(() => {
    if (fitAddonRef.current) {
      setTimeout(() => fitAddonRef.current?.fit(), 10);
    }
  }, [height]);

  return (
    <div className="terminal-widget" style={{ marginBottom: "1rem" }}>
      {/* Header */}
      <div
        className="terminal-widget-header"
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          padding: "6px 12px",
          backgroundColor: "var(--bg-secondary)",
          borderRadius: "8px 8px 0 0",
          border: "1px solid var(--border)",
          borderBottom: "none",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "8px", flex: 1, minWidth: 0 }}>
          <svg
            width="14"
            height="14"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            style={{ flexShrink: 0, color: "var(--text-secondary)" }}
          >
            <polyline points="4 17 10 11 4 5" />
            <line x1="12" y1="19" x2="20" y2="19" />
          </svg>
          <code
            style={{
              fontSize: "12px",
              fontFamily: 'Consolas, "Liberation Mono", Menlo, monospace',
              color: "var(--text-primary)",
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
            }}
          >
            {command}
          </code>
          {status === "running" && (
            <span
              style={{
                fontSize: "11px",
                color: "var(--success-text)",
                fontWeight: 500,
                flexShrink: 0,
              }}
            >
              ● running
            </span>
          )}
          {status === "exited" && (
            <span
              style={{
                fontSize: "11px",
                color: exitCode === 0 ? "var(--success-text)" : "var(--error-text)",
                fontWeight: 500,
                flexShrink: 0,
              }}
            >
              exit {exitCode}
            </span>
          )}
          {status === "error" && (
            <span
              style={{
                fontSize: "11px",
                color: "var(--error-text)",
                fontWeight: 500,
                flexShrink: 0,
              }}
            >
              ● error
            </span>
          )}
        </div>

        {/* Action buttons - styled like MessageActionBar */}
        <div
          style={{
            display: "flex",
            gap: "2px",
            background: "var(--bg-base)",
            border: "1px solid var(--border)",
            borderRadius: "4px",
            padding: "2px",
          }}
        >
          <ActionButton
            onClick={copyScreen}
            title="Copy visible screen to clipboard"
            feedback={copyFeedback === "copyScreen"}
          >
            {copyFeedback === "copyScreen" ? <CheckIcon /> : <CopyIcon />}
          </ActionButton>
          <ActionButton
            onClick={copyScrollback}
            title="Copy all output to clipboard"
            feedback={copyFeedback === "copyAll"}
          >
            {copyFeedback === "copyAll" ? (
              <CheckIcon />
            ) : (
              <svg
                width="14"
                height="14"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
                <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
                <line x1="12" y1="17" x2="18" y2="17" />
              </svg>
            )}
          </ActionButton>
          {onInsertIntoInput && (
            <>
              <ActionButton
                onClick={handleInsertScreen}
                title="Insert visible screen into message input"
                feedback={copyFeedback === "insertScreen"}
              >
                {copyFeedback === "insertScreen" ? <CheckIcon /> : <InsertIcon />}
              </ActionButton>
              <ActionButton
                onClick={handleInsertScrollback}
                title="Insert all output into message input"
                feedback={copyFeedback === "insertAll"}
              >
                {copyFeedback === "insertAll" ? (
                  <CheckIcon />
                ) : (
                  <svg
                    width="14"
                    height="14"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  >
                    <path d="M12 3v12" />
                    <path d="m8 11 4 4 4-4" />
                    <path d="M4 21h16" />
                    <line x1="4" y1="18" x2="20" y2="18" />
                  </svg>
                )}
              </ActionButton>
            </>
          )}
          <div
            style={{
              width: "1px",
              background: "var(--border)",
              margin: "2px 2px",
            }}
          />
          <ActionButton onClick={handleClose} title="Close terminal and kill process">
            <CloseIcon />
          </ActionButton>
        </div>
      </div>

      {/* Terminal container */}
      <div
        ref={terminalRef}
        style={{
          height: `${height}px`,
          backgroundColor: isDark ? "#1a1b26" : "#f8f9fa",
          border: "1px solid var(--border)",
          borderTop: "none",
          borderBottom: "none",
          overflow: "hidden",
        }}
      />

      {/* Resize handle */}
      <div
        onMouseDown={handleMouseDown}
        style={{
          height: "8px",
          cursor: "ns-resize",
          backgroundColor: "var(--bg-secondary)",
          border: "1px solid var(--border)",
          borderTop: "none",
          borderRadius: "0 0 8px 8px",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        }}
      >
        <div
          style={{
            width: "40px",
            height: "3px",
            backgroundColor: "var(--text-tertiary)",
            borderRadius: "2px",
          }}
        />
      </div>
    </div>
  );
}
