package orchestration

import "github.com/samclement/swhurl-platform/v2/kernel"

// recordsRoot is where a v2-owned instance keeps its own records, under the checkout the caller writes into. It is
// this use case's own output and becomes a profile fact only if a second writer needs it.
const recordsRoot = "v2-records"

// recordFiles is the records one instance states: one app record and the environment's pair, under the records root
// the caller chose. An instance with a broker identity also keeps its encrypted seed beside the record, before the
// instance that names its public key, so the record folder is what a later compile reads. The documents it publishes
// are declared inputs of the same folder, so a later compile reads them from the record rather than from the compiled
// copy a prune may remove.
func recordFiles(root, application, environment string, app, release, instance, eventsSecret []byte,
	offers []kernel.File) []kernel.File {
	folder := kernel.RecordFolder(root, application, environment)
	records := []kernel.File{
		{Path: kernel.AppRecordPath(root, application), Document: app},
		{Path: folder + "/" + kernel.ReleaseRecordName, Document: release},
	}
	if len(eventsSecret) > 0 {
		records = append(records, kernel.File{Path: folder + "/" + kernel.EventsSecretFile, Document: eventsSecret})
	}
	records = append(records, kernel.File{Path: folder + "/" + kernel.InstanceRecordName, Document: instance})
	for _, document := range offers {
		records = append(records, kernel.File{Path: folder + "/offers/" + document.Path,
			Document: document.Document})
	}
	return records
}
