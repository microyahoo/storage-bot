package inspect

import "testing"

func TestLevelString(t *testing.T) {
	cases := map[Level]string{
		LevelOK: "OK", LevelWarn: "WARN", LevelCritical: "CRITICAL", LevelUnknown: "UNKNOWN",
	}
	for lvl, want := range cases {
		if got := lvl.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", lvl, got, want)
		}
	}
}

func TestLevelEmoji(t *testing.T) {
	if LevelCritical.Emoji() != "🔴" || LevelWarn.Emoji() != "🟡" || LevelOK.Emoji() != "🟢" {
		t.Errorf("unexpected emoji mapping")
	}
}

func TestMaxLevel(t *testing.T) {
	got := MaxLevel([]Finding{{Level: LevelOK}, {Level: LevelCritical}, {Level: LevelWarn}})
	if got != LevelCritical {
		t.Errorf("MaxLevel = %v, want Critical", got)
	}
	if MaxLevel(nil) != LevelOK {
		t.Errorf("MaxLevel(nil) should be OK")
	}
}
