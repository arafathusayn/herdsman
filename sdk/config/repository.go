package config

import (
	"net/url"
	"strings"
)

// RepositoryKey is the name a repository's overrides go under in the user
// file: the host and path of its remote, as github.com/acme/app. It is empty
// for a remote with no host, such as a local folder.
func RepositoryKey(remote string) string {
	host, path := remoteParts(strings.TrimSpace(remote))
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return ""
	}
	return strings.ToLower(host) + "/" + path
}

func remoteParts(remote string) (host, path string) {
	if strings.Contains(remote, "://") {
		parsed, err := url.Parse(remote)
		if err != nil {
			return "", ""
		}
		return parsed.Hostname(), parsed.Path
	}
	// The short form, [user@]host:path. A colon after a slash is a local path.
	head, tail, found := strings.Cut(remote, ":")
	if !found || strings.ContainsAny(head, "/\\") {
		return "", ""
	}
	if _, after, hasUser := strings.Cut(head, "@"); hasUser {
		head = after
	}
	return head, tail
}
