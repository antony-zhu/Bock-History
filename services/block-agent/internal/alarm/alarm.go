package alarm

import "block.local/block-agent/internal/state"

// Active returns a defensive copy of the current active-alarm projection.
// Alarm lifecycle persistence is owned by storage; this package keeps alarm
// policy out of the PLC adapter and HMI layers.
func Active(items []state.Alarm) []state.Alarm {
	return append([]state.Alarm(nil), items...)
}
