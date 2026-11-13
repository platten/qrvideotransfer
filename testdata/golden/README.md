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

Provenance: every value was computed by small scripts written from FORMAT.md alone, independently of
the Go code, using exact integer and fraction arithmetic, and then reproduced by the Go
implementation. The numbers printed in FORMAT.md sections 5.3 and 13 are also asserted literally in
the tests.
