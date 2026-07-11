// Package protocol defines the versioned event-ingestion contract shared by
// the Pi agent and the server. It deliberately describes logical events and
// artifacts rather than exposing TeslaCam filesystem paths as the API model.
package protocol

import "time"

type EventUpsert struct {
	DeviceID   string      `json:"device_id"`
	Source     EventSource `json:"source"`
	DetectedAt time.Time   `json:"detected_at"`
	Trigger    *Trigger    `json:"trigger,omitempty"`
	Location   *Location   `json:"location,omitempty"`
}

type EventSource struct {
	Type          string `json:"type"`
	DirectoryName string `json:"directory_name"`
}

type Trigger struct {
	OccurredAtLocal string `json:"occurred_at_local,omitempty"`
	CameraCode      string `json:"camera_code,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type Location struct {
	City               string  `json:"city,omitempty"`
	EstimatedLatitude  float64 `json:"estimated_latitude,omitempty"`
	EstimatedLongitude float64 `json:"estimated_longitude,omitempty"`
}

type Manifest struct {
	Generation      int        `json:"generation"`
	ObservedThrough time.Time  `json:"observed_through"`
	Artifacts       []Artifact `json:"artifacts"`
}

type Artifact struct {
	ID         string        `json:"id"`
	Kind       string        `json:"kind"` // video | thumbnail | source_metadata | other
	SHA256     string        `json:"sha256"`
	Size       int64         `json:"size"`
	MediaType  string        `json:"media_type"`
	SourceName string        `json:"source_name"`
	Segment    *VideoSegment `json:"segment,omitempty"`
}

type VideoSegment struct {
	StartedAtLocal string `json:"started_at_local,omitempty"`
	Camera         string `json:"camera"` // open string: new camera names remain forward-compatible
}

type FinalizeRequest struct {
	CompletionReason string    `json:"completion_reason"`
	SettledAt        time.Time `json:"settled_at"`
}

type ManifestStatus struct {
	EventID      string   `json:"event_id,omitempty"`
	Generation   int      `json:"generation"`
	Status       string   `json:"status"`
	MissingBlobs []string `json:"missing_blobs,omitempty"`
}
