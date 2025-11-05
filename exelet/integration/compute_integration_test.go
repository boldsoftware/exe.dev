package integration

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"exe.dev/exelet/integration/helpers"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func TestComputeCreateAlpine(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("exelet: skipping CI testing for now")
	}
	c, err := helpers.GetClient()
	assert.NoError(t, err, "error getting client")
	defer c.Close()

	ctx, cancel := helpers.GetContext()
	defer cancel()

	testInstanceName := fmt.Sprintf("test-%d", time.Now().UnixNano())
	testInstanceImage := "docker.io/library/alpine:latest"

	stream, err := c.CreateInstance(ctx, &api.CreateInstanceRequest{
		Name:   testInstanceName,
		Image:  testInstanceImage,
		CPUs:   1,
		Memory: 1 * 1000 * 1000 * 1000,
		Disk:   1 * 1000 * 1000 * 1000,
	})
	if err != nil {
		t.Fatalf("error creating instance: %v", err)
	}
	assert.NotNil(t, stream)
	instanceID := ""
	for {
		resp, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		switch v := resp.Type.(type) {
		case *api.CreateInstanceResponse_Instance:
			instanceID = v.Instance.ID
		}
	}

	// cleanup
	_, err = c.DeleteInstance(ctx, &api.DeleteInstanceRequest{
		ID: instanceID,
	})
	assert.NoError(t, err, "error deleting instance")
}

func TestComputeCreateValidateOutput(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("exelet: skipping CI testing for now")
	}
	c, err := helpers.GetClient()
	assert.NoError(t, err, "error getting client")
	defer c.Close()

	ctx, cancel := helpers.GetContext()
	defer cancel()

	testInstanceName := fmt.Sprintf("test-%d", time.Now().UnixNano())
	testInstanceImage := "docker.io/library/alpine:latest"

	stream, err := c.CreateInstance(ctx, &api.CreateInstanceRequest{
		Name:   testInstanceName,
		Image:  testInstanceImage,
		CPUs:   1,
		Memory: 1 * 1000 * 1000 * 1000,
		Disk:   1 * 1000 * 1000 * 1000,
	})
	if err != nil {
		t.Fatalf("error creating instance: %v", err)
	}
	assert.NoError(t, err, "error creating instance")
	assert.NotNil(t, stream)
	instanceID := ""
	for {
		resp, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		switch v := resp.Type.(type) {
		case *api.CreateInstanceResponse_Instance:
			instanceID = v.Instance.ID
			break
		}
	}

	// check output
	wCtx, cancel := context.WithTimeout(ctx, time.Second*20)
	defer cancel()
	waitErr := helpers.WaitForOutput(wCtx, instanceID, "exe-init as init")
	assert.NoError(t, waitErr)

	// cleanup
	_, err = c.DeleteInstance(ctx, &api.DeleteInstanceRequest{
		ID: instanceID,
	})
	assert.NoError(t, err, "error deleting instance")
}

func TestComputeCreateValidateRedis(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("exelet: skipping CI testing for now")
	}
	c, err := helpers.GetClient()
	assert.NoError(t, err, "error getting client")
	defer c.Close()

	ctx, cancel := helpers.GetContext()
	defer cancel()

	testInstanceName := fmt.Sprintf("test-%d", time.Now().UnixNano())
	testInstanceImage := "docker.io/library/redis:latest"

	stream, err := c.CreateInstance(ctx, &api.CreateInstanceRequest{
		Name:   testInstanceName,
		Image:  testInstanceImage,
		CPUs:   1,
		Memory: 1 * 1000 * 1000 * 1000,
		Disk:   1 * 1000 * 1000 * 1000,
	})
	if err != nil {
		t.Fatalf("error creating instance: %v", err)
	}
	assert.NotNil(t, stream)
	instanceID := ""
	for {
		resp, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		switch v := resp.Type.(type) {
		case *api.CreateInstanceResponse_Instance:
			instanceID = v.Instance.ID
		}
	}

	// check output
	wCtx, cancel := context.WithTimeout(ctx, time.Second*20)
	defer cancel()
	waitErr := helpers.WaitForOutput(wCtx, instanceID, "Redis Open Source")
	assert.NoError(t, waitErr)

	// cleanup
	_, err = c.DeleteInstance(ctx, &api.DeleteInstanceRequest{
		ID: instanceID,
	})
	assert.NoError(t, err, "error deleting instance")
}
