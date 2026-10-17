package models

import "testing"

// TestParseRegionPrefix 区域前缀解析表。
func TestParseRegionPrefix(t *testing.T) {
	cases := []struct {
		in     string
		filter RegionFilter
		bare   string
	}{
		{"cn/Qwen3.8-Flash", RegionCN, "Qwen3.8-Flash"},
		{"gl/Qwen3.8-Flash", RegionGlobal, "Qwen3.8-Flash"},
		{"global/Qwen3.8-Flash", RegionGlobal, "Qwen3.8-Flash"},
		{"auto/Qwen3.8-Flash", RegionAuto, "Qwen3.8-Flash"},
		{"Qwen3.8-Flash", RegionDefault, "Qwen3.8-Flash"},
		{"", RegionDefault, ""},
		// 未知前缀原样保留，避免误伤未来含斜杠的模型名。
		{"vendor/model", RegionDefault, "vendor/model"},
		// 只有前缀没有模型名：原样保留。
		{"cn/", RegionDefault, "cn/"},
		{"cn/  Qwen3.8-Flash  ", RegionCN, "Qwen3.8-Flash"},
	}
	for _, tc := range cases {
		f, bare := ParseRegionPrefix(tc.in)
		if f != tc.filter || bare != tc.bare {
			t.Errorf("ParseRegionPrefix(%q) = (%v, %q), want (%v, %q)", tc.in, f, bare, tc.filter, tc.bare)
		}
	}
}

// TestRegionFilterRegion RegionFilter.Region() 的映射。
func TestRegionFilterRegion(t *testing.T) {
	cases := map[RegionFilter]string{
		RegionDefault: "",
		RegionCN:      "cn",
		RegionGlobal:  "global",
		RegionAuto:    "",
	}
	for f, want := range cases {
		if got := f.Region(); got != want {
			t.Errorf("%v.Region() = %q, want %q", f, got, want)
		}
	}
}
