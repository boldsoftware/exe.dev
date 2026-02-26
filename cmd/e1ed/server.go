package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Server struct {
	pool     *Pool
	repoPath string

	mu   sync.Mutex
	runs []RunInfo
}

func NewServer(pool *Pool, repoPath string) *Server {
	return &Server{
		pool:     pool,
		repoPath: repoPath,
	}
}

func (s *Server) trackRun(info RunInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, info)
}

func (s *Server) untrackRun(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.runs {
		if r.ID == id {
			s.runs = append(s.runs[:i], s.runs[i+1:]...)
			return
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /run", s.handleRun)
	mux.HandleFunc("POST /recycle", s.handleRecycle)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("GET /ready", s.handleReady)
	return mux
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if err := req.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	flush := func() {
		flusher.Flush()
	}

	exitCode, err := s.executeRun(r.Context(), req, w, flush)
	if err != nil {
		slog.ErrorContext(r.Context(), "run failed", "commit", req.Commit, "err", err)
		writeMsg(w, "error", err.Error())
		if exitCode == 0 {
			exitCode = 1
		}
		writeDone(w, exitCode, 0)
		flush()
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	runs := make([]statusRun, len(s.runs))
	for i, ri := range s.runs {
		runs[i] = statusRun{
			ID:      ri.ID,
			Commit:  ri.Commit,
			Started: ri.Started.Format(time.RFC3339),
			Elapsed: time.Since(ri.Started).Round(time.Second).String(),
		}
	}
	s.mu.Unlock()

	resp := statusResponse{
		Pool: s.pool.Status(),
		Runs: runs,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRecycle(w http.ResponseWriter, r *http.Request) {
	n := s.pool.Recycle()
	slog.InfoContext(r.Context(), "recycle requested", "recycled", n)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"recycled": n})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.pool.WaitReady(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.pool.Status())
}

type statusRun struct {
	ID      string `json:"id"`
	Commit  string `json:"commit"`
	Started string `json:"started"`
	Elapsed string `json:"elapsed"`
}

type statusResponse struct {
	Pool PoolStatus  `json:"pool"`
	Runs []statusRun `json:"runs"`
}
