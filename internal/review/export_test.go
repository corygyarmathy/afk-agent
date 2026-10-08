package review

import (
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/spend"
)

// Body is the comment a review of head, or of since..head, is posted as, for
// the tests that read one as it is posted.
func (d *Deps) Body(n int, since, head string, reply opencode.Reply) string {
	return d.body(n, since, head, reply, request{}, spend.Spent{})
}
