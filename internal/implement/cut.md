The agent counted your commits: {{.Lines}} changed lines, and {{.Tests}} of tests,
not counting generated, vendored or lock files, or files deleted whole. That
is over the size signal of {{.Signal}}: more than one concern, or more than one
sitting's review.

Cut the branch to a first coherent piece: one concern, under the signal, worth
merging on its own. A refactor the rest needs is the natural first piece. A
refactor the work does not need goes in the rest, and is never mixed into the
piece. Rewrite the commits on `{{.Branch}}` so that it holds the piece and
nothing else - reset it and commit again if you need to, but do not create,
switch or delete branches - and run the local gate, `{{.Gate}}`, on it. The
work as it stands is kept on the remote as `{{.Kept}}`, so nothing you reset
away is lost: it is named in the issue filed for the rest.

Then write what is left to `.git/afk-remainder.md`, in GitHub-flavoured
markdown: the agent files it as a new issue, for a human to decide on. Say
what the rest of the work is, not what the piece does. And make
`.git/afk-description.md` describe the piece, with its first line a plain line,
not a heading, that gives a title for the piece.

This is the only cut. If no coherent first piece exists, leave the branch as it
is: the agent then pushes it and hands the work back to a human, as it does a
piece still over the signal.
