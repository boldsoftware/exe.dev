package exe

import (
	"context"
	"strconv"
	"strings"
	"time"

	"exe.dev/sshbuf"
)

// TerminalMode represents whether the terminal is in dark or light mode
type TerminalMode int

const (
	TerminalModeDark TerminalMode = iota
	TerminalModeLight
)

// detectTerminalMode queries the terminal background color using OSC 11 and determines if it's dark or light mode
func (s *Server) detectTerminalMode(channel *sshbuf.Channel) TerminalMode {
	// Send OSC 11 query for background color
	query := []byte("\033]11;?\033\\")
	if _, err := channel.Write(query); err != nil {
		return TerminalModeDark
	}

	// Read response with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Buffer to collect potential OSC response
	var buffer []byte
	temp := make([]byte, 1)
	state := "initial" // States: initial, saw_esc, saw_bracket, in_osc, done

	for {
		n, err := channel.ReadCtx(ctx, temp)
		if err != nil || n == 0 {
			// Timeout or error - put back any non-OSC data we collected
			if len(buffer) > 0 && !isOSCResponse(buffer) {
				channel.Unread(buffer)
			}
			return TerminalModeDark
		}

		b := temp[0]
		buffer = append(buffer, b)

		switch state {
		case "initial":
			if b == '\033' {
				state = "saw_esc"
			} else {
				// Not an OSC response - this is user input!
				// Put it back and return default
				channel.Unread(buffer)
				return TerminalModeDark
			}

		case "saw_esc":
			if b == ']' {
				state = "in_osc"
			} else {
				// Not an OSC sequence - put back and return
				channel.Unread(buffer)
				return TerminalModeDark
			}

		case "in_osc":
			// Look for OSC terminator
			if b == '\033' {
				// Might be ESC \ terminator
				state = "saw_esc_in_osc"
			} else if b == '\007' {
				// BEL terminator - we have complete OSC response
				state = "done"
			}

		case "saw_esc_in_osc":
			if b == '\\' {
				// Found ESC \ terminator
				state = "done"
			} else {
				// Continue in OSC
				state = "in_osc"
			}
		}

		if state == "done" {
			// We got a complete OSC response
			response := string(buffer)
			return parseBackgroundColor(response)
		}

		// Safety check - don't read too much
		if len(buffer) > 100 {
			// Too much data, probably not a valid OSC response
			// Put back everything that's not OSC-like
			if !isOSCResponse(buffer) {
				channel.Unread(buffer)
			}
			return TerminalModeDark
		}
	}
}

func isOSCResponse(data []byte) bool {
	s := string(data)
	return strings.HasPrefix(s, "\033]")
}

// parseBackgroundColor parses the OSC 11 response and determines if it's dark or light
func parseBackgroundColor(response string) TerminalMode {
	// Look for the rgb: pattern in the response
	// Format can be: \033]11;rgb:RRRR/GGGG/BBBB\033\\ or with \007 (BEL) terminator

	// Find the rgb: part
	rgbIndex := strings.Index(response, "rgb:")
	if rgbIndex == -1 {
		return TerminalModeDark // Default to dark if we can't parse
	}

	// Extract the color values
	colorPart := response[rgbIndex+4:]

	// Find the terminator (either ESC \ or BEL)
	endIndex := strings.IndexAny(colorPart, "\033\007")
	if endIndex > 0 {
		colorPart = colorPart[:endIndex]
	}

	// Parse RGB values (format: RRRR/GGGG/BBBB where each component can be 1-4 hex digits)
	parts := strings.Split(colorPart, "/")
	if len(parts) != 3 {
		return TerminalModeDark // Default to dark if format is unexpected
	}

	// Convert hex values to integers and normalize to 0-255 range
	var rgb [3]int
	for i, part := range parts {
		// Parse as hex
		val, err := strconv.ParseInt(part, 16, 64)
		if err != nil {
			return TerminalModeDark
		}

		// Normalize to 0-255 range based on the number of hex digits
		// 1 digit: 0-F -> multiply by 17 (0x0 -> 0, 0xF -> 255)
		// 2 digits: 00-FF -> use as is
		// 3 digits: 000-FFF -> divide by 16
		// 4 digits: 0000-FFFF -> divide by 256
		switch len(part) {
		case 1:
			rgb[i] = int(val * 17)
		case 2:
			rgb[i] = int(val)
		case 3:
			rgb[i] = int(val / 16)
		case 4:
			rgb[i] = int(val / 256)
		default:
			return TerminalModeDark
		}
	}

	// Calculate luminance using the relative luminance formula
	// L = 0.2126 * R + 0.7152 * G + 0.0722 * B
	luminance := float64(rgb[0])*0.2126 + float64(rgb[1])*0.7152 + float64(rgb[2])*0.0722

	// If luminance > 128 (middle of 0-255 range), consider it light mode
	if luminance > 128 {
		return TerminalModeLight
	}

	return TerminalModeDark
}

