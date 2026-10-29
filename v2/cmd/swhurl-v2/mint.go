package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/samclement/swhurl-platform/v2/adapters/files"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// mintDocument is what `mint` writes: the instance, the public key its record now states, the key it replaced
// when this was a rotation, and the files written. The seed is not in it, in any form.
type mintDocument struct {
	Kind        string          `json:"kind"`
	Version     int             `json:"version"`
	Application string          `json:"application"`
	Environment string          `json:"environment"`
	Public      string          `json:"public"`
	Previous    string          `json:"previous,omitempty"`
	Files       []files.Outcome `json:"files"`
}

// mint gives one instance a broker identity. The seed is drawn here, because randomness is an effect and the
// model takes it as an argument; the use case derives the pair, encrypts the Secret and writes it before the
// record that states the key.
func (c cli) mint(r request) int {
	instance, status := c.read(r.instance, "instance")
	if status != 0 {
		return status
	}
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return status
	}
	if c.appTreeRefused(r.out) {
		return 2
	}
	seed, err := c.seed()
	if err != nil {
		fmt.Fprintln(c.diagnostic, "io: randomness: cannot read a seed")
		return 1
	}
	outcome, err := orchestration.Mint(orchestration.MintInputs{Instance: instance, Profile: profile,
		Seed: seed, Rotate: r.rotate}, r.out, c.writer, c.encrypter)
	if err != nil {
		return c.failure(err)
	}
	document := mintDocument{Kind: "v2-mint", Version: 1, Application: outcome.Application,
		Environment: outcome.Environment, Public: outcome.Public, Previous: outcome.Previous, Files: outcome.Written}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fail(c.diagnostic, err)
	}
	action := "minted"
	if outcome.Previous != "" {
		action = "rotated"
	}
	fmt.Fprintf(c.diagnostic, "mint: %s %s/%s, public key %s\n", action, outcome.Application,
		outcome.Environment, outcome.Public)
	return emit(c.out, c.diagnostic, bytes.TrimSuffix(out.Bytes(), []byte{'\n'}))
}

// randomSeed is the command's own effect: 32 random bytes, which the kernel turns into a pair. The seed is not
// kept anywhere else; it leaves here only inside the encrypted file.
func randomSeed() ([32]byte, error) {
	var seed [32]byte
	_, err := rand.Read(seed[:])
	return seed, err
}
