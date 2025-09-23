package container

import "strings"

// ExpandImageName expands short image names to full paths
// This is the original function that works with Docker
func ExpandImageName(image string) string {
	return expandImageNameInternal(image, false)
}

// ExpandImageNameForContainerd expands image names with full registry paths for containerd/ctr
func ExpandImageNameForContainerd(image string) string {
	return expandImageNameInternal(image, true)
}

// expandImageNameInternal is the internal implementation
func expandImageNameInternal(image string, forContainerd bool) string {
	// Handle local development case: sha256:{hash} -> local-dev@sha256:{hash}
	// This allows developers to reference locally built images by digest
	if strings.HasPrefix(image, "sha256:") {
		return image
	}

	// If no tag is specified, add :latest
	if !strings.Contains(image, ":") && !strings.Contains(image, "@") {
		image += ":latest"
	}

	// Resolve common short names to non-docker.io images (GHCR/Quay/ECR)
	switch image {
	case "exeuntu:latest":
		// Use the public GitHub Container Registry image from Bold Software org
		return "ghcr.io/boldsoftware/exeuntu:latest"
	case "ubuntu:latest":
		// Canonical doesn't publish to GHCR/Quay; use Canonical's public ECR
		return "public.ecr.aws/lts/ubuntu:24.04"
	case "debian:latest":
		return "ghcr.io/linuxcontainers/debian:bookworm"
	case "alpine:latest":
		return "ghcr.io/linuxcontainers/alpine:latest"
	case "python:latest":
		return "quay.io/sclorg/python-313"
	case "node:latest":
		return "quay.io/sclorg/nodejs-22" // -22 is LTS
	case "golang:latest":
		return "quay.io/sclorg/golang-1.25"
	case "rust:latest":
		return "ghcr.io/rust-lang/rust:latest"
	}

	// For containerd, add full registry paths
	if forContainerd {
		// If the image doesn't have a registry prefix, add docker.io/library/ or docker.io/
		if !strings.Contains(image, "/") {
			// Simple names like "alpine:latest" -> "docker.io/library/alpine:latest"
			return "docker.io/library/" + image
		}

		// If it has one slash but no registry domain, add docker.io/
		parts := strings.SplitN(image, "/", 2)
		if len(parts) == 2 && !strings.Contains(parts[0], ".") && !strings.Contains(parts[0], ":") && parts[0] != "localhost" {
			// e.g., "myuser/myimage" -> "docker.io/myuser/myimage"
			return "docker.io/" + image
		}
	}

	return image
}

// GetDisplayImageName returns a user-friendly display name for an image
func GetDisplayImageName(image string) string {
	// Handle local development images
	if strings.HasPrefix(image, "sha256:") {
		hash := strings.TrimPrefix(image, "sha256:")
		if len(hash) > 8 {
			hash = hash[:8]
		}
		return "local:" + hash
	}

	suffix := ""
	if strings.Contains(image, "@sha256:") {
		cutIdx := strings.Index(image, "@sha256:")
		hash := image[cutIdx+len("@sha256:"):]
		if len(hash) > 8 {
			hash = hash[:8]
		}
		image = image[:cutIdx]
		suffix = "@sha256:" + hash
	}

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
		return "exeuntu" + suffix
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

	return image + suffix
}
