package helm

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// Pin is the profile's chart identity and its resolved HelmRepository URL.
// Cache keys intentionally retain the existing name-version layout.
type Pin struct{ Name, Version, Repository string }

// Acquirer returns a local chart with matching metadata, pulling only a missing pin into the given cache.
// It never replaces an existing chart or contacts Kubernetes. Rendering remains a separate operation.
type Acquirer interface {
	Acquire(Pin, string) (string, error)
}

var _ Acquirer = (*Command)(nil)

func chartFailure(reason string) error { return errors.New("refused_chart: " + reason) }

// Acquire validates even a cached chart's metadata. A cold pull is extracted privately, checked and
// renamed into place; failures remove only this call's temporary directory, never another caller's chart.
func (c *Command) Acquire(pin Pin, cache string) (string, error) {
	if err := validPin(pin, cache); err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(cache, pin.Name+"-"+pin.Version))
	if err != nil {
		return "", chartFailure("invalid_cache")
	}
	if _, err := os.Stat(filepath.Join(target, "Chart.yaml")); err == nil {
		return acquired(target, pin)
	} else if !os.IsNotExist(err) {
		return "", chartFailure("cache_read_failed")
	}
	return c.pullChart(pin, filepath.Dir(target), target)
}

func validPin(pin Pin, cache string) error {
	segment := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]*$`)
	if cache == "" || !segment.MatchString(pin.Name) || !segment.MatchString(pin.Version) {
		return chartFailure("invalid_pin")
	}
	repository, err := url.Parse(pin.Repository)
	if err != nil || (repository.Scheme != "https" && repository.Scheme != "http") || repository.Host == "" || repository.User != nil || repository.Fragment != "" {
		return chartFailure("invalid_repository")
	}
	return nil
}

func acquired(path string, pin Pin) (string, error) {
	if chartMatches(Request{Chart: path, ChartName: pin.Name, ChartVersion: pin.Version}) != nil {
		return "", chartFailure("chart_mismatch")
	}
	return path, nil
}

func (c *Command) pullChart(pin Pin, cache, target string) (string, error) {
	if os.MkdirAll(cache, 0o755) != nil {
		return "", chartFailure("cache_write_failed")
	}
	temporary, err := os.MkdirTemp(cache, "pull-*")
	if err != nil {
		return "", chartFailure("cache_write_failed")
	}
	defer os.RemoveAll(temporary)
	if err := c.fetchChart(pin, temporary); err != nil {
		return "", err
	}
	unpacked, err := acquired(filepath.Join(temporary, pin.Name), pin)
	if err != nil {
		return "", err
	}
	if os.Rename(unpacked, target) != nil {
		// Another check may have published first. Only its complete, matching chart is usable.
		if _, err := os.Stat(filepath.Join(target, "Chart.yaml")); err != nil {
			return "", chartFailure("cache_publish_failed")
		}
		return acquired(target, pin)
	}
	return target, nil
}

func (c *Command) fetchChart(pin Pin, destination string) error {
	timeout := 2 * time.Minute
	if c.timeout > 0 && c.timeout < timeout {
		timeout = c.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, c.Binary, "pull", pin.Name, "--repo", pin.Repository,
		"--version", pin.Version, "--untar", "--untardir", destination,
		"--repository-cache", filepath.Join(destination, "repository"),
		"--repository-config", filepath.Join(destination, "repositories.yaml"))
	command.WaitDelay = time.Second
	command.Stdout = &bounded{limit: MaxOutput}
	command.Stderr = &bounded{limit: MaxStderr}
	err := command.Run()
	switch {
	case ctx.Err() != nil:
		return chartFailure("timeout")
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return chartFailure("helm_not_found")
	case err != nil:
		return chartFailure("pull_failed")
	}
	return nil
}
