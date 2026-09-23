package model

import "testing"

func TestToolActivityLabelUsesArguments(t *testing.T) {
	cases := []struct {
		name, display, function, args, want string
	}{
		{
			name:     "inspect grep drops persian display name",
			display:  "بررسی خروجی",
			function: "inspect_result",
			args:     `{"action":"grep","query":"funding","result_id":"r_1"}`,
			want:     "Inspect result · grep funding",
		},
		{
			name:     "extract query",
			display:  "استخراج از خروجی",
			function: "collect_result",
			args:     `{"query":"the error line","result_id":"r_1"}`,
			want:     "Extract result · the error line",
		},
		{
			name:     "schedule create",
			display:  "مدیریت زمانبندی ها",
			function: "manage_schedules",
			args:     `{"action":"create","name":"Morning BTC"}`,
			want:     "Schedules · create Morning BTC",
		},
		{
			name:     "price history symbol and interval",
			display:  "Price history",
			function: "get_price_history",
			args:     `{"symbol":"BTCUSDT","interval":"1h"}`,
			want:     "Price history · BTCUSDT 1h",
		},
		{
			name:     "open trade side and symbol",
			display:  "Open trade",
			function: "open_trade",
			args:     `{"symbol":"ETHUSDT","side":"LONG"}`,
			want:     "Open trade · long ETHUSDT",
		},
		{
			name:     "browser url",
			display:  "Open URL",
			function: "browser_open",
			args:     `{"url":"https://www.coinglass.com/Funding"}`,
			want:     "Open URL · www.coinglass.com/Funding",
		},
		{
			name:     "inspect head lines",
			display:  "Inspect result",
			function: "inspect_result",
			args:     `{"action":"head","lines":30}`,
			want:     "Inspect result · head 30",
		},
		{
			name:     "inspect slice",
			display:  "Inspect result",
			function: "inspect_result",
			args:     `{"action":"slice","start":10,"end":40}`,
			want:     "Inspect result · slice 10-40",
		},
		{
			name:     "no useful args",
			display:  "Schedules",
			function: "manage_schedules",
			args:     `{"password":"secret"}`,
			want:     "Schedules",
		},
		{
			name:     "already composed does not double",
			display:  "Inspect result · grep funding",
			function: "inspect_result",
			args:     `{"action":"grep","query":"funding"}`,
			want:     "Inspect result · grep funding",
		},
		{
			name:     "persian query stays as the argument",
			display:  "Extract result",
			function: "collect_result",
			args:     `{"query":"نرخ فاندینگ"}`,
			want:     "Extract result · نرخ فاندینگ",
		},
		{
			name:     "unknown persian display falls back to function name",
			display:  "قیمت",
			function: "get_live_chart",
			args:     `{"symbol":"BTCUSDT"}`,
			want:     "Get live chart · BTCUSDT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolActivityLabel(tc.display, tc.function, tc.args); got != tc.want {
				t.Fatalf("label = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLatinActivityTextDropsPersianOnly(t *testing.T) {
	if got := LatinActivityText("بررسی خروجی"); got != "" {
		t.Fatalf("persian label = %q", got)
	}
	if got := LatinActivityText("Extract result · نرخ فاندینگ"); got != "Extract result · نرخ فاندینگ" {
		t.Fatalf("mixed label = %q", got)
	}
}
