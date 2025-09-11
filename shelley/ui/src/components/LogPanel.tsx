import React, { useState, useEffect, useRef } from 'react';

interface LogEntry {
  timestamp: string;
  level: string;
  message: string;
  fields?: Record<string, any>;
}

const LogPanel: React.FC = () => {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [connected, setConnected] = useState(false);
  const logContentRef = useRef<HTMLDivElement>(null);
  const eventSourceRef = useRef<EventSource | null>(null);

  useEffect(() => {
    // Connect to the SSE endpoint
    const eventSource = new EventSource('/api/logs/stream');
    eventSourceRef.current = eventSource;

    eventSource.onopen = () => {
      setConnected(true);
    };

    eventSource.onmessage = (event) => {
      try {
        const newLogs: LogEntry[] = JSON.parse(event.data);
        setLogs(prevLogs => {
          // If this is the initial batch (many logs), replace all
          if (newLogs.length > 10) {
            return newLogs;
          }
          // Otherwise, append new logs
          return [...prevLogs, ...newLogs];
        });
      } catch (error) {
        console.error('Failed to parse log data:', error);
      }
    };

    eventSource.onerror = () => {
      setConnected(false);
    };

    return () => {
      eventSource.close();
    };
  }, []);

  // Auto-scroll to bottom when new logs arrive
  useEffect(() => {
    if (logContentRef.current) {
      logContentRef.current.scrollTop = logContentRef.current.scrollHeight;
    }
  }, [logs]);

  const formatTimestamp = (timestamp: string) => {
    const date = new Date(timestamp);
    return date.toLocaleTimeString('en-US', { hour12: false });
  };

  const getLevelColor = (level: string) => {
    switch (level.toLowerCase()) {
      case 'error': return 'text-red-600';
      case 'warn': return 'text-yellow-600';
      case 'info': return 'text-blue-600';
      case 'debug': return 'text-gray-500';
      default: return 'text-gray-700';
    }
  };

  const formatFields = (fields?: Record<string, any>) => {
    if (!fields || Object.keys(fields).length === 0) {
      return null;
    }
    
    return Object.entries(fields)
      .map(([key, value]) => `${key}=${JSON.stringify(value)}`)
      .join(' ');
  };

  return (
    <div className="h-full flex flex-col">
      <div className="p-3 border-b border-gray-200 bg-gray-50">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-800">Server Logs</h3>
          <div className={`w-2 h-2 rounded-full ${
            connected ? 'bg-green-500' : 'bg-red-500'
          }`} title={connected ? 'Connected' : 'Disconnected'} />
        </div>
      </div>
      <div 
        ref={logContentRef}
        className="flex-1 p-3 overflow-auto text-xs leading-relaxed font-mono bg-white"
      >
        {logs.length === 0 ? (
          <div className="text-gray-600">
            {connected ? 'No logs yet...' : 'Connecting to log stream...'}
          </div>
        ) : (
          logs.map((log, index) => {
            const fieldsStr = formatFields(log.fields);
            return (
              <div key={index} className="mb-1 break-words">
                <span className="text-gray-500 mr-2">
                  {formatTimestamp(log.timestamp)}
                </span>
                <span className={`${getLevelColor(log.level)} uppercase font-semibold mr-2 text-xs`}>
                  {log.level}
                </span>
                <span className="text-gray-800 mr-2">
                  {log.message}
                </span>
                {fieldsStr && (
                  <span className="text-gray-600 text-xs">
                    {fieldsStr}
                  </span>
                )}
              </div>
            );
          })
        )}
      </div>
    </div>
  );
};

export default LogPanel;
