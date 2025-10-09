import React from "react";

interface ContextMenuProps {
  x: number;
  y: number;
  onClose: () => void;
  items: ContextMenuItem[];
}

interface ContextMenuItem {
  label: string;
  icon: React.ReactNode;
  onClick: () => void;
}

function ContextMenu({ x, y, onClose, items }: ContextMenuProps) {
  // Clamp menu within viewport
  const vw = typeof window !== "undefined" ? window.innerWidth : 0;
  const vh = typeof window !== "undefined" ? window.innerHeight : 0;
  const menuWidth = 200;
  const menuHeight = items.length * 44 + 8; // approximate height

  const clampedX = Math.max(8, Math.min(x, vw - menuWidth - 8));
  const clampedY = Math.max(8, Math.min(y, vh - menuHeight - 8));

  // Close on any click outside (handled by parent)
  React.useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (!target.closest("[data-context-menu]")) {
        onClose();
      }
    };

    // Use capture phase to ensure we catch the click before other handlers
    document.addEventListener("mousedown", handleClickOutside, true);
    return () => document.removeEventListener("mousedown", handleClickOutside, true);
  }, [onClose]);

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
      data-context-menu
      style={{
        position: "fixed",
        left: `${clampedX}px`,
        top: `${clampedY}px`,
        backgroundColor: "#ffffff",
        border: "1px solid #e5e7eb",
        borderRadius: "6px",
        boxShadow: "0 10px 15px -3px rgba(0, 0, 0, 0.1), 0 4px 6px -2px rgba(0, 0, 0, 0.05)",
        zIndex: 10000,
        minWidth: `${menuWidth}px`,
        padding: "4px 0",
      }}
    >
      {items.map((item, index) => (
        <button
          key={index}
          onClick={() => {
            item.onClick();
            onClose();
          }}
          style={{
            display: "flex",
            alignItems: "center",
            gap: "12px",
            width: "100%",
            padding: "10px 16px",
            border: "none",
            backgroundColor: "transparent",
            cursor: "pointer",
            fontSize: "14px",
            color: "#1f2937",
            textAlign: "left",
            transition: "background-color 0.1s",
          }}
          onMouseEnter={(e) => {
            e.currentTarget.style.backgroundColor = "#f3f4f6";
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.backgroundColor = "transparent";
          }}
        >
          <span style={{ display: "flex", alignItems: "center", width: "20px", height: "20px" }}>
            {item.icon}
          </span>
          <span>{item.label}</span>
        </button>
      ))}
    </div>
  );
}

export default ContextMenu;
