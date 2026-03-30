package browse

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
)

// Screencast limits.
const (
	// ScreencastMaxFrames is the maximum number of frames before auto-stopping.
	ScreencastMaxFrames = 10000
	// ScreencastMaxDuration is the maximum duration before auto-stopping.
	ScreencastMaxDuration = 30 * time.Minute
	// ScreencastDir is the directory where screencast output files are stored.
	ScreencastDir = "/tmp/shelley-screencasts"
)

// screencastState holds the state of an active screencast recording.
type screencastState struct {
	mu         sync.Mutex
	active     bool
	starting   bool // true while screencastStart is in progress (prevents TOCTOU)
	sessionID  string
	outputPath string
	frameCount int
	startTime  time.Time
	stopTimer  *time.Timer

	// ffmpeg process — frames are piped directly to stdin.
	ffmpegCmd *exec.Cmd
	ffmpegIn  io.WriteCloser // ffmpeg's stdin pipe

	// ackCh sends frame session IDs to the ack goroutine.
	ackCh chan int64
	// stopCh is closed to signal the ack goroutine to stop.
	stopCh chan struct{}
	// stopped is closed by the ack goroutine when it exits.
	stopped chan struct{}
}

// handleScreencastFrame processes incoming screencast frame events.
// Called from handleBrowserEvent — must NOT call chromedp.Run (deadlock).
func (b *BrowseTools) handleScreencastFrame(e *page.EventScreencastFrame) {
	sc := &b.screencast
	sc.mu.Lock()
	if !sc.active {
		sc.mu.Unlock()
		return
	}

	// Check frame limit.
	if sc.frameCount >= ScreencastMaxFrames {
		log.Printf("screencast: max frames (%d) reached, will auto-stop", ScreencastMaxFrames)
		sc.mu.Unlock()
		// Full teardown in a goroutine (can't call chromedp.Run from here).
		go b.screencastStopInternal()
		return
	}

	sc.frameCount++
	ffmpegIn := sc.ffmpegIn
	ackCh := sc.ackCh
	sc.mu.Unlock()

	// Decode and pipe frame to ffmpeg outside the lock.
	data, err := base64.StdEncoding.DecodeString(e.Data)
	if err != nil {
		log.Printf("screencast: failed to decode frame: %v", err)
	} else if ffmpegIn != nil {
		if _, err := ffmpegIn.Write(data); err != nil {
			log.Printf("screencast: failed to write frame to ffmpeg: %v", err)
		}
	}

	// Send ack to background goroutine (non-blocking).
	select {
	case ackCh <- e.SessionID:
	default:
	}
}

// screencastAckLoop runs in a goroutine and acks screencast frames.
// It stops the CDP screencast and exits when stopCh is closed.
func (b *BrowseTools) screencastAckLoop(browserCtx context.Context, ackCh chan int64, stopCh, stopped chan struct{}) {
	defer close(stopped)
	for {
		select {
		case sessionID := <-ackCh:
			if err := chromedp.Run(browserCtx, page.ScreencastFrameAck(sessionID)); err != nil {
				log.Printf("screencast: failed to ack frame: %v", err)
			}
		case <-stopCh:
			// Drain any pending acks.
			for {
				select {
				case sessionID := <-ackCh:
					if err := chromedp.Run(browserCtx, page.ScreencastFrameAck(sessionID)); err != nil {
						log.Printf("screencast: failed to ack frame during drain: %v", err)
					}
				default:
					goto done
				}
			}
		}
	}
done:
	if err := chromedp.Run(browserCtx, page.StopScreencast()); err != nil {
		log.Printf("screencast: failed to stop CDP screencast: %v", err)
	}
}

