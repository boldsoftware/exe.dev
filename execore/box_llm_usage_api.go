package execore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"exe.dev/exedb"
)

// llmUsageResponse is the response body for GET /api/llm-usage.
type llmUsageResponse struct {
	Days        []llmUsageDayGroup `json:"days"`
	TotalCost   string             `json:"totalCost"`
	TotalCount  int64              `json:"totalCount"`
	PeriodStart string             `json:"periodStart"`
	PeriodEnd   string             `json:"periodEnd"`
}

type llmUsageDayGroup struct {
	Day     string             `json:"day"` // "2025-04-16"
	Entries []llmUsageDayEntry `json:"entries"`
	Cost    string             `json:"cost"`
	Count   int64              `json:"count"`
}

type llmUsageDayEntry struct {
	Box          string `json:"box"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	Cost         string `json:"cost"`
	RequestCount int64  `json:"requestCount"`
}

// handleAPILLMUsage handles GET /api/llm-usage?date=YYYY-MM-DD
// Returns usage for the calendar month (UTC) containing the given date.
// If date is omitted, uses the current month.
func (s *Server) handleAPILLMUsage(w http.ResponseWriter, r *http.Request, userID string) {
	ref := time.Now().UTC()
	var err error
	if dateStr := r.URL.Query().Get("date"); dateStr != "" {
		ref, err = time.Parse("2006-01-02", dateStr)
		if err != nil {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
	}

	start, end := calendarMonthPeriod(ref)

	rows, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserLLMUsageDaily, exedb.GetUserLLMUsageDailyParams{
		UserID:       userID,
		HourBucket:   start,
		HourBucket_2: end,
	})
	if err != nil {
		http.Error(w, "failed to query usage", http.StatusInternalServerError)
		return
	}

	type dayAccum struct {
		group  llmUsageDayGroup
		costMC int64
	}

	var totalMicrocents int64
	var totalCount int64
	dayMap := make(map[string]*dayAccum)
	var dayOrder []string
	for _, row := range rows {
		day := row.Day
		totalMicrocents += row.CostMicrocents
		totalCount += row.RequestCount

		a, ok := dayMap[day]
		if !ok {
			a = &dayAccum{group: llmUsageDayGroup{Day: day}}
			dayMap[day] = a
			dayOrder = append(dayOrder, day)
		}
		a.costMC += row.CostMicrocents
		a.group.Count += row.RequestCount
		a.group.Entries = append(a.group.Entries, llmUsageDayEntry{
			Box:          row.BoxName,
			Model:        row.Model,
			Provider:     row.Provider,
			Cost:         formatMicrocents(row.CostMicrocents),
			RequestCount: row.RequestCount,
		})
	}

	days := make([]llmUsageDayGroup, 0, len(dayOrder))
	for _, day := range dayOrder {
		a := dayMap[day]
		a.group.Cost = formatMicrocents(a.costMC)
		days = append(days, a.group)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(llmUsageResponse{
		Days:        days,
		TotalCost:   formatMicrocents(totalMicrocents),
		TotalCount:  totalCount,
		PeriodStart: start.Format(time.RFC3339),
		PeriodEnd:   end.Format(time.RFC3339),
	})
}

// boxLLMUsageResponse is the response body for GET /api/vm/<name>/llm-usage.
type boxLLMUsageResponse struct {
	Models      []boxLLMUsageModel `json:"models"`
	TotalCost   string             `json:"totalCost"`
	PeriodStart string             `json:"periodStart"`
	PeriodEnd   string             `json:"periodEnd"`
}

type boxLLMUsageModel struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Cost     string `json:"cost"`
}

// handleAPIBoxLLMUsage handles GET /api/vm/<name>/llm-usage
// Returns per-model LLM usage summary for a single box in the current UTC calendar month.
func (s *Server) handleAPIBoxLLMUsage(w http.ResponseWriter, r *http.Request, userID, boxName string) {
	start, end := calendarMonthPeriod(time.Now().UTC())

	box, err := withRxRes1(s, r.Context(), (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: userID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(boxLLMUsageResponse{
				TotalCost:   formatMicrocents(0),
				PeriodStart: start.Format(time.RFC3339),
				PeriodEnd:   end.Format(time.RFC3339),
			})
			return
		}
		http.Error(w, "failed to look up VM", http.StatusInternalServerError)
		return
	}

	rows, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetBoxLLMUsageSummary, exedb.GetBoxLLMUsageSummaryParams{
		BoxID:        int(box.ID),
		HourBucket:   start,
		HourBucket_2: end,
	})
	if err != nil {
		http.Error(w, "failed to query usage", http.StatusInternalServerError)
		return
	}

	var totalMC int64
	models := make([]boxLLMUsageModel, 0, len(rows))
	for _, row := range rows {
		totalMC += row.TotalCostMicrocents
		models = append(models, boxLLMUsageModel{
			Model:    row.Model,
			Provider: row.Provider,
			Cost:     formatMicrocents(row.TotalCostMicrocents),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(boxLLMUsageResponse{
		Models:      models,
		TotalCost:   formatMicrocents(totalMC),
		PeriodStart: start.Format(time.RFC3339),
		PeriodEnd:   end.Format(time.RFC3339),
	})
}

// formatMicrocents formats microcents (1/1,000,000 of a dollar) as a dollar
// string rounded to two decimal places (e.g. "$1.48").
func formatMicrocents(microcents int64) string {
	if microcents < 0 {
		microcents = 0
	}
	// Round to nearest cent (10,000 microcents = 1 cent).
	cents := (microcents + 5_000) / 10_000
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}
