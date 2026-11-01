package kernel

// Creation is what a create decides about a new instance before anything is written: the app and its team, the
// release its image publishes, the declaration that release states, and the instance's own choices — the memory it is
// given, its exposure, the volume an image with a database keeps its file on, and the public half of its broker
// identity. It is the app's first instance, so it is staging's: production follows a promotion, a decision this one
// does not make.
type Creation struct {
	Application, Team string
	Release           ReleaseBundle
	Declaration       Declaration
	// MemoryMiB is the memory the operator chose; zero takes the floor the image states, which is what the image is
	// known to run in and what a deploy holds a later image under.
	MemoryMiB int32
	// StorageMiB is the volume a new database is created with. A size is a decision, so an image that keeps a
	// database and is given none is refused rather than defaulted.
	StorageMiB int32
	Exposure   string
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
	memory := c.memory()
	if memory < 1 {
		return InstanceSpec{}, AppSpec{}, refuse(Invalid, "configuration.memoryMiB",
			"a new instance needs a memory size, and this image states none")
	}
	spec := InstanceSpec{SchemaVersion: 1, Application: c.Application, Environment: "staging", Release: c.Release.ReleaseID,
		Configuration: Configuration{Replicas: 1, MemoryMiB: memory}, Grants: Grants{Exposure: c.exposure()},
		Storage: Storage{SizeMiB: c.StorageMiB}, Events: Events{Key: c.Key}}
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

// memory is the instance's memory: the size the operator chose, or the floor the image states when they chose none.
func (c Creation) memory() int32 {
	if c.MemoryMiB > 0 {
		return c.MemoryMiB
	}
	return c.Declaration.MemoryMiB
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
	case c.Declaration.Database == "" && c.StorageMiB > 0:
		return refuse(Invalid, "storage", "the image keeps no database, so a volume would hold nothing")
	case c.Declaration.Database != "" && c.StorageMiB < 1:
		return refuse(Invalid, "storage.sizeMiB", "a database needs a volume size")
	}
	return nil
}
