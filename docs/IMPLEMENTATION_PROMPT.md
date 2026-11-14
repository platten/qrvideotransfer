# Implementation prompt: build qrvt

You are the lead engineer for qrvt, working in the repository that contains this file. You will build
the tool from an empty repository to a release candidate, directing other agents for work that can run
in parallel. The project owner has reviewed the design and wants to be interrupted only when their phone
is needed or when something measurable has gone wrong.

## Authorisations

The owner sets the right-hand column before starting you. Anything not marked `yes` is not allowed.
These were set by the owner on 2026-11-13.

| Action | Allowed |
| --- | --- |
| Install development tools on this machine with `scripts/setup.*`, including the two tools only spike S1 needs (a native ffmpeg with an HEVC encoder, and the wasmtime command-line runner) | yes |
| Push branches to `github.com/platten/qrvideotransfer`. The repository already exists: the owner created it, and it is public | yes |
| Open pull requests and merge them when CI is green and the reviewing agent approves | yes |

Never without asking in the moment, whatever the table says: publish a release or push a `v*` tag, change
repository settings or secrets, change the repository's visibility, or delete anything outside this repository.

If GitHub is not authorised, work on local branches and run the same checks through the scripts on this
machine. An exit test that names "six runners" is then met by passing on this machine and
cross-compiling all six targets; record every measurement that needed a runner as unverified, and say so
in each report. That is not a reason to stop.

If a tool is missing and installing is not authorised, say which, and continue with whatever does not
need it.

## 1. What you are building

qrvt turns files into a looping video. The video is played full-screen, filmed with a phone, and the
recording is decoded back into the files on another machine. The reader of your code is a maintainer who
will later add a desktop GUI on top of the same core.

Read these before anything else. They are the specification; this prompt is the plan for building it.

| Document | Holds |
| --- | --- |
| `docs/DESIGN.md` | The design and the reason for each choice |
| `docs/FORMAT.md` | The on-screen format, bit for bit |
| `docs/SECURITY.md` | The threat model, limits and security tests |

Those documents are a draft written before any measurement. When something you measure contradicts
them, the measurement wins: change the document in the same pull request as the code, and add a line to
its change log. Do not quietly build something different from what the documents say.

## 2. Ground rules, and why

| Rule | Reason |
| --- | --- |
| Go 1.27. Release builds and ordinary tests use `CGO_ENABLED=0`; the binary is static, for linux, darwin and windows on amd64 and arm64 | The owner's first requirement is one binary with no runtime dependencies |
| Licence GPL-3.0-or-later | The embedded ffmpeg includes x264 |
| Tests first: in each branch, a commit of failing tests comes before the commit that makes them pass | The owner asked for TDD, and the commit order is the evidence |
| At least 95% statement coverage in every package. Excluded: `cmd/qrvt/main.go`, generated tables, `tools/`, `spikes/` | Owner's requirement |
| The core does no terminal I/O, never calls `os.Exit`, and reports progress only through typed events | A GUI will link the same core |
| Workers share no mutable state and communicate through channels. Race tests run on every pull request, with cgo enabled, on the five runners that support the race detector (all but windows/arm64) | Data races are Go's one memory-safety gap, and decoded input is hostile |
| Within the root package, `cmd/` and `internal/`: only `internal/video` may import `os/exec`, and nothing may import `unsafe`. `tools/` and `spikes/` are exempt | Keeps the attack surface where SECURITY.md expects it; tooling must launch other programs |
| Everything read from a video is validated before it sizes an allocation or names a file | SECURITY.md section 1 |
| Concise code. A doc comment on every package and exported name. No abstraction beyond the frozen interfaces until a second use exists | The owner asked for maintainability through brevity and documentation |
| Conventional commit messages | release-please derives the changelog and version from them |

## 3. Facts you can rely on

Checked on 2026-11-13. "Verify" means nobody has run it yet; M0 does.

