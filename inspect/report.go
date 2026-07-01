package inspect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/microyahoo/storage-bot/card"
)

type Report struct {
	Cluster    string        `json:"cluster"`
	StartedAt  time.Time     `json:"started_at"`
	Duration   time.Duration `json:"duration"`
	Overall    Level         `json:"overall"`
	Findings   []Finding     `json:"findings"`
	LLMSummary string        `json:"llm_summary,omitempty"`
}

// Finalize sets Overall from the findings. Call after collecting all findings.
func (r *Report) Finalize() { r.Overall = MaxLevel(r.Findings) }

func (r *Report) Counts() (ok, warn, crit, unknown int) {
	for _, f := range r.Findings {
		switch f.Level {
		case LevelOK:
			ok++
		case LevelWarn:
			warn++
		case LevelCritical:
			crit++
		case LevelUnknown:
			unknown++
		}
	}
	return
}

// Abnormal returns findings at Warn or above, plus Unknown. Order: Critical → Warn → Unknown.
func (r *Report) Abnormal() []Finding {
	var crit, warn, unknown []Finding
	for _, f := range r.Findings {
		switch f.Level {
		case LevelCritical:
			crit = append(crit, f)
		case LevelWarn:
			warn = append(warn, f)
		case LevelUnknown:
			unknown = append(unknown, f)
		}
	}
	out := append(crit, warn...)
	return append(out, unknown...)
}

// nodeGroup is a set of abnormal findings sharing one node (or the cluster-level
// group when Node is empty).
type nodeGroup struct {
	node     string // "" for cluster-scope findings
	nodeIP   string
	findings []Finding
}

// AbnormalByNode groups Abnormal() findings by node so the renderer can separate
// each node with a divider. The cluster-scope group (Node == "") sorts first;
// node groups follow in name order. Within a group, findings keep Abnormal()'s
// severity order (Critical → Warn → Unknown).
func (r *Report) AbnormalByNode() []nodeGroup {
	idx := map[string]int{}
	var groups []nodeGroup
	for _, f := range r.Abnormal() {
		i, ok := idx[f.Node]
		if !ok {
			i = len(groups)
			idx[f.Node] = i
			groups = append(groups, nodeGroup{node: f.Node, nodeIP: f.NodeIP})
		}
		groups[i].findings = append(groups[i].findings, f)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if (groups[i].node == "") != (groups[j].node == "") {
			return groups[i].node == "" // cluster-scope first
		}
		return groups[i].node < groups[j].node
	})
	return groups
}

func itemLabel(f Finding) string {
	if f.Node != "" {
		label := f.Item + " · " + f.Node
		if f.NodeIP != "" {
			label += "(" + f.NodeIP + ")"
		}
		return label
	}
	return f.Item
}

func (r *Report) RenderText() string {
	var b strings.Builder
	ok, warn, crit, unknown := r.Counts()
	stat := fmt.Sprintf("🔴%d 🟡%d 🟢%d", crit, warn, ok)
	if unknown > 0 {
		stat += fmt.Sprintf(" ⚪%d", unknown)
	}
	fmt.Fprintf(&b, "**集群巡检 · %s**\n总体：%s · %s · %s\n\n",
		r.Cluster, r.Overall.String(), stat, r.StartedAt.Format("2006-01-02 15:04"))
	groups := r.AbnormalByNode()
	if len(groups) == 0 {
		b.WriteString("✅ 全部正常\n")
	} else {
		// One block per node, separated by a divider so dense multi-node output
		// doesn't blur together. The node name heads each block; findings below
		// it drop the redundant node suffix.
		for gi, g := range groups {
			if gi > 0 {
				b.WriteString("\n---\n")
			}
			if g.node == "" {
				b.WriteString("📦 **集群级**\n")
			} else if g.nodeIP != "" {
				fmt.Fprintf(&b, "🖥 **%s**（%s）\n", g.node, g.nodeIP)
			} else {
				fmt.Fprintf(&b, "🖥 **%s**\n", g.node)
			}
			for _, f := range g.findings {
				fmt.Fprintf(&b, "%s `%s` — %s\n", f.Level.Emoji(), f.Item, f.Summary)
				if f.Advice != "" {
					fmt.Fprintf(&b, "    建议：%s\n", f.Advice)
				}
			}
		}
		if ok > 0 {
			fmt.Fprintf(&b, "\n🟢 其余 %d 项正常\n", ok)
		}
	}
	if r.LLMSummary != "" {
		fmt.Fprintf(&b, "\n🤖 %s\n", r.LLMSummary)
	}
	return b.String()
}

func themeFor(l Level) card.Theme {
	switch l {
	case LevelCritical:
		return card.ThemeRed
	case LevelWarn:
		return card.ThemeOrange
	default:
		return card.ThemeGreen
	}
}

// RenderCard builds the reviewed layout: colored header + stats + abnormal
// markdown table + collapsed-normal line + report link. webBaseURL may be ""
// to omit the link.
func (r *Report) RenderCard(webBaseURL string) *card.Card {
	ok, warn, crit, unknown := r.Counts()
	c := card.New(r.Overall.Emoji(), "集群巡检报告 · "+r.Cluster, themeFor(r.Overall)).
		Subtitle(fmt.Sprintf("总体：%s · %s", r.Overall.String(), r.StartedAt.Format("2006-01-02 15:04")))

	stat := fmt.Sprintf("🔴 严重 %d · 🟡 警告 %d · 🟢 正常 %d", crit, warn, ok)
	if unknown > 0 {
		stat += fmt.Sprintf(" · ⚪ 未知 %d", unknown)
	}
	c.Body(stat)
	c.Divider()

	ab := r.AbnormalByNode()
	if len(ab) == 0 {
		c.Body("✅ 全部正常")
	} else {
		// One card section per node, each with a node header and its own
		// mini-table. Sections are separated by a Divider so dense multi-node
		// output doesn't blur into a single wall of text — mirrors RenderText.
		for _, g := range ab {
			c.Divider()
			var nodeHeader string
			if g.node == "" {
				nodeHeader = "📦 **集群级**"
			} else if g.nodeIP != "" {
				nodeHeader = fmt.Sprintf("🖥 **%s**（%s）", g.node, g.nodeIP)
			} else {
				nodeHeader = fmt.Sprintf("🖥 **%s**", g.node)
			}

			var t strings.Builder
			t.WriteString(nodeHeader + "\n")
			t.WriteString("**级别 | 巡检项 | 结论**\n")
			for _, f := range g.findings {
				row := fmt.Sprintf("%s | `%s` | %s", f.Level.Emoji(), f.Item, f.Summary)
				if f.Advice != "" {
					row += fmt.Sprintf("\n　建议：%s", f.Advice)
				}
				t.WriteString(row + "\n")
			}
			c.Body(t.String())
		}
		if ok > 0 {
			c.Divider()
			c.Body(fmt.Sprintf("🟢 其余 %d 项正常", ok))
		}
	}

	if r.LLMSummary != "" {
		c.Divider()
		c.Body("🤖 " + r.LLMSummary)
	}

	if webBaseURL != "" {
		c.Divider()
		c.Note(fmt.Sprintf("[📄 查看完整报告](%s/inspect/%s) · 耗时 %s",
			strings.TrimRight(webBaseURL, "/"), r.Cluster, r.Duration.Round(time.Second)))
	} else {
		c.Note(fmt.Sprintf("耗时 %s", r.Duration.Round(time.Second)))
	}
	return c
}
