package domain

import (
	"testing"
	"time"
)

// at 构造当日 hh:mm（UTC）的时间点。
func at(h, m int) time.Time {
	return time.Date(2026, 8, 6, h, m, 0, 0, time.UTC)
}

func TestContinuousSedentaryMinutes(t *testing.T) {
	cases := []struct {
		name  string
		spans []SessionSpan
		want  int
	}{
		{"空列表", nil, 0},
		{"单段 30 分钟不足阈值", []SessionSpan{{at(9, 0), at(9, 30)}}, 30},
		{"单段 50 分钟超过阈值", []SessionSpan{{at(9, 0), at(9, 50)}}, 50},
		{"间隔 5 分钟连续累计", []SessionSpan{{at(9, 0), at(9, 30)}, {at(9, 35), at(9, 55)}}, 50},
		{"间隔恰好 10 分钟不断档", []SessionSpan{{at(9, 0), at(9, 30)}, {at(9, 40), at(10, 0)}}, 50},
		{"间隔 15 分钟断档取最大值", []SessionSpan{{at(9, 0), at(9, 30)}, {at(9, 45), at(10, 15)}}, 30},
		{"乱序输入按开始时间排序", []SessionSpan{{at(10, 0), at(10, 30)}, {at(9, 0), at(9, 40)}}, 40},
		{"三段两次断档取最大窗口", []SessionSpan{{at(9, 0), at(9, 30)}, {at(9, 35), at(9, 50)}, {at(10, 10), at(10, 30)}}, 45},
		{"窗口累计恰好 45 分钟", []SessionSpan{{at(9, 0), at(9, 30)}, {at(9, 35), at(9, 50)}}, 45},
	}
	for _, c := range cases {
		if got := ContinuousSedentaryMinutes(c.spans); got != c.want {
			t.Fatalf("%s: expected %d, got %d", c.name, c.want, got)
		}
	}
}

func TestSedentaryWindowsRested(t *testing.T) {
	cases := []struct {
		name  string
		spans []SessionSpan
		want  []SedentaryWindow
	}{
		{"空列表", nil, nil},
		{
			"单窗口且后续无会话视为已休息",
			[]SessionSpan{{at(9, 0), at(9, 50)}},
			[]SedentaryWindow{{Minutes: 50, Rested: true}},
		},
		{
			"两个窗口：中间断档视为已休息，末窗口视为已休息",
			[]SessionSpan{{at(9, 0), at(9, 30)}, {at(9, 45), at(10, 15)}},
			[]SedentaryWindow{{Minutes: 30, Rested: true}, {Minutes: 30, Rested: true}},
		},
		{
			"断档 20 分钟（>10）：前一窗口已休息",
			[]SessionSpan{{at(9, 0), at(9, 50)}, {at(10, 10), at(10, 30)}},
			[]SedentaryWindow{{Minutes: 50, Rested: true}, {Minutes: 20, Rested: true}},
		},
		{
			"跨过阈值后仍追加会话：未休息",
			[]SessionSpan{{at(9, 0), at(9, 50)}, {at(9, 55), at(10, 25)}},
			[]SedentaryWindow{{Minutes: 80, Rested: false}},
		},
		{
			"跨过阈值后断档：已休息",
			[]SessionSpan{{at(9, 0), at(9, 50)}, {at(10, 10), at(10, 30)}},
			[]SedentaryWindow{{Minutes: 50, Rested: true}, {Minutes: 20, Rested: true}},
		},
	}
	for _, c := range cases {
		got := SedentaryWindows(c.spans)
		if len(got) != len(c.want) {
			t.Fatalf("%s: expected %d windows, got %d (%+v)", c.name, len(c.want), len(got), got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: window[%d] = %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestValidNightMode(t *testing.T) {
	for _, ok := range []string{NightModeAuto, NightModeLight, NightModeDark, NightModeCustom} {
		if !ValidNightMode(ok) {
			t.Fatalf("expected %q to be valid night mode", ok)
		}
	}
	for _, bad := range []string{"", "AUTO", "sepia", "night", "blue"} {
		if ValidNightMode(bad) {
			t.Fatalf("expected %q to be invalid night mode", bad)
		}
	}
}
