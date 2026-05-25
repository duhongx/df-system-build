package stages

import (
	"context"
	"fmt"
	"os"
	"strings"

	"df-build-server/internal/pipeline/types"
	"df-build-server/pkg/logger"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type GitCloneStage struct{}

func (s *GitCloneStage) Name() string { return "拉取代码" }
func (s *GitCloneStage) Code() string { return "GIT_CLONE" }

func (s *GitCloneStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	sourceDir := pCtx.Workspace.SourceDir()
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("$ git clone -b %s %s %s", pCtx.GitBranch, pCtx.GitRepo, sourceDir), "stdout")

	// Setup SSH auth for git@ URLs
	var authMethod *ssh.PublicKeys
	if strings.HasPrefix(pCtx.GitRepo, "git@") || strings.HasPrefix(pCtx.GitRepo, "ssh://") {
		// Try to read SSH key from common locations
		keyPaths := []string{"/root/.ssh/id_rsa", "/root/.ssh/id_ed25519"}
		for _, keyPath := range keyPaths {
			keyData, err := os.ReadFile(keyPath)
			if err == nil {
				auth, err := ssh.NewPublicKeys("git", keyData, "")
				if err == nil {
					auth.HostKeyCallback = gossh.InsecureIgnoreHostKey()
					authMethod = auth
					break
				}
			}
		}
	}

	cloneOpts := &git.CloneOptions{
		URL:           pCtx.GitRepo,
		ReferenceName: plumbing.NewBranchReferenceName(pCtx.GitBranch),
		SingleBranch:  true,
		Depth:         0,
		Progress:      &logWriter{pCtx: pCtx},
	}
	if authMethod != nil {
		cloneOpts.Auth = authMethod
	}

	repo, err := git.PlainCloneContext(ctx, sourceDir, false, cloneOpts)
	if err != nil {
		errMsg := fmt.Sprintf("git clone 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	pCtx.OnLog(pCtx.PipelineID, 0, "代码拉取完成", "stdout")

	// List directory contents
	pCtx.OnLog(pCtx.PipelineID, 0, "", "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, "$ ls -al "+sourceDir, "stdout")
	entries, err := os.ReadDir(sourceDir)
	if err == nil {
		for _, entry := range entries {
			info, _ := entry.Info()
			if info != nil {
				pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("%s %8d %s %s",
					info.Mode(), info.Size(), info.ModTime().Format("Jan 02 15:04"), entry.Name()), "stdout")
			}
		}
	}

	// Parse git commit info
	pCtx.OnLog(pCtx.PipelineID, 0, "", "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, "解析最新提交信息...", "stdout")
	ref, err := repo.Head()
	if err == nil {
		commit, err := repo.CommitObject(ref.Hash())
		if err == nil {
			commitHash := commit.Hash.String()
			commitAuthor := commit.Author.Name
			commitEmail := commit.Author.Email
			commitMessage := strings.Split(commit.Message, "\n")[0]

			pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Commit: %s", commitHash[:8]), "stdout")
			pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Author: %s <%s>", commitAuthor, commitEmail), "stdout")
			pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Message: %s", commitMessage), "stdout")

			if pCtx.Pipeline != nil {
				pCtx.Pipeline.GitCommitHash = commitHash
				pCtx.Pipeline.GitCommitAuthor = commitAuthor
				pCtx.Pipeline.GitCommitMessage = commitMessage
			}
		}
	}

	logger.Log.Infof("Git clone completed: %s", sourceDir)
	return &types.StageResult{ExitCode: 0}, nil
}

// logWriter implements io.Writer to stream git progress to SSE
type logWriter struct {
	pCtx *types.PipelineContext
	buf  string
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	w.buf += string(p)
	for {
		idx := strings.IndexByte(w.buf, '\n')
		if idx < 0 {
			idx = strings.IndexByte(w.buf, '\r')
			if idx < 0 { break }
		}
		line := strings.TrimSpace(w.buf[:idx])
		w.buf = w.buf[idx+1:]
		if line != "" {
			w.pCtx.OnLog(w.pCtx.PipelineID, 0, line, "stdout")
		}
	}
	return len(p), nil
}
