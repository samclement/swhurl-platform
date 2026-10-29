package legacy

import (
	"slices"
	"strings"
)

// Supplied is what no document in this repository states: the source repository and the full commit the
// predecessor's image was built from. A predecessor keeps only an abbreviation of the commit, so the
// migration takes both from the operator, checks them against what the records do state, and records them;
// it never expands an abbreviation and never invents a repository.
type Supplied struct{ Repository, Commit string }

// Source is a predecessor's repository and the full commit its image was built from.
type Source struct{ Repository, Commit string }

// Migration is one predecessor instance's facts as v2's record pair states them. Sources says where each
// recorded fact came from and Gaps names every legacy fact the pair cannot carry. Calls and Allows are the
// predecessor's declarations, read only so the caller can report which of them the resolution connects:
// no record carries them, because the resolution is their authority.
type Migration struct {
	Application, Environment string
	Source                   Source
	Workload                 Workload
	Instance                 Instance
	Database                 string
	Claim                    string
	Corroborated             []string
	Calls, Allows            []string
	Sources                  map[string]string
	Gaps                     []Gap
}

// Migrate reads the records and the predecessor's committed output as v2's record pair needs them. It
// refuses a layout it does not support, a supplied source the records contradict, and a legacy fact the
// migration would have to drop to change what the app can do — its secrets. Everything else it cannot carry
// is a gap, so a legacy capability is never silently dropped.
//
// The checks run in a fixed order: the committed output, the three records, the workload, the environment's
// configuration, the supplied source, the release's secrets and volume, and the relationships it declared.
func Migrate(records Records, supplied Supplied) (Migration, error) {
	committed, err := openCommitted(records.Committed)
	if err != nil {
		return Migration{}, err
	}
	app, err := open("app.yaml", records.App)
	if err != nil {
		return Migration{}, err
	}
	instance, err := open("instance.yaml", records.Instance)
	if err != nil {
		return Migration{}, err
	}
	release, err := open("release.yaml", records.Release)
	if err != nil {
		return Migration{}, err
	}
	migration := Migration{Application: committed.application, Environment: committed.environment,
		Sources: map[string]string{}}
	leftovers, err := migration.facts(app, instance, release, committed)
	if err != nil {
		return Migration{}, err
	}
	if migration.Source, migration.Corroborated, err = sourceOf(migration.Workload, release, supplied); err != nil {
		return Migration{}, err
	}
	migration.Sources["source.repository"] = "supplied"
	migration.Sources["source.commit"] = "supplied"
	if err := migration.storage(release, committed); err != nil {
		return Migration{}, err
	}
	if err := migration.declarations(instance); err != nil {
		return Migration{}, err
	}
	migration.Gaps = migration.gaps(app, instance, release, leftovers)
	return migration, nil
}

// facts reads what v2's records state — the workload, the environment's configuration and the app's team —
// and returns the image fields the migration cannot carry.
func (m *Migration) facts(app, instance, release *record, committed effective) ([]string, error) {
	workload, leftovers, err := workloadOf(release)
	if err != nil {
		return nil, err
	}
	m.Workload = workload
	for _, fact := range []string{"image", "kind", "digest", "port", "healthPath", "uid", "telemetry"} {
		m.Sources["workload."+fact] = "release.yaml"
	}
	configuration, settings, err := settings(instance, committed)
	if err != nil {
		return nil, err
	}
	m.Instance = configuration
	for name, source := range settings {
		m.Sources[name] = source
	}
	if team, err := app.text("team"); err != nil {
		return nil, err
	} else if team != "" {
		m.Instance.Team = team
		m.Sources["instance.team"] = "app.yaml"
	}
	return leftovers, nil
}

// storage reads what the release keeps: its database and the claim the instance adopts, with the secrets v2
// cannot bind refused rather than dropped — losing them would change what the app can do.
func (m *Migration) storage(release *record, committed effective) error {
	secrets, err := release.texts("secrets")
	if err != nil {
		return err
	}
	if len(secrets) > 0 {
		return refuse(Unsupported, "release.yaml: secrets", "bindings_are_not_modelled")
	}
	if m.Database, err = release.text("database"); err != nil {
		return err
	}
	if m.Database == "" {
		return nil
	}
	m.Sources["database"] = "release.yaml"
	claim, source, err := claimOf(committed)
	if err != nil {
		return err
	}
	m.Claim, m.Sources["storage.claimName"] = claim, source
	return nil
}

