package exedb

import (
	"encoding/json"
	"log"
)

// Route represents a routing configuration for a box
type Route struct {
	Port        int    `json:"port"`
	Share       string `json:"share"`
	TeamSSH     bool   `json:"team_ssh,omitempty"`
	TeamShelley bool   `json:"team_shelley,omitempty"`
}

// GetRoute returns the routing configuration for the box
func (b *Box) GetRoute() Route {
	if b.Routes == nil || *b.Routes == "" {
		return DefaultRoute()
	}

	var route Route
	err := json.Unmarshal([]byte(*b.Routes), &route)
	if err != nil {
		return DefaultRoute()
	}

	return route
}

// SetRoute sets the box's routing configuration
func (b *Box) SetRoute(route Route) {
	data, err := json.Marshal(route)
	if err != nil {
		panic("Failed to marshal route: " + err.Error())
	}
	routesStr := string(data)
	b.Routes = &routesStr
}

// DefaultRoute returns the default routing configuration
func DefaultRoute() Route {
	return Route{
		Port:  80,
		Share: "private",
	}
}

// DefaultRouteJSON returns the default route as JSON.
func DefaultRouteJSON() string {
	route := DefaultRoute()
	data, err := json.Marshal(route)
	if err != nil {
		log.Fatalf("Failed to marshal default route: %v", err)
	}
	return string(data)
}

// GetTags parses the tags JSON column and returns the list of tags.
func (b *Box) GetTags() []string {
	if b.Tags == "" || b.Tags == "[]" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(b.Tags), &tags); err != nil {
		log.Printf("failed to unmarshal tags: %v", err)
		return nil
	}
	return tags
}

// TagsJSON encodes a tag slice as JSON for storage.
func TagsJSON(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	data, err := json.Marshal(tags)
	if err != nil {
		panic("failed to marshal tags: " + err.Error())
	}
	return string(data)
}
