package exeweb

const (
	VMPushMaxTitleLen    = 200       // Maximum title length in characters
	VMPushMaxBodyLen     = 4 * 1024  // Maximum body length (4KB)
	VMPushMaxRequestSize = 64 * 1024 // Maximum request size (64KB)
)

// VMPushRequest is the JSON request body for sending push notifications from a VM.
type VMPushRequest struct {
	Title string            `json:"title"`
	Body  string            `json:"body"`
	Data  map[string]string `json:"data,omitempty"`
}

// VMPushResponse is the JSON response for the push endpoint.
type VMPushResponse struct {
	Success bool   `json:"success,omitempty"`
	Sent    int    `json:"sent,omitempty"`
	Error   string `json:"error,omitempty"`
}
