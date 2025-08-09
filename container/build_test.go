package container

import (
	"testing"
)

func TestValidateDockerfile(t *testing.T) {
	manager := &GKEManager{}

	tests := []struct {
		name       string
		dockerfile string
		wantErr    bool
	}{
		{
			name: "valid dockerfile",
			dockerfile: `FROM ubuntu:latest
RUN apt-get update
USER 1000
WORKDIR /app`,
			wantErr: false,
		},
		{
			name: "privileged container",
			dockerfile: `FROM ubuntu:latest
RUN docker run --privileged
USER 1000`,
			wantErr: true,
		},
		{
			name: "root user",
			dockerfile: `FROM ubuntu:latest
RUN apt-get update
USER root`,
			wantErr: true,
		},
		{
			name: "no user specified",
			dockerfile: `FROM ubuntu:latest
RUN apt-get update
WORKDIR /app`,
			wantErr: true,
		},
		{
			name: "system directory access",
			dockerfile: `FROM ubuntu:latest
RUN ls /proc
USER 1000`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.validateDockerfile(tt.dockerfile)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDockerfile() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateBaseImage(t *testing.T) {
	manager := &GKEManager{}

	tests := []struct {
		name       string
		dockerfile string
		wantErr    bool
	}{
		{
			name: "allowed ubuntu",
			dockerfile: `FROM ubuntu:latest
USER 1000`,
			wantErr: false,
		},
		{
			name: "allowed python with registry",
			dockerfile: `FROM python:3.9
USER 1000`,
			wantErr: false,
		},
		{
			name: "disallowed custom image",
			dockerfile: `FROM malicious/image:latest
USER 1000`,
			wantErr: true,
		},
		{
			name: "allowed golang with full registry path",
			dockerfile: `FROM gcr.io/distroless/golang:latest
USER 1000`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.validateBaseImage(tt.dockerfile)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBaseImage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetBuildBucket(t *testing.T) {
	config := &Config{
		ProjectID: "test-project-123",
	}
	manager := &GKEManager{config: config}

	bucket := manager.getBuildBucket()
	expected := "test-project-123_cloudbuild"

	if bucket != expected {
		t.Errorf("getBuildBucket() = %s, want %s", bucket, expected)
	}
}