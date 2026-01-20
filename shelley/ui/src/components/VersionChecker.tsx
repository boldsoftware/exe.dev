import React, { useState, useEffect, useCallback } from "react";
import { api } from "../services/api";
import { VersionInfo, CommitInfo } from "../types";

interface VersionCheckerProps {
  onUpdateAvailable?: (hasUpdate: boolean) => void;
}

interface VersionModalProps {
  isOpen: boolean;
  onClose: () => void;
  versionInfo: VersionInfo | null;
  isLoading: boolean;
}

function VersionModal({ isOpen, onClose, versionInfo, isLoading }: VersionModalProps) {
  const [commits, setCommits] = useState<CommitInfo[]>([]);
  const [loadingCommits, setLoadingCommits] = useState(false);
  const [upgrading, setUpgrading] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const [upgradeMessage, setUpgradeMessage] = useState<string | null>(null);
  const [upgradeError, setUpgradeError] = useState<string | null>(null);

  useEffect(() => {
    if (isOpen && versionInfo?.has_update && versionInfo.current_tag && versionInfo.latest_tag) {
      loadCommits(versionInfo.current_tag, versionInfo.latest_tag);
    }
  }, [isOpen, versionInfo]);

  const loadCommits = async (currentTag: string, latestTag: string) => {
    setLoadingCommits(true);
    try {
      const result = await api.getChangelog(currentTag, latestTag);
      setCommits(result || []);
    } catch (err) {
      console.error("Failed to load changelog:", err);
      setCommits([]);
    } finally {
      setLoadingCommits(false);
    }
  };

  const handleUpgrade = async () => {
    setUpgrading(true);
    setUpgradeError(null);
    setUpgradeMessage(null);
    try {
      const result = await api.upgrade();
      setUpgradeMessage(result.message);
    } catch (err) {
      const message = err instanceof Error ? err.message : "Unknown error";
      setUpgradeError(message);
    } finally {
      setUpgrading(false);
    }
  };

  const handleExit = async () => {
    setRestarting(true);
    try {
      await api.exit();
      setTimeout(() => {
        window.location.reload();
      }, 2000);
    } catch {
      setTimeout(() => {
        window.location.reload();
      }, 2000);
    }
  };

  if (!isOpen) return null;

  const formatDateTime = (dateStr: string) => {
    const date = new Date(dateStr);
    return date.toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  };

  const formatDate = (dateStr: string) => {
    const date = new Date(dateStr);
    return date.toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  };

  const getCommitUrl = (sha: string) => {
    return `https://github.com/boldsoftware/shelley/commit/${sha}`;
  };

  return (
    <div className="version-modal-overlay" onClick={onClose}>
      <div className="version-modal" onClick={(e) => e.stopPropagation()}>
        <div className="version-modal-header">
          <h2>Version</h2>
          <button onClick={onClose} className="version-modal-close" aria-label="Close">
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
        </div>

        <div className="version-modal-content">
          {isLoading ? (
            <div className="version-loading">Checking for updates...</div>
          ) : versionInfo ? (
            <>
              <div className="version-info-row">
                <span className="version-label">Current:</span>
                <span className="version-value">
                  {versionInfo.current_tag || versionInfo.current_version || "dev"}
                </span>
                {versionInfo.current_commit_time && (
                  <span className="version-date">
                    ({formatDateTime(versionInfo.current_commit_time)})
                  </span>
                )}
              </div>

              {versionInfo.latest_tag && (
                <div className="version-info-row">
                  <span className="version-label">Latest:</span>
                  <span className="version-value">{versionInfo.latest_tag}</span>
                  {versionInfo.published_at && (
                    <span className="version-date">({formatDate(versionInfo.published_at)})</span>
                  )}
                </div>
              )}

              {versionInfo.error && (
                <div className="version-error">
                  <span>Error: {versionInfo.error}</span>
                </div>
              )}

              {/* Changelog */}
              {versionInfo.has_update && (
                <div className="version-changelog">
                  <h3>
                    <a
                      href={`https://github.com/boldsoftware/shelley/compare/${versionInfo.current_tag}...${versionInfo.latest_tag}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="changelog-link"
                    >
                      Changelog
                    </a>
                  </h3>
                  {loadingCommits ? (
                    <div className="version-loading">Loading...</div>
                  ) : commits.length > 0 ? (
                    <ul className="commit-list">
                      {commits.map((commit) => (
                        <li key={commit.sha} className="commit-item">
                          <a
                            href={getCommitUrl(commit.sha)}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="commit-sha"
                          >
                            {commit.sha}
                          </a>
                          <span className="commit-message">{commit.message}</span>
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <div className="version-no-commits">No commits found</div>
                  )}
                </div>
              )}

              {/* Upgrade/Restart buttons */}
              {versionInfo.has_update && versionInfo.download_url && (
                <div className="version-actions">
                  {upgradeMessage && (
                    <div className="version-success">
                      Upgraded {versionInfo.executable_path || "shelley"}
                    </div>
                  )}
                  {upgradeError && <div className="version-error">{upgradeError}</div>}

                  {!upgradeMessage ? (
                    <button
                      onClick={handleUpgrade}
                      disabled={upgrading}
                      className="version-btn version-btn-primary"
                    >
                      {upgrading
                        ? "Upgrading..."
                        : `Upgrade ${versionInfo.executable_path || "shelley"} in place`}
                    </button>
                  ) : (
                    <button
                      onClick={handleExit}
                      disabled={restarting}
                      className="version-btn version-btn-primary"
                    >
                      {restarting
                        ? versionInfo.running_under_systemd
                          ? "Restarting..."
                          : "Killing..."
                        : versionInfo.running_under_systemd
                          ? "Restart"
                          : "Kill Shelley Server"}
                    </button>
                  )}
                </div>
              )}
            </>
          ) : (
            <div className="version-loading">Loading...</div>
          )}
        </div>
      </div>
    </div>
  );
}

export function useVersionChecker({ onUpdateAvailable }: VersionCheckerProps = {}) {
  const [versionInfo, setVersionInfo] = useState<VersionInfo | null>(null);
  const [showModal, setShowModal] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const [shouldNotify, setShouldNotify] = useState(false);

  const checkVersion = useCallback(async () => {
    setIsLoading(true);
    try {
      // Always force refresh when checking
      const info = await api.checkVersion(true);
      setVersionInfo(info);
      setShouldNotify(info.should_notify);
      onUpdateAvailable?.(info.should_notify);
    } catch (err) {
      console.error("Failed to check version:", err);
    } finally {
      setIsLoading(false);
    }
  }, [onUpdateAvailable]);

  // Check version on mount (uses cache)
  useEffect(() => {
    const checkInitial = async () => {
      try {
        const info = await api.checkVersion(false);
        setVersionInfo(info);
        setShouldNotify(info.should_notify);
        onUpdateAvailable?.(info.should_notify);
      } catch (err) {
        console.error("Failed to check version:", err);
      }
    };
    checkInitial();
  }, [onUpdateAvailable]);

  const openModal = useCallback(() => {
    setShowModal(true);
    // Always check for new version when opening modal
    checkVersion();
  }, [checkVersion]);

  const closeModal = useCallback(() => {
    setShowModal(false);
  }, []);

  const VersionModalComponent = (
    <VersionModal
      isOpen={showModal}
      onClose={closeModal}
      versionInfo={versionInfo}
      isLoading={isLoading}
    />
  );

  return {
    hasUpdate: shouldNotify, // For red dot indicator (5+ days apart)
    versionInfo,
    openModal,
    closeModal,
    isLoading,
    VersionModal: VersionModalComponent,
  };
}

export default useVersionChecker;
