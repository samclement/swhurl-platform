package kernel

// Creation is what a create decides about a new instance before anything is written: the app and its team, the
// release its image publishes, the declaration that release states, and the instance's own choices — its exposure
// and the public half of its broker identity. The image states the memory it runs in and the volume a database is
// created with, so a create has no size to choose. It is the app's first instance, so it is staging's: production
// follows a promotion, a decision this one does not make.
type Creation struct {
	Application, Team string
	Release           ReleaseBundle
	Declaration       Declaration
	Exposure          string
	// Key is the public nkey of the broker identity the instance signs in with. Its seed is written, encrypted,
	// before the record that states this key, so a failure leaves a record that states no identity.
	Key string
}

// Instance is the staging instance a create writes, with the app record its namespace label takes its team from.
// Every rule is the record's own: a create adds one replica, which a database admits no more of, and otherwise no
// fact the image and the operator did not state.
func (c Creation) Instance() (InstanceSpec, AppSpec, error) {
	if c.Release.Application != c.Application {
		return InstanceSpec{}, AppSpec{}, refuse(WrongApplication, "application",
			"the release belongs to a different application")
	}
	if err := c.validateVolume(); err != nil {
		return InstanceSpec{}, AppSpec{}, err
	}
	if c.Declaration.MemoryMiB < 1 {
		return InstanceSpec{}, AppSpec{}, refuse(Invalid, "declaration.memoryMiB",
			"a new instance needs the memory the image runs in")
	}
	spec := InstanceSpec{SchemaVersion: 1, Application: c.Application, Environment: "staging", Release: c.Release.ReleaseID,
		Configuration: Configuration{Replicas: 1, MemoryMiB: c.Declaration.MemoryMiB}, Grants: Grants{Exposure: c.exposure()},
		Storage: Storage{SizeMiB: c.storage()}, Events: Events{Key: c.Key}}
	if err := declarationUnmodelled(c.Declaration, spec); err != nil {
		return InstanceSpec{}, AppSpec{}, err
	}
	if _, err := spec.Revision(); err != nil {
		return InstanceSpec{}, AppSpec{}, err
	}
	app := AppSpec{SchemaVersion: 1, Application: c.Application, Team: c.Team}
	if _, err := app.ID(); err != nil {
		return InstanceSpec{}, AppSpec{}, err
	}
	return spec, app, nil
}

// storage is the volume a new database is created with: the size the image states, and none without a database.
func (c Creation) storage() int32 {
	if c.Declaration.Database == "" {
		return 0
	}
	return c.Declaration.DatabaseSizeMiB
}

// exposure is the instance's grant: a new app is private unless the operator says it serves people signed in.
func (c Creation) exposure() string {
	if c.Exposure == "" {
		return "private"
	}
	return c.Exposure
}

// validateVolume checks the volume against what the image needs: a database is given one, and an image without a
// database has nothing to put in one.
func (c Creation) validateVolume() error {
	switch {
	case c.Declaration.Database == "" && c.Declaration.DatabaseSizeMiB > 0:
		return refuse(Invalid, "storage", "the image keeps no database, so a volume would hold nothing")
	case c.Declaration.Database != "" && c.Declaration.DatabaseSizeMiB < 1:
		return refuse(Invalid, "storage.sizeMiB", "a database needs a volume size")
	}
	return nil
}
