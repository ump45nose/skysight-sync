package syncer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

const Version = "1.0.3"
const renderVersion = 3
const MaxDocument = 8 << 20
const MaxManifest = 32 << 20

var namePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2})-([A-Za-z0-9_]+)-(10min|6h)-memory-summary\.md$`)
var devicePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Config struct {
	DeviceID    string   `json:"device_id"`
	InstanceID  string   `json:"instance_id"`
	CodexHome   string   `json:"codex_home"`
	SSHTarget   string   `json:"ssh_target"`
	SSHHostname string   `json:"ssh_hostname,omitempty"`
	RemoteDir   string   `json:"remote_dir"`
	Devices     []string `json:"devices"`
}

func (c Config) Resources() string {
	return filepath.Join(c.CodexHome, "memories", "extensions", "skysight", "resources")
}
func (c Config) Validate() error {
	if !devicePattern.MatchString(c.DeviceID) || c.InstanceID == "" {
		return errors.New("invalid device ID or missing installation identity")
	}
	if !filepath.IsAbs(c.CodexHome) || !strings.HasPrefix(c.RemoteDir, "/") || strings.ContainsAny(c.RemoteDir, "\x00\r\n") {
		return errors.New("codex_home and remote_dir must be absolute paths")
	}
	if c.SSHTarget == "" || strings.HasPrefix(c.SSHTarget, "-") || strings.ContainsAny(c.SSHTarget, "\x00\r\n ") {
		return errors.New("invalid SSH target")
	}
	if strings.ContainsAny(c.SSHHostname, "\x00\r\n ") || strings.HasPrefix(c.SSHHostname, "-") {
		return errors.New("invalid SSH hostname")
	}
	seen := map[string]bool{}
	for _, d := range c.Devices {
		if !devicePattern.MatchString(d) || seen[d] {
			return errors.New("invalid or duplicate participating device")
		}
		seen[d] = true
	}
	if !seen[c.DeviceID] {
		return errors.New("participating devices must include this device")
	}
	return nil
}

func LoadConfig(p string) (Config, error) {
	var c Config
	err := readJSON(p, &c)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// A source digest is its immutable transfer version and conflict/integrity check.
func Digest(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }

type Entry struct {
	Name   string `json:"name"`
	Digest string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Manifest struct {
	DeviceID   string           `json:"device_id"`
	InstanceID string           `json:"instance_id"`
	Cutoff     time.Time        `json:"cutoff"`
	Entries    map[string]Entry `json:"entries"`
}
type NativeRef struct {
	DeviceID string `json:"device_id"`
	Digest   string `json:"sha256"`
}
type Published struct {
	Filename string   `json:"filename"`
	Inputs   []string `json:"inputs"`
	Digest   string   `json:"sha256"`
}
type State struct {
	RenderVersion int                  `json:"render_version"`
	Sources       map[string]Manifest  `json:"sources"`
	Native        map[string]NativeRef `json:"native"`
	Published     map[string]Published `json:"published"`
	Suppressed    map[string]bool      `json:"suppressed"`
	LastSuccess   *time.Time           `json:"last_success,omitempty"`
	SuccessDay    string               `json:"success_day,omitempty"`
	Cutoff        time.Time            `json:"cutoff"`
	FailureSince  *time.Time           `json:"failure_since,omitempty"`
	LastError     string               `json:"last_error,omitempty"`
	Notified      bool                 `json:"notified,omitempty"`
	Restored      bool                 `json:"restored,omitempty"`
	Issues        map[string]FileIssue `json:"issues,omitempty"`
}

func emptyState() State {
	return State{Sources: map[string]Manifest{}, Native: map[string]NativeRef{}, Published: map[string]Published{}, Suppressed: map[string]bool{}}
}
func loadState(root string) (State, error) {
	s := emptyState()
	err := readJSON(filepath.Join(root, "state.json"), &s)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if s.Sources == nil || s.Native == nil || s.Published == nil || s.Suppressed == nil {
		return s, errors.New("incomplete state.json; restore from a saved checkpoint")
	}
	if s.Issues == nil {
		s.Issues = map[string]FileIssue{}
	}
	for window, p := range s.Published {
		if _, _, err := ParseName(p.Filename); err != nil {
			return s, fmt.Errorf("invalid published file for %s: %w", window, err)
		}
		if !digestPattern.MatchString(p.Digest) {
			return s, errors.New("invalid published digest")
		}
	}
	for name, n := range s.Native {
		if _, _, err := ParseName(name); err != nil {
			return s, err
		}
		if !devicePattern.MatchString(n.DeviceID) || !digestPattern.MatchString(n.Digest) {
			return s, errors.New("invalid native file state")
		}
	}
	for d, m := range s.Sources {
		if err := validateManifest(m, d); err != nil {
			return s, err
		}
	}
	return s, nil
}

func validateManifest(m Manifest, device string) error {
	if m.DeviceID != device || !devicePattern.MatchString(device) || m.InstanceID == "" || m.Entries == nil || m.Cutoff.IsZero() {
		return fmt.Errorf("invalid manifest for %s", device)
	}
	for k, e := range m.Entries {
		if k != e.Name || !digestPattern.MatchString(e.Digest) || e.Size < 1 || e.Size > MaxDocument {
			return fmt.Errorf("invalid manifest entry %s", k)
		}
		if _, _, err := ParseName(k); err != nil {
			return err
		}
	}
	return nil
}
func ParseName(name string) (time.Time, time.Duration, error) {
	m := namePattern.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, 0, fmt.Errorf("unsupported summary filename: %s", name)
	}
	t, err := time.Parse("2006-01-02T15-04-05", m[1])
	if err != nil {
		return t, 0, err
	}
	d := 10 * time.Minute
	if m[3] == "6h" {
		d = 6 * time.Hour
	}
	return t, d, nil
}
func DayCutoff(now time.Time) (time.Time, string) {
	z, _ := time.LoadLocation("Asia/Shanghai")
	t := now.In(z)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, z).UTC(), t.Format("2006-01-02")
}
func WindowKey(t time.Time) string { return t.UTC().Truncate(6 * time.Hour).Format(time.RFC3339) }

type Frontmatter struct {
	Title        string   `yaml:"title"`
	Description  string   `yaml:"description"`
	Applications []string `yaml:"applications"`
}
type Summary struct {
	Device            string
	Entry             Entry
	Start             time.Time
	Duration          time.Duration
	Meta              Frontmatter
	Body              string
	Raw               []byte
	MetadataRecovered bool
}

// Content validation is distinct from I/O, transport and integrity failures.
var ErrInvalidSummary = errors.New("invalid summary content")

func invalidSummary(name, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidSummary, name, reason)
}

func Parse(device, name string, b []byte) (Summary, error) {
	s := Summary{Device: device, Entry: Entry{Name: name, Digest: Digest(b), Size: int64(len(b))}, Raw: b}
	var err error
	s.Start, s.Duration, err = ParseName(name)
	if err != nil {
		return s, invalidSummary(name, "invalid filename timestamp")
	}
	if len(b) > MaxDocument {
		return s, invalidSummary(name, "summary exceeds 8 MiB")
	}
	normalized := bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return s, invalidSummary(name, "missing YAML frontmatter")
	}
	front := normalized[4:]
	end := bytes.Index(front, []byte("\n---\n"))
	if end < 0 && bytes.HasSuffix(front, []byte("\n---")) {
		end = len(front) - 4
	}
	if end < 0 {
		return s, invalidSummary(name, "unterminated YAML")
	}
	if err = yaml.Unmarshal(normalized[4:4+end], &s.Meta); err != nil {
		// Native generators sometimes emit an unquoted ': ' in these scalar
		// fields. Quote only that unambiguous case in memory; keep Raw and its
		// immutable transfer digest exactly as produced by the source.
		header := strings.Split(string(normalized[4:4+end]), "\n")
		changed := false
		for i, line := range header {
			key, value, ok := strings.Cut(line, ":")
			value = strings.TrimSpace(value)
			if !ok || (key != "title" && key != "description") || !strings.Contains(value, ": ") ||
				strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") || strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
				continue
			}
			quoted, _ := json.Marshal(value)
			header[i] = key + ": " + string(quoted)
			changed = true
		}
		s.Meta = Frontmatter{}
		if !changed || yaml.Unmarshal([]byte(strings.Join(header, "\n")), &s.Meta) != nil {
			return s, invalidSummary(name, "invalid YAML frontmatter")
		}
		s.MetadataRecovered = true
	}
	if strings.TrimSpace(s.Meta.Title) == "" || strings.TrimSpace(s.Meta.Description) == "" || s.Meta.Applications == nil {
		return s, invalidSummary(name, "missing title, description or applications")
	}
	bodyStart := 4 + end + len("\n---")
	s.Body = strings.TrimSpace(string(normalized[bodyStart:]))
	return s, nil
}

type SourceRef struct {
	Device   string
	Entry    Entry
	Start    time.Time
	Duration time.Duration
}

func Select(sources map[string]Manifest, cutoff time.Time) (map[string][]SourceRef, error) {
	windows := map[string][]SourceRef{}
	for device, m := range sources {
		for _, e := range m.Entries {
			t, d, err := ParseName(e.Name)
			if err != nil {
				return nil, err
			}
			if t.Add(d).After(cutoff) {
				continue
			}
			key := WindowKey(t)
			if t.Add(d).After(t.Truncate(6 * time.Hour).Add(6 * time.Hour)) {
				return nil, fmt.Errorf("summary crosses natural UTC window: %s", e.Name)
			}
			windows[key] = append(windows[key], SourceRef{device, e, t, d})
		}
	}
	for key, refs := range windows {
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].Device != refs[j].Device {
				return refs[i].Device < refs[j].Device
			}
			if !refs[i].Start.Equal(refs[j].Start) {
				return refs[i].Start.Before(refs[j].Start)
			}
			return refs[i].Entry.Name < refs[j].Entry.Name
		})
		selected := []SourceRef{}
		for _, r := range refs {
			covered := false
			if r.Duration == 10*time.Minute {
				for _, other := range refs {
					if other.Device == r.Device && other.Duration == 6*time.Hour && !r.Start.Before(other.Start) && !r.Start.Add(r.Duration).After(other.Start.Add(other.Duration)) {
						covered = true
						break
					}
				}
			}
			if !covered {
				selected = append(selected, r)
			}
		}
		windows[key] = selected
	}
	return windows, nil
}
func InputKeys(refs []SourceRef) []string {
	r := make([]string, 0, len(refs))
	for _, ref := range refs {
		r = append(r, ref.Device+":"+ref.Entry.Name+":"+ref.Entry.Digest)
	}
	return r
}
func objectPath(root, device string, e Entry) string {
	return filepath.Join(root, "archive", device, "objects", e.Digest, e.Name)
}

func Render(root, _ string, refs []SourceRef, read func(string) ([]byte, error)) ([]byte, error) {
	if len(refs) == 0 {
		return nil, errors.New("cannot render an empty window")
	}
	apps := map[string]bool{}
	summaries := make([]Summary, 0, len(refs))
	for _, ref := range refs {
		p := objectPath(root, ref.Device, ref.Entry)
		b, err := read(p)
		if err != nil {
			return nil, err
		}
		if int64(len(b)) != ref.Entry.Size || Digest(b) != ref.Entry.Digest {
			return nil, fmt.Errorf("archive integrity mismatch: %s/%s", ref.Device, ref.Entry.Name)
		}
		s, err := Parse(ref.Device, ref.Entry.Name, b)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
		for _, a := range s.Meta.Applications {
			apps[a] = true
		}
	}
	// Reuse one native overview verbatim: 6h first, then stable source order.
	sort.Slice(summaries, func(i, j int) bool {
		a, b := summaries[i], summaries[j]
		if a.Duration != b.Duration {
			return a.Duration > b.Duration
		}
		if a.Device != b.Device {
			return a.Device < b.Device
		}
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		return a.Entry.Name < b.Entry.Name
	})
	meta := Frontmatter{Title: summaries[0].Meta.Title, Description: summaries[0].Meta.Description, Applications: []string{}}
	// Interleave activities, not devices. Source identity remains in state and
	// archive links; neither transport metadata nor repeated YAML belongs here.
	sort.SliceStable(summaries, func(i, j int) bool {
		if !summaries[i].Start.Equal(summaries[j].Start) {
			return summaries[i].Start.Before(summaries[j].Start)
		}
		if summaries[i].Device != summaries[j].Device {
			return summaries[i].Device < summaries[j].Device
		}
		return summaries[i].Entry.Name < summaries[j].Entry.Name
	})
	var body strings.Builder
	escape := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]")
	for _, s := range summaries {
		title := escape.Replace(strings.Join(strings.Fields(s.Meta.Title), " "))
		fmt.Fprintf(&body, "## [%s](<%s>)\n\n", title, filepath.ToSlash(objectPath(root, s.Device, s.Entry)))
		text := s.Body
		// Native summaries use this generic opening heading. The activity title
		// above replaces it; substantive headings and source text stay intact.
		if text == "## Memory summary" || strings.HasPrefix(text, "## Memory summary\n") {
			text = strings.TrimSpace(strings.TrimPrefix(text, "## Memory summary"))
		}
		body.WriteString(text)
		body.WriteString("\n\n")
	}
	for a := range apps {
		meta.Applications = append(meta.Applications, a)
	}
	sort.Strings(meta.Applications)
	y, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return append(append([]byte("---\n"), y...), []byte("---\n\n"+body.String())...), nil
}

func cloneState(s State) State {
	b, _ := json.Marshal(s)
	var r State
	_ = json.Unmarshal(b, &r)
	return r
}
