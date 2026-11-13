# qrvt security

This document states what qrvt defends against, how, how that is tested, and how the final security
review before release is run. The design is in [DESIGN.md](DESIGN.md) and the wire format in
[FORMAT.md](FORMAT.md).

## 1. Position

qrvt reads video files and turns what it finds in them into files on disk. **Everything that comes out
of a video is hostile until proven otherwise.** That covers the recording file itself, the header and
descriptor read from its frames, the recovered stream, the archive inside it, and every file name.

Two guarantees are the goal:

1. A crafted video or recording cannot make `qrvt decode` crash the machine, run code, exhaust memory or
   disk, hang, or write outside the chosen output folder.
2. When the receiver names the senders they trust, a transfer that was not produced by one of them, or
   that was altered, is rejected before any of its content is decrypted or unpacked.

## 2. What is in scope

| In scope | Out of scope, by decision |
| --- | --- |
| Hostile recordings and videos given to `decode`, `inspect` and `calibrate read` | Marking decoded files with quarantine or Mark-of-the-Web flags |
| Hostile content reached through a genuine recording of a hostile screen | Scanning decoded files for malware |
| Forged or altered transfers | Hiding that a transfer is taking place |
| Confidentiality of the payload from someone else filming the screen, when a passphrase is used | Protecting a machine whose user account is already compromised |
| The integrity of released binaries | |

qrvt never opens, interprets or runs the files it decodes. Whether a decoded file is itself dangerous to
open is for the receiver and their operating system to judge.

## 3. Trust boundaries

```
recording file --> [ ffmpeg in WebAssembly sandbox ] --> raw frames
raw frames     --> [ vision, header, descriptor ]    --> soft bits, parameters
soft bits      --> [ LDPC, Reed-Solomon ]            --> stream
stream         --> [ hash, signature ]               --> body          <- authenticity is decided here
body           --> [ age, decompressor ]             --> archive
archive        --> [ manifest check, unpack ]        --> files in a staging folder --> output folder
```

Each arrow carries attacker-controlled data. Each box must hold when its input is arbitrary.

## 4. Threats and defences

| # | Threat | Defence |
| --- | --- | --- |
| T1 | A malformed container or bitstream exploits the video decoder | ffmpeg runs inside the wazero WebAssembly sandbox. It is given a read-only view of the single input file where the runtime allows, otherwise of the file's directory; no network; a memory ceiling; and it is cancelled with the command's context. Its only output is raw frames on a pipe, which are length-checked |
| T2 | A lying header or descriptor drives huge allocations or loops | Every field is used only after its CRC passes and its value is checked against section 5. Parameters derived from the stream length are recomputed and must match (FORMAT.md section 10). A decode locks onto the first valid session; frames from any other session are counted and ignored |
| T3 | Soft-bit or shard floods exhaust memory or disk | The soft-bit store is capped and evicts. The shard store is a file whose size is fixed by the validated descriptor, and free disk space is checked before it is created |
| T4 | A decompression bomb | Decompression is streaming and stops at the archive length declared in the stream header. That length must not exceed `--max-size` or the free space on disk |
| T5 | An archive writes outside the output folder or replaces something it should not | The whole manifest is validated against the path rules (FORMAT.md section 12) before any file is written. Only regular files and directories are accepted. Files are written through a directory-rooted handle (`os.Root`) into a new staging folder inside the output folder, their hashes are checked, and only then are they moved into place. Existing files are not overwritten without `--overwrite`. Permission bits are limited to the low nine |
| T6 | File names inject terminal escape sequences | Every string that originates in the input is stripped of control characters before it is printed, in the dashboard, plain and JSON renderers alike |
| T7 | A forged or altered transfer | An optional Ed25519 signature over the stream's header and body, checked before decryption and unpacking (section 6) |
| T8 | Corruption passes as success | The stream's SHA-256 is checked before anything else is done with it, and each file's SHA-256 is checked before it leaves staging. The outcome is correct files or an error, never quietly wrong files |
| T9 | A tampered release or dependency | Section 8 |
| T10 | A passphrase or private key leaks through the command line | Passphrases are read from a prompt or a file, never from an argument. Private key files are created with owner-only permissions, and `qrvt` warns when it reads one that is more widely readable |