| Component | Version | Notes |
| --- | --- | --- |
| Go | 1.27.4 | `testing/synctest`, `T.ArtifactDir`, `os.Root` are available |
| `codeberg.org/gruf/go-ffmpreg` | v0.6.22 | ffmpeg n5.1.10 with libx264, libvpx, libaom; no x265. `wasm.Run(ctx, rt, mod, wasm.Args{Name, Args, Stdin, Stdout, Stderr, Config})`. The same module acts as `ffmpeg` or `ffprobe` according to `Name`. Files are reached through wazero directory mounts. Single-threaded, no assembly. About 20 MB, 7.5 MB compressed. **Verify**: streaming raw frames through `Stdin` and `Stdout` |
| wazero | v1.12.0 | Use the module path go-ffmpreg's `go.mod` requires. Needs the SIMD, exception-handling and tail-call features enabled. Compiler mode exists for amd64 and arm64 only. Use `wazero.NewCompilationCacheWithDir` under `os.UserCacheDir()`. **Verify**: windows/arm64 |
| `github.com/klauspost/reedsolomon` | v1.14.2 | `New(data, parity, WithLeopardGF16(true))`. Shard size must be a multiple of 64 bytes. In-memory only; the streaming API does not support GF(2^16), so encode and reconstruct block by block. Erasures only |
| `github.com/klauspost/compress` | v1.20.4 | zstd `SpeedBestCompression` is roughly reference level 11 |
| `github.com/ulikunitz/xz` | v0.5.17 | Latest stable tag; later tags are pre-releases |
| `filippo.io/age` | v1.3.2 | Scrypt recipient for passphrases |
| `github.com/spf13/cobra` | v1.10.2 | |
| `charm.land/fang/v2` | v2.0.1 | `fang.Execute(ctx, rootCmd)`; styled help, man page, completions, version |
| `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2` | v2.1.1, v2.2.1, v2.1.0 | Note the `charm.land` import paths |
| `github.com/charmbracelet/x/exp/teatest/v2` | pseudo-version | Golden-file tests for the dashboard; pin the colour profile in tests |
| golangci-lint | v2.15.0 | Version 2 configuration format; action `golangci/golangci-lint-action` v9 |
| GoReleaser | v2.20.2 | Action v7. Use `homebrew_casks`, `archives.formats`, `snapshot.version_template`. Notarisation through `notarize.macos` is in the free edition |
| Other actions | | `actions/checkout` v7, `actions/setup-go` v7, `golang/govulncheck-action` v1, `github/codeql-action` v4, `vladopajic/go-test-coverage` v2, `actions/attest` v4, `googleapis/release-please-action` v5 |
| Runners | | `ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-26` (arm64), `macos-26-intel`, `windows-2025`, `windows-11-arm` |

Do not build on these:

| Library | Why |
| --- | --- |
| `github.com/oops1/go.264`, `github.com/go-avkit/*` | One author, months old. Links glibc on Linux through purego and switches to hardware codecs on Windows unless built with special tags. `avkit` reads every sample of a file into memory |
| Any cgo binding | Breaks the static build |
| Fountain-code libraries | All months old; the design does not need one |

Two things you must not reproduce from memory:

- **The IEEE 802.11n LDPC matrices** for block length 1944. Take them from IEEE Std 802.11-2020 Annex F
  or from two independent published implementations (for example `github.com/simgunz/802.11n-ldpc`, not
  yet opened by anyone on this project), confirm the sources agree entry for entry, and record where
  they came from beside the table.
- **Version numbers and APIs not in the table above.** Look them up.

## 4. How to organise the work

- You are the orchestrator. For each parallel workstream, start one subagent in its own git worktree.
- A subagent's brief contains: the goal; the paths it owns; the frozen interfaces it must not change;
  the tests it must write first; the exit test; and the sections of the three documents it needs.
  Paste the relevant text of those sections; a subagent has not read this prompt.
- A subagent edits only the paths it owns. If it needs an interface to change, it stops and tells you;
  you decide, change the interface on the main branch, and tell every affected subagent.
- After M0 you own `internal/frame`, `internal/fec` (not its sub-packages), `internal/fec/conv` and the
  root package's types. Changes to them go through you.
- Each workstream ends as one pull request (or one local branch). Before merging, a separate agent that
  has not seen the work reviews the diff against the relevant document sections for correctness and for
  the rules in section 2. It is asked to find defects, not to comment on style.
- Merge only when every check passes. After each wave, run the whole test suite on the merged result
  before starting the next wave.
