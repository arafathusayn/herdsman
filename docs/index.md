# herdsman documentation

herdsman runs a team of coding agents inside Herdr. Start with the [README](../README.md) for installation and the shape of a route. These pages are the reference.

## Reference

- [Commands](commands/index.md): one page per command, with its usage and exit codes.
- [Configuration](config.md): every section and key of the configuration file.
- [SDK](sdk.md): talking to the daemon from Go or TypeScript, and reading the contracts.
- [Plugins](plugins.md): extending herdsman with a separate process.
- [Experimental surfaces](experimental.md): what is exempt from the compatibility promise.
- [Threat model](threat-model.md): what herdsman defends against and what it leaves to you.
- [Design decisions](adr/README.md): why things are the way they are.
- [Release notes](releases/v0.11.0.md): what v0.11.0 adds, starting with herdr-core as an experiment.

The command pages and the configuration page are generated from the binary with `make docs`. To change one, change the command's help text or the contract's comment, then regenerate. CI fails when the committed pages differ from the generated ones. The manual pages in `man/` come from the same command table.
