package inspection

import "time"

// Execution is the actual command/SQL and its captured result, not a diagnosis
// derived from a separately configured environment baseline.
type Execution struct {
	Kind       string    `json:"kind"`
	Command    string    `json:"command"`
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
	ExitCode   int       `json:"exitCode"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Truncated  bool      `json:"truncated"`
	Error      string    `json:"error"`
}
