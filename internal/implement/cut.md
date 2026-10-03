The agent counted your commits: {{.Lines}} changed lines, and {{.Tests}} of tests.
That is over the size signal of {{.Signal}}.

Cut the branch to a first coherent piece: one concern, under the signal, worth
merging on its own. A refactor the work does not need goes in the rest. Rewrite
the commits on `{{.Branch}}` so that it holds the piece and nothing else -
reset it and commit again if you need to - and run the local gate on it. The
work as it stands is kept on the remote as `{{.Kept}}`, and the issue filed for
the rest names it, so nothing you reset away is lost.

Then write what is left to `.git/afk-remainder.md`: what the rest of the work
is, not what the piece does. Make `.git/afk-description.md` describe the piece,
titled as a first piece.

This is the only cut. If no coherent first piece exists, leave the branch as it
is, and the agent hands the work back to a human.
