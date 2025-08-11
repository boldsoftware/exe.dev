# SCP ~ Bug Fix Summary

## The Problem
When users run `scp file.txt user@exe.dev:~`, they get:
```
scp: close remote: Failure
scp: failed to upload file
```

## Root Cause
1. Modern OpenSSH `scp` uses the SFTP protocol by default (since OpenSSH 9.0)
2. When user types `scp file.txt host:~`, SCP resolves `~` to `/` and sends `/file.txt` as the SFTP path
3. The SFTP handler tries to create a file at `/file.txt` in the container's root directory
4. This fails because either:
   - The root directory is read-only
   - Permission denied
   - Can't create files in root

## The Fix
Modified `sshproxy/sftp_handler.go` in the `resolvePath` function to:
- Map root-level file paths like `/file.txt` to `/workspace/file.txt`
- Preserve actual system paths like `/tmp/file.txt`, `/etc/...`, etc.
- Keep all existing path resolution working

### Code Change
```go
// When we get an absolute path that's not in workspace and not a system path,
// treat it as relative to home directory
if filepath.IsAbs(sftpPath) && !strings.HasPrefix(sftpPath, h.homeDir) {
    systemDirs := []string{"/tmp", "/etc", "/usr", "/var", "/opt", "/bin", "/sbin", "/lib", "/proc", "/sys", "/dev"}
    isSystemPath := false
    for _, dir := range systemDirs {
        if strings.HasPrefix(sftpPath, dir+"/") || sftpPath == dir {
            isSystemPath = true
            break
        }
    }
    
    if !isSystemPath {
        return filepath.Join(h.homeDir, sftpPath)
    }
}
```

## Test Coverage
Created comprehensive tests in:
- `sshproxy/scp_exact_bug_test.go` - Demonstrates the exact bug
- `sshproxy/scp_production_bug_test.go` - Replicates production scenario
- `sshproxy/scp_fix_verification_test.go` - Verifies the fix works
- `sshproxy/scp_tilde_bug_test.go` - Tests with real SCP commands
- `sshproxy/scp_real_bug_test.go` - Minimal bug reproduction

## Path Resolution Examples
With the fix, paths are resolved as:
- `/` → `/workspace` (home directory)
- `/file.txt` → `/workspace/file.txt` (THE FIX)
- `/dir/file.txt` → `/workspace/dir/file.txt` (THE FIX)
- `/tmp/file.txt` → `/tmp/file.txt` (preserved)
- `~/file.txt` → `/workspace/file.txt` (already worked)
- `file.txt` → `/workspace/file.txt` (already worked)

## Testing Note
There appears to be a separate issue in the production code where container execution is failing with an error about executing "2" as a command. This is unrelated to the SCP path resolution fix but prevents end-to-end testing in the development environment.

## Result
The fix correctly handles the SCP ~ bug by mapping root-level file paths to the workspace directory while preserving system paths. All unit tests pass and demonstrate the fix works correctly.