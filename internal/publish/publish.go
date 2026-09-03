// Package publish pushes finished reports to the Linode over the standard
// deploy SSH access, into the deepspaceplace.com web root. A publish
// failure must never stop the local report — the analysis is the product,
// the upload is a copy.
package publish

import (
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exploded/skyq/internal/config"
)

// Report uploads one report plus a regenerated index page.
func Report(cfg config.Publish, reportsDir, reportFile string) error {
	if !cfg.Enabled {
		return nil
	}
	idx := filepath.Join(reportsDir, "index.html")
	if err := writeIndex(reportsDir, idx); err != nil {
		return fmt.Errorf("building index: %w", err)
	}
	for _, f := range []string{reportFile, idx} {
		if err := scp(cfg, f); err != nil {
			return err
		}
	}
	return nil
}

func scp(cfg config.Publish, file string) error {
	dest := fmt.Sprintf("%s@%s:%s/", cfg.User, cfg.Host, strings.TrimRight(cfg.Dest, "/"))
	cmd := exec.Command("scp", "-P", fmt.Sprint(cfg.Port), "-o", "BatchMode=yes", file, dest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp %s: %v: %s", filepath.Base(file), err, strings.TrimSpace(string(out)))
	}
	return nil
}

var indexTmpl = template.Must(template.New("idx").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Deep Space Place — night reports</title>
<style>
:root{color-scheme:light dark}
body{margin:0;background:#f9f9f7;color:#0b0b0b;font:15px/1.6 system-ui,-apple-system,"Segoe UI",sans-serif}
@media (prefers-color-scheme:dark){body{background:#0d0d0d;color:#fff}a{color:#3987e5}}
.wrap{max-width:640px;margin:0 auto;padding:48px 24px}
h1{font-size:22px;margin:0 0 4px}
p{color:#898781;margin:0 0 24px}
li{margin:4px 0}
a{color:#2a78d6;text-decoration:none}
a:hover{text-decoration:underline}
</style></head><body><div class="wrap">
<h1>Night reports</h1>
<p>Sky transparency, one self-contained report per night.</p>
<ul>
{{range .}}<li><a href="{{.}}">{{.}}</a></li>
{{end}}</ul>
</div></body></html>
`))

func writeIndex(reportsDir, dest string) error {
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".html") && n != "index.html" {
			names = append(names, n)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	return indexTmpl.Execute(f, names)
}
