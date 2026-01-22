package replication

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	api "exe.dev/pkg/api/exe/replication/v1"
)

var statusCommand = &cli.Command{
	Name:  "status",
	Usage: "Show replicator status and queue",
	Action: func(clix *cli.Context) error {
		c, err := helpers.GetClient(clix)
		if err != nil {
			return err
		}
		defer c.Close()

		ctx := context.WithoutCancel(clix.Context)
		resp, err := c.GetStatus(ctx, &api.GetStatusRequest{})
		if err != nil {
			return fmt.Errorf("failed to get status: %w", err)
		}

		status := resp.Status
		if status == nil {
			fmt.Println("Replicator: DISABLED")
			return nil
		}

		// Print replicator status
		if status.Enabled {
			fmt.Println("Replicator: ACTIVE")
		} else {
			fmt.Println("Replicator: DISABLED")
			return nil
		}

		fmt.Printf("Target: %s\n", status.Target)
		fmt.Printf("Interval: %s\n", formatDuration(time.Duration(status.IntervalSeconds)*time.Second))
		if status.NextRunSeconds > 0 {
			fmt.Printf("Next run: %s\n", formatDuration(time.Duration(status.NextRunSeconds)*time.Second))
		}
		fmt.Printf("Workers: %d/%d busy\n", status.WorkersBusy, status.WorkersTotal)
		fmt.Println()

		// Print queue
		if len(status.Queue) == 0 {
			fmt.Println("Queue: (empty)")
			return nil
		}

		fmt.Println("Queue:")
		w := tabwriter.NewWriter(os.Stdout, 2, 1, 3, ' ', 0)
		fmt.Fprintf(w, "  VOLUME ID\tSTATE\tPROGRESS\n")

		for _, entry := range status.Queue {
			state := stateString(entry.State)
			progress := ""

			switch entry.State {
			case api.ReplicationState_REPLICATION_STATE_SENDING:
				if entry.BytesTotal > 0 {
					progress = fmt.Sprintf("%.1f%% (%s/%s)",
						entry.ProgressPercent,
						formatBytes(entry.BytesTransferred),
						formatBytes(entry.BytesTotal))
				} else {
					progress = fmt.Sprintf("%.1f%%", entry.ProgressPercent)
				}
			case api.ReplicationState_REPLICATION_STATE_COMPLETE:
				if entry.CompletedAt > 0 {
					progress = formatTimeAgo(time.Unix(entry.CompletedAt, 0))
				}
			case api.ReplicationState_REPLICATION_STATE_FAILED:
				progress = entry.ErrorMessage
				if len(progress) > 50 {
					progress = progress[:50] + "..."
				}
			}

			fmt.Fprintf(w, "  %s\t%s\t%s\n", entry.VolumeID, state, progress)
		}

		w.Flush()
		return nil
	},
}

func stateString(state api.ReplicationState) string {
	switch state {
	case api.ReplicationState_REPLICATION_STATE_IDLE:
		return "IDLE"
	case api.ReplicationState_REPLICATION_STATE_PENDING:
		return "PENDING"
	case api.ReplicationState_REPLICATION_STATE_SNAPSHOTTING:
		return "SNAPSHOTTING"
	case api.ReplicationState_REPLICATION_STATE_SENDING:
		return "SENDING"
	case api.ReplicationState_REPLICATION_STATE_COMPLETE:
		return "COMPLETE"
	case api.ReplicationState_REPLICATION_STATE_FAILED:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}
