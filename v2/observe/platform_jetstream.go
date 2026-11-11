package observe

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// JetStream reserves each file stream's whole size cap when the stream is made, and refuses a new stream whose cap
// does not fit what is left. App streams are protected (preventDelete), so a removed instance's stream stays in NATS,
// reserving its cap, until the operator deletes it: the check says when that has left no room for one more stream, and
// names the streams no instance declares.

// JetStreamStorage is NATS's own account of its file store: what the monitoring endpoint's /jsz answers with every
// stream's configuration, or that it could not be read.
type JetStreamStorage struct {
	Output []byte
	Failed bool
}

// jetStreamGuide is where the operator reads how to delete a leftover stream.
const jetStreamGuide = "docs/operations.md#leftover-event-streams"

const mebibyte = 1 << 20

// jsz is the part of NATS's /jsz answer the check reads.
type jsz struct {
	Config struct {
		MaxStorage int64 `json:"max_storage"`
	} `json:"config"`
	ReservedStorage *int64 `json:"reserved_storage"`
	Accounts        []struct {
		Streams []struct {
			Name   string `json:"name"`
			Config struct {
				MaxBytes int64 `json:"max_bytes"`
			} `json:"config"`
			State struct {
				Messages int64 `json:"messages"`
			} `json:"state"`
		} `json:"stream_detail"`
	} `json:"account_details"`
}

// jetStreamEntries judges the store against the streams the compiled instances declare: whether one more stream of
// the largest declared cap still fits, and which streams in NATS no instance declares. Key-value buckets (KV_*) are the
// platform's own and never leftovers. Both findings warn: nothing running is affected until the next new stream.
func jetStreamEntries(storage JetStreamStorage, compiled []CompiledInstance) []PlatformEntry {
	answer, valid := parseJetStream(storage)
	if !valid {
		return []PlatformEntry{warn("cannot read JetStream's storage from nats-0, so whether a new stream fits is unchecked")}
	}
	declared, largest := declaredStreams(compiled)
	leftovers, held := []string{}, int64(0)
	for _, account := range answer.Accounts {
		for _, stream := range account.Streams {
			if declared[stream.Name] || strings.HasPrefix(stream.Name, "KV_") {
				continue
			}
			leftovers = append(leftovers, fmt.Sprintf("%s (%d messages)", stream.Name, stream.State.Messages))
			held += stream.Config.MaxBytes
		}
	}
	sort.Strings(leftovers)
	entries := []PlatformEntry{}
	free := answer.Config.MaxStorage - *answer.ReservedStorage
	if free < largest {
		entries = append(entries, warn(fmt.Sprintf("JetStream has %d MiB unreserved, less than a stream's %d MiB: the next "+
			"instance that publishes events is refused its stream (%s)", free/mebibyte, largest/mebibyte, jetStreamGuide)))
	} else {
		entries = append(entries, ok(fmt.Sprintf("JetStream has %d of %d MiB unreserved", free/mebibyte,
			answer.Config.MaxStorage/mebibyte)))
	}
	if len(leftovers) > 0 {
		entries = append(entries, warn(fmt.Sprintf("%d stream(s) in NATS that no instance declares reserve %d MiB: %s (%s)",
			len(leftovers), held/mebibyte, strings.Join(leftovers, ", "), jetStreamGuide)))
	}
	return entries
}

// parseJetStream distinguishes an explicitly empty reservation from absent or invalid capacity evidence.
func parseJetStream(storage JetStreamStorage) (jsz, bool) {
	var answer jsz
	valid := !storage.Failed && json.Unmarshal(storage.Output, &answer) == nil && answer.Config.MaxStorage > 0 &&
		answer.ReservedStorage != nil && *answer.ReservedStorage >= 0
	return answer, valid
}

// declaredStreams is the name of every stream the compiled instances declare, and the largest cap among them: the
// room a new instance's stream needs. With none declared it is the size every app stream is rendered with.
func declaredStreams(compiled []CompiledInstance) (map[string]bool, int64) {
	names, largest := map[string]bool{}, int64(64*mebibyte)
	for _, instance := range compiled {
		var stream struct {
			Spec struct {
				Name     string `json:"name"`
				MaxBytes int64  `json:"maxBytes"`
			} `json:"spec"`
		}
		if json.Unmarshal(instance.Files["stream.json"], &stream) != nil || stream.Spec.Name == "" {
			continue
		}
		names[stream.Spec.Name] = true
		largest = max(largest, stream.Spec.MaxBytes)
	}
	return names, largest
}
