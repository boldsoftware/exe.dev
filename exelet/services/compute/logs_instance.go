package compute

import (
	"bytes"
	"errors"
	"io"
	"os"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (s *Service) GetInstanceLogs(req *api.GetInstanceLogsRequest, stream api.ComputeService_GetInstanceLogsServer) error {
	ctx := stream.Context()
	logging.AddFields(ctx, logging.Fields{"container_id", req.ID})

	// ensure instance exists
	resp, err := s.GetInstance(ctx, &api.GetInstanceRequest{ID: req.ID})
	if err != nil {
		// Check if instance not found
		if st, ok := status.FromError(err); ok && st.Code() == codes.Internal {
			// If the internal error suggests file missing, treat as NotFound
			if os.IsNotExist(errors.Unwrap(err)) {
				return status.Error(codes.NotFound, "instance not found")
			}
		}
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	instance := resp.Instance

	r, err := s.vmm.Logs(ctx, instance.ID)
	if err != nil {
		// If log file doesn't exist, instance might be gone/crashed
		if os.IsNotExist(err) {
			return status.Error(codes.NotFound, "instance logs not found")
		}
		return status.Error(codes.Internal, err.Error())
	}
	defer r.Close()

	doneCh := make(chan struct{})
	errCh := make(chan error)

	// send to server - read in chunks to handle sparse files with null byte holes
	go func() {
		defer close(doneCh)

		buf := make([]byte, 64*1024) // 64KB chunks
		for {
			n, err := r.Read(buf)
			if n > 0 {
				// Filter out null bytes (sparse file holes)
				chunk := bytes.ReplaceAll(buf[:n], []byte{0}, nil)
				if len(chunk) > 0 {
					if sendErr := stream.Send(&api.GetInstanceLogsResponse{
						Log: &api.Log{
							Type:    api.Log_STDOUT,
							Message: string(chunk),
						},
					}); sendErr != nil {
						errCh <- sendErr
						return
					}
				}
			}
			if err != nil {
				if err != io.EOF {
					errCh <- err
				}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != nil {
			return err
		}
	case err := <-errCh:
		return err
	case <-doneCh:
	}

	return nil
}
