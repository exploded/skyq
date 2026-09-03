package allsky

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Fetcher pulls stills from the AllSky box's web server into a local cache.
// The images URL is password protected (and should stay that way), so every
// request carries HTTP Basic auth. Credentials come from config, never from
// the repo.
type Fetcher struct {
	BaseURL  string // e.g. http://allsky.local/images
	Username string
	Password string
	CacheDir string
	Client   *http.Client
}

func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// reHref finds still filenames in a directory listing. Matching the
// filename pattern rather than the listing's HTML structure keeps this
// working across lighttpd/apache/nginx index formats.
var reHref = regexp.MustCompile(`image-\d{14}\.jpg`)

// List returns the still filenames available for one date directory
// (YYYYMMDD), sorted, deduplicated.
func (f *Fetcher) List(ctx context.Context, dateDir string) ([]string, error) {
	u, err := url.JoinPath(f.BaseURL, dateDir)
	if err != nil {
		return nil, err
	}
	body, err := f.get(ctx, u+"/")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	html, err := io.ReadAll(io.LimitReader(body, 32<<20))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, m := range reHref.FindAllString(string(html), -1) {
		if !seen[m] {
			seen[m] = true
			names = append(names, m)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Sync downloads any stills in dateDir that the cache doesn't have yet and
// returns the local paths of the newly fetched files. The cache mirrors the
// AllSky layout: <CacheDir>/<YYYYMMDD>/<filename>.
func (f *Fetcher) Sync(ctx context.Context, dateDir string) ([]string, error) {
	names, err := f.List(ctx, dateDir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(f.CacheDir, dateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var fetched []string
	var failed int
	var lastErr error
	for _, name := range names {
		dest := filepath.Join(dir, name)
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		if err := f.download(ctx, dateDir, name, dest); err != nil {
			// One bad file (mid-write on the Pi, a permissions hiccup)
			// must not abort the night's sync — skip it and keep going.
			// A cancelled context stops the loop: every remaining file
			// would fail the same way.
			failed++
			lastErr = fmt.Errorf("fetching %s/%s: %w", dateDir, name, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		fetched = append(fetched, dest)
	}
	if failed > 0 {
		return fetched, fmt.Errorf("%d of %d stills failed, e.g. %w", failed, len(names), lastErr)
	}
	return fetched, nil
}

func (f *Fetcher) download(ctx context.Context, dateDir, name, dest string) error {
	u, err := url.JoinPath(f.BaseURL, dateDir, name)
	if err != nil {
		return err
	}
	body, err := f.get(ctx, u)
	if err != nil {
		return err
	}
	defer body.Close()
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, body); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

func (f *Fetcher) get(ctx context.Context, u string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if f.Username != "" {
		req.SetBasicAuth(f.Username, f.Password)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp.Body, nil
}
