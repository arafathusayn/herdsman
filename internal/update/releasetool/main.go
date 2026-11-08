// Command releasetool does the release steps that need herdsman's own formats:
// signing the checksum list, verifying it, making a key, writing the SBOM. It
// runs in the release build and ships in no archive.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/update"
)

const (
	keyEnv   = "HERDSMAN_RELEASE_SIGNING_KEY"
	allowEnv = "HERDSMAN_ALLOW_UNTRUSTED_KEY"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stderr)) }

const usage = "usage: releasetool sign|verify|keygen|sbom [flags]\n"

func run(args []string, getenv func(string) string, out io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return 2
	}
	tools := map[string]func([]string, func(string) string, io.Writer) int{
		"sign": sign, "verify": verify, "keygen": keygen, "sbom": sbom,
	}
	tool, ok := tools[args[0]]
	if !ok {
		fmt.Fprint(out, usage)
		return 2
	}
	return tool(args[1:], getenv, out)
}

// flags parses a subcommand's flags, all strings and all required.
func flags(name string, args []string, out io.Writer, names ...string) (map[string]*string, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	values := map[string]*string{}
	for _, n := range names {
		values[n] = fs.String(n, "", n)
	}
	if err := fs.Parse(args); err != nil {
		return nil, false
	}
	for _, n := range names {
		if *values[n] == "" {
			fmt.Fprintf(out, "%s: --%s is required\n", name, n)
			return nil, false
		}
	}
	return values, true
}

func fail(out io.Writer, name string, err error) int {
	fmt.Fprintf(out, "releasetool %s: %v\n", name, err)
	return 1
}

func sign(args []string, getenv func(string) string, out io.Writer) int {
	v, ok := flags("sign", args, out, "in", "out")
	if !ok {
		return 2
	}
	encoded := strings.TrimSpace(getenv(keyEnv))
	if encoded == "" {
		return fail(out, "sign", fmt.Errorf("%s is not set", keyEnv))
	}
	seed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(seed) != ed25519.SeedSize {
		return fail(out, "sign", fmt.Errorf("%s is not a base64 %d-byte seed", keyEnv, ed25519.SeedSize))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if getenv(allowEnv) != "1" {
		trusted, err := update.TrustedKeys()
		if err != nil {
			return fail(out, "sign", err)
		}
		pub := priv.Public().(ed25519.PublicKey)
		if !slices.ContainsFunc(trusted, func(k update.Key) bool { return pub.Equal(k.Pub) }) {
			return fail(out, "sign", fmt.Errorf("the binaries do not trust this key: add %s to internal/update/release_keys.txt", update.PublicKeyLine(pub)))
		}
	}
	data, err := os.ReadFile(*v["in"])
	if err != nil {
		return fail(out, "sign", err)
	}
	if err := os.WriteFile(*v["out"], []byte(update.Sign(priv, data)), 0o644); err != nil {
		return fail(out, "sign", err)
	}
	return 0
}

func verify(args []string, _ func(string) string, out io.Writer) int {
	v, ok := flags("verify", args, out, "in", "sig", "keys")
	if !ok {
		return 2
	}
	var files [3][]byte
	for i, name := range []string{"in", "sig", "keys"} {
		data, err := os.ReadFile(*v[name])
		if err != nil {
			return fail(out, "verify", err)
		}
		files[i] = data
	}
	keys, err := update.ParseKeys(string(files[2]))
	if err != nil {
		return fail(out, "verify", err)
	}
	if err := update.VerifySignature(keys, files[0], files[1]); err != nil {
		return fail(out, "verify", err)
	}
	return 0
}

func keygen(args []string, _ func(string) string, out io.Writer) int {
	v, ok := flags("keygen", args, out, "seed-out", "public-out")
	if !ok {
		return 2
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fail(out, "keygen", err)
	}
	// O_EXCL: a key file is never overwritten.
	f, err := os.OpenFile(*v["seed-out"], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(out, "keygen", err)
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"); err != nil {
		return fail(out, "keygen", err)
	}
	if err := os.WriteFile(*v["public-out"], []byte(update.PublicKeyLine(pub)+"\n"), 0o644); err != nil {
		return fail(out, "keygen", err)
	}
	return 0
}

func sbom(args []string, _ func(string) string, out io.Writer) int {
	v, ok := flags("sbom", args, out, "root", "version", "created", "out")
	if !ok {
		return 2
	}
	components, err := update.Components(*v["root"], update.Gitlink)
	if err != nil {
		return fail(out, "sbom", err)
	}
	components = append(components, update.ToolchainComponent())
	doc, err := update.SPDX("herdsman", *v["version"], *v["created"], components)
	if err != nil {
		return fail(out, "sbom", err)
	}
	if err := os.WriteFile(*v["out"], doc, 0o644); err != nil {
		return fail(out, "sbom", err)
	}
	return 0
}
