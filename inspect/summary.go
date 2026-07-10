package inspect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/microyahoo/storage-bot/card"
)

// Summary aggregates the results of a scheduled batch inspection run.
type Summary struct {
	Total       int
	OK          int
	Warn        int
	Critical    int
	Failed      int
	OKNames     []string
	FailedNames []string
	StartedAt   time.Time
	Duration    time.Duration
}

// RenderCard builds a Feishu card summarising the batch inspection.
func (s *Summary) RenderCard(webBaseURL string) *card.Card {
	theme := card.ThemeGreen
	emoji := "✅"
	if s.Critical > 0 {
		theme = card.ThemeRed
		emoji = "🔴"
	} else if s.Warn > 0 {
		theme = card.ThemeOrange
		emoji = "🟡"
	} else if s.Failed > 0 {
		theme = card.ThemeOrange
		emoji = "⚠️"
	}

	c := card.New(emoji, "集群巡检汇总", theme).
		Subtitle(s.StartedAt.Format("2006-01-02 15:04"))

	stat := fmt.Sprintf("共 **%d** 套集群 · 🟢 正常 %d · 🟡 警告 %d · 🔴 严重 %d",
		s.Total, s.OK, s.Warn, s.Critical)
	if s.Failed > 0 {
		stat += fmt.Sprintf(" · ❌ 失败 %d", s.Failed)
	}
	c.Body(stat)

	if len(s.OKNames) > 0 {
		c.Divider()
		sort.Strings(s.OKNames)
		c.Body("🟢 **正常集群**\n" + strings.Join(s.OKNames, ", "))
	}

	if len(s.FailedNames) > 0 {
		c.Divider()
		sort.Strings(s.FailedNames)
		c.Body("❌ **巡检失败**\n" + strings.Join(s.FailedNames, ", "))
	}

	c.Divider()
	c.Note(fmt.Sprintf("耗时 %s", s.Duration.Round(time.Second)))
	return c
}
