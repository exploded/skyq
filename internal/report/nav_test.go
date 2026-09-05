package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A minimal report: the nav markers Render emits plus a verdict paragraph.
func fakeReport(night, verdict string) string {
	return "<html><body>" + navBlock("top", night, "", "") +
		`<h1>x</h1><p class="sub">` + verdict + `</p>` +
		navBlock("bottom", night, "", "") + "</body></html>"
}

func TestRelinkAndIndex(t *testing.T) {
	dir := t.TempDir()
	nights := map[string]string{
		"2026-08-31": "No cloud onset detected. The transparency index held at 98 (IQR 92–104) across 40 light frames.",
		"2026-09-02": "Cloud arrived at 03:48 &mdash; detected by the all-sky camera, independent of N.I.N.A. Before it the sky ran at index 100 (IQR 93–107); after it 0 (IQR 0–0).",
		"2026-09-30": "No cloud onset detected. The transparency index held at 101 (IQR 95–106) across 12 light frames.",
	}
	for n, v := range nights {
		if err := os.WriteFile(filepath.Join(dir, n+".html"), []byte(fakeReport(n, v)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A pre-navigation report with no markers must be left untouched.
	old := filepath.Join(dir, "2026-07-27.html")
	os.WriteFile(old, []byte(`<html><p class="sub">Old.</p></html>`), 0o644)

	changed, err := Relink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 3 {
		t.Fatalf("relink changed %d files, want 3: %v", len(changed), changed)
	}
	mid, _ := os.ReadFile(filepath.Join(dir, "2026-09-02.html"))
	for _, want := range []string{
		`<a rel="prev" href="2026-08-31.html">&larr; 31 Aug</a>`,
		`<a rel="next" href="2026-09-30.html">30 Sep &rarr;</a>`,
		`<a href="index.html">All nights</a>`,
	} {
		if !strings.Contains(string(mid), want) {
			t.Errorf("middle night missing %q", want)
		}
	}
	if strings.Count(string(mid), `rel="prev"`) != 2 {
		t.Error("expected a prev link in both the top and bottom nav")
	}
	first, _ := os.ReadFile(filepath.Join(dir, "2026-07-27.html"))
	if strings.Contains(string(first), "nights") {
		t.Error("marker-less report was rewritten")
	}
	last, _ := os.ReadFile(filepath.Join(dir, "2026-09-30.html"))
	if !strings.Contains(string(last), "Latest night") || !strings.Contains(string(last), `href="2026-09-02.html"`) {
		t.Error("latest night should have a prev link and a disabled next")
	}

	// Idempotent: nothing changes on a second pass.
	if changed, _ := Relink(dir); len(changed) != 0 {
		t.Errorf("second relink changed %v", changed)
	}

	idx, err := WriteIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile(idx)
	s := string(page)
	for _, want := range []string{
		"4 nights so far",
		"<h2>September 2026</h2>", "<h2>August 2026</h2>", "<h2>July 2026</h2>",
		`href="2026-09-02.html"`,
		"Wed 2–3 Sep", "Wed 30 Sep – 1 Oct",
		`<span class="pill">Index 100</span><span class="pill">Cloud 03:48</span>`,
		`<span class="pill">Index 98</span><span class="pill">Clear all night</span>`,
		"independent of N.I.N.A. Before it",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("index missing %q", want)
		}
	}
	// Newest first, within and across months.
	if strings.Index(s, "2026-09-30") > strings.Index(s, "2026-09-02") || strings.Index(s, "2026-09-02") > strings.Index(s, "2026-08-31") {
		t.Error("index is not newest first")
	}
	if strings.Contains(s, "http://") || strings.Contains(s, "https://") {
		t.Error("index references an external URL")
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("word ", 60)
	got := truncate(long, 40)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) > 41 {
		t.Errorf("truncate = %q", got)
	}
	if truncate("short", 40) != "short" {
		t.Error("short strings must pass through")
	}
}

// Builds a browsable copy of the local reports directory under .local so the
// index and navigation can be eyeballed. Skipped when there are no reports.
func TestPreviewIndex(t *testing.T) {
	src := "../../reports"
	nights, err := Nights(src)
	if err != nil || len(nights) == 0 {
		t.Skip("no local reports")
	}
	dst := "../../.local/preview-reports"
	os.MkdirAll(dst, 0o755)
	for _, n := range nights {
		b, err := os.ReadFile(filepath.Join(src, n+".html"))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dst, n+".html"), b, 0o644)
	}
	// Reports rendered before navigation existed carry no markers; give the
	// newest one a fresh nav pair so Relink has something to work with.
	newest := filepath.Join(dst, nights[len(nights)-1]+".html")
	b, _ := os.ReadFile(newest)
	if !strings.Contains(string(b), navEnd) {
		s := strings.Replace(string(b), "<h1>", navBlock("top", nights[len(nights)-1], "", "")+"\n<h1>", 1)
		s = strings.Replace(s, "</p>\n</div>\n<script>", "</p>\n"+navBlock("bottom", nights[len(nights)-1], "", "")+"\n</div>\n<script>", 1)
		os.WriteFile(newest, []byte(s), 0o644)
	}
	if _, err := Relink(dst); err != nil {
		t.Fatal(err)
	}
	idx, err := WriteIndex(dst)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("preview index written to %s", idx)
}
