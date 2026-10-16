package badge

import (
	"math/big"
	"strings"
	"testing"
)

func TestRenderFlat(t *testing.T) {
	svg := Render(Options{
		Label:   "stars",
		Message: "1.2k",
		Color:   "#007ec6",
		Style:   StyleFlat,
		Radius:  -1,
	})

	assertContains(t, svg, `<svg xmlns="http://www.w3.org/2000/svg"`)
	assertContains(t, svg, `</svg>`)
	assertContains(t, svg, `stars`)
	assertContains(t, svg, `1.2k`)
	assertContains(t, svg, `fill="#007ec6"`)
	assertContains(t, svg, `fill="#555"`)
	assertContains(t, svg, `rx="3"`)
	assertContains(t, svg, `url(#s)`)
	assertContains(t, svg, `fill-opacity=".3"`)
}

func TestRenderFlatSquare(t *testing.T) {
	svg := Render(Options{
		Label:   "build",
		Message: "passing",
		Color:   "#4b0",
		Style:   StyleFlatSquare,
	})

	assertContains(t, svg, `shape-rendering="crispEdges"`)
	assertNotContains(t, svg, `clipPath`)
	assertNotContains(t, svg, `fill-opacity=".3"`)
	assertNotContains(t, svg, `url(#s)`)
}

func TestRenderPlastic(t *testing.T) {
	svg := Render(Options{
		Label:   "version",
		Message: "1.0.0",
		Color:   "#ea7233",
		Style:   StylePlastic,
		Radius:  -1,
	})

	assertContains(t, svg, `rx="4"`)
	assertContains(t, svg, `stop-opacity=".7"`)
	assertContains(t, svg, `fill="#ea7233"`)
}

func TestRenderMessageOnly(t *testing.T) {
	svg := Render(Options{
		Label:   "",
		Message: "hello",
		Color:   "#4b0",
		Style:   StyleFlat,
	})

	assertNotContains(t, svg, `fill="#555"`)
	assertContains(t, svg, `hello`)
}

func TestRenderCustomHeight(t *testing.T) {
	svg := Render(Options{
		Label:   "test",
		Message: "ok",
		Style:   StyleFlat,
		Height:  28,
	})

	assertContains(t, svg, `height="28"`)
}

func TestRenderCustomRadius(t *testing.T) {
	svg := Render(Options{
		Label:   "test",
		Message: "ok",
		Style:   StyleFlat,
		Radius:  10,
	})

	assertContains(t, svg, `rx="10"`)
}

func TestRenderZeroRadius(t *testing.T) {
	svg := Render(Options{
		Label:   "test",
		Message: "ok",
		Style:   StyleFlat,
		Radius:  0,
	})

	assertNotContains(t, svg, `clipPath`)
	assertContains(t, svg, `crispEdges`)
}

func TestRenderEscapesXML(t *testing.T) {
	svg := Render(Options{
		Label:   "a<b",
		Message: "c&d",
		Style:   StyleFlat,
	})

	assertContains(t, svg, `a&lt;b`)
	assertContains(t, svg, `c&amp;d`)
	assertNotContains(t, svg, `a<b`)
	assertNotContains(t, svg, `c&d`)
}

func TestRenderAccessibility(t *testing.T) {
	svg := Render(Options{
		Label:   "coverage",
		Message: "95%",
		Style:   StyleFlat,
	})

	assertContains(t, svg, `role="img"`)
	assertContains(t, svg, `aria-label="coverage: 95%"`)
	assertContains(t, svg, `<title>coverage: 95%</title>`)
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1.0k"},
		{1200, "1.2k"},
		{5274, "5.3k"},
		{10000, "10k"},
		{12345, "12k"},
		{999999, "1000k"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
		{10000000, "10M"},
		{123456789, "123M"},
	}

	for _, tc := range tests {
		got := FormatNumber(tc.n)
		if got != tc.want {
			t.Errorf("FormatNumber(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestMeasureText(t *testing.T) {
	w := measureText("stars")
	if w < 20 || w > 40 {
		t.Errorf("measureText('stars') = %f, expected reasonable width", w)
	}

	w2 := measureText("downloads")
	if w2 <= w {
		t.Error("'downloads' should be wider than 'stars'")
	}

	empty := measureText("")
	if empty != 0 {
		t.Errorf("measureText('') = %f, want 0", empty)
	}
}

func TestRoundUpToOdd(t *testing.T) {
	if roundUpToOdd(4) != 5 {
		t.Error("roundUpToOdd(4) should be 5")
	}
	if roundUpToOdd(5) != 5 {
		t.Error("roundUpToOdd(5) should be 5")
	}
	if roundUpToOdd(0) != 1 {
		t.Error("roundUpToOdd(0) should be 1")
	}
}

func assertContains(t *testing.T, svg, substr string) {
	t.Helper()
	if !strings.Contains(svg, substr) {
		t.Errorf("SVG should contain %q", substr)
	}
}

func assertNotContains(t *testing.T, svg, substr string) {
	t.Helper()
	if strings.Contains(svg, substr) {
		t.Errorf("SVG should not contain %q", substr)
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{nil, "0"},
		{int64(42), "42"},
		{int64(1500), "1.5k"},
		{float64(2500000), "2.5M"},
		{big.NewInt(1500), "1.5k"},
		{new(big.Int).Lsh(big.NewInt(1), 70), "1180591620717411303424"},
		{"n/a", "n/a"},
	}
	for _, tt := range tests {
		if got := FormatValue(tt.in); got != tt.want {
			t.Errorf("FormatValue(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