// declarations reads the relationships the predecessor declared. The resolution is their authority, so they
// are reported and never become record facts.
func (m *Migration) declarations(instance *record) error {
	var err error
	if m.Calls, err = instance.texts("calls"); err != nil {
		return err
	}
	if m.Allows, err = instance.texts("allows"); err != nil {
		return err
	}
	slices.Sort(m.Calls)
	slices.Sort(m.Allows)
	return nil
}

// gaps is every legacy fact the record pair cannot carry: a field the records state that no record uses, a
// field this translator does not know, and the provenance the pair states but does not verify.
func (m *Migration) gaps(app, instance, release *record, leftovers []string) []Gap {
	var found []Gap
	for _, field := range leftovers {
		found = append(found, Gap{Where: "release.yaml: image." + field, Fact: "release.yaml.image." + field,
			Owner: ownerOf(field)})
	}
	found = append(found, gaps(app, instance, release, m.Instance)...)
	found = append(found,
		Gap{Where: "release.yaml: image", Fact: "image.provenance", Owner: "release/build"},
		// The digest pins the artifact; a tag is not one of v2's release facts.
		Gap{Where: "release.yaml: image.tag", Fact: "release.yaml.image.tag", Owner: "v2 release model (digest-only)"})
	return sortGaps(found)
}

// sourceOf checks what the operator supplied against the only evidence the records keep: the image's name,
// the commit the tag abbreviates, and the commit the app's offered documents were read at. An abbreviation
// is compared, never expanded, and a record with no abbreviation leaves the supplied commit unverified
// rather than checked.
func sourceOf(workload Workload, release *record, supplied Supplied) (Source, []string, error) {
	if supplied.Repository == "" || supplied.Commit == "" {
		return Source{}, nil, refuse(Missing, "supplied: source", "the_records_keep_no_full_commit")
	}
	if imageName(workload.Image) != imageName(supplied.Repository) {
		return Source{}, nil, refuse(Invalid, "supplied: source.repository", "does_not_match_the_image_name")
	}
	offers, err := release.peek("offers")
	if err != nil {
		return Source{}, nil, err
	}
	corroborated := []string{}
	for _, evidence := range []struct{ field, value, reason string }{
		{"release.yaml: image.tag", commitAbbreviation(workload.Tag), "does_not_match_the_tag"},
		{"release.yaml: offers", commitAbbreviation(offers), "does_not_match_offers"},
	} {
		if evidence.value == "" {
			continue
		}
		if !strings.HasPrefix(supplied.Commit, evidence.value) {
			return Source{}, nil, refuse(Invalid, "supplied: source.commit", evidence.reason)
		}
		corroborated = append(corroborated, evidence.field)
	}
	return Source{Repository: supplied.Repository, Commit: supplied.Commit}, corroborated, nil
}

// commitAbbreviation is the abbreviated commit a predecessor's value carries: the last hyphen-separated
// segment when it is hex and long enough to be a commit prefix, and nothing otherwise.
func commitAbbreviation(value string) string {
	segment := value[strings.LastIndex(value, "-")+1:]
	if len(segment) < 7 || strings.Trim(segment, "0123456789abcdef") != "" {
		return ""
	}
	return segment
}

// imageName is the name an image or a repository is known by: its last path segment.
func imageName(value string) string {
	return value[strings.LastIndex(value, "/")+1:]
}

// claimOf is the claim a database instance adopts: the one its predecessor's release named, or the one the
// pinned chart generated for it. The chart names a generated claim after the release, which its own render
// test ties; nothing here reads the cluster.
func claimOf(committed effective) (string, string, error) {
	if !committed.hasVolume {
		return "", "", refuse(Missing, "committed HelmRelease: values.persistence.data",
			"the_database_claim_is_not_stated")
	}
	if committed.existingClaim != "" {
		return committed.existingClaim, "helmrelease.yaml: values.persistence.data.existingClaim", nil
	}
	return committed.application, "helmrelease.yaml: metadata.name (the pinned chart's claim name)", nil
}
