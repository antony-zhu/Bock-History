package uplink

import "fmt"

type Topics struct {
	Presence   string
	Hello      string
	Heartbeat  string
	Snapshot   string
	Event      string
	Alarm      string
	Replay     string
	SyncStatus string
	DownSync   string
}

func NewTopics(siteID, blockID string) Topics {
	base := fmt.Sprintf("bdm/v1/sites/%s/blocks/%s", siteID, blockID)
	return Topics{
		Presence: base + "/up/presence", Hello: base + "/up/hello",
		Heartbeat: base + "/up/heartbeat", Snapshot: base + "/up/snapshot",
		Event: base + "/up/event", Alarm: base + "/up/alarm",
		Replay: base + "/up/replay", SyncStatus: base + "/up/sync-status",
		DownSync: base + "/down/sync",
	}
}

func (t Topics) Upstream(channel string) (string, error) {
	switch channel {
	case "snapshot":
		return t.Snapshot, nil
	case "event":
		return t.Event, nil
	case "alarm":
		return t.Alarm, nil
	default:
		return "", fmt.Errorf("unknown reliable channel %q", channel)
	}
}
