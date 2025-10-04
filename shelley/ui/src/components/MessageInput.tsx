import React, { useState, useRef, useEffect } from "react";

interface MessageInputProps {
  onSend: (message: string) => void;
  disabled?: boolean;
}

function MessageInput({ onSend, disabled = false }: MessageInputProps) {
  const [message, setMessage] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (message.trim() && !disabled) {
      onSend(message);
      setMessage("");
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

  return (
    <div className="message-input-container">
      <form onSubmit={handleSubmit} className="message-input-form">
        <textarea
          ref={textareaRef}
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Type your message... (Press Enter to send, Shift+Enter for new line)"
          className="message-textarea"
          disabled={disabled}
          style={{ height: "auto" }}
          aria-label="Message input"
          data-testid="message-input"
        />
        <button
          type="submit"
          disabled={disabled || !message.trim()}
          className="message-send-btn"
          aria-label="Send message"
          data-testid="send-button"
        >
          {disabled ? (
            <div className="flex items-center justify-center">
              <div className="spinner spinner-small" style={{ borderTopColor: "white" }}></div>
            </div>
          ) : (
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M12 19l9 2-9-18-9 18 9-2zm0 0v-8"
              />
            </svg>
          )}
        </button>
      </form>
    </div>
  );
}

export default MessageInput;
