# Shelley UI Changes - Model Selection and CWD Support

## Summary
This branch implements the following changes to Shelley's UI and backend:

### UI Changes
1. **Model Selection for Empty Conversations**: When there's no active conversation, the status bar now displays a dropdown to select which model to use for the new conversation.

2. **Working Directory (CWD) Input**: For empty conversations, users can now specify a working directory where tools (bash, patch, etc.) will operate. This allows Shelley to work in directories other than where it was started.

3. **Settings Dialog Removed**: The settings modal has been removed from the overflow menu (the "..." menu). Configuration options are now inline in the status bar for empty conversations, making them more discoverable and contextual.

### Backend Changes
1. **Database Schema**: Added `cwd` column to the `conversations` table (migration 006) to store the working directory per conversation.

2. **API Changes**: The `ChatRequest` type now includes an optional `cwd` field that is passed when creating new conversations.

3. **Tool Context**: The working directory is now passed through the context to tools:
   - Added `WorkingDir` field to `loop.Config` and `Loop` struct
   - Tools receive the working directory via `claudetool.WithWorkingDir(ctx, wd)`
   - `BashTool` and `PatchTool` now use the working directory from context if available

4. **Conversation Management**: The `ConversationManager` loads and stores the `cwd` from the database and passes it to the loop when executing tools.

## Testing
The changes have been tested with a local server instance. The UI correctly displays:
- Model selector (when models are configured)
- Directory input field with placeholder text
- Both controls only appear for empty conversations
- Settings dialog is no longer accessible from the overflow menu

## Files Changed
- Frontend: `ui/src/App.tsx`, `ui/src/components/ChatInterface.tsx`, `ui/src/types.ts`, `ui/src/styles.css`
- Backend: `server/handlers.go`, `server/convo.go`, `loop/loop.go`, `claudetool/bash.go`, `claudetool/patch.go`
- Database: `db/schema/006-add-cwd.sql`, `db/query/conversations.sql`, `db/db.go`, `db/generated/*.go`
- CLI: `cmd/shelley/main.go`

## Branch
The changes are on the `shelley-ui-changes` branch in the second worktree at `~/exe-shelley-ui`.
