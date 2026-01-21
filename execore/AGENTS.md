# execore - SSH Command Handlers

This package contains the SSH command handlers for exe.dev.

## JSON Output Convention

When a command supports `--json` output:

1. **Success responses** should include all relevant data fields
2. **Error responses** should include an `"error"` key with the error message
3. Use `cc.WriteJSON()` for structured output
4. Use `cc.Errorf()` for errors that should halt execution - these are automatically formatted as `{"error": "..."}` in JSON mode

Example success response:
```json
{
  "vm_name": "my-box",
  "status": "running",
  "some_value": 123
}
```

Example error response (from `cc.Errorf()`):
```json
{
  "error": "VM not found"
}
```

Example partial success with warning:
```json
{
  "vm_name": "my-box",
  "volume_new_bytes": 10737418240,
  "error": "resize2fs failed: some error"
}
```

## Testing

SSH command handlers should have e1e tests that verify:
1. The command actually does what it claims (not just returns success)
2. Error cases are handled properly
3. JSON output includes all expected fields including errors
