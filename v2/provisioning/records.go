package provisioning

import "github.com/samclement/swhurl-platform/v2/kernel"

// RecordsRoot is where a v2-owned instance keeps its own records, under the checkout the caller writes into. It is
// this capability's own output and becomes a profile fact only if a second writer needs it.
const RecordsRoot = "v2-records"

// RecordFiles is the records one instance states: one app record and the environment's pair, under the records root
// the caller chose. An instance with a broker identity also keeps its encrypted seed beside the record, before the
// instance that names its public key, so the record folder is what a later compile reads. The documents it publishes
// are declared inputs of the same folder, so a later compile reads them from the record rather than from the compiled
// copy a prune may remove.
func RecordFiles(root, application, environment string, app, release, instance, eventsSecret []byte,
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

// InstanceDocuments is what a delivery or a promotion writes: the release first, then the instance that selects it,
// then the compiled files, each with the documents the release's image states. A failure leaves at worst a release
// nothing selects.
func InstanceDocuments(spec kernel.InstanceSpec, release kernel.ReleaseBundle,
	compiled kernel.Compilation, offers []kernel.File) ([]kernel.File, error) {
	releaseDocument, err := kernel.EncodeRelease(release)
	if err != nil {
		return nil, err
	}
	instanceDocument, err := kernel.EncodeInstance(spec)
	if err != nil {
		return nil, err
	}
	folder := kernel.RecordFolder(RecordsRoot, spec.Application, spec.Environment)
	records, err := withOffers([]kernel.File{
		{Path: folder + "/" + kernel.ReleaseRecordName, Document: releaseDocument},
		{Path: folder + "/" + kernel.InstanceRecordName, Document: instanceDocument},
	}, offers)
	if err != nil {
		return nil, err
	}
	compiledDocuments, err := withOffers(compiled.Files, offers)
	if err != nil {
		return nil, err
	}
	return append(records, compiledDocuments...), nil
}
