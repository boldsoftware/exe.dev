package execore

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// grafanaExploreURL builds a Grafana Explore URL with one or more PromQL
// queries shown in a single pane.
//
// An example URL looks like:
//
//	https://grafana.crocodile-vector.ts.net/explore?schemaVersion=1
//	    &panes={"p49":{"datasource":"PBFA97CFB590B2093",
//	                   "queries":[{"refId":"A","expr":"rate(...)","range":true,...}],
//	                   "range":{"from":"now-1h","to":"now"}}}
//	    &orgId=1
//
// The datasource UID below is the Prometheus datasource in crocodile-vector.
func grafanaExploreURL(exprs ...string) string {
	const (
		host          = "https://grafana.crocodile-vector.ts.net/explore"
		datasourceUID = "PBFA97CFB590B2093"
		paneID        = "p49"
	)

	type datasourceRef struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
	}
	type query struct {
		RefID        string        `json:"refId"`
		Expr         string        `json:"expr"`
		Range        bool          `json:"range"`
		Instant      bool          `json:"instant"`
		Datasource   datasourceRef `json:"datasource"`
		EditorMode   string        `json:"editorMode"`
		LegendFormat string        `json:"legendFormat"`
	}
	type timeRange struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	type pane struct {
		Datasource string    `json:"datasource"`
		Queries    []query   `json:"queries"`
		Range      timeRange `json:"range"`
	}

	queries := make([]query, len(exprs))
	for i, e := range exprs {
		queries[i] = query{
			RefID:        string(rune('A' + i)),
			Expr:         e,
			Range:        true,
			Instant:      true,
			Datasource:   datasourceRef{Type: "prometheus", UID: datasourceUID},
			EditorMode:   "code",
			LegendFormat: "__auto",
		}
	}
	panes := map[string]pane{
		paneID: {
			Datasource: datasourceUID,
			Queries:    queries,
			Range:      timeRange{From: "now-1h", To: "now"},
		},
	}
	b, err := json.Marshal(panes)
	if err != nil {
		// Should never happen with well-typed structs above.
		return host
	}
	q := url.Values{}
	q.Set("schemaVersion", "1")
	q.Set("panes", string(b))
	q.Set("orgId", "1")
	return host + "?" + q.Encode()
}

// vmGrafanaLink describes a single link rendered on the VM debug page.
type vmGrafanaLink struct {
	Label string
	URL   string
}

// vmGrafanaLinks returns Grafana Explore URLs for all per-VM Prometheus
// metrics that carry a vm_name label. Where it makes sense, related metrics
// (e.g. network rx/tx) are grouped into the same pane.
func vmGrafanaLinks(vmName string) []vmGrafanaLink {
	sel := fmt.Sprintf(`{vm_name=%q}`, vmName)
	rate := func(metric string) string {
		return fmt.Sprintf("rate(%s%s[$__rate_interval])", metric, sel)
	}
	gauge := func(metric string) string {
		return metric + sel
	}
	return []vmGrafanaLink{
		{
			Label: "CPU (rate of cpu_seconds_total)",
			URL:   grafanaExploreURL(rate("exelet_vm_cpu_seconds_total")),
		},
		{
			Label: "Network rx + tx bytes/sec",
			URL: grafanaExploreURL(
				rate("exelet_vm_net_rx_bytes_total"),
				rate("exelet_vm_net_tx_bytes_total"),
			),
		},
		{
			Label: "Disk I/O read + write bytes/sec",
			URL: grafanaExploreURL(
				rate("exelet_vm_io_read_bytes_total"),
				rate("exelet_vm_io_write_bytes_total"),
			),
		},
		{
			Label: "Memory + swap bytes",
			URL: grafanaExploreURL(
				gauge("exelet_vm_memory_bytes"),
				gauge("exelet_vm_swap_bytes"),
			),
		},
		{
			Label: "Disk used bytes",
			URL:   grafanaExploreURL(gauge("exelet_vm_disk_used_bytes")),
		},
		{
			Label: "LLM tokens/sec (by token_type, model)",
			URL:   grafanaExploreURL(fmt.Sprintf(`sum by (token_type, model) (rate(llm_tokens_total%s[$__rate_interval]))`, sel)),
		},
		{
			Label: "LLM cost USD/sec (by model)",
			URL:   grafanaExploreURL(fmt.Sprintf(`sum by (model) (rate(llm_cost_usd_total%s[$__rate_interval]))`, sel)),
		},
	}
}
