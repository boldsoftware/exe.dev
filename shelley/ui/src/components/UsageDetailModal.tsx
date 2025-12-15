import React from "react";
import { Usage } from "../types";

interface UsageDetailModalProps {
  usage: Usage;
  durationMs: number | null;
  onClose: () => void;
}

function UsageDetailModal({ usage, durationMs, onClose }: UsageDetailModalProps) {
  // Format duration in human-readable format
  const formatDuration = (ms: number): string => {
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(2)}s`;
    return `${(ms / 60000).toFixed(2)}m`;
  };

  // Format timestamp for display
  const formatTimestamp = (isoString: string): string => {
    const date = new Date(isoString);
    return date.toLocaleString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  };

  // Close on escape key
  React.useEffect(() => {
    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
      }
    };
    document.addEventListener("keydown", handleEscape);
    return () => document.removeEventListener("keydown", handleEscape);
  }, [onClose]);

  return (
    <div
      style={{
        position: "fixed",
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        backgroundColor: "rgba(0, 0, 0, 0.5)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        zIndex: 10001,
        padding: "16px",
      }}
      onClick={onClose}
    >
      <div
        style={{
          backgroundColor: "#ffffff",
          borderRadius: "8px",
          padding: "24px",
          maxWidth: "500px",
          width: "100%",
          boxShadow: "0 20px 25px -5px rgba(0, 0, 0, 0.1), 0 10px 10px -5px rgba(0, 0, 0, 0.04)",
        }}
        onClick={(e) => e.stopPropagation()}
      >
        <div
          style={{
            display: "flex",
            justifyContent: "space-between",
            alignItems: "center",
            marginBottom: "20px",
          }}
        >
          <h2 style={{ fontSize: "18px", fontWeight: "600", color: "#1f2937", margin: 0 }}>
            Usage Details
          </h2>
          <button
            onClick={onClose}
            style={{
              background: "none",
              border: "none",
              fontSize: "24px",
              color: "#6b7280",
              cursor: "pointer",
              padding: "0",
              width: "32px",
              height: "32px",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              borderRadius: "4px",
            }}
            onMouseEnter={(e) => {
              e.currentTarget.style.backgroundColor = "#f3f4f6";
            }}
            onMouseLeave={(e) => {
              e.currentTarget.style.backgroundColor = "transparent";
            }}
            aria-label="Close"
          >
            ×
          </button>
        </div>
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "auto 1fr",
            gap: "12px 20px",
            fontSize: "14px",
          }}
        >
          {usage.model && (
            <>
              <div style={{ color: "#6b7280", fontWeight: "500" }}>Model:</div>
              <div style={{ color: "#1f2937" }}>{usage.model}</div>
            </>
          )}
          <div style={{ color: "#6b7280", fontWeight: "500" }}>Input Tokens:</div>
          <div style={{ color: "#1f2937" }}>{usage.input_tokens.toLocaleString()}</div>
          {usage.cache_read_input_tokens > 0 && (
            <>
              <div style={{ color: "#6b7280", fontWeight: "500" }}>Cache Read:</div>
              <div style={{ color: "#1f2937" }}>
                {usage.cache_read_input_tokens.toLocaleString()}
              </div>
            </>
          )}
          {usage.cache_creation_input_tokens > 0 && (
            <>
              <div style={{ color: "#6b7280", fontWeight: "500" }}>Cache Write:</div>
              <div style={{ color: "#1f2937" }}>
                {usage.cache_creation_input_tokens.toLocaleString()}
              </div>
            </>
          )}
          <div style={{ color: "#6b7280", fontWeight: "500" }}>Output Tokens:</div>
          <div style={{ color: "#1f2937" }}>{usage.output_tokens.toLocaleString()}</div>
          {usage.cost_usd > 0 && (
            <>
              <div style={{ color: "#6b7280", fontWeight: "500" }}>Cost:</div>
              <div style={{ color: "#1f2937" }}>${usage.cost_usd.toFixed(4)}</div>
            </>
          )}
          {durationMs !== null && (
            <>
              <div style={{ color: "#6b7280", fontWeight: "500" }}>Duration:</div>
              <div style={{ color: "#1f2937" }}>{formatDuration(durationMs)}</div>
            </>
          )}
          {usage.end_time && (
            <>
              <div style={{ color: "#6b7280", fontWeight: "500" }}>Timestamp:</div>
              <div style={{ color: "#1f2937" }}>{formatTimestamp(usage.end_time)}</div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

export default UsageDetailModal;
