package sshproxy

import (
	"os"
	"os/exec"
)

// scpCommand creates an scp command with all necessary options to avoid prompts
func scpCommand(args ...string) *exec.Cmd {
	// Always prepend the necessary SSH options
	scpArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "PasswordAuthentication=no",
		"-o", "PubkeyAuthentication=no",
		"-o", "LogLevel=ERROR", // Suppress warnings
	}
	scpArgs = append(scpArgs, args...)
	
	cmd := exec.Command("scp", scpArgs...)
	// Set environment to avoid any SSH agent issues
	cmd.Env = append(os.Environ(),
		"SSH_AUTH_SOCK=", // Disable SSH agent
	)
	return cmd
}

// sftpCommand creates an sftp command with all necessary options to avoid prompts
func sftpCommand(args ...string) *exec.Cmd {
	// Always prepend the necessary SSH options
	sftpArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "PasswordAuthentication=no",
		"-o", "PreferredAuthentications=none",
		"-o", "LogLevel=ERROR",
	}
	sftpArgs = append(sftpArgs, args...)
	
	cmd := exec.Command("sftp", sftpArgs...)
	cmd.Env = append(os.Environ(),
		"SSH_AUTH_SOCK=", // Disable SSH agent
	)
	return cmd
}

// sshCommand creates an ssh command with all necessary options to avoid prompts
func sshCommand(args ...string) *exec.Cmd {
	// Always prepend the necessary SSH options
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "PasswordAuthentication=no",
		"-o", "PreferredAuthentications=none",
		"-o", "LogLevel=ERROR",
	}
	sshArgs = append(sshArgs, args...)
	
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Env = append(os.Environ(),
		"SSH_AUTH_SOCK=", // Disable SSH agent
	)
	return cmd
}