package models

import "strings"

// RegionFilter selects which Qoder region a request may be routed to. It is
// parsed from an optional model-name prefix (cn/, gl/, global/, auto/); a bare
// model name keeps the historical CN-first behaviour.
type RegionFilter int

const (
	// RegionDefault means the client wrote no prefix: prefer CN accounts, and
	// fall back to any region when the pool has no CN account.
	RegionDefault RegionFilter = iota
	// RegionCN restricts routing to China accounts (region "" or "cn").
	RegionCN
	// RegionGlobal restricts routing to international accounts (region "global").
	RegionGlobal
	// RegionAuto lets the router balance across both regions.
	RegionAuto
)

// Region returns the concrete region string this filter pins, or "" when the
// filter does not pin one (default/auto).
func (f RegionFilter) Region() string {
	switch f {
	case RegionCN:
		return "cn"
	case RegionGlobal:
		return "global"
	default:
		return ""
	}
}

// String renders the filter for logs.
func (f RegionFilter) String() string {
	switch f {
	case RegionCN:
		return "cn"
	case RegionGlobal:
		return "global"
	case RegionAuto:
		return "auto"
	default:
		return "default"
	}
}

// regionPrefixes maps the accepted model-name prefixes to a filter. The prefix
// must be followed by a non-empty model name; model names never contain "/", so
// there is no ambiguity with a genuine model id.
var regionPrefixes = []struct {
	prefix string
	filter RegionFilter
}{
	{"cn/", RegionCN},
	{"gl/", RegionGlobal},
	{"global/", RegionGlobal},
	{"auto/", RegionAuto},
}

// ParseRegionPrefix splits an optional region prefix off a model name. It
// returns the filter and the bare model name. An unrecognised prefix is left
// untouched (filter = RegionDefault, bare = the original string) so a future
// model id containing "/" is never mangled.
func ParseRegionPrefix(model string) (RegionFilter, string) {
	for _, p := range regionPrefixes {
		if !strings.HasPrefix(model, p.prefix) {
			continue
		}
		bare := strings.TrimSpace(model[len(p.prefix):])
		if bare == "" {
			// "cn/" alone is not a usable model name; treat it as-is.
			return RegionDefault, model
		}
		return p.filter, bare
	}
	return RegionDefault, model
}
