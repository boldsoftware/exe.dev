package replication

import (
	"context"
	"log/slog"
	"strings"
)

// VolumeDeleter is an interface for targets that support deleting entire volumes
type VolumeDeleter interface {
	DeleteVolume(ctx context.Context, volumeID string) error
}

// Pruner handles removal of orphaned backups from the target
type Pruner struct {
	target   Target
	log      *slog.Logger
	enabled  bool
	nodeName string
}

// NewPruner creates a new pruner
func NewPruner(target Target, enabled bool, nodeName string, log *slog.Logger) *Pruner {
	return &Pruner{
		target:   target,
		log:      log,
		enabled:  enabled,
		nodeName: nodeName,
	}
}

// Prune removes backups for volumes that no longer exist locally
func (p *Pruner) Prune(ctx context.Context, localVolumeIDs map[string]struct{}) error {
	if !p.enabled {
		return nil
	}

	// Get all volumes on target
	targetVolumes, err := p.target.ListVolumes(ctx)
	if err != nil {
		return err
	}

	// Find orphaned volumes belonging to this node.
	// Only consider volumes in this node's namespace:
	//   - VM instance IDs (vm\d{6}-*): globally unique, safe to prune
	//   - Non-VM datasets ending with -<nodeName>: belong to this node
	// Skip everything else (base images, other nodes' datasets).
	suffix := "-" + p.nodeName
	var orphaned []string
	for _, tv := range targetVolumes {
		if strings.HasPrefix(tv, "sha256:") || strings.HasPrefix(tv, "tmp-sha256:") {
			continue
		}
		if !isVMInstanceID(tv) && !strings.HasSuffix(tv, suffix) {
			p.log.DebugContext(ctx, "skipping volume from another node during prune", "volume", tv)
			continue
		}
		if _, exists := localVolumeIDs[tv]; !exists {
			orphaned = append(orphaned, tv)
		}
	}

	if len(orphaned) == 0 {
		p.log.DebugContext(ctx, "no orphaned volumes to prune")
		return nil
	}

	p.log.InfoContext(ctx, "pruning orphaned volumes", "count", len(orphaned))

	// Delete orphaned volumes
	for _, volumeID := range orphaned {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check if target supports volume deletion
		if deleter, ok := p.target.(VolumeDeleter); ok {
			if err := deleter.DeleteVolume(ctx, volumeID); err != nil {
				p.log.ErrorContext(ctx, "failed to prune volume", "volume_id", volumeID, "error", err)
				continue
			}
			p.log.InfoContext(ctx, "pruned orphaned volume", "volume_id", volumeID)
		} else {
			// Fall back to deleting individual snapshots
			snapshots, err := p.target.ListSnapshots(ctx, volumeID)
			if err != nil {
				p.log.ErrorContext(ctx, "failed to list snapshots for pruning", "volume_id", volumeID, "error", err)
				continue
			}
			for _, snap := range snapshots {
				if err := p.target.Delete(ctx, volumeID, snap); err != nil {
					p.log.ErrorContext(ctx, "failed to delete snapshot during prune", "volume_id", volumeID, "snapshot", snap, "error", err)
				}
			}
			p.log.InfoContext(ctx, "pruned orphaned volume snapshots", "volume_id", volumeID, "count", len(snapshots))
		}
	}

	return nil
}
