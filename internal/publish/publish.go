// Package publish pushes finished reports to the Linode over the standard
// deploy SSH access, into the deepspaceplace.com web root. A publish
// failure must never stop the local report — the analysis is the product,
// the upload is a copy.
package publish

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/exploded/skyq/internal/config"
)

// Files uploads the given files — the night's report, the regenerated
// index, any neighbours whose navigation changed — into the remote
// reports directory. Duplicates are sent once.
func Files(cfg config.Publish, files ...string) error {
	if !cfg.Enabled {
		return nil
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		if err := scp(cfg, f); err != nil {
			return err
		}
	}
	return nil
}

func scp(cfg config.Publish, file string) error {
	dest := fmt.Sprintf("%s@%s:%s/", cfg.User, cfg.Host, strings.TrimRight(cfg.Dest, "/"))
	args := []string{"-P", fmt.Sprint(cfg.Port), "-o", "BatchMode=yes"}
	if cfg.IdentityFile != "" {
		args = append(args, "-i", cfg.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, file, dest)
	cmd := exec.Command("scp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp %s: %v: %s", filepath.Base(file), err, strings.TrimSpace(string(out)))
	}
	return nil
}
