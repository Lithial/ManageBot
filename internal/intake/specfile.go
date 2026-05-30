package intake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SpecfileAdapter implements the `wrap submit <spec.md>` intake path: a spec
// file that may carry its own `---` frontmatter (project/repo/verification_command),
// making it self-contained for batch/automation use. Emission writes a `.DONE`
// sidecar next to the spec.
type SpecfileAdapter struct {
	submitter RunSubmitter
}

func NewSpecfileAdapter(s RunSubmitter) *SpecfileAdapter {
	return &SpecfileAdapter{submitter: s}
}

// specMeta is the optional frontmatter of a spec file.
type specMeta struct {
	Project             string
	Repo                string
	VerificationCommand string
}

// parseFrontmatter splits leading `---`-fenced frontmatter from the spec body.
// Frontmatter is flat `key: value` lines; unknown keys and blank lines are
// ignored. Without a leading `---`, the whole content is the body.
func parseFrontmatter(content string) (specMeta, string) {
	var meta specMeta
	if !strings.HasPrefix(content, "---\n") {
		return meta, content
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		// Unterminated frontmatter — treat the whole thing as body, untouched.
		return specMeta{}, content
	}
	block := rest[:end]
	body := rest[end+len("\n---\n"):]
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "project":
			meta.Project = val
		case "repo":
			meta.Repo = val
		case "verification_command":
			meta.VerificationCommand = val
		}
	}
	return meta, body
}

// SubmitFromFile reads specPath, applies its frontmatter (falling back to
// repoFallback / the repo basename when fields are omitted), and submits a run.
func (a *SpecfileAdapter) SubmitFromFile(ctx context.Context, specPath, repoFallback string) (SubmitRunResponse, error) {
	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("abs spec path: %w", err)
	}
	raw, err := os.ReadFile(absSpec)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("read spec: %w", err)
	}
	meta, body := parseFrontmatter(string(raw))

	repo := meta.Repo
	if repo == "" {
		repo = repoFallback
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return SubmitRunResponse{}, fmt.Errorf("abs repo path: %w", err)
	}
	project := meta.Project
	if project == "" {
		project = filepath.Base(absRepo)
	}

	return a.submitter.SubmitRun(ctx, SubmitRunRequest{
		ProjectName:         project,
		RepoPath:            absRepo,
		IntakeKind:          "specfile",
		IntakeRef:           absSpec,
		SpecMD:              body,
		VerificationCommand: meta.VerificationCommand,
	})
}
