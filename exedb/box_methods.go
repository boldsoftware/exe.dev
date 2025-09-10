package exedb

import "encoding/json"

// Route represents a routing configuration for a box
type Route struct {
	Port  int    `json:"port"`
	Share string `json:"share"`
}

// GetRoute returns the routing configuration for the box
func (b *Box) GetRoute() Route {
	if b.Routes == nil || *b.Routes == "" {
		return b.GetDefaultRoute()
	}

	var route Route
	err := json.Unmarshal([]byte(*b.Routes), &route)
	if err != nil {
		return b.GetDefaultRoute()
	}

	return route
}

// SetRoute sets the box's routing configuration
func (b *Box) SetRoute(route Route) error {
	data, err := json.Marshal(route)
	if err != nil {
		return err
	}
	routesStr := string(data)
	b.Routes = &routesStr
	return nil
}

// GetDefaultRoute returns the default routing configuration
func (b *Box) GetDefaultRoute() Route {
	return Route{
		Port:  80,
		Share: "private",
	}
}
