# Golden vectors

These files freeze the arithmetic of the format (docs/FORMAT.md section 13). Each is checked by the
tests of the package named below; a change to any value is a format change.

| File | Checks | Test |
| --- | --- | --- |
| `prng.json` | SplitMix64 outputs (including state 0), Shuffle, Seed (section 1) | `internal/prng` |
| `layout.json` | Reserved, anchor and data-cell counts for the five presets and seven other grids; first anchors and cells; rasterised rectangles at 480p (sections 4 and 5) | `internal/frame` |
| `header.json` | CRC-16 check value; header fields to 14 bytes and to the 236 slot bits (section 6) | `internal/frame` |
| `mapping.json` | Modulation and inner-code tables; Nbits and C for every preset, modulation and inner code; interleave seeds, rotations, positions and scramble bits (sections 7 to 9) | `internal/frame` |
| `descriptor.json` | CRC-32C check value; descriptor fields to 59 bytes (section 10) | `internal/frame` |
| `plan.json` | Nd, B, K, M, T, F, packet locations and the packet carried by a codeword (section 11) | `internal/fec` |
| `render.json` | SHA-256 of the Y, Cb, Cr planes of whole frames in every modulation, frames 0 and 1, at 480p with fixed symbols (sections 4 to 7) | `internal/render` |
| `ldpc.json` | For each inner code, the all-zero shard and a pseudo-random shard (big-endian SplitMix64 outputs from state = id) to packed sent bits (section 8) | `internal/fec/ldpc` |
| `stream.json` | Unsigned and signed streams from fixed inputs; zstd, xz and age streams that must keep decoding; manifests that must be refused and accepted (section 12) | `internal/payload` |
| `frames.json` | A fixed stream (its bytes included), session and outer order seed, encoded in Gray2, Gray4 and RGB8 at 480p, to the SHA-256 of the luma plane of frames 0 and 1 (sections 4 to 11) | `internal/pipeline` (`TestFrameVectors`) |
| `reference-v1.mp4`, `reference-v1.json` | The reference video of format version 1 (96,041 bytes, 4 frames, 480p Gray4 `normal`, zstd, signed) and the file it must decode to, with its SHA-256 and signer | root package (`TestReferenceVideo`, through the public API) |

Provenance: every value except those of `frames.json` and the reference video was computed by small
scripts written from FORMAT.md alone, independently of the Go code, using exact integer and fraction
arithmetic, and then reproduced by the Go implementation. The numbers printed in FORMAT.md sections
5.3 and 13 are also asserted literally in the tests. `frames.json` and the reference video were
produced by the Go encoder (`go run ./tools/dev format-vectors`), not reproduced independently; they
are regenerated if FORMAT.md changes before v1 is frozen and are immutable after (FORMAT.md section
13). The reference video's bytes are what is immutable: the test decodes the committed file, and a
regeneration need not give the same bytes.

Not a format vector: `channel.json` holds the SHA-256 of each simulator preset's captures and ground
truth on a fixed input (DESIGN.md section 15), checked by `internal/channel`; regenerate it with
`go test ./internal/channel -update` when the simulator changes deliberately.
