// Package skycam reads the night sky from skycam, the all-sky camera
// software on the observatory Pi (skycam.local). skycam owns everything
// about the sky: it measures cloud from each frame's star count, knows the
// roof state from the roll-off controller, and works out the cloud onset.
// skyq only reads its answers, through GET /api/nights/<YYYYMMDD>/sky, and
// fetches the odd still for the report's thumbnails.
//
// Unknown is never clear and never cloudy (SPEC §8). A night skycam cannot
// serve — a 404, an unreachable Pi, a reply that will not parse — comes back
// as an error, and the caller treats the night's cloud as unknown.
package skycam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/exploded/skyq/internal/analysis"
	"github.com/exploded/skyq/internal/ninalog"
)

// ErrNotFound is skycam answering 404: no frames for that night, or a
// still it no longer keeps. The JPEGs go after 14 nights while the frame
// rows stay for a year, so a 404 on an image is routine.
var ErrNotFound = errors.New("not found on skycam")

// Roof values, exactly as skycam reports them from the roll-off
// controller. Anything other than RoofOpen means the camera, which sits
// inside the observatory, was not looking at the sky.
const (
	RoofOpen    = "OPEN"
	RoofClosed  = "CLOSED"
	RoofOpening = "OPENING"
	RoofClosing = "CLOSING"
	RoofError   = "ERROR"
	RoofUnknown = "unknown" // the controller's reading was stale
)

// Frame is one night-mode frame (sun below −6°).
type Frame struct {
	At    time.Time
	Image string // path under /images/, "" if skycam did not keep the still
	// Cloud is 0–1, or nil when skycam had no reading: the roof not open,
	// the sun above −12°, or the first ten minutes or so of a session while
	// skycam builds its clear-sky baseline.
	Cloud    *float64
	Stars    int64
	SkyLevel float64
	Roof     string
	Rain     string
}

// Night is one night as skycam saw it, frames oldest first.
type Night struct {
	Frames []Frame
	// Onset is skycam's cloud onset; HasOnset is false if cloud never set
	// in. skyq takes it as given rather than re-deriving it, so the rule
	// lives in one place (skycam's analysis.CloudOnset).
	Onset    time.Time
	HasOnset bool
}

// cloudReach is how far from a moment a frame's cloud reading still
// describes it. skycam shoots about every 20 seconds, so a gap this long is
// a run of null frames, not cadence.
const cloudReach = 5 * time.Minute

// CloudKnown reports whether skycam read cloud at any moment in [from, to].
// A night with no reading across the whole imaging session has unknown
// cloud, not no cloud.
func (n *Night) CloudKnown(from, to time.Time) bool {
	for _, f := range n.Frames {
		if f.Cloud != nil && !f.At.Before(from) && !f.At.After(to) {
			return true
		}
	}
	return false
}

// CloudKnownAt reports whether skycam had a cloud reading within cloudReach
// of a moment. A failure in a stretch with no reading is never blamed on
// cloud.
func (n *Night) CloudKnownAt(at time.Time) bool {
	for _, f := range n.Frames {
		if f.Cloud == nil {
			continue
		}
		d := f.At.Sub(at)
		if d < 0 {
			d = -d
		}
		if d <= cloudReach {
			return true
		}
	}
	return false
}

// RoofSpans turns the frames' roof readings into the spans the report's
// cloud chart draws. CLOSED and ERROR are shut, OPENING and CLOSING are
// moving, and both get a hatched band. unknown (a stale controller
// reading), anything unrecognised, and a gap of more than cloudReach
// between frames get no span: the chart leaves them plain, because a
// missing reading is not evidence the roof was shut.
func (n *Night) RoofSpans() []analysis.RoofSpan {
	var spans []analysis.RoofSpan
	for i, f := range n.Frames {
		var state ninalog.RoofState
		switch f.Roof {
		case RoofOpen:
			state = ninalog.RoofOpen
		case RoofClosed, RoofError:
			state = ninalog.RoofClosed
		case RoofOpening, RoofClosing:
			state = ninalog.RoofMoving
		default:
			continue
		}
		if i+1 >= len(n.Frames) {
			break
		}
		end := n.Frames[i+1].At
		if end.Sub(f.At) > cloudReach {
			continue
		}
		if last := len(spans) - 1; last >= 0 && spans[last].State == state && spans[last].To.Equal(f.At) {
			spans[last].To = end
			continue
		}
		spans = append(spans, analysis.RoofSpan{From: f.At, To: end, State: state})
	}
	return spans
}

