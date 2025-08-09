package container

import (
	"context"
	"fmt"
	"strings"
)

// BuildImage builds a Docker image from a Dockerfile using Cloud Build for security isolation
func (m *GKEManager) BuildImage(ctx context.Context, req *BuildRequest) (*BuildResult, error) {
	// Validate Dockerfile before building
	if err := m.validateDockerfile(req.Dockerfile); err != nil {
		return nil, fmt.Errorf("dockerfile validation failed: %w", err)
	}

	if err := m.validateBaseImage(req.Dockerfile); err != nil {
		return nil, fmt.Errorf("base image validation failed: %w", err)
	}

	// For now, return a placeholder result
	// TODO: Implement actual Cloud Build integration
	imageName := fmt.Sprintf("%s/%s/user-%s:%s", 
		m.config.RegistryHost, 
		m.config.ProjectID,
		req.UserID,
		req.BuildID)

	return &BuildResult{
		BuildID:   req.BuildID,
		ImageName: imageName,
		Status:    "QUEUED",
		LogsURL:   fmt.Sprintf("https://console.cloud.google.com/cloud-build/builds/%s", req.BuildID),
	}, nil
}

// GetBuildStatus checks the status of a Cloud Build operation
func (m *GKEManager) GetBuildStatus(ctx context.Context, buildID string) (*BuildResult, error) {
	// TODO: Implement actual Cloud Build status checking
	return &BuildResult{
		BuildID: buildID,
		Status:  "SUCCESS", // Placeholder
	}, nil
}

// getBuildBucket returns the Cloud Storage bucket for build sources
func (m *GKEManager) getBuildBucket() string {
	// Default Cloud Build bucket naming convention
	return fmt.Sprintf("%s_cloudbuild", m.config.ProjectID)
}

// validateDockerfile performs basic security validation on Dockerfiles
func (m *GKEManager) validateDockerfile(dockerfile string) error {
	lines := strings.Split(dockerfile, "\n")
	
	for _, line := range lines {
		line = strings.TrimSpace(strings.ToUpper(line))
		
		// Basic security checks - prevent dangerous operations
		if strings.Contains(line, "--PRIVILEGED") {
			return fmt.Errorf("privileged containers not allowed")
		}
		
		if strings.Contains(line, "USER ROOT") || strings.Contains(line, "USER 0") {
			return fmt.Errorf("running as root not allowed")
		}
		
		// Prevent network access during build that could be used for attacks
		if strings.Contains(line, "CURL") && strings.Contains(line, "HTTP") {
			return fmt.Errorf("HTTP requests during build may not be secure")
		}
		
		// Block attempts to escape the container
		if strings.Contains(line, "/PROC") || strings.Contains(line, "/SYS") {
			return fmt.Errorf("accessing system directories not allowed")
		}
	}
	
	// Require a non-root user
	if !strings.Contains(strings.ToUpper(dockerfile), "USER ") {
		return fmt.Errorf("Dockerfile must specify a non-root USER")
	}
	
	return nil
}

// Security: List of allowed base images to prevent malicious images
var allowedBaseImages = map[string]bool{
	"ubuntu":        true,
	"debian":        true,
	"alpine":        true,
	"python":        true,
	"node":          true,
	"golang":        true,
	"openjdk":       true,
	"nginx":         true,
	"redis":         true,
	"postgres":      true,
	"mysql":         true,
	"mongo":         true,
}

// validateBaseImage ensures only trusted base images are used
func (m *GKEManager) validateBaseImage(dockerfile string) error {
	lines := strings.Split(dockerfile, "\n")
	
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			// Extract the base image
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				image := parts[1]
				
				// Remove tag/version if present
				if colonIdx := strings.Index(image, ":"); colonIdx != -1 {
					image = image[:colonIdx]
				}
				
				// Remove registry if present
				if slashIdx := strings.LastIndex(image, "/"); slashIdx != -1 {
					image = image[slashIdx+1:]
				}
				
				if !allowedBaseImages[image] {
					return fmt.Errorf("base image %s is not allowed", image)
				}
			}
		}
	}
	
	return nil
}