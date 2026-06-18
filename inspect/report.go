package inspect

import (
	"fmt"
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

func (r *Report) Counts() (ok, warn, crit int) {
	for _, f := range r.Findings {
		switch f.Level {
		case LevelOK:
			ok++
		case LevelWarn:
			warn++
		case LevelCritical:
			crit++
		}
	}
	return
}

// Abnormal returns findings at Warn or above, Critical first then Warn.
func (r *Report) Abnormal() []Finding {
	var crit, warn []Finding
	for _, f := range r.Findings {
		switch f.Level {
		case LevelCritical:
			crit = append(crit, f)
		case LevelWarn:
			warn = append(warn, f)
		}
	}
	return append(crit, warn...)
}

func itemLabel(f Finding) string {
	if f.Node != "" {
		return f.Item + " · " + f.Node
	}
	return f.Item
}

func (r *Report) RenderText() string {
	var b strings.Builder
	ok, warn, crit := r.Counts()
	fmt.Fprintf(&b, "**集群巡检 · %s**\n总体：%s · 🔴%d 🟡%d 🟢%d · %s\n\n",
		r.Cluster, r.Overall.String(), crit, warn, ok, r.StartedAt.Format("2006-01-02 15:04"))
	ab := r.Abnormal()
	if len(ab) == 0 {
		b.WriteString("✅ 全部正常\n")
	} else {
		for _, f := range ab {
			fmt.Fprintf(&b, "%s `%s` — %s\n", f.Level.Emoji(), itemLabel(f), f.Summary)
			if f.Advice != "" {
				fmt.Fprintf(&b, "    建议：%s\n", f.Advice)
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
	ok, warn, crit := r.Counts()
	c := card.New(r.Overall.Emoji(), "集群巡检报告 · "+r.Cluster, themeFor(r.Overall)).
		Subtitle(fmt.Sprintf("总体：%s · %s", r.Overall.String(), r.StartedAt.Format("2006-01-02 15:04")))

	c.Body(fmt.Sprintf("🔴 严重 %d · 🟡 警告 %d · 🟢 正常 %d", crit, warn, ok))
	c.Divider()

	ab := r.Abnormal()
	if len(ab) == 0 {
		c.Body("✅ 全部正常")
	} else {
		var t strings.Builder
		t.WriteString("**级别 | 巡检项 | 结论**\n")
		for _, f := range ab {
			fmt.Fprintf(&t, "%s | `%s` | %s\n", f.Level.Emoji(), itemLabel(f), f.Summary)
		}
		c.Body(t.String())
		if ok > 0 {
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
