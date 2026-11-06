package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// readCheckout reads the files the platform scope compares the cluster with from the checkout at root: the profile,
// the team registry, the egress allocation, every app and release record, and each compiled instance's objects. A
// file the checkout does not have is absent, which the checks report; one it cannot read stops the run.
func (c cli) readCheckout(root string) (orchestration.Checkout, int) {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		fmt.Fprintf(c.diagnostic, "invalid: root: %s is not a directory\n", root)
		return orchestration.Checkout{}, 2
	}
	checkout := orchestration.Checkout{Apps: map[string][]byte{}, Releases: map[string][]byte{}}
	for _, file := range []struct {
		path   string
		target *[]byte
	}{
		{observe.CheckoutProfile, &checkout.Profile},
		{observe.CheckoutTeams, &checkout.Teams},
		{observe.CheckoutAllocation, &checkout.Allocation},
	} {
		data, status := c.checkoutFile(filepath.Join(root, file.path))
		if status != 0 {
			return orchestration.Checkout{}, status
		}
		*file.target = data
	}
	if status := c.readRecords(filepath.Join(root, observe.CheckoutRecords), checkout); status != 0 {
		return orchestration.Checkout{}, status
	}
	compiled, status := c.readCompiled(filepath.Join(root, observe.CheckoutCompiled))
	checkout.Compiled = compiled
	return checkout, status
}

// checkoutFile is one file of the checkout; nil when it has no such file.
func (c cli) checkoutFile(path string) ([]byte, int) {
	data, err := c.readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0
	}
	if err != nil {
		return c.read(path, path) // reports why, as every other input's failure is reported
	}
	return data, 0
}

// readRecords reads each app's record and each of its environments' release records into the checkout.
func (c cli) readRecords(records string, checkout orchestration.Checkout) int {
	apps, status := c.folders(records)
	if status != 0 {
		return status
	}
	for _, app := range apps {
		data, status := c.checkoutFile(filepath.Join(records, app, kernel.AppRecordName))
		if status != 0 {
			return status
		}
		if data != nil {
			checkout.Apps[app] = data
		}
		environments, status := c.folders(filepath.Join(records, app))
		if status != 0 {
			return status
		}
		for _, environment := range environments {
			release, status := c.checkoutFile(filepath.Join(records, app, environment, kernel.ReleaseRecordName))
			if status != 0 {
				return status
			}
			if release != nil {
				checkout.Releases[app+"/"+environment] = release
			}
		}
	}
	return 0
}

// readCompiled reads every compiled instance folder, <app>/<env>, with the objects the checks compare, in order.
func (c cli) readCompiled(base string) ([]observe.CompiledInstance, int) {
	compiled := []observe.CompiledInstance{}
	apps, status := c.folders(base)
	if status != 0 {
		return nil, status
	}
	for _, app := range apps {
		environments, status := c.folders(filepath.Join(base, app))
		if status != 0 {
			return nil, status
		}
		for _, environment := range environments {
			instance, status := c.compiledInstance(filepath.Join(base, app, environment), app, environment)
			if status != 0 {
				return nil, status
			}
			compiled = append(compiled, instance)
		}
	}
	return compiled, 0
}

func (c cli) compiledInstance(folder, app, environment string) (observe.CompiledInstance, int) {
	instance := observe.CompiledInstance{Application: app, Environment: environment, Files: map[string][]byte{}}
	entries, err := os.ReadDir(folder)
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: checkout: cannot read %s\n", folder)
		return instance, 1
	}
	for _, entry := range entries {
		if entry.IsDir() || !observe.CompiledObject(entry.Name()) {
			continue
		}
		data, status := c.read(filepath.Join(folder, entry.Name()), filepath.Join(folder, entry.Name()))
		if status != 0 {
			return instance, status
		}
		instance.Files[entry.Name()] = data
	}
	return instance, 0
}

// folders are a directory's subdirectories, sorted; none when the checkout has no such directory.
func (c cli) folders(dir string) ([]string, int) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0
	}
	if err != nil {
		fmt.Fprintf(c.diagnostic, "io: checkout: cannot read %s\n", dir)
		return nil, 1
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, 0
}
