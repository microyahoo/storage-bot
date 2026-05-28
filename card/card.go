// Package card builds Feishu interactive-card JSON payloads.
//
// Feishu cards (msg_type = "interactive") give us a colored header bar, markdown
// body, optional notes, and a divider — much richer than plain text. We use them
// for every bot reply so users see a consistent, themed UI: blue for info,
// green for success, red for errors, gray for dry-run.
package card

import (
	"encoding/json"
	"strings"
)

// Theme picks the colored header bar template Feishu renders. See
// https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/feishu-cards/card-components/containers/card-header
// for the full template list.
type Theme string

const (
	ThemeBlue      Theme = "blue"
	ThemeGreen     Theme = "green"
	ThemeRed       Theme = "red"
	ThemeOrange    Theme = "orange"
	ThemeGray      Theme = "grey"
	ThemeTurquoise Theme = "turquoise"
	ThemePurple    Theme = "purple"
)

// Card is the assembled payload. Build with New(...) and Body/Note/Section/etc.
type Card struct {
	header   header
	elements []any
}

type header struct {
	Title    string `json:"-"`
	Subtitle string `json:"-"`
	Template Theme  `json:"-"`
}

// New constructs a card with a header (emoji prefix + title) and a theme color.
func New(emoji, title string, theme Theme) *Card {
	t := strings.TrimSpace(title)
	if emoji != "" {
		t = emoji + " " + t
	}
	return &Card{header: header{Title: t, Template: theme}}
}

// Subtitle adds a smaller line under the header title.
func (c *Card) Subtitle(s string) *Card {
	c.header.Subtitle = s
	return c
}

// Body appends a markdown block. Feishu supports a subset: **bold**, *italic*,
// `code`, ```code blocks```, links, lists, and \n line breaks.
func (c *Card) Body(md string) *Card {
	if md == "" {
		return c
	}
	c.elements = append(c.elements, map[string]any{
		"tag":     "markdown",
		"content": md,
	})
	return c
}

// Divider appends a horizontal rule.
func (c *Card) Divider() *Card {
	c.elements = append(c.elements, map[string]any{"tag": "hr"})
	return c
}

// Note appends a small gray footer line (useful for hints, timestamps).
func (c *Card) Note(md string) *Card {
	if md == "" {
		return c
	}
	c.elements = append(c.elements, map[string]any{
		"tag": "note",
		"elements": []any{
			map[string]any{"tag": "plain_text", "content": md},
		},
	})
	return c
}

// JSON renders the card to the JSON payload Feishu expects.
func (c *Card) JSON() (string, error) {
	hdr := map[string]any{
		"template": string(c.header.Template),
		"title": map[string]any{
			"tag":     "plain_text",
			"content": c.header.Title,
		},
	}
	if c.header.Subtitle != "" {
		hdr["subtitle"] = map[string]any{
			"tag":     "plain_text",
			"content": c.header.Subtitle,
		}
	}
	payload := map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   hdr,
		"elements": c.elements,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
