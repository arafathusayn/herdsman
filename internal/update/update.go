package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Repository is where releases are published.
const Repository = "arafathusayn/herdsman"

// Size limits for what comes over the network.
const (
	maxLatest    = 1 << 20
	maxChecksums = 1 << 20
	maxSignature = 1 << 12
	maxArchive   = 512 << 20
	maxBinary    = 1 << 30
)

// Options is one run of the update. The real values are set by the command;
// tests point Client, API and Base at a local server.
type Options struct {
	Client  *http.Client
	API     string // GitHub API root
	Base    string // root of the release downloads
	Keys    []Key
	Current string // this binary's version, or "dev"
	Exe     string
	GOOS    string
	GOARCH  string
	Smoke   SmokeFunc
	Stdout  io.Writer

	Target         string // a version to install instead of the latest
	Check          bool   // report only
	AllowDowngrade bool
}

// Run finds the release, verifies it and replaces the binary.
func Run(ctx context.Context, o Options) error {
	if !o.Check && len(o.Keys) == 0 {
		return ErrNoKeys
	}
	target := o.Target
	if target == "" {
		tag, err := latestTag(ctx, o)
		if err != nil {
			return err
		}
		target = strings.TrimPrefix(tag, "v")
	}
	if _, err := parseVersion(target); err != nil {
		return fmt.Errorf("release version: %w", err)
	}
	order := 1 // a dev build is older than any release
	if o.Current != "dev" {
		var err error
		if order, err = CompareVersions(target, o.Current); err != nil {
			return fmt.Errorf("this binary's version: %w", err)
		}
	}
	if order == 0 {
		fmt.Fprintf(o.Stdout, "herdsman %s is up to date\n", o.Current)
		return nil
	}
	if o.Check {
		fmt.Fprintf(o.Stdout, "herdsman %s installed, %s available\n", o.Current, target)
		return nil
	}
	if order < 0 && !o.AllowDowngrade {
		return fmt.Errorf("%w: %s is older than %s; pass --allow-downgrade to install it", ErrDowngrade, target, o.Current)
	}
	return install(ctx, o, target)
}

func install(ctx context.Context, o Options, version string) error {
	dir := fmt.Sprintf("%s/%s/releases/download/v%s/", o.Base, Repository, version)
	checksums, err := fetch(ctx, o, dir+"checksums.txt", maxChecksums)
	if err != nil {
		return err
	}
	sig, err := fetch(ctx, o, dir+"checksums.txt.sig", maxSignature)
	if err != nil {
		return err
	}
	if err := VerifySignature(o.Keys, checksums, sig); err != nil {
		return err
	}
	sums, err := ParseChecksums(checksums)
	if err != nil {
		return err
	}
	// The name holds the version and is in the signed list, which ties the
	// signature to this release: an older signed list has no such entry.
	name := fmt.Sprintf("herdsman_%s_%s_%s.tar.gz", version, o.GOOS, o.GOARCH)
	if _, ok := sums[name]; !ok {
		return fmt.Errorf("%w: %s is not listed", ErrBadChecksum, name)
	}
	archive, err := fetch(ctx, o, dir+name, maxArchive)
	if err != nil {
		return err
	}
	if err := VerifyArchive(sums, name, archive); err != nil {
		return err
	}
	binary, err := ExtractBinary(archive, maxBinary)
	if err != nil {
		return err
	}
	if err := Replace(ctx, o.Exe, binary, version, o.Smoke); err != nil {
		return err
	}
	fmt.Fprintf(o.Stdout, "herdsman updated from %s to %s; restart a running daemon to use it\n", o.Current, version)
	return nil
}

func latestTag(ctx context.Context, o Options) (string, error) {
	data, err := fetch(ctx, o, o.API+"/repos/"+Repository+"/releases/latest", maxLatest)
	if err != nil {
		return "", err
	}
	var latest struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(data, &latest); err != nil {
		return "", fmt.Errorf("read the latest release: %w", err)
	}
	return latest.TagName, nil
}

func fetch(ctx context.Context, o Options, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("fetch %s: larger than %d bytes", url, limit)
	}
	return data, nil
}

type parsedVersion struct {
	core [3]int
	pre  []string
}

func parseVersion(v string) (parsedVersion, error) {
	var p parsedVersion
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return p, fmt.Errorf("%q is not a release version", v)
	}
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return p, fmt.Errorf("%q is not a release version", v)
		}
		p.core[i] = n
	}
	if hasPre {
		if pre == "" || strings.ContainsAny(pre, "/\\") {
			return p, errors.New("bad pre-release in " + strconv.Quote(v))
		}
		p.pre = strings.Split(pre, ".")
	}
	return p, nil
}

// CompareVersions orders two semantic versions: -1, 0 or 1.
func CompareVersions(a, b string) (int, error) {
	x, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	y, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range x.core {
		if c := cmpInt(x.core[i], y.core[i]); c != 0 {
			return c, nil
		}
	}
	switch {
	case len(x.pre) == 0 && len(y.pre) == 0:
		return 0, nil
	case len(x.pre) == 0:
		return 1, nil
	case len(y.pre) == 0:
		return -1, nil
	}
	for i := 0; i < len(x.pre) && i < len(y.pre); i++ {
		if c := cmpIdent(x.pre[i], y.pre[i]); c != 0 {
			return c, nil
		}
	}
	return cmpInt(len(x.pre), len(y.pre)), nil
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpIdent(a, b string) int {
	x, errX := strconv.Atoi(a)
	y, errY := strconv.Atoi(b)
	switch {
	case errX == nil && errY == nil:
		return cmpInt(x, y)
	case errX == nil:
		return -1
	case errY == nil:
		return 1
	}
	return strings.Compare(a, b)
}
