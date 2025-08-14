package container

import "strings"

// ExpandImageName expands short image names to full paths
func ExpandImageName(image string) string {
	// If no tag is specified, add :latest
	if !strings.Contains(image, ":") && !strings.Contains(image, "@") {
		image += ":latest"
	}
	
	// Expand common short names
	switch {
	case image == "exeuntu" || image == "exeuntu:latest":
		// Use the public GitHub Container Registry image from Bold Software org
		return "ghcr.io/boldsoftware/exeuntu:latest"
	case image == "ubuntu" || image == "ubuntu:latest":
		return "ubuntu:22.04"
	case image == "debian" || image == "debian:latest":
		return "debian:bookworm"
	case image == "alpine" || image == "alpine:latest":
		return "alpine:latest"
	case image == "python" || image == "python:latest":
		return "python:3.11"
	case image == "node" || image == "node:latest":
		return "node:20"
	case image == "golang" || image == "golang:latest":
		return "golang:1.21"
	case image == "rust" || image == "rust:latest":
		return "rust:latest"
	}
	
	return image
}

// GetDisplayImageName returns a user-friendly display name for an image
func GetDisplayImageName(image string) string {
	// Remove registry prefix for cleaner display
	parts := strings.Split(image, "/")
	if len(parts) > 1 {
		// Check if first part looks like a registry
		if strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") {
			// Remove registry prefix
			image = strings.Join(parts[1:], "/")
		}
	}
	
	// Simplify common images
	switch image {
	case "ghcr.io/boldsoftware/exeuntu:latest", "exeuntu:latest", "exeuntu":
		return "exeuntu"
	case "ubuntu:24.04", "ubuntu:22.04", "ubuntu:latest":
		return "ubuntu"
	case "debian:bookworm", "debian:latest":
		return "debian"
	case "alpine:latest":
		return "alpine"
	case "python:3.11", "python:latest":
		return "python"
	case "node:20", "node:latest":
		return "node"
	case "golang:1.21", "golang:latest":
		return "golang"
	case "rust:latest":
		return "rust"
	case "nginx:latest":
		return "nginx"
	}
	
	// Remove :latest suffix for cleaner display
	if strings.HasSuffix(image, ":latest") {
		return strings.TrimSuffix(image, ":latest")
	}
	
	return image
}