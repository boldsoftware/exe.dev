package exedb

import (
	"encoding/json"
	"log"
)

// Route represents a routing configuration for a box
type Route struct {
	Port  int    `json:"port"`
	Share string `json:"share"`
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
