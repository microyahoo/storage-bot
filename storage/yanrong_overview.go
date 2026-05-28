package storage

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// formatOverview extracts a small set of operator-relevant fields from the
// /api/v3/overview response and renders them as a short text summary.
//
// Fields shown:
//   - product.{product_name, product_serial, product_version, manufacturer}
//   - redundancy, ECModelN, ECModelM
//   - health.health (the cluster's overall health verdict)
//   - cluster[] entries whose name starts with "yrfs_capacity_" (capacity in bytes)
//
// Returns (summary, true) on success or (raw, false) if the response shape
// doesn't match expectations — the caller is expected to fall back to the
// raw body so the operator can still see what came back.
func formatOverview(body []byte) (string, bool) {
	var resp struct {
		Code string             `json:"code"`
		Data overviewDataFields `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", false
	}
	if resp.Data.Health == nil && resp.Data.Product == nil && len(resp.Data.Cluster) == 0 {
		// Nothing we recognize — likely a different endpoint shape.
		return "", false
	}

	var b strings.Builder

	if p := resp.Data.Product; p != nil {
		b.WriteString("🏷 **产品信息**\n")
		writeKV(&b, "  🏭 manufacturer", p.Manufacturer)
		writeKV(&b, "  📦 product     ", p.ProductName)
		writeKV(&b, "  🔖 serial      ", p.ProductSerial)
		writeKV(&b, "  🏁 version     ", p.ProductVersion)
		b.WriteString("\n")
	}

	b.WriteString("⚙️ **集群配置**\n")
	writeKV(&b, "  🧱 redundancy  ", resp.Data.Redundancy)
	if resp.Data.Redundancy == "EC" || resp.Data.ECModelN != 0 || resp.Data.ECModelM != 0 {
		fmt.Fprintf(&b, "  🧮 EC model    : N=%d M=%d\n", resp.Data.ECModelN, resp.Data.ECModelM)
	}
	b.WriteString("\n")

	if h := resp.Data.Health; h != nil {
		b.WriteString("🩺 **健康状态**\n")
		fmt.Fprintf(&b, "  %s health      : %s\n", healthIcon(h.Health), defaultStr(h.Health))
		b.WriteString("\n")
	}

	caps := filterCapacity(resp.Data.Cluster)
	if len(caps) > 0 {
		b.WriteString("💽 **容量** (yrfs_capacity_*)\n")
		// Sort by name for stable output.
		sort.Slice(caps, func(i, j int) bool { return caps[i].Name < caps[j].Name })
		for _, c := range caps {
			fmt.Fprintf(&b, "  %s %-26s %s  (raw=%.0f bytes)\n", capacityIcon(c.Name), c.Name, humanBytes(c.Value), c.Value)
		}
	}

	return strings.TrimRight(b.String(), "\n"), true
}

// healthIcon maps Yanrong's verdict strings to a status emoji. "health" is the
// healthy state in this API (not "ok"); warning/error map to ⚠️/❌.
func healthIcon(verdict string) string {
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case "health", "healthy", "ok":
		return "🟢"
	case "warning", "warn":
		return "🟡"
	case "error", "critical", "fatal":
		return "🔴"
	default:
		return "⚪"
	}
}

// capacityIcon picks an emoji for each yrfs_capacity_* row so total/used/available
// are visually distinct in the list.
func capacityIcon(name string) string {
	switch {
	case strings.HasSuffix(name, "_total"):
		return "📦"
	case strings.HasSuffix(name, "_used"):
		return "💾"
	case strings.HasSuffix(name, "_available"), strings.HasSuffix(name, "_free"):
		return "🆓"
	default:
		return "📊"
	}
}

func defaultStr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type overviewDataFields struct {
	Redundancy string                `json:"redundancy"`
	ECModelN   int                   `json:"ECModelN"`
	ECModelM   int                   `json:"ECModelM"`
	Health     *overviewHealth       `json:"health"`
	Cluster    []overviewClusterItem `json:"cluster"`
	Product    *overviewProduct      `json:"product"`
}

type overviewHealth struct {
	// Yanrong's response uses {"health": {"health": "...", ...}} — the inner
	// "health" string is the overall verdict (e.g. "health", "warning").
	Health string `json:"health"`
}

type overviewClusterItem struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"` // bytes; float to handle very large capacities cleanly
}

type overviewProduct struct {
	ProductName    string `json:"product_name"`
	ProductSerial  string `json:"product_serial"`
	ProductVersion string `json:"product_version"`
	Manufacturer   string `json:"manufacturer"`
}

func filterCapacity(items []overviewClusterItem) []overviewClusterItem {
	out := make([]overviewClusterItem, 0, 4)
	for _, it := range items {
		if strings.HasPrefix(it.Name, "yrfs_capacity_") {
			out = append(out, it)
		}
	}
	return out
}

func writeKV(b *strings.Builder, key, val string) {
	if val == "" {
		val = "-"
	}
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(val)
	b.WriteByte('\n')
}

// humanBytes formats a byte count using IEC binary units (KiB, MiB, ...).
// Yanrong's capacity values reach into the PiB range so we go up to EiB.
func humanBytes(n float64) string {
	if n < 0 {
		return fmt.Sprintf("%.0f", n)
	}
	const unit = 1024.0
	if n < unit {
		return fmt.Sprintf("%.0f B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	v := n / unit
	for _, u := range units {
		if v < unit {
			return fmt.Sprintf("%.2f %s", v, u)
		}
		v /= unit
	}
	return fmt.Sprintf("%.2f ZiB", v)
}
