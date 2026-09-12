package ablation

import (
	"context"
	"net/http"
	"strings"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// This file exposes the minimum surface needed by sibling experiment commands
// without widening the internal API of the harness itself.

// ExportDefaultCatalog returns the catalog the harness uses.
func ExportDefaultCatalog() taxonomy.Catalog { return defaultCatalog() }

// ExportRunFull runs the production (FULL) variant for one sample.
func ExportRunFull(ctx context.Context, r *Runner, s Sample) Outcome { return runFull(ctx, r, s) }

// ExportHead returns the first n characters of a string plus an ellipsis.
func ExportHead(s string, n int) string { return truncate(s, n) }

// ExportHTTPClient returns the shared experiment transport.
func ExportHTTPClient() *http.Client { return &httpClient }

// ExportResolveAuthor asks the endpoint which handle actually owns a post URL.
// It returns the handle, the permalink the tool reported, and any error. This
// is the ground-truth check the corpus depends on: a sample whose claimed
// author differs from the resolved author is mislabelled and must not be scored.
func ExportResolveAuthor(ctx context.Context, r *Runner, postURL string) (string, string, error) {
	content := "使用 x_search 读取此 X 帖。只回答两行，不要解释：\n" +
		"AUTHOR: <该帖作者 handle，含 @>\nLINK: <该帖完整链接>\nURL: " + postURL
	env, err := r.Call(ctx, Request{Content: content, Tools: true, ToolChoice: "required", MaxTok: 300})
	if err != nil {
		return "", "", err
	}
	text, _, err := structuredPayload(env)
	if err != nil {
		// This probe uses free-form output, so read the message block directly.
		for _, item := range env.Output {
			if item.Type != "message" {
				continue
			}
			for _, c := range item.Content {
				if c.Type == "output_text" {
					text = c.Text
				}
			}
		}
		if text == "" {
			return "", "", err
		}
	}
	var author, link string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if v, found := strings.CutPrefix(line, "AUTHOR:"); found {
			author = strings.TrimPrefix(strings.TrimSpace(v), "@")
		}
		if v, found := strings.CutPrefix(line, "LINK:"); found {
			link = strings.TrimSpace(v)
		}
	}
	return author, link, nil
}

// JudgeExport re-scores a stored outcome against a sample using the current
// scorer, so a corpus correction does not require new model calls.
func JudgeExport(out Outcome, s Sample) Score { return Judge(out, s) }

// ExportRunThread runs the production search prompt that also reads thread
// comments.
func ExportRunThread(ctx context.Context, r *Runner, s Sample) Outcome {
	return runFull(ctx, r, s)
}

// ExportRunPostOnly runs the same search path but asks the model not to expand
// comments, isolating the value of thread retrieval.
func ExportRunPostOnly(ctx context.Context, r *Runner, s Sample) Outcome {
	return runPostOnly(ctx, r, s)
}