- Run no more subagents at once than the wave lists.
- If you cannot start subagents where you are running, do each workstream yourself, one after another,
  keeping the same ownership boundaries, tests-first order and independent review as far as you can, and
  say so in your reports.
- Throwaway spike code lives under `spikes/` and is deleted once its report is written. The one
  exception is the S2 pattern program, which stays until the S2 recordings have been analysed.

## 5. Milestones

### M0. Foundations and spikes (2 agents)

Purpose: make every later step safe to run in parallel, and measure the things nobody has measured.
The two agents start together. The S2 pattern at the end of Agent 2's list waits for Agent 1's merge.

**Agent 1: repository and the arithmetic of the format**

- `git init`, `go.mod` (`github.com/platten/qrvideotransfer`, `go 1.27`), `LICENSE`, `NOTICE`, README
  stub, `.gitignore` (including `recordings/` and `dist/`), linter and coverage configuration.
- `scripts/setup.sh` and `scripts/setup.ps1`, which stand alone because Go may not be installed: they
  install or verify Go and then run `tools/dev install-tools`; with `--spike` they also install the two
  spike tools. They print what they will install before installing anything.
- `tools/dev` with tasks `install-tools`, `check-tools`, `build`, `test`, `cover`, `lint`, `fuzz`, and the
  thin wrappers `scripts/build.*` and `scripts/test.*` (DESIGN.md section 16).
- A CI workflow that calls those scripts on all six runners: lint, tests, coverage gate, vulnerability
  scan, a static-build check, race tests with cgo on the five supporting runners, and the import-rule
  test of SECURITY.md section 9.6.
- Every package of DESIGN.md section 14 with its doc comment, the interfaces shown there, and a field
  list for each type in that section's "What the named types carry" table.
- These parts of FORMAT.md, implemented and tested:
  - section 1: SplitMix64, Shuffle, Seed;
  - sections 4 and 5: structure geometry, reserved cells, anchors, data cells (`internal/frame`);
  - section 6: header fields and coding, including the convolutional encoder and a soft-input Viterbi
    decoder (`internal/fec/conv`). The decoder must correct every pattern of up to 4 errors among the
    236 coded bits;
  - the tables of sections 7 and 8 (bits per cell, shard size, sent bits) and all of section 9: codewords
    per frame, interleaver, rotation, scrambler (`internal/frame`);
  - section 10: descriptor encoding and validation;
  - section 11: block plan, transmission order, frame count (`internal/fec`), with a property test that
    the Reed-Solomon library accepts every (K, M) the plan can produce.
- Golden vectors for all of the above under `testdata/golden/`. Every number in FORMAT.md section 5.3
  and every row of the two worked tables in section 13 must be reproduced exactly. If one is not, find
  out whether the code or the document is wrong before changing either.

**Agent 2: spikes**

*S1, the video engine.* With the native ffmpeg, generate 10 s clips of noise-like content (a scaled
random-cell pattern, so the bitrate is spent as a real recording would spend it): 1080p30 H.264 at
17 Mbit/s, 4K60 HEVC 8-bit at 53 Mbit/s, and 4K60 HEVC 10-bit HLG at 53 Mbit/s. Then measure, on this
machine and on each CI runner:

| Measure | Go if | Otherwise |
| --- | --- | --- |
| Decode speed per engine instance | 4K HEVC at least 4 fps; 1080p H.264 at least 15 fps | Try brightness-only output and skipping the loop filter; then `github.com/gen2brain/h265` as a fast path; if still short, stop and ask whether a native ffmpeg should become the default when present |
| Encode speed per instance, x264 intra-only | 1080p at least 4 fps; 4K at least 1.2 fps | Schedule an in-repository flat-cell H.264 writer into M3 (DESIGN.md section 12) |
| Streaming | 10 GB of raw frames in through `Stdin` and out through `Stdout`, with backpressure, at constant memory and without deadlock | Chunked temporary files in the mounted directory |
| Parallel instances | At least 70% efficiency at 4 instances | Cap the worker count |
| Start-up | At most 30 s on first run and 3 s cached | Document it; show a first-run notice |
| Memory per instance at 4K | At most 600 MB decoding and 1 GB encoding | Lower the default worker count (DESIGN.md section 8) and document it |
| Six targets | Static build and the spike both pass on every runner | Ship windows/arm64 as experimental |
| Display bitrate | Bits per cell near 8.4 for four greys at quantiser 32; zero cell errors decoding the engine's own output | Adjust the quantiser; record the figure |
| wazero against wasmtime | wazero meets the decode threshold, or wasmtime's command-line runner is less than twice as fast on the same module and clips | **Stop and report both sets of numbers.** The owner will decide whether to move to Rust |
| HDR output | Whether the engine's libaom can write 10-bit AV1 at a usable speed | Record the options for `--hdr`; nothing else depends on it |

