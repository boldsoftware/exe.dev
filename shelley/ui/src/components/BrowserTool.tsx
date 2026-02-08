import React from "react";
import { LLMContent } from "../types";
import BrowserNavigateTool from "./BrowserNavigateTool";
import BrowserEvalTool from "./BrowserEvalTool";
import BrowserResizeTool from "./BrowserResizeTool";
import BrowserConsoleLogsTool from "./BrowserConsoleLogsTool";
import ScreenshotTool from "./ScreenshotTool";
import GenericTool from "./GenericTool";

interface BrowserToolProps {
  toolInput?: unknown;
  isRunning?: boolean;
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown;
}

function getAction(toolInput: unknown): string {
  if (
    typeof toolInput === "object" &&
    toolInput !== null &&
    "action" in toolInput &&
    typeof (toolInput as Record<string, unknown>).action === "string"
  ) {
    return (toolInput as Record<string, unknown>).action as string;
  }
  return "";
}

function BrowserTool(props: BrowserToolProps) {
  const action = getAction(props.toolInput);

  switch (action) {
    case "navigate":
      return <BrowserNavigateTool {...props} />;
    case "eval":
      return <BrowserEvalTool {...props} />;
    case "resize":
      return <BrowserResizeTool {...props} />;
    case "screenshot":
      return <ScreenshotTool {...props} />;
    case "console_logs":
      return <BrowserConsoleLogsTool toolName="browser_recent_console_logs" {...props} />;
    case "clear_console_logs":
      return <BrowserConsoleLogsTool toolName="browser_clear_console_logs" {...props} />;
    default:
      return <GenericTool toolName={`browser (${action || "unknown"})`} {...props} />;
  }
}

export default BrowserTool;
