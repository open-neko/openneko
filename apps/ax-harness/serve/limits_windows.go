package serve

// limitRunner has nothing to set: the job object that StartRunner puts the
// runner in holds its limits.
func limitRunner(RunnerLimits) error { return nil }