Starting points to validate, not to trust: feed the encoder `yuv420p` planes you built yourself, and
never let ffmpeg convert between limited and full range; take brightness with `-vf extractplanes=y`;
pass `-noautorotate`; request 10-bit output only when the probe reports a 10-bit source.

*S3, LDPC speed.* Obtain the matrices as section 3 requires and commit them, with their sources, as a
data file under `internal/fec/ldpc`. A layered min-sum decoder for the `normal` code must reach
20 Mbit/s per core, counted in sent bits, at 10 iterations. If not, reduce iterations or use
word-parallel arithmetic, and record the result.

*The S2 test pattern.* After Agent 1's work is merged, write `spikes/pattern`, a self-contained program
(its own small painter, the S1 engine wrapper) that writes `pattern-<W>x<H>.mp4` for a given monitor
resolution:

- One file at 60 fps, as FORMAT.md section 3.
- Segments of about 4 s, in this order: for each display rate of 12, 30, 60 fps; for each cell size of
  4, 8 and, where it divides both W and H, 12 pixels; for each of modulations 1, 2 and 3.
- A display rate of r is produced by showing each code frame for 60 / r video frames.
- Every code frame is a real FORMAT.md frame: structure, anchors with phase markers, and a header whose
  session is `5332` (hexadecimal), whose frame index counts code frames from 0 across the whole file,
  whose F is the total code-frame count rounded up to a multiple of 4 (lengthen the last segment to
  match), whose grid and modulation are the segment's, and whose inner code is 2.
- Data cells carry no codewords. Every bit position holds its scramble bit alone, as in FORMAT.md
  section 9 with all codeword bits zero. The analysis can therefore recompute every expected symbol from
  the header.

Hand the file to the owner with the S2 checklist in section 6, then continue. Do not wait.

**Exit**: `docs/spikes/S1.md` and `S3.md` record every measurement beside its threshold; the golden
vectors pass on all six runners; any "otherwise" branch taken is reflected in the documents.

### M1. Building blocks (7 agents)

| Stream | Owns | Tests to write first | Exit |
| --- | --- | --- | --- |
| A payload | `internal/payload` | Round trip of many files, nested and empty directories, an empty file, a 1 GB sparse file; the input-to-path rules and every refusal of FORMAT.md section 12; wrong passphrase gives the typed error; key generation, key-file parsing, and a warning for a private key readable by others; signed and unsigned stream vectors; flipping any one bit of a signed stream fails | Stream golden vectors frozen |
| B error correction | `internal/fec/ldpc`, `internal/fec/rs` | `H c = 0` for every code; shortened encode vectors; the error-rate test below; Reed-Solomon recovers from any K of K + M; the shard store survives a kill during a write | LDPC vectors frozen |
| C modem and render | `internal/modem`, `internal/render` | Bit-to-level mapping for each mode; soft bits have the right sign and scale on synthetic observations; for a fixed array of symbols in each mode at 480p, the SHA-256 of the rendered planes | Render vectors frozen |
| D video | `internal/video` | Raw-frame backend round trip; embedded encode output probes as H.264 High, yuv420p, MP4, BT.709 limited; decoding that file and taking the nearest of four fixed grey levels at each cell centre reproduces every symbol; sliced decode covers every frame; the native backend behaves identically; the engine binary's SHA-256 matches a pinned value | Digital loopback at 480p |
| E simulator | `internal/channel` | Each stage against a case with a known answer; identical output for identical seeds on every platform; the presets of DESIGN.md section 15 | Presets frozen |
| F command line | `internal/cli`, `cmd/qrvt` | Golden renderings of the dashboard fed with scripted events; NDJSON events validate against a schema; plain output when not a terminal; control characters stripped from every printed string; every command and flag of DESIGN.md section 13, including `keygen`, `--dry-run`, `--report`, `--overwrite` and `--verify-key`; passphrases never accepted as arguments | `qrvt --help` and a scripted dashboard demo |
| G release plumbing | `.github/`, `.goreleaser.yaml`, `scripts/`, `tools/dev` | A snapshot release builds all six targets and every package format; a check that every action is pinned by commit and every workflow declares its permissions; CodeQL, the licence-compatibility check and dependency updates are wired; a nightly workflow exists and runs long fuzzing | Snapshot release green |

