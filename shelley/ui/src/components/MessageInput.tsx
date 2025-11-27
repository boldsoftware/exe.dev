import React, { useState, useRef, useEffect } from "react";

interface MessageInputProps {
  onSend: (message: string) => Promise<void>;
  disabled?: boolean;
  autoFocus?: boolean;
}

function MessageInput({ onSend, disabled = false, autoFocus = false }: MessageInputProps) {
  const [message, setMessage] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (message.trim() && !disabled && !submitting) {
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

  return (
    <div className="message-input-container">
      <form onSubmit={handleSubmit} className="message-input-form">
        <textarea
          ref={textareaRef}
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Type your message..."
          className="message-textarea"
          disabled={disabled}
          style={{ height: "auto" }}
          aria-label="Message input"
          data-testid="message-input"
          autoFocus={autoFocus}
        />
        <button
          type="submit"
          disabled={disabled || submitting || !message.trim()}
          className="message-send-btn"
          aria-label="Send message"
          data-testid="send-button"
        >
          {disabled || submitting ? (
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
