package worker

// recoverForTest runs the recovery once, as the minute tick would.
func (w *Worker) recoverForTest() bool {
	return w.recover() == nil && w.Status().Available
}