### What a native ffmpeg changes

`--ffmpeg system` runs an external ffmpeg with no sandbox, so T1 is then defended only by that ffmpeg's
own robustness. The help text and user guide say so, and recommend it only for recordings the user made
themselves.

## 5. Limits

Fixed by the format: grid at most 2,047 cells each way; at most 4,194,300 frames; at most 32,768 data and
32,768 parity shards per block.

Implementation defaults, each a named constant with a test that exceeds it:

| Limit | Default |
| --- | --- |
| Unpacked size (`--max-size`) | 16 GiB. No ratio limit is applied, because a legitimate sparse file compresses by more than any sensible ratio |
| Stream length | Free disk space less 1 GiB, and at most 64 GiB |
| Manifest | 64 MiB and 1,000,000 entries |
| Soft-bit store | 256 MB |
| LDPC iterations per codeword | 25 |
| Sessions tracked per decode | 1 |
| Engine memory per instance | 1 GiB decoding, 1.5 GiB encoding |
| Capture size accepted from the engine | 8,192 x 8,192 pixels |

A decode that reaches a limit stops with an error that names the limit and, where one exists, the flag
that raises it. Only the unpacked size has a flag in version 1.

## 6. Sender authenticity

- `qrvt keygen -o name` writes an Ed25519 key pair: `name.key` (owner-readable only) and `name.pub`.
  Keys are one line of text: `qrvt-private-key-v1:<base64 of the 32-byte seed>` and
  `qrvt-public-key-v1:<base64 of the 32-byte key>`. A key's fingerprint is the first 16 hexadecimal
  digits of its public key's SHA-256.
- `encode --sign-key name.key` appends the public key and a signature to the stream (FORMAT.md section 12).
- `decode` behaves as follows:

| Transfer | `--verify-key` | Result |
| --- | --- | --- |
| Unsigned | not given | Proceeds; the summary says "unsigned" |
| Signed, signature valid | not given | Proceeds; the summary shows the fingerprint and says it was not checked against a trusted key |
| Signed, signature invalid | given or not | Fails before decryption and unpacking |
| Signed, signature valid, by one of the keys given | given | Proceeds; the summary says "verified" |
| Unsigned, or signed by any other key | given | Fails before decryption and unpacking |

Giving `--verify-key` is what turns enforcement on. There is deliberately no mode that checks a signature
when one is present but accepts its absence: an attacker would simply leave the signature out.

What a verified signature proves: the holder of that private key produced exactly this stream. What it
does not prove: that the video is recent. A recording of an old signed video verifies again.

The signature is over ciphertext when the transfer is encrypted, so it can be checked without the
passphrase. The signer's public key is visible to anyone who films the screen.

## 7. Confidentiality

With `--encrypt`, the body is encrypted in the age format under a key derived from the passphrase with
scrypt. Someone else filming the screen obtains only ciphertext, and its secrecy is as strong as the
passphrase. age authenticates its ciphertext, so tampering is also detected at decryption. Without
`--encrypt`, anyone who films the screen can decode the files.

## 8. Supply chain

- Dependencies are pinned in `go.mod` with checksums in `go.sum`; the embedded ffmpeg module is pinned
  by version and its WebAssembly binary by SHA-256, checked in a test.
- Every pull request runs `govulncheck`, CodeQL and a licence-compatibility check.
- CI actions are pinned by commit hash. Workflows declare the least token permissions they need, never
  run on `pull_request_target`, and never expose secrets to pull requests from forks.
- Releases carry checksums, a keyless cosign signature, an SBOM and build provenance; macOS binaries are
  notarised and Windows binaries signed when the certificates are configured.
- The repository contains the script that rebuilds the embedded ffmpeg from source, and the licence
  notices its GPL terms require.

## 9. Security test plan

### 9.1 Fuzz targets

Each runs briefly on every pull request and for longer nightly, with its corpus committed.

