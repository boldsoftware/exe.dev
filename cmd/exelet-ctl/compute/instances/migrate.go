package instances

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/helpers"
	"exe.dev/exelet/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

var migrateInstanceCommand = &cli.Command{
	Name:      "migrate",
	Usage:     "migrate an instance to another exelet",
	ArgsUsage: "<instance-id> <target-address>",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "delete",
			Usage: "delete instance from source after successful migration",
		},
		&cli.BoolFlag{
			Name:  "two-phase",
			Usage: "use two-phase migration (snapshot while running, then send diff)",
		},
	},
	Action: func(clix *cli.Context) error {
		if clix.NArg() < 2 {
			return fmt.Errorf("usage: migrate <instance-id> <target-address>")
		}

		instanceID := clix.Args().Get(0)
		targetAddr := clix.Args().Get(1)
		deleteAfter := clix.Bool("delete")
		twoPhase := clix.Bool("two-phase")

		// Connect to source exelet (from --addr flag)
		sourceClient, err := helpers.GetClient(clix)
		if err != nil {
			return fmt.Errorf("failed to connect to source: %w", err)
		}
		defer sourceClient.Close()

		// Connect to target exelet
		targetClient, err := client.NewClient(targetAddr, client.WithInsecure())
		if err != nil {
			return fmt.Errorf("failed to connect to target: %w", err)
		}
		defer targetClient.Close()

		// WithoutCancel prevents Ctrl-C from aborting a migration midway.
		// WithCancel lets us clean up gRPC streams on return, which
		// releases the source exelet's migration lock.
		ctx, cancel := context.WithCancel(context.WithoutCancel(clix.Context))
		defer cancel()

		// Start spinner
		sp := helpers.NewSpinner("starting migration...")
		sp.Start()
		defer sp.Stop()

		// Start SendVM stream on source
		sp.Update("connecting to source...")
		sendStream, err := sourceClient.SendVM(ctx)
		if err != nil {
			return fmt.Errorf("failed to start SendVM: %w", err)
		}

		// Send start request to source
		sp.Update("requesting VM data...")
		if err := sendStream.Send(&api.SendVMRequest{
			Type: &api.SendVMRequest_Start{
				Start: &api.SendVMStartRequest{
					InstanceID:         instanceID,
					TargetHasBaseImage: true,
					TwoPhase:           twoPhase,
					AcceptStatus:       true,
				},
			},
		}); err != nil {
			return fmt.Errorf("failed to send start request: %w", err)
		}

		// Receive metadata from source (may be preceded by status messages)
		var metadata *api.SendVMMetadata
		for {
			resp, err := sendStream.Recv()
			if err != nil {
				return fmt.Errorf("failed to receive metadata: %w", err)
			}
			if st := resp.GetStatus(); st != nil {
				sp.Update(fmt.Sprintf("source: %s", st.Message))
				continue
			}
			metadata = resp.GetMetadata()
			if metadata == nil {
				return fmt.Errorf("expected metadata, got %T", resp.Type)
			}
			break
		}

		if twoPhase {
			sp.Update(fmt.Sprintf("migrating %s (%s, two-phase)...", metadata.Instance.Name, humanize.Bytes(metadata.TotalSizeEstimate)))
		} else {
			sp.Update(fmt.Sprintf("migrating %s (%s)...", metadata.Instance.Name, humanize.Bytes(metadata.TotalSizeEstimate)))
		}

		// Start ReceiveVM stream on target
		recvStream, err := targetClient.ReceiveVM(ctx)
		if err != nil {
			return fmt.Errorf("failed to start ReceiveVM: %w", err)
		}

		// Send start request to target
		if err := recvStream.Send(&api.ReceiveVMRequest{
			Type: &api.ReceiveVMRequest_Start{
				Start: &api.ReceiveVMStartRequest{
					InstanceID:     instanceID,
					SourceInstance: metadata.Instance,
					BaseImageID:    metadata.BaseImageID,
					Encrypted:      metadata.Encrypted,
					EncryptionKey:  metadata.EncryptionKey,
					GroupID:        metadata.Instance.GroupID,
				},
			},
		}); err != nil {
			return fmt.Errorf("failed to send receive start: %w", err)
		}

		// Receive ready from target
		recvResp, err := recvStream.Recv()
		if err != nil {
			return fmt.Errorf("failed to receive ready: %w", err)
		}
		ready := recvResp.GetReady()
		if ready == nil {
			return fmt.Errorf("expected ready, got %T", recvResp.Type)
		}

		// Pipe data from source to target
		var totalBytes uint64
		for {
			resp, err := sendStream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("failed to receive from source: %w", err)
			}

			switch v := resp.Type.(type) {
			case *api.SendVMResponse_Data:
				totalBytes += uint64(len(v.Data.Data))
				sp.Update(fmt.Sprintf("transferring %s / %s...",
					humanize.Bytes(totalBytes),
					humanize.Bytes(metadata.TotalSizeEstimate)))

				if err := recvStream.Send(&api.ReceiveVMRequest{
					Type: &api.ReceiveVMRequest_Data{
						Data: &api.ReceiveVMDataChunk{
							Data:        v.Data.Data,
							IsBaseImage: v.Data.IsBaseImage,
						},
					},
				}); err != nil {
					if recvErr := recvTargetErr(recvStream); recvErr != nil {
						return fmt.Errorf("target error: %w", recvErr)
					}
					return fmt.Errorf("failed to send to target: %w", err)
				}

			case *api.SendVMResponse_PhaseComplete:
				sp.Update(fmt.Sprintf("phase 1 complete (%s), stopping VM for phase 2...",
					humanize.Bytes(v.PhaseComplete.PhaseBytes)))
				// Forward phase complete to target
				if err := recvStream.Send(&api.ReceiveVMRequest{
					Type: &api.ReceiveVMRequest_PhaseComplete{
						PhaseComplete: &api.ReceiveVMPhaseComplete{},
					},
				}); err != nil {
					if recvErr := recvTargetErr(recvStream); recvErr != nil {
						return fmt.Errorf("target error: %w", recvErr)
					}
					return fmt.Errorf("failed to send phase complete to target: %w", err)
				}

			case *api.SendVMResponse_Complete:
				sp.Update("verifying transfer...")
				if err := recvStream.Send(&api.ReceiveVMRequest{
					Type: &api.ReceiveVMRequest_Complete{
						Complete: &api.ReceiveVMComplete{
							Checksum: v.Complete.Checksum,
						},
					},
				}); err != nil {
					if recvErr := recvTargetErr(recvStream); recvErr != nil {
						return fmt.Errorf("target error: %w", recvErr)
					}
					return fmt.Errorf("failed to send complete: %w", err)
				}
			}
		}

		// Close send direction on target to signal we're done
		if err := recvStream.CloseSend(); err != nil {
			return fmt.Errorf("failed to close send: %w", err)
		}

		// Receive result from target
		for {
			recvResp, err := recvStream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("failed to receive result: %w", err)
			}

			if result := recvResp.GetResult(); result != nil {
				if result.Error != "" {
					return fmt.Errorf("migration failed: %s", result.Error)
				}
				sp.Update("migration complete")
				break
			}
		}

		// Delete from source if requested
		if deleteAfter {
			sp.Update("deleting from source...")
			if _, err := sourceClient.DeleteInstance(ctx, &api.DeleteInstanceRequest{ID: instanceID}); err != nil {
				return fmt.Errorf("migration succeeded but failed to delete from source: %w", err)
			}
		}

		sp.Final(fmt.Sprintf("migrated %s to %s (%s transferred)", instanceID, targetAddr, humanize.Bytes(totalBytes)))

		return nil
	},
}

// recvTargetErr attempts to retrieve the server-side error from a ReceiveVM
// stream after a Send failure. It uses a short timeout to avoid blocking
// forever if the server is unreachable.
func recvTargetErr(stream api.ComputeService_ReceiveVMClient) error {
	ch := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		ch <- err
	}()
	select {
	case err := <-ch:
		if err != nil && err != io.EOF {
			return err
		}
		return nil
	case <-time.After(5 * time.Second):
		return nil
	}
}
