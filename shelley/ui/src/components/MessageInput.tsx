import React, { useState, useRef, useEffect } from "react";

interface MessageInputProps {
  onSend: (message: string) => Promise<void>;
  disabled?: boolean;
  autoFocus?: boolean;
  onFocus?: () => void;
}

function MessageInput({ onSend, disabled = false, autoFocus = false, onFocus }: MessageInputProps) {
  const [message, setMessage] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [uploadsInProgress, setUploadsInProgress] = useState(0);
  const [dragCounter, setDragCounter] = useState(0);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const uploadFile = async (file: File, insertPosition: number) => {
    const textBefore = message.substring(0, insertPosition);
    const textAfter = message.substring(insertPosition);

    // Add a loading indicator
    const loadingText = `[uploading ${file.name}...]`;
    setMessage(`${textBefore}${loadingText}${textAfter}`);
    setUploadsInProgress((prev) => prev + 1);

    try {
      const formData = new FormData();
      formData.append("file", file);

      const response = await fetch("/api/upload", {
        method: "POST",
        body: formData,
      });

      if (!response.ok) {
        throw new Error(`Upload failed: ${response.statusText}`);
      }

      const data = await response.json();

      // Replace the loading placeholder with the actual file path
      setMessage((currentMessage) => currentMessage.replace(loadingText, `[${data.path}]`));
    } catch (error) {
      console.error("Failed to upload file:", error);
      // Replace loading indicator with error message
      const errorText = `[upload failed: ${error instanceof Error ? error.message : "unknown error"}]`;
      setMessage((currentMessage) => currentMessage.replace(loadingText, errorText));
    } finally {
      setUploadsInProgress((prev) => prev - 1);
    }
  };

  const handlePaste = async (event: React.ClipboardEvent) => {
    // Check if the clipboard contains files
    if (event.clipboardData && event.clipboardData.files.length > 0) {
      const file = event.clipboardData.files[0];
      event.preventDefault();

      const cursorPos = textareaRef.current?.selectionStart ?? message.length;
      await uploadFile(file, cursorPos);
    }
  };

  const handleDragOver = (event: React.DragEvent) => {
    event.preventDefault();
    event.stopPropagation();
  };

  const handleDragEnter = (event: React.DragEvent) => {
    event.preventDefault();
    event.stopPropagation();
    setDragCounter((prev) => prev + 1);
  };

  const handleDragLeave = (event: React.DragEvent) => {
    event.preventDefault();
    event.stopPropagation();
    setDragCounter((prev) => prev - 1);
  };

  const handleDrop = async (event: React.DragEvent) => {
    event.preventDefault();
    event.stopPropagation();
    setDragCounter(0);

    if (event.dataTransfer && event.dataTransfer.files.length > 0) {
      // Process all dropped files
      for (let i = 0; i < event.dataTransfer.files.length; i++) {
        const file = event.dataTransfer.files[i];
        const insertPosition =
          i === 0 ? (textareaRef.current?.selectionStart ?? message.length) : message.length;
        await uploadFile(file, insertPosition);
        // Add a space between files
        if (i < event.dataTransfer.files.length - 1) {
          setMessage((prev) => prev + " ");
        }
      }
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (message.trim() && !disabled && !submitting && uploadsInProgress === 0) {
      const messageToSend = message;
      setSubmitting(true);
      try {
        await onSend(messageToSend);
        // Only clear on success
        setMessage("");
      } catch {
        // Keep the message on error so user can retry
      } finally {
        setSubmitting(false);
      }
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      handleSubmit(e);
    }
  };

  const adjustTextareaHeight = () => {
    if (textareaRef.current) {
      textareaRef.current.style.height = "auto";
      const scrollHeight = textareaRef.current.scrollHeight;
      const maxHeight = 200; // Maximum height in pixels
      textareaRef.current.style.height = `${Math.min(scrollHeight, maxHeight)}px`;
    }
  };

  useEffect(() => {
    adjustTextareaHeight();
  }, [message]);

  useEffect(() => {
    if (autoFocus && textareaRef.current) {
      // Use setTimeout to ensure the component is fully rendered
      setTimeout(() => {
        textareaRef.current?.focus();
      }, 0);
    }
  }, [autoFocus]);

  const isDisabled = disabled || uploadsInProgress > 0;
  const canSubmit = message.trim() && !isDisabled && !submitting;

  const isDraggingOver = dragCounter > 0;

  return (
    <div
      className={`message-input-container ${isDraggingOver ? "drag-over" : ""}`}
      onDragOver={handleDragOver}
      onDragEnter={handleDragEnter}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {isDraggingOver && (
        <div className="drag-overlay">
          <div className="drag-overlay-content">Drop files here</div>
        </div>
      )}
      <form onSubmit={handleSubmit} className="message-input-form">
        <textarea
          ref={textareaRef}
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          onFocus={() => {
            // Scroll to bottom after keyboard animation settles
            if (onFocus) {
              requestAnimationFrame(() => requestAnimationFrame(onFocus));
            }
          }}
          placeholder="Type your message..."
          className="message-textarea"
          disabled={isDisabled}
          style={{ height: "auto" }}
          aria-label="Message input"
          data-testid="message-input"
          autoFocus={autoFocus}
        />
        <button
          type="submit"
          disabled={!canSubmit}
          className="message-send-btn"
          aria-label="Send message"
          data-testid="send-button"
        >
          {isDisabled || submitting ? (
            <div className="flex items-center justify-center">
              <div className="spinner spinner-small" style={{ borderTopColor: "white" }}></div>
            </div>
          ) : (
            <svg fill="currentColor" viewBox="0 0 24 24" width="20" height="20">
              <path d="M12 4l-1.41 1.41L16.17 11H4v2h12.17l-5.58 5.59L12 20l8-8z" />
            </svg>
          )}
        </button>
      </form>
    </div>
  );
}

export default MessageInput;
