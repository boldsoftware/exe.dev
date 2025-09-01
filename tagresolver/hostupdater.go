package tagresolver

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"exe.dev/sqlite"
	"exe.dev/sshpool"
)

// HostUpdater manages image updates on container hosts
type HostUpdater struct {
	db       *sqlite.DB
	resolver *TagResolver
	sshPool  *sshpool.Pool
	hosts    []string // List of container host addresses

	mu           sync.RWMutex
	pullInFlight map[string]bool // Track pulls in progress per digest

	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewHostUpdater creates a new host updater
func NewHostUpdater(db *sqlite.DB, resolver *TagResolver, hosts []string) *HostUpdater {
	return &HostUpdater{
		db:           db,
		resolver:     resolver,
		sshPool:      sshpool.New(),
		hosts:        hosts,
		pullInFlight: make(map[string]bool),
		stopChan:     make(chan struct{}),
	}
}

// Start begins listening for tag updates and updating hosts
func (hu *HostUpdater) Start(ctx context.Context) {
	hu.wg.Add(1)
	go hu.updateLoop(ctx)
}

// Stop stops the host updater
func (hu *HostUpdater) Stop() {
	close(hu.stopChan)
	hu.wg.Wait()
	hu.sshPool.Close()
}

// updateLoop listens for tag updates and triggers host updates
func (hu *HostUpdater) updateLoop(ctx context.Context) {
	defer hu.wg.Done()

	updateChan := hu.resolver.GetUpdateChannel()

	for {
		select {
		case <-ctx.Done():
			return
		case <-hu.stopChan:
			return
		case update := <-updateChan:
			// Handle update asynchronously
			go hu.handleTagUpdate(ctx, update)
		}
	}
}

// handleTagUpdate processes a tag update by pulling the new image on all hosts
func (hu *HostUpdater) handleTagUpdate(ctx context.Context, update TagUpdate) {
	// Check if we're already pulling this digest
	hu.mu.Lock()
	if hu.pullInFlight[update.PlatformDigest] {
		hu.mu.Unlock()
		return
	}
	hu.pullInFlight[update.PlatformDigest] = true
	hu.mu.Unlock()

	// Clean up when done
	defer func() {
		hu.mu.Lock()
		delete(hu.pullInFlight, update.PlatformDigest)
		hu.mu.Unlock()
	}()

	log.Printf("Handling tag update for %s/%s:%s (new digest: %s)",
		update.Registry, update.Repository, update.Tag, update.PlatformDigest)

	// Build the image reference with digest
	imageRef := fmt.Sprintf("%s/%s@%s", update.Registry, update.Repository, update.PlatformDigest)

	// Normalize Docker Hub references
	if update.Registry == "docker.io" {
		if strings.HasPrefix(update.Repository, "library/") {
			// Official images don't need library/ prefix
			imageRef = fmt.Sprintf("%s@%s",
				strings.TrimPrefix(update.Repository, "library/"),
				update.PlatformDigest)
		} else {
			imageRef = fmt.Sprintf("%s@%s", update.Repository, update.PlatformDigest)
		}
	}

	// Pull on each host
	var wg sync.WaitGroup
	for _, host := range hu.hosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()

			if err := hu.pullImageOnHost(ctx, host, imageRef); err != nil {
				log.Printf("Failed to pull %s on host %s: %v", imageRef, host, err)
			} else {
				log.Printf("Successfully pulled %s on host %s", imageRef, host)

				// Increment seen_on_hosts counter
				hu.resolver.IncrementSeenOnHosts(ctx,
					update.Registry, update.Repository, update.Tag, update.Platform)
			}
		}(host)
	}

	// Wait for all pulls to complete
	wg.Wait()
}

// pullImageOnHost pulls an image on a specific host using nerdctl
func (hu *HostUpdater) pullImageOnHost(ctx context.Context, host, imageRef string) error {
	// Parse SSH host
	host = strings.TrimPrefix(host, "ssh://")
	if host == "" || strings.HasPrefix(host, "/") {
		return fmt.Errorf("invalid SSH host: %s", host)
	}

	// Build nerdctl pull command with Nydus snapshotter
	args := []string{
		"sudo", "nerdctl", "--namespace", "exe",
		"--snapshotter", "nydus",
		"pull", imageRef,
	}

	// Execute pull command
	cmd := hu.sshPool.ExecCommand(ctx, host, args...)

	// Set a timeout for the pull operation
	pullCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's just because the image already exists
		if strings.Contains(string(output), "already exists") {
			log.Printf("Image %s already exists on host %s", imageRef, host)
			return nil
		}
		return fmt.Errorf("pull failed: %w: %s", err, output)
	}

	// Optionally trigger Nydus prefetch for hot paths
	// This would require knowing which paths are hot for specific images
	if shouldPrefetch(imageRef) {
		hu.prefetchImage(pullCtx, host, imageRef)
	}

	return nil
}

// prefetchImage triggers Nydus prefetch for commonly accessed files
func (hu *HostUpdater) prefetchImage(ctx context.Context, host, imageRef string) {
	// This is a placeholder for Nydus prefetch logic
	// In practice, you'd use nydusify or similar tools to optimize
	// For now, just log that we would prefetch
	log.Printf("Would prefetch hot paths for %s on host %s", imageRef, host)
}

// shouldPrefetch determines if an image should have its hot paths prefetched
func shouldPrefetch(imageRef string) bool {
	// Prefetch common base images that are frequently used
	commonImages := []string{
		"ubuntu", "debian", "alpine", "python", "node", "golang", "rust",
		"exeuntu", "ghcr.io/boldsoftware/exeuntu",
	}

	for _, img := range commonImages {
		if strings.Contains(imageRef, img) {
			return true
		}
	}

	return false
}

// PullImageByDigest pulls a specific image by digest on all hosts
func (hu *HostUpdater) PullImageByDigest(ctx context.Context, registry, repository, digest string) error {
	imageRef := fmt.Sprintf("%s/%s@%s", registry, repository, digest)

	// Normalize Docker Hub references
	if registry == "docker.io" {
		if strings.HasPrefix(repository, "library/") {
			imageRef = fmt.Sprintf("%s@%s",
				strings.TrimPrefix(repository, "library/"), digest)
		} else {
			imageRef = fmt.Sprintf("%s@%s", repository, digest)
		}
	}

	// Pull on each host in parallel
	var wg sync.WaitGroup
	var pullErr error
	var errMu sync.Mutex

	for _, host := range hu.hosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()

			if err := hu.pullImageOnHost(ctx, host, imageRef); err != nil {
				log.Printf("Failed to pull %s on host %s: %v", imageRef, host, err)
				errMu.Lock()
				if pullErr == nil {
					pullErr = err
				}
				errMu.Unlock()
			}
		}(host)
	}

	wg.Wait()
	return pullErr
}

// EnsureImageOnHost ensures an image exists on a specific host
func (hu *HostUpdater) EnsureImageOnHost(ctx context.Context, host, imageRef string) error {
	// First check if image exists
	host = strings.TrimPrefix(host, "ssh://")

	checkArgs := []string{
		"sudo", "nerdctl", "--namespace", "exe",
		"image", "inspect", imageRef,
		"--format", "{{.RepoDigests}}",
	}

	cmd := hu.sshPool.ExecCommand(ctx, host, checkArgs...)
	if output, err := cmd.Output(); err == nil && len(output) > 0 {
		// Image exists
		return nil
	}

	// Image doesn't exist, pull it
	return hu.pullImageOnHost(ctx, host, imageRef)
}
