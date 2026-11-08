package suggest

import (
	"cmp"
	"slices"
	"strings"
)

// The points behind a score. Integers only, so a score never depends on
// floating-point rounding.
const (
	PointsHarness    = 30
	PointsEcosystem  = 20
	PointsLabel      = 10
	PointsEvent      = 5
	PointsQueryTitle = 8
	PointsQueryTag   = 4
	PointsQueryBody  = 1
)

// The reason codes. New codes may be added; these never change meaning.
const (
	ReasonHarness          = "harness"
	ReasonEcosystem        = "ecosystem"
	ReasonLabel            = "label"
	ReasonEvent            = "event"
	ReasonQueryTitle       = "query-title"
	ReasonQueryTag         = "query-tag"
	ReasonQueryBody        = "query-body"
	ReasonGeneral          = "general"
	ReasonHarnessMismatch  = "harness-mismatch"
	ReasonPlatformMismatch = "platform-mismatch"
	ReasonInstalled        = "installed"
)

// Installed is what the machine already has.
type Installed struct {
	IDs   []string `json:"ids,omitempty"`
	Repos []string `json:"repos,omitempty"`
}

// Machine is everything a ranking depends on. The caller detects it; the
// ranking reads no clock, file or network, which is what makes it repeatable.
type Machine struct {
	Platform     string    `json:"platform"`
	Harnesses    []string  `json:"harnesses"`
	Ecosystems   []string  `json:"ecosystems"`
	Labels       []string  `json:"labels"`
	RecentEvents []string  `json:"recent_events"`
	Installed    Installed `json:"installed"`
}

// Query narrows a ranking. For replaces the detected harnesses.
type Query struct {
	Text string
	Kind string
	For  string
	Tag  string
}

// Reason explains points given, or why an entry was left out.
type Reason struct {
	Code   string `json:"code"`
	Value  string `json:"value,omitempty"`
	Points int    `json:"points"`
}

// Scored is a ranked entry. Its score is the sum of its reasons' points.
type Scored struct {
	Entry   Entry    `json:"entry"`
	Score   int      `json:"score"`
	Reasons []Reason `json:"reasons"`
}

// Excluded is an entry left out for this machine.
type Excluded struct {
	ID     string `json:"id"`
	Reason Reason `json:"reason"`
}

// Ranking is the result of Rank.
type Ranking struct {
	Items    []Scored   `json:"items"`
	Excluded []Excluded `json:"excluded"`
}

