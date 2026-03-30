package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"shelley.exe.dev/llm"
)

// handleMessageImage serves an image extracted from a message's llm_data.
// Route: GET /api/message/{message_id}/image/{content_index}/{toolresult_index}
//
// The image data is stored as base64 in the llm_data JSON. This endpoint
// decodes it and serves the raw image bytes with proper Content-Type and
// cache headers. This allows the UI to load images on demand instead of
// receiving all base64 blobs in the conversation JSON.
func (s *Server) handleMessageImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	messageID := r.PathValue("message_id")
	contentIdxStr := r.PathValue("content_index")
	trIdxStr := r.PathValue("toolresult_index")

	contentIdx, err := strconv.Atoi(contentIdxStr)
	if err != nil {
		http.Error(w, "invalid content_index", http.StatusBadRequest)
		return
	}
	trIdx, err := strconv.Atoi(trIdxStr)
	if err != nil {
		http.Error(w, "invalid toolresult_index", http.StatusBadRequest)
		return
	}

	// Fetch message from DB
	msg, err := s.db.GetMessageByID(r.Context(), messageID)
	if err != nil {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}

	if msg.LlmData == nil {
		http.Error(w, "message has no llm_data", http.StatusNotFound)
		return
	}

	// Parse llm_data to find the image
	var llmMsg llm.Message
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		http.Error(w, "failed to parse message data", http.StatusInternalServerError)
		return
	}

	if contentIdx < 0 || contentIdx >= len(llmMsg.Content) {
		http.Error(w, "content_index out of range", http.StatusNotFound)
		return
	}

	// Navigate to the image content
	var imageContent *llm.Content
	content := &llmMsg.Content[contentIdx]

	if trIdx >= 0 {
		// Image is inside a ToolResult
		if trIdx >= len(content.ToolResult) {
			http.Error(w, "toolresult_index out of range", http.StatusNotFound)
			return
		}
		imageContent = &content.ToolResult[trIdx]
	} else {
		imageContent = content
	}

	if imageContent.MediaType == "" || imageContent.Data == "" {
		http.Error(w, "no image data at this index", http.StatusNotFound)
		return
	}

	// Decode base64 image data
	imageBytes, err := base64.StdEncoding.DecodeString(imageContent.Data)
	if err != nil {
		http.Error(w, "failed to decode image data", http.StatusInternalServerError)
		return
	}

	// Serve with aggressive caching — message images are immutable
	w.Header().Set("Content-Type", imageContent.MediaType)
	w.Header().Set("Cache-Control", "public, max-age=1209600")
	w.Header().Set("Content-Length", strconv.Itoa(len(imageBytes)))
	w.Write(imageBytes)
}

// stripImageDataFromLLMData removes base64 image data from llm_data JSON and
// replaces it with a URL pointing to the /api/message/{id}/image endpoint.
// This dramatically reduces response sizes for conversations with screenshots.
func stripImageDataFromLLMData(llmData *string, messageID string) *string {
	if llmData == nil {
		return nil
	}
	var msg llm.Message
	if err := json.Unmarshal([]byte(*llmData), &msg); err != nil {
		return llmData
	}
	if !stripImageDataFromContents(msg.Content, messageID) {
		return llmData
	}
	stripped, err := json.Marshal(msg)
	if err != nil {
		return llmData
	}
	s := string(stripped)
	return &s
}

// imageURL builds the URL for an image served from the DB.
func imageURL(messageID string, contentIdx, trIdx int) string {
	return fmt.Sprintf("/api/message/%s/image/%d/%d", messageID, contentIdx, trIdx)
}

// stripImageDataFromContents removes Data from content items that have a MediaType
// (i.e., image content) and replaces it with a placeholder URL in the Text field.
// Returns true if any data was stripped.
func stripImageDataFromContents(contents []llm.Content, messageID string) bool {
	changed := false
	for i := range contents {
		if contents[i].MediaType != "" && contents[i].Data != "" {
			// Replace inline data with a URL to the image endpoint.
			// Use trIdx=-1 for top-level content (not inside ToolResult).
			contents[i].DisplayImageURL = imageURL(messageID, i, -1)
			contents[i].Data = ""
			changed = true
		}
		// Recurse into tool results
		for j := range contents[i].ToolResult {
			tr := &contents[i].ToolResult[j]
			if tr.MediaType != "" && tr.Data != "" {
				tr.DisplayImageURL = imageURL(messageID, i, j)
				tr.Data = ""
				changed = true
			}
		}
	}
	return changed
}