// screencastStart begins a screencast recording, piping frames into ffmpeg.
func (b *BrowseTools) screencastStart(format string, quality, maxWidth, maxHeight, everyNthFrame int64) (string, error) {
	sc := &b.screencast
	sc.mu.Lock()
	if sc.active || sc.starting {
		sid := sc.sessionID
		fc := sc.frameCount
		sc.mu.Unlock()
		return "", fmt.Errorf("screencast is already active (session %s, %d frames so far) — stop it first", sid, fc)
	}
	sc.starting = true
	sc.mu.Unlock()

	var started bool
	defer func() {
		if !started {
			sc.mu.Lock()
			sc.starting = false
			sc.mu.Unlock()
		}
	}()

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return "", err
	}

	// Defaults.
	scFormat := page.ScreencastFormatJpeg
	inputFormat := "mjpeg"
	if format == "png" {
		scFormat = page.ScreencastFormatPng
		inputFormat = "image2pipe" // for piped PNG frames
	}
	if quality <= 0 {
		quality = 60
	}
	if maxWidth <= 0 {
		maxWidth = 1280
	}
	if maxHeight <= 0 {
		maxHeight = 720
	}
	if everyNthFrame <= 0 {
		everyNthFrame = 1
	}

	sessionID := uuid.New().String()[:8]
	if err := os.MkdirAll(ScreencastDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create screencast dir: %w", err)
	}
	outputPath := filepath.Join(ScreencastDir, sessionID+".mp4")

	// Start ffmpeg: read frames from stdin, output MP4.
	// -framerate 4: assume ~4fps from Chrome screencast (adjustable via every_nth_frame)
	// -f mjpeg or image2pipe: tell ffmpeg the input format
	// -c:v libx264 -pix_fmt yuv420p: widely compatible H.264 MP4
	ffmpegCmd := exec.Command("ffmpeg",
		"-y",
		"-f", inputFormat,
		"-framerate", "4",
		"-i", "pipe:0",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-preset", "fast",
		"-movflags", "+faststart",
		outputPath,
	)
	ffmpegIn, err := ffmpegCmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create ffmpeg stdin pipe: %w", err)
	}
	// Capture stderr for diagnostics on failure.
	ffmpegCmd.Stderr = &limitedBuffer{max: 4096}

	if err := ffmpegCmd.Start(); err != nil {
		ffmpegIn.Close()
		return "", fmt.Errorf("failed to start ffmpeg (is it installed?): %w", err)
	}

	// Start CDP screencast.
	err = chromedp.Run(browserCtx,
		page.StartScreencast().
			WithFormat(scFormat).
			WithQuality(quality).
			WithMaxWidth(maxWidth).
			WithMaxHeight(maxHeight).
			WithEveryNthFrame(everyNthFrame),
	)
	if err != nil {
		ffmpegIn.Close()
		ffmpegCmd.Wait()
		os.Remove(outputPath)
		return "", fmt.Errorf("failed to start screencast: %w", err)
	}

	ackCh := make(chan int64, 4)
	stopCh := make(chan struct{})
	stoppedCh := make(chan struct{})
	go b.screencastAckLoop(browserCtx, ackCh, stopCh, stoppedCh)

	started = true
	sc.mu.Lock()
	sc.active = true
	sc.starting = false
	sc.sessionID = sessionID
	sc.outputPath = outputPath
	sc.frameCount = 0
	sc.startTime = time.Now()
	sc.ffmpegCmd = ffmpegCmd
	sc.ffmpegIn = ffmpegIn
	sc.ackCh = ackCh
	sc.stopCh = stopCh
	sc.stopped = stoppedCh
	sc.stopTimer = time.AfterFunc(ScreencastMaxDuration, func() {
		log.Printf("screencast: max duration (%v) reached, auto-stopping", ScreencastMaxDuration)
		b.screencastStopInternal()
	})
	sc.mu.Unlock()

	return sessionID, nil
}

// screencastStopInternal stops the screencast. Safe to call from any goroutine.
func (b *BrowseTools) screencastStopInternal() {
	sc := &b.screencast
	sc.mu.Lock()
	if !sc.active {
		sc.mu.Unlock()
		return
	}
	sc.active = false
	if sc.stopTimer != nil {
		sc.stopTimer.Stop()
		sc.stopTimer = nil
	}
	stopCh := sc.stopCh
	stopped := sc.stopped
	ffmpegIn := sc.ffmpegIn
	ffmpegCmd := sc.ffmpegCmd
	sc.stopCh = nil
	sc.ffmpegIn = nil
	sc.mu.Unlock()

	// Signal the ack goroutine to stop.
	if stopCh != nil {
		close(stopCh)
	}
	if stopped != nil {
		<-stopped
	}

	// Close ffmpeg's stdin to signal EOF, then wait for it to finish encoding.
	if ffmpegIn != nil {
		ffmpegIn.Close()
	}
	if ffmpegCmd != nil {
		if err := ffmpegCmd.Wait(); err != nil {
			stderr := ""
			if lb, ok := ffmpegCmd.Stderr.(*limitedBuffer); ok {
				stderr = lb.String()
			}
			log.Printf("screencast: ffmpeg exited with error: %v; stderr: %s", err, stderr)
		}
	}
}

// screencastStop stops the screencast and returns summary info.
func (b *BrowseTools) screencastStop() (sessionID, outputPath string, frameCount int, duration time.Duration, err error) {
	sc := &b.screencast
	sc.mu.Lock()
	if !sc.active {
		sc.mu.Unlock()
		return "", "", 0, 0, fmt.Errorf("no active screencast — call screencast_start first")
	}
	sessionID = sc.sessionID
	outputPath = sc.outputPath
	frameCount = sc.frameCount
	duration = time.Since(sc.startTime)
	sc.mu.Unlock()

	b.screencastStopInternal()
	return sessionID, outputPath, frameCount, duration, nil
}

// screencastStatus returns the current status of the screencast.
func (b *BrowseTools) screencastStatus() (active bool, sessionID string, frameCount int, elapsed time.Duration) {
	sc := &b.screencast
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.active {
		return false, "", 0, 0
	}
	return true, sc.sessionID, sc.frameCount, time.Since(sc.startTime)
}

// limitedBuffer is a bytes.Buffer that stops accepting writes after max bytes.
type limitedBuffer struct {
	buf []byte
	max int
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	remaining := lb.max - len(lb.buf)
	if remaining > 0 {
		n := len(p)
		if n > remaining {
			n = remaining
		}
		lb.buf = append(lb.buf, p[:n]...)
	}
	// Always report full length consumed so ffmpeg doesn't get write errors.
	return len(p), nil
}

func (lb *limitedBuffer) String() string {
	return string(lb.buf)
}