| Target | Must hold |
| --- | --- |
| Header decoding from arbitrary soft bits | No panic; a result only with a valid CRC |
| Descriptor parsing and plan derivation | No panic; no allocation before validation |
| Viterbi and LDPC decoding from arbitrary soft bits | No panic; terminates within the iteration cap |
| Stream header, manifest and key-file parsing | No panic; rejects everything FORMAT.md forbids |
| Unpacking arbitrary archive bytes into a temporary root | No file created outside the root; no panic |
| Finder detection and mesh fitting on arbitrary images | No panic; time bounded by image size |
| Whole decode from arbitrary raw frames, seeded with valid and near-valid frames | No panic; reports success only when the recovered stream matches the descriptor's hash; writes nothing outside the root |

### 9.2 Hostile corpus

Fixtures under `testdata/hostile/`, each with the exact error it must produce, run on Linux, macOS and
Windows:

- **Paths**: absolute; `..`; backslashes; drive letters and colons; NUL and control characters; ANSI
  escape sequences; trailing dot or space; every Windows device name with and without an extension;
  over-long components and paths; two names differing only in case or normalisation; a file under a file.
- **Archive entries**: symbolic links, hard links, devices, FIFOs; entries not in the manifest; entries
  out of order; sizes or hashes that disagree with the manifest.
- **Sizes**: an archive length far beyond `--max-size`; a body that decompresses past its declared
  length; a manifest at and beyond its limits; a stream length beyond free disk space.
- **Format**: every reserved field non-zero; unknown versions and ids; a descriptor that disagrees with
  the header or with the values derived from its own stream length; frames from two sessions interleaved.
- **Video files**: truncated, zero-length, wrong container, and structurally corrupt files. Each must end
  in a clear error within a time bound.

### 9.3 Sandbox tests

- The embedded engine, asked to read a path outside its view, fails.
- The embedded engine, asked to write during a decode, fails.
- An engine instance that exceeds its memory ceiling is stopped and reported, and the process survives.
- Cancelling the command stops every engine instance promptly.

### 9.4 Authenticity tests

For signed transfers: flipping any single bit of the stream header, body, public key or signature is
rejected; nothing is written to the output folder; with `--verify-key`, unsigned transfers and transfers
signed by any other key are rejected; verification happens before the passphrase is requested.

### 9.5 Resource tests

Decoding each hostile input that aims at a limit in section 5 stays under 1 GiB of peak memory and
finishes within 60 seconds on a CI runner. Both are asserted in the test.

### 9.6 Static checks

The race detector on every pull request, CodeQL, `gosec` rules through the linter, and a test over the
root package, `cmd/` and `internal/` that fails if any package other than `internal/video` imports
`os/exec`, or if any package imports `unsafe`. Developer tooling under `tools/` and throwaway code under
`spikes/` are outside that rule, since they must launch other programs.

## 10. Final security pass (milestone M8)

The pass happens when the tool is feature-complete and before the first release candidate is tagged.

1. **Fresh reviewers.** Each reviewer is an agent that has not seen the implementation history. It is
   given this document, FORMAT.md and the code, and one attack surface:
   - recording file to raw frames (engine wrapper and sandbox configuration);
   - raw frames to soft bits (vision, header, descriptor);
   - soft bits to stream (LDPC, Reed-Solomon, stores);
   - stream to files (hash, signature, age, decompression, manifest, unpacking);
   - command line, key and passphrase handling, output sanitising;
   - CI and release workflows, dependency pinning, the embedded binary's provenance.
2. **Automated review** of the complete diff against the empty repository.
3. **Each reviewer attempts to break guarantees 1 and 2** in section 1 for its surface and writes a
   failing test for anything it finds.
4. **Findings** are recorded in `docs/security/REVIEW.md` with severity, the failing test, the fix, and
   the reviewer's confirmation that the fix holds.
5. **Exit.** No open finding of high or medium severity; every section 9 test passes on all three
   systems; the limits table matches the constants in the code.

Severity: *high* breaks a guarantee in section 1; *medium* weakens a defence in section 4 without yet
breaking a guarantee, or is a denial of service that stays within the limits; *low* is everything else.

## 11. Reporting a vulnerability

Report privately through the repository's "Report a vulnerability" page (GitHub private vulnerability
reporting), not in a public issue. The root `SECURITY.md` created in milestone M7 repeats this
instruction and states which versions receive fixes.
