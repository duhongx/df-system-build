package inspection

// FactualObservation applies the confirmed report policy. Availability is
// independent from operating state; only storage/inode usage has a threshold.
func FactualObservation(o *Observation) {
	o.Advice, o.Threshold = "", ""
	if o.Status == "UNKNOWN" || o.Status == "NOT_RUN" || o.Status == "NOT_APPLICABLE" {
		return
	}
	o.Status = "RECORDED"
	o.Reason = ""
	if o.Attributes["spaceExceeded"] == "true" {
		o.Status = "CRITICAL"
		o.Threshold = "磁盘容量或 inode 使用率超过 75%"
		o.Reason = o.Attributes["spaceReason"]
	}
}
