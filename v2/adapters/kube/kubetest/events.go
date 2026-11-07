package kubetest

import (
	"encoding/json"
	"github.com/samclement/swhurl-platform/v2/observe"
)

func filterEvents(all observe.List, kind string) observe.List {
	selected := [][]byte{}
	for _, item := range all.Items {
		var event struct{ InvolvedObject struct{ Kind string } }
		_ = json.Unmarshal(item, &event)
		if event.InvolvedObject.Kind == kind {
			selected = append(selected, item)
		}
	}
	all.Items = selected
	return all
}
