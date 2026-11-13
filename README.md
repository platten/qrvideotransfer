# qrvt

qrvt turns files into a looping video. Play the video full-screen, film the screen with a phone, copy
the recording to another machine, and qrvt turns the recording back into the files. Nothing but light
crosses between the two machines.

**Status: under construction.** Nothing works end to end yet.

- [Design](docs/DESIGN.md)
- [On-screen format](docs/FORMAT.md)
- [Security](docs/SECURITY.md)

## Developing

```sh
scripts/setup.sh            # installs or verifies Go 1.27.4 and the pinned tools
scripts/test.sh             # unit tests (CGO_ENABLED=0)
scripts/test.sh -race       # race detector (needs cgo)
scripts/test.sh cover       # tests with the 95% per-package coverage gate
scripts/test.sh lint        # golangci-lint
scripts/test.sh fuzz -time 10s
scripts/build.sh -all       # static builds for the six targets into dist/
```

On Windows use the `.ps1` scripts. All scripts are thin wrappers around `go run ./tools/dev <task>`.

## Licence

GPL-3.0-or-later. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