*The LDPC error-rate test.* For each code, find a published curve for the same code and take the
signal-to-noise ratio at which its frame error rate is 1 in 100. The decoder must reach that error rate
within 0.5 dB of it. Record the source. If no curve can be found for a code, require the error rate
within 2.5 dB of the binary-input capacity limit for the code's rate, and record that this is the weaker
check.

*For stream G.* The repository is public, so CodeQL, private vulnerability reporting and Pages should be
available. Wire each, and list in your report any that could not be exercised.

### M2. Vision (2 agents)

| Stream | Owns | Asserted on the simulator presets `tripod` and `handheld` |
| --- | --- | --- |
| Geometry | detector, header reader, rectifier and sampler files of `internal/vision` | Finders found in at least 99.9% of 1,000 random poses; the header read in at least 99.5% of captures on `handheld`, trying each copy alone, then pairs, then all four; cell-centre error at most 0.15 cell RMS under perspective to 25 degrees, lens distortion and rolling shear |
| Radiometry and phase | equaliser, stripe-cancellation and phase files of `internal/vision` | Honest confidence, as defined in DESIGN.md section 15, under gamma, white-balance and vignetting changes; decoding succeeds with stripe cancellation on at a fringe amplitude where it fails with it off; phase weight error at most 0.05 for boundaries in any direction; no soft bit credited to the wrong code frame |

When M2 is merged and S2 recordings exist, analyse them before anything else, using `spikes/pattern` and
the M2 code: delivered bits per cell for each combination, the fraction of clean cells, and clipping.
Compare with the S2 bars in section 6. Then update the default mode wherever it is stated (DESIGN.md
sections 5 and 13, FORMAT.md section 14) and the planning figures in DESIGN.md section 11.

### M3. Pipeline and public API (2 agents)

| Stream | Owns | Tests to write first |
| --- | --- | --- |
| Pipeline and API | `internal/pipeline`, the root package, `tools/dev testvideos` | SHA-256 round trip for every row of the matrix in DESIGN.md section 15; several recordings passed to one decode add up, and two that each fail alone succeed together; start mid-loop; frame loss at the two levels given in DESIGN.md section 15; `DryRun` reports length, size and filming time without rendering; the JSON report; cancellation leaves no temporary files; typed errors. Then: the frame vectors and the reference video of FORMAT.md section 13, and the committed set of synthetic test clips |
| Hostile inputs | `testdata/hostile/`, fuzz targets, end-to-end tests | Everything in SECURITY.md sections 9.1 to 9.6 |

**Exit**: every row of the assertion table and of the round-trip matrix in DESIGN.md section 15 passes.
Then, if S2 has not been analysed because the recordings are missing, stop and ask for them.

### M4. Calibration and guidance (2 agents)

`internal/calib` and the `calibrate`, `inspect` and `profiles` commands. `calibrate read` on simulated
devices must recommend the setting the simulator is known to favour, within one step. A failed decode
must say what is missing and what to do.

Then stop for **G1** (section 6).

### M5. Real devices (3 agents)

Make every G1 recording decode. Add each as a local regression clip. Tune every mode, the merging of
recordings, stripe cancellation and 10-bit recordings against them. Replace the planning figures in
DESIGN.md section 11 and the simulator presets in section 15 with what was measured, and update the
defaults.

### M6. Scale and HDR output (3 agents)

- Sliced parallel encode and decode; resuming with `--state-dir`; and the memory bounds of DESIGN.md
  section 8. Add to the nightly workflow a 1 GB loopback that asserts those bounds, the full simulator
  matrix, and benchmarks with regression alerts.
- The experimental `--hdr` mode, using the option chosen in S1, added to the round-trip matrix.

Then stop for **G2**.

