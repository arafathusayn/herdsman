package wtree

import (
	"fmt"
	"os"
	"strings"
)

// Plan is what a repository's root files imply for setting up a worktree.
type Plan struct {
	Share          []string // Ignored folders worth sharing from the main tree.
	Install        []string // Commands that install dependencies, in order.
	GoCachePerTask bool     // Give each task its own Go build cache.
	Compose        bool     // The repository defines a compose project.
	EnvFiles       []string // .env files to mention; none is ever copied by default.
}

// Detect reads the root of a repository once and returns its setup plan.
func Detect(root string) (Plan, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return Plan{}, fmt.Errorf("wtree: detect ecosystem: %w", err)
	}
	present := make(map[string]bool, len(entries))
	for _, entry := range entries {
		present[entry.Name()] = true
	}
	var plan Plan
	if install := nodeInstall(present); install != "" {
		plan.Share = append(plan.Share, "node_modules")
		plan.Install = append(plan.Install, install)
	}
	if present["Cargo.lock"] {
		plan.Share = append(plan.Share, "target")
	}
	if present["uv.lock"] {
		plan.Install = append(plan.Install, "uv sync")
	}
	if present["poetry.lock"] {
		plan.Install = append(plan.Install, "poetry install")
	}
	plan.GoCachePerTask = present["go.sum"]
	plan.Compose = present["compose.yaml"] || present["compose.yml"] ||
		present["docker-compose.yml"] || present["docker-compose.yaml"]
	if present[".env.example"] {
		for _, entry := range entries {
			if name := entry.Name(); name == ".env" || strings.HasPrefix(name, ".env.") {
				plan.EnvFiles = append(plan.EnvFiles, name)
			}
		}
	}
	return plan, nil
}

// nodeInstall picks one install command when several lock files are present,
// since they would all write the same node_modules.
func nodeInstall(present map[string]bool) string {
	switch {
	case present["bun.lock"]:
		return "bun install --frozen-lockfile"
	case present["pnpm-lock.yaml"]:
		return "pnpm install --frozen-lockfile"
	case present["package-lock.json"]:
		return "npm ci"
	case present["yarn.lock"] && present[".yarnrc.yml"]:
		return "yarn install --immutable" // Yarn 2 and later dropped the older flag.
	case present["yarn.lock"]:
		return "yarn install --frozen-lockfile"
	}
	return ""
}