// getTerminalColors returns appropriate colors based on terminal mode
func (s *Server) getTerminalColors(mode TerminalMode) struct {
	grayText    string
	fadeToColor string
	fadeSteps   []struct {
		color string
		delay time.Duration
	}
} {
	if mode == TerminalModeLight {
		// Light mode: use black text instead of gray, fade to white
		return struct {
			grayText    string
			fadeToColor string
			fadeSteps   []struct {
				color string
				delay time.Duration
			}
		}{
			grayText:    "\033[0;30m", // Black text for better contrast
			fadeToColor: "\033[37m",   // White
			fadeSteps: []struct {
				color string
				delay time.Duration
			}{
				{"\033[1;32m", 500 * time.Millisecond},     // Bright green
				{"\033[0;32m", 200 * time.Millisecond},     // Normal green
				{"\033[2;32m", 150 * time.Millisecond},     // Dim green
				{"\033[38;5;114m", 150 * time.Millisecond}, // Light green
				{"\033[38;5;150m", 150 * time.Millisecond}, // Lighter green
				{"\033[38;5;194m", 100 * time.Millisecond}, // Very light green
				{"\033[37m", 100 * time.Millisecond},       // White (invisible on light bg)
			},
		}
	}

	// Dark mode: use existing gray text and fade to black
	return struct {
		grayText    string
		fadeToColor string
		fadeSteps   []struct {
			color string
			delay time.Duration
		}
	}{
		grayText:    "\033[2;37m", // Gray text (existing)
		fadeToColor: "\033[30m",   // Black
		fadeSteps: []struct {
			color string
			delay time.Duration
		}{
			{"\033[1;32m", 500 * time.Millisecond},    // Bright green
			{"\033[0;32m", 200 * time.Millisecond},    // Normal green
			{"\033[2;32m", 150 * time.Millisecond},    // Dim green
			{"\033[38;5;28m", 150 * time.Millisecond}, // Dark green
			{"\033[38;5;22m", 150 * time.Millisecond}, // Darker green
			{"\033[38;5;16m", 100 * time.Millisecond}, // Very dark
			{"\033[30m", 100 * time.Millisecond},      // Black (invisible on dark bg)
		},
	}
}

// clearOSCResponse clears any remaining OSC response from the input buffer
// This function is now deprecated and does nothing to prevent consuming user input
func (s *Server) clearOSCResponse(channel *sshbuf.Channel) {
	// This function used to consume input aggressively, causing the bug where
	// the first two characters of email input were lost during signup.
	//
	// The fix is to not clear the OSC response aggressively since:
	// 1. detectTerminalMode() already reads the OSC response with proper timeout
	// 2. Any remaining OSC data is harmless and will be ignored by readLineFromChannel
	// 3. Aggressively reading here risks consuming user input that arrives quickly
	//
	// If there are any remaining OSC bytes, they will be handled naturally by
	// the input processing logic which knows how to distinguish between
	// escape sequences and user input.
}

// getGrayText returns the appropriate gray/black text color based on terminal mode
func (s *Server) getGrayText(channel *sshbuf.Channel) string {
	mode := s.detectTerminalMode(channel)
	s.clearOSCResponse(channel)

	if mode == TerminalModeLight {
		return "\033[0;30m" // Black text for light terminals
	}
	return "\033[2;37m" // Gray text for dark terminals
}
