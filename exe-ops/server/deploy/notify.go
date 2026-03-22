package deploy

// Notifier receives deploy lifecycle events. Implementations must be
// safe for concurrent use; notifications are best-effort and errors
// are logged but never block the deploy.
type Notifier interface {
	// DeployStarted is called when a deploy begins executing.
	DeployStarted(Status)

	// DeployFinished is called when a deploy completes or fails.
	DeployFinished(Status)
}
