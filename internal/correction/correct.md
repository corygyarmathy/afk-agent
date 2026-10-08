The pull request is open at `{{.Reviewed}}`, and its advisory review found
what is in `.git/afk-findings.md`: Correctness and Standards findings, each
numbered as the review numbers it. They are yours to make right before the
pull request is handed off, not advice.

- A Correctness finding carries its reproduction: a test, or a command and
  anything it needs. Commit it as a regression test, so that the local gate and
  CI run it from now on, and make it pass.
- A Standards finding cites a rule in a file of this repository. Make the code
  meet it.
- A finding you find mistaken, leave alone: it stays in the review as advice.

Commit on top of `{{.Reviewed}}` on `{{.Branch}}`, and do not rewrite it: the
review's citations are at that commit. In each commit's message, name every
finding the commit corrects on a trailer line of its own, such as
`Corrects: advisory 3`: the agent links each finding to the commit that names
it. Then run the local gate, `{{.Gate}}`. Nothing reviews the correction again:
the gate and CI are what check it.
