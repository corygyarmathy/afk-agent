package revise

// RoundTrip loads job jobID's progress and saves it again, as a transition that
// writes progress does: what a progress file written by older code must come
// through with its keys and values.
func RoundTrip(d *Deps, jobID string) error {
	p, err := d.load(jobID)
	if err != nil {
		return err
	}
	return d.save(jobID, p)
}