### M7. Final security pass

Follow SECURITY.md section 10 exactly. The reviewers must be agents that have seen none of the
implementation history. Exit: no open finding of high or medium severity.

### M8. Documentation and release (2 agents)

- The user guide, an architecture guide, the website and its publishing workflow.
- Root `SECURITY.md`; package formats; signing and notarisation wiring; the script that rebuilds the
  embedded ffmpeg from source, with the licence notices its GPL terms require; and a full release dry
  run. List for the owner every secret and external repository the release workflow needs.
- Exit: the release dry run succeeds, and an independent delta security review of everything merged since the M7 pass
  ended (release workflows, packaging configuration, signing steps, and any code touched), with no
  open finding of high or medium severity.

When M8 exits cleanly, prepare the `v1.0.0-rc.1` tag and stop; pushing it is the owner's decision.

## 6. Steps that need the owner

Give the owner the matching checklist, the files to play, and the folder to put recordings in.
Recordings stay on this machine: `recordings/` is ignored by git, and nothing from it is ever committed
or uploaded.

**General filming notes for every step.** Play the video full-screen at the monitor's native resolution
with any picture enhancement off. Fill about 80% of the phone's frame with the screen. Copy the original
file from the phone by cable, AirDrop or a drive; messaging apps re-compress video and ruin it.
Name files `<device>-<camera mode>-<hand or tripod>`.

**S2: test pattern** (asked for in M0, not blocking)

- Tell me each monitor's native resolution, so I can generate a pattern for it.
- For each phone, record the pattern once in each of: its highest resolution at its highest frame rate;
  4K at 30 fps; 1080p at 30 fps. On an iPhone, once with HDR video on and once with it off.
- One extra take per phone on a tripod or propped up.
- Put the files in `recordings/s2/`.

How S2 is judged, on the best phone and its best combination:

| Bar | If it is not met |
| --- | --- |
| Four greys deliver at least 1.5 bits per cell at 5 camera pixels per cell | The default becomes black-and-white or colour, whichever measures best |
| At least 90% of cells are clean when the camera runs at twice the display rate | The default display rate is lowered |
| Estimated net rate is at least 8% of the recording's bitrate, where estimated net rate = delivered bits per cell x data cells per frame x display rate x 0.75 | Rescale the planning table in DESIGN.md section 11 by the measured ratio and give the owner the new time for 1 GB in your next report |

None of these is a reason to stop.

**G1: calibration and sample transfers** (after M4)

- Film `calibration.mp4` with each phone, handheld.
- Run the command `calibrate read` recommends for a 1 MB, a 10 MB and a 50 MB sample, and film each.
- One take starting mid-loop; one deliberately shaky; one stopped halfway and restarted as a second file.
- Put the files in `recordings/g1/`.

**G2: large transfer** (after M6)

- One transfer between 200 MB and 1 GB at the recommended setting, on a tripod. If the phone stops
  recording or overheats, restart and keep every part.
- If an HDR monitor is available, one `--hdr` clip.
- Put the files in `recordings/g2/`.

## 7. When to stop

Stop and report when:

- G1 or G2 is reached, or M3 is complete and S2 recordings are still missing;
- a spike threshold fails and its "otherwise" says to stop, or has no "otherwise" left;
- an exit test cannot be met after a genuine attempt at a different approach;
- you would need to do something the Authorisations table does not allow, and nothing else can proceed;
- the documents contradict each other in a way you cannot resolve from DESIGN.md's stated reasons.

Do not stop for ordinary failures: a failing test, a flaky runner or a dependency error is yours to
diagnose and fix.

A stop report contains, in this order: what you need from the owner; what was completed, with the exit
tests that passed; every measurement taken beside its threshold; every place where the documents were
changed and why; and anything not verified, said plainly.

## 8. Done

The work is finished when M7 and M8 have exited cleanly, all checks pass on the six runners, the three
documents describe what was actually built, and the release candidate tag is prepared for the owner.

## Change log

| Date | Change |
| --- | --- |
| 2026-11-14 | The owner made the GitHub repository public (Authorisations, stream G note); the owner swapped the last two milestones: M7 is now the final security pass and M8 documentation and release, which gains a delta security review of everything merged after M7 before the release candidate is prepared |
