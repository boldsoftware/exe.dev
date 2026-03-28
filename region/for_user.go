package region

import (
	"math"
	"strings"
)

// ForUser picks the best active region for a user given their IPQS-derived
// country code and coordinates. Country code is the primary signal; lat/lon
// is used as a fallback for unmapped countries and to split the US east/west.
func ForUser(countryCode string, lat, lon float64) Region {
	countryCode = strings.ToUpper(countryCode)

	// US/CA: split east/west at -100° longitude (roughly the 100th meridian).
	// Zero lon means "no coordinates available" (IPQS returns null for unknown locations;
	// Go decodes null to 0. No real users are at null island).
	if countryCode == "US" || countryCode == "CA" {
		if lon != 0 && lon < -100 {
			return mustByCode("lax")
		}
		if lon != 0 {
			return mustByCode("nyc")
		}
		// No coordinates available; default to lax (non-sticky) rather than nyc (sticky).
		return mustByCode("lax")
	}

	if r, ok := countryToRegion[countryCode]; ok {
		return r
	}

	// Unmapped country: fall back to nearest datacenter by coordinates.
	if lat != 0 || lon != 0 {
		return nearest(lat, lon)
	}

	// pdx is intentionally excluded from new-user routing; lax serves as the
	// primary US west region. pdx remains Default() for legacy/fallback use only.
	return Default()
}

// nearest returns the active region whose datacenter is closest to (lat, lon).
func nearest(lat, lon float64) Region {
	best := Default()
	bestDist := math.MaxFloat64
	for _, r := range allRegions {
		if !r.Active {
			continue
		}
		d := haversine(lat, lon, r.Lat, r.Lon)
		if d < bestDist {
			bestDist = d
			best = r
		}
	}
	return best
}

// haversine returns the great-circle distance in km between two points.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	lat1r := lat1 * math.Pi / 180
	lat2r := lat2 * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1r)*math.Cos(lat2r)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// countryToRegion maps ISO 3166-1 alpha-2 country codes to their best region.
// US is handled separately (east/west split). Unmapped countries fall back to
// nearest-by-coordinates.
var countryToRegion = map[string]Region{
	// North America (non-US); CA is handled above with the US east/west split
	"MX": mustByCode("lax"),

	// Central America & Caribbean
	"GT": mustByCode("lax"),
	"CR": mustByCode("lax"),
	"PA": mustByCode("lax"),
	"CU": mustByCode("nyc"),
	"JM": mustByCode("nyc"),
	"PR": mustByCode("nyc"),

	// South America
	"BR": mustByCode("nyc"),
	"AR": mustByCode("nyc"),
	"CL": mustByCode("nyc"),
	"CO": mustByCode("nyc"),
	"PE": mustByCode("nyc"),
	"VE": mustByCode("nyc"),
	"UY": mustByCode("nyc"),
	"EC": mustByCode("nyc"),

	// Continental Europe → Frankfurt
	"DE": mustByCode("fra"),
	"FR": mustByCode("fra"),
	"NL": mustByCode("fra"),
	"BE": mustByCode("fra"),
	"LU": mustByCode("fra"),
	"CH": mustByCode("fra"),
	"AT": mustByCode("fra"),
	"IT": mustByCode("fra"),
	"ES": mustByCode("fra"),
	"PT": mustByCode("fra"),
	"DK": mustByCode("fra"),
	"SE": mustByCode("fra"),
	"NO": mustByCode("fra"),
	"FI": mustByCode("fra"),
	"PL": mustByCode("fra"),
	"CZ": mustByCode("fra"),
	"SK": mustByCode("fra"),
	"HU": mustByCode("fra"),
	"RO": mustByCode("fra"),
	"BG": mustByCode("fra"),
	"HR": mustByCode("fra"),
	"SI": mustByCode("fra"),
	"GR": mustByCode("fra"),
	"RS": mustByCode("fra"),

	// UK & Ireland → London
	"GB": mustByCode("lon"),
	"IE": mustByCode("lon"),
	"IS": mustByCode("lon"),

	// Eastern Europe & Central Asia → Frankfurt
	"UA": mustByCode("fra"),
	"RU": mustByCode("fra"),
	"BY": mustByCode("fra"),
	"TR": mustByCode("fra"),
	"GE": mustByCode("fra"),
	"KZ": mustByCode("fra"),

	// Middle East → Frankfurt
	"IL": mustByCode("fra"),
	"AE": mustByCode("fra"),
	"SA": mustByCode("fra"),
	"QA": mustByCode("fra"),
	"BH": mustByCode("fra"),
	"KW": mustByCode("fra"),
	"IQ": mustByCode("fra"),
	"IR": mustByCode("fra"),
	"PK": mustByCode("fra"), // routes via DXB anycast; DXB→FRA is much shorter than DXB→TYO

	// South & Southeast Asia → Tokyo
	"IN": mustByCode("tyo"),
	"BD": mustByCode("tyo"),
	"LK": mustByCode("tyo"),
	"SG": mustByCode("tyo"),
	"MY": mustByCode("tyo"),
	"TH": mustByCode("tyo"),
	"VN": mustByCode("tyo"),
	"PH": mustByCode("tyo"),
	"ID": mustByCode("tyo"),
	"MM": mustByCode("tyo"),
	"KH": mustByCode("tyo"),

	// East Asia → Tokyo
	"JP": mustByCode("tyo"),
	"KR": mustByCode("tyo"),
	"TW": mustByCode("tyo"),
	"HK": mustByCode("tyo"),
	"CN": mustByCode("tyo"),
	"MN": mustByCode("tyo"),

	// Oceania → Sydney
	"AU": mustByCode("syd"),
	"NZ": mustByCode("syd"),

	// Africa → Frankfurt
	"ZA": mustByCode("fra"),
	"NG": mustByCode("fra"),
	"KE": mustByCode("fra"),
	"GH": mustByCode("fra"),
	"EG": mustByCode("fra"),
	"MA": mustByCode("fra"),
	"TN": mustByCode("fra"),
	"DZ": mustByCode("fra"),
}