// Rank orders entries for a machine: by score, then source precedence, then
// id. Ids are unique in a catalog, so the order is total and does not depend
// on the input order. Entries that cannot apply are reported in Excluded;
// entries the query filters out are dropped.
func Rank(entries []Entry, m Machine, q Query) Ranking {
	in := newMachineSets(m, q)
	out := Ranking{Items: []Scored{}, Excluded: []Excluded{}}
	terms := uniqueLower(strings.Fields(q.Text))
	for _, e := range entries {
		if !matchesFilters(e, q) {
			continue
		}
		matched, ok := matchTerms(e, terms)
		if !ok {
			continue
		}
		if reason, excluded := in.exclude(e); excluded {
			out.Excluded = append(out.Excluded, Excluded{ID: e.ID, Reason: reason})
			continue
		}
		reasons := append(in.score(e), matched...)
		if len(reasons) == 0 {
			reasons = []Reason{{Code: ReasonGeneral}}
		}
		total := 0
		for _, r := range reasons {
			total += r.Points
		}
		out.Items = append(out.Items, Scored{Entry: e, Score: total, Reasons: reasons})
	}
	slices.SortFunc(out.Items, func(a, b Scored) int {
		return cmp.Or(
			cmp.Compare(b.Score, a.Score),
			cmp.Compare(a.Entry.Source.Kind.rank(), b.Entry.Source.Kind.rank()),
			cmp.Compare(a.Entry.ID, b.Entry.ID),
		)
	})
	slices.SortFunc(out.Excluded, func(a, b Excluded) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func matchesFilters(e Entry, q Query) bool {
	if q.Kind != "" && string(e.Kind) != q.Kind {
		return false
	}
	return q.Tag == "" || slices.Contains(e.Tags, strings.ToLower(q.Tag))
}

// matchTerms needs every term to match, and each counts once, at its best
// place.
func matchTerms(e Entry, terms []string) ([]Reason, bool) {
	title, body := strings.ToLower(e.Title), strings.ToLower(e.Body)
	var reasons []Reason
	for _, t := range terms {
		switch {
		case strings.Contains(title, t):
			reasons = append(reasons, Reason{ReasonQueryTitle, t, PointsQueryTitle})
		case slices.Contains(e.Tags, t):
			reasons = append(reasons, Reason{ReasonQueryTag, t, PointsQueryTag})
		case strings.Contains(body, t):
			reasons = append(reasons, Reason{ReasonQueryBody, t, PointsQueryBody})
		default:
			return nil, false
		}
	}
	return reasons, true
}

type machineSets struct {
	platform   string
	harnesses  []string
	ecosystems []string
	labels     []string
	events     []string
	ids        []string
	repos      []string
}

func newMachineSets(m Machine, q Query) machineSets {
	harnesses := m.Harnesses
	if q.For != "" {
		harnesses = []string{q.For}
	}
	repos := make([]string, 0, len(m.Installed.Repos))
	for _, r := range m.Installed.Repos {
		repos = append(repos, normalizeRepo(r))
	}
	return machineSets{
		platform:   strings.ToLower(m.Platform),
		harnesses:  uniqueLower(harnesses),
		ecosystems: uniqueLower(m.Ecosystems),
		labels:     uniqueLower(m.Labels),
		events:     uniqueLower(m.RecentEvents),
		ids:        uniqueLower(m.Installed.IDs),
		repos:      repos,
	}
}

// exclude says why an entry cannot apply. A machine with no detected harness
// or platform excludes nothing on that ground: unknown is not a mismatch.
func (in machineSets) exclude(e Entry) (Reason, bool) {
	if want := e.AppliesTo.Harnesses; len(want) > 0 && len(in.harnesses) > 0 && len(overlap(want, in.harnesses)) == 0 {
		return Reason{Code: ReasonHarnessMismatch, Value: strings.Join(want, ",")}, true
	}
	if want := e.AppliesTo.Platforms; len(want) > 0 && in.platform != "" && !slices.Contains(want, in.platform) {
		return Reason{Code: ReasonPlatformMismatch, Value: strings.Join(want, ",")}, true
	}
	if slices.Contains(in.ids, e.ID) {
		return Reason{Code: ReasonInstalled, Value: e.ID}, true
	}
	for _, repo := range []string{e.Repo, skillRepo(e)} {
		if repo != "" && slices.Contains(in.repos, normalizeRepo(repo)) {
			return Reason{Code: ReasonInstalled, Value: repo}, true
		}
	}
	return Reason{}, false
}

func skillRepo(e Entry) string {
	if e.Install == nil {
		return ""
	}
	return e.Install.SkillRepo
}

func (in machineSets) score(e Entry) []Reason {
	var reasons []Reason
	add := func(code string, points int, values []string) {
		for _, v := range values {
			reasons = append(reasons, Reason{code, v, points})
		}
	}
	add(ReasonHarness, PointsHarness, overlap(e.AppliesTo.Harnesses, in.harnesses))
	add(ReasonEcosystem, PointsEcosystem, overlap(e.AppliesTo.Ecosystems, in.ecosystems))
	add(ReasonLabel, PointsLabel, overlap(e.Tags, in.labels))
	add(ReasonEvent, PointsEvent, overlap(e.Tags, in.events))
	return reasons
}

// overlap returns the sorted values of have that are in set, without repeats.
func overlap(have, set []string) []string {
	var out []string
	for _, v := range uniqueLower(have) {
		if slices.Contains(set, v) {
			out = append(out, v)
		}
	}
	return out
}

func uniqueLower(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, strings.ToLower(v))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// normalizeRepo makes the forms of one repository address compare equal.
func normalizeRepo(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, prefix := range []string{"https://", "ssh://", "git@"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.Replace(s, ":", "/", 1)
}