// Client talks to skycam. The sky API and /images/ share one set of HTTP
// Basic credentials, which come from config, never from the repo.
type Client struct {
	BaseURL  string // e.g. http://skycam.local
	Username string
	Password string
	// CacheDir keeps fetched stills as <CacheDir>/<YYYYMMDD>/<file>, the
	// layout the old whole-night sync used, so a still fetched once — or
	// cached before skycam — outlives the Pi's 14-night retention.
	CacheDir string
	Client   *http.Client
}

func (c *Client) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

var reNight = regexp.MustCompile(`^\d{8}$`)

// The sky API's JSON, as skycam's internal/web/sky.go writes it. Only the
// fields skyq reads are declared.
type nightJSON struct {
	Onset  *time.Time  `json:"onset"`
	Frames []frameJSON `json:"frames"`
}

type frameJSON struct {
	Time     time.Time `json:"time"`
	Image    string    `json:"image"`
	Stars    int64     `json:"stars"`
	Cloud    *float64  `json:"cloud"`
	SkyLevel float64   `json:"sky_level"`
	Roof     string    `json:"roof"`
	Rain     string    `json:"rain"`
}

// Night fetches one night's frames. night is allsky's night date,
// YYYYMMDD: frames before noon belong to the previous evening, the same
// convention as skyq's own night-of. Times come back in loc.
func (c *Client) Night(ctx context.Context, night string, loc *time.Location) (*Night, error) {
	if !reNight.MatchString(night) {
		return nil, fmt.Errorf("night %q is not YYYYMMDD", night)
	}
	u, err := url.JoinPath(c.BaseURL, "api", "nights", night, "sky")
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	n, err := Decode(io.LimitReader(body, 64<<20), loc)
	if err != nil {
		return nil, fmt.Errorf("sky API reply for %s: %w", night, err)
	}
	return n, nil
}

// Decode reads one sky API reply. Times come back in loc.
func Decode(r io.Reader, loc *time.Location) (*Night, error) {
	var raw nightJSON
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}
	out := &Night{Frames: make([]Frame, len(raw.Frames))}
	for i, f := range raw.Frames {
		out.Frames[i] = Frame{
			At: f.Time.In(loc), Image: f.Image, Cloud: f.Cloud,
			Stars: f.Stars, SkyLevel: f.SkyLevel, Roof: f.Roof, Rain: f.Rain,
		}
	}
	if raw.Onset != nil {
		out.Onset, out.HasOnset = raw.Onset.In(loc), true
	}
	return out, nil
}

// Still returns the local path of a frame's still, fetching it into the
// cache first if needed. image is the frame's path under /images/. A still
// skycam no longer keeps returns ErrNotFound.
func (c *Client) Still(ctx context.Context, image string) (string, error) {
	rel := strings.TrimPrefix(path.Clean("/"+image), "/images/")
	night, file := path.Split(rel)
	night = strings.TrimSuffix(night, "/")
	if !reNight.MatchString(night) || file == "" || strings.Contains(night+file, "..") {
		return "", fmt.Errorf("unexpected still path %q", image)
	}
	dest := filepath.Join(c.CacheDir, night, file)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	u, err := url.JoinPath(c.BaseURL, "images", night, file)
	if err != nil {
		return "", err
	}
	body, err := c.get(ctx, u)
	if err != nil {
		return "", err
	}
	defer body.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, body); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dest, os.Rename(tmp, dest)
}

func (c *Client) get(ctx context.Context, u string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %w", u, ErrNotFound)
	}
	resp.Body.Close()
	return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
}
