// Package publish pushes finished reports to the Linode over the standard
// deploy SSH access, into the deepspaceplace.com web root. A publish
// failure must never stop the local report — the analysis is the product,
// the upload is a copy.
package publish

import (
	"fmt"
	"os"
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

// scp uploads to a temporary name in the destination directory, then
// renames it over the target.
//
// A plain scp of an existing file opens it with O_TRUNC, so overwriting
// needs write permission on the *file*. The deepspaceplace deploy script
// ends with `chown -R www-data:www-data /var/www/deepspaceplace`, which
// takes that away from deploy every time the site ships — and index.html
// is rewritten on every run. Upload-then-rename needs only write
// permission on the directory, which that chown does not disturb.
func scp(cfg config.Publish, file string) error {
	dir := strings.TrimRight(cfg.Dest, "/")
	base := filepath.Base(file)
	// Same directory as the target, so the rename is atomic: readers see
	// the old report or the new one, never a half-written file.
	tmp := fmt.Sprintf("%s/.skyq-upload-%d-%s", dir, os.Getpid(), base)

	args := sshOpts(cfg, "-P")
	args = append(args, file, fmt.Sprintf("%s@%s:%s", cfg.User, cfg.Host, tmp))
	if out, err := run("scp", args); err != nil {
		return fmt.Errorf("scp %s: %w: %s", base, err, out)
	}

	// 644 so Caddy and the site can read it whoever ends up owning it.
	remote := fmt.Sprintf("chmod 644 %q && mv -f %q %q/%q", tmp, tmp, dir, base)
	args = sshOpts(cfg, "-p")
	args = append(args, fmt.Sprintf("%s@%s", cfg.User, cfg.Host), remote)
	if out, err := run("ssh", args); err != nil {
		cleanup(cfg, tmp)
		return fmt.Errorf("install %s: %w: %s", base, err, out)
	}
	return nil
}

// sshOpts returns the connection flags shared by scp and ssh. They differ
// only in the port flag: scp spells it -P, ssh spells it -p.
func sshOpts(cfg config.Publish, portFlag string) []string {
	args := []string{portFlag, fmt.Sprint(cfg.Port), "-o", "BatchMode=yes"}
	if cfg.IdentityFile != "" {
		args = append(args, "-i", cfg.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	return args
}

// cleanup removes a temporary upload that never made it to its final
// name. Best effort — the caller is already returning an error.
func cleanup(cfg config.Publish, tmp string) {
	args := sshOpts(cfg, "-p")
	args = append(args, fmt.Sprintf("%s@%s", cfg.User, cfg.Host), fmt.Sprintf("rm -f %q", tmp))
	_, _ = run("ssh", args)
}

func run(bin string, args []string) (string, error) {
	out, err := exec.Command(bin, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
