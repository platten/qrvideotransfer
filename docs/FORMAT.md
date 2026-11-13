# qrvt on-screen format, version 1 (draft)

**Status: draft.** Each part becomes normative when its golden test vectors are frozen: the arithmetic
of sections 1, 4 to 6 and 9 to 11 in milestone M0; the codes and the stream (sections 8 and 12) in M1;
whole frames and the reference video in M3. Until then, an implementer who finds an ambiguity or an
error resolves it here first and records the change in section 15.

The reasons behind each choice are in [DESIGN.md](DESIGN.md). This document says only what an encoder
must produce and what a decoder may rely on.

## 1. Conventions

- "Must" marks a requirement. "Default" marks an encoder choice a decoder must not depend on.
- Multi-byte integers are big-endian. Bits within a field are most significant first. Fields are
  concatenated in the order listed.
- Coordinates: x to the right, y down, origin at the top-left corner of the frame. Positions are in
  *cells* unless stated, and are continuous: cell (cx, cy) covers the square [cx, cx+1) x [cy, cy+1).
- All layout arithmetic is exact rational arithmetic. Where a value must become an integer the rule is
  given explicitly. Implementations must not use floating point for layout.
- `SHA-256(a | b)` hashes the concatenation of `a` and `b`. Quoted strings are ASCII without a terminator.
- **SplitMix64** with 64-bit state `s`: `s += 0x9E3779B97F4A7C15; z = s; z = (z ^ (z >> 30)) *
  0xBF58476D1CE4E5B9; z = (z ^ (z >> 27)) * 0x94D049BB133111EB; return z ^ (z >> 31)`, all modulo 2^64.
- **Shuffle(N, seed)**: start with `a[i] = i` for `i` in 0..N-1 and a SplitMix64 generator whose state is
  `seed`. For `i` from N-1 down to 1: `j = floor(next() * (i + 1) / 2^64)`; swap `a[i]` and `a[j]`.
  The result is the permutation `a`.
- **Seed(label, bytes)** is the first 8 bytes of `SHA-256(label | bytes)` read as a big-endian integer.

## 2. Terms

| Term | Meaning |
| --- | --- |
| Code frame | One image of the encoded video |
| Capture | One frame of a recording of the screen |
| Grid | The lattice of GridX x GridY cells covering the whole code frame |
| Structure | Border, finders, separators, header copies and palette strips |
| Anchor | A geometry-and-phase marker inside the grid |
| Data cell | A cell that is neither structure nor anchor |
| Shard | A fixed-size piece of the stream; the unit of the outer code |
| Codeword | One inner-code block carrying one shard |
| Packet | A shard in transmission order |

## 3. Video

An encoder must produce:

| Property | Value |
| --- | --- |
| Container | MP4 (ISO base media), one video track, no audio, no edit list |
| Codec | H.264 Main or High profile, progressive, 8-bit 4:2:0, CABAC |
| Frame types | Every frame is an IDR frame |
| Colour tags | BT.709 primaries, transfer and matrix; limited range |
| Pixel aspect | 1:1 |
| Frame rate | Constant; 12, 30 or 60 by preset, any positive integer otherwise |
| Size | W x H pixels, both multiples of the cell pitch P and of 2 |
| Cell pitch P | 4 (default), 8, 12 or 16 pixels |
| Frame count | F, a multiple of 4; the video is meant to loop from frame F-1 to frame 0 |

Default quantiser is 32. The requirement is on the outcome, not the setting: decoding the encoder's own
MP4 must reproduce every cell exactly.

GridX = W / P and GridY = H / P. The grid origin is the frame origin. Limits: GridX and GridY at most
2047; H at least 480. An encoder must not produce a video whose width or height is not a multiple of P.
Since P is at most 16, GridY is at least 30, and section 4 needs Dx > 0, that is 15 GridX > GridY. A
decoder must reject a header whose grid breaks any of these limits.
(The command line reduces a requested size to the nearest lower multiples of P and reports the size used.)

| Preset | W x H | Grid at P = 4 |
| --- | --- | --- |
| 480p | 848 x 480 | 212 x 120 |
| 720p | 1280 x 720 | 320 x 180 |
| 1080p | 1920 x 1080 | 480 x 270 |
| 1440p | 2560 x 1440 | 640 x 360 |
| 4K | 3840 x 2160 | 960 x 540 |

The HDR output mode (modulation 4) uses a 10-bit video. Its codec and tags are not yet specified; see
section 14.

## 4. Structure geometry

The structure is defined so that it occupies the same fraction of the screen for every grid. A decoder
can therefore find and read it knowing nothing about the grid.

Define, in cells:

```
Uy = GridY / 135          structure unit, vertical; also the finder module size
c0 = 9/2 * Uy             distance of each finder centre from the two nearest frame edges
Dx = GridX - 9 * Uy       horizontal distance between finder centres
Dy = 126 * Uy             vertical distance between finder centres
Ux = Dx / 231             structure unit, horizontal
```

For a 16:9 grid Ux = Uy. At 1080p, Uy = Ux = 2, c0 = 9, Dx = 462, Dy = 252.

The elements below are given for the top-left corner. The other three corners are mirror images:
top-right maps x to GridX - x, bottom-left maps y to GridY - y, bottom-right maps both.

| Element | x range | y range |
| --- | --- | --- |
| Border band | within Uy of any frame edge | |
| Finder | [Uy, 8 Uy) | [Uy, 8 Uy) |
| Separator | the part of [Uy, 9 Uy) x [Uy, 9 Uy) outside the finder | |
| Header copy | [c0 + 11/2 Ux, c0 + 129/2 Ux) | [c0 - 7/2 Uy, c0 + 1/2 Uy) |
| Palette strip | [c0 + 129/2 Ux, c0 + 161/2 Ux) | [c0 - 7/2 Uy, c0 - 3/2 Uy) |

### 4.1 Finders

A finder is 7 x 7 square modules of side Uy. For module (mx, my) with mx, my in 0..6, let
`ring = max(|mx - 3|, |my - 3|)`. The module is dark when ring is 0, 1 or 3 and light when ring is 2.
A line through the centre therefore crosses dark, light, dark, light, dark in the ratio 1:1:3:1:1.

The top-left, top-right and bottom-left finders are as described and their separators are light.
The **bottom-right finder is inverted**: dark and light are exchanged in the finder and its separator.
This fixes rotation and mirroring.

### 4.2 Header slots

A header copy is 59 slots wide and 4 slots tall. Slot (i, r), with i in 0..58 and r in 0..3, of the
top-left copy covers

```
x in [c0 + (11/2 + i) Ux,  c0 + (13/2 + i) Ux)
y in [c0 + (r - 7/2) Uy,   c0 + (r - 5/2) Uy)
```

Its centre, expressed as a fraction of the finder-centre rectangle, is
`xi = (6 + i) / 231`, `eta = (r - 3) / 126`. These fractions do not depend on the grid.

Mirrored copies use the same (i, r) numbering: i counts away from the copy's own finder horizontally and
r counts from the frame edge toward the frame centre.

### 4.3 Palette strips

A strip has 8 patches. Patch k, with k in 0..7, of the top-left strip covers

```
x in [c0 + (129/2 + 2k) Ux,  c0 + (133/2 + 2k) Ux)
y in [c0 - 7/2 Uy,           c0 - 3/2 Uy)
```

Patch k shows level `k mod L`, where L is the number of levels of the modulation (section 7). In mirrored
strips k counts away from the strip's own finder.

### 4.4 Colours of structure

| Element | Appearance |
| --- | --- |
| Dark | The modulation's lowest level |
| Light | The modulation's highest level |
| Border band | Mid-grey: luma 126, neutral chroma |
| Header slot | Dark for bit 0, light for bit 1 |

### 4.5 Rasterising

Structure rectangles are converted to pixels by multiplying by P and rounding each edge to the nearest
integer, halves up. A frame is painted in this order: fill with mid-grey; paint data cells and anchors;
paint structure. At 1080p and 4K every structure edge falls on a cell boundary.

The chroma planes have half the resolution of luma. Each chroma sample takes the colour of the top-left
of the four luma pixels it covers.

In a 10-bit frame (modulation 4), mid-grey is luma 504 and chroma 512 (the 8-bit values times 4), and
dark and light are the modulation's lowest and highest ten-bit levels.

## 5. Grid contents

### 5.1 Reserved cells

A cell is **reserved** when its square intersects, with positive area, any of the following for any of the
four corners: the border band, a finder with its separator ([Uy, 9 Uy) squared), a header copy, a palette
strip. The test uses the exact, unrounded rectangles of section 4.

### 5.2 Anchors

An anchor occupies 8 x 4 cells. Candidate positions have their top-left cell at
`x = 12 + 32a`, `y = 13 + 30b` for integers a, b >= 0 with `x + 8 <= GridX` and `y + 4 <= GridY`.
A candidate is an anchor when none of its 32 cells is reserved.

| Part | Cells (relative to the anchor's top-left) | Content |
| --- | --- | --- |
| Saddle | x 0..3, y 0..3 | Four 2 x 2 quadrants: top-left and bottom-right dark, top-right and bottom-left light |
| Phase marker | x 4..7, y 0..3 | Four 2 x 2 quadrants numbered 0 top-left, 1 top-right, 2 bottom-right, 3 bottom-left. Quadrant `frame index mod 4` is light; the others are dark |

### 5.3 Data cells

Every cell that is neither reserved nor part of an anchor is a data cell. Data cells are numbered
0..Ncells-1 in row-major order: increasing y, then increasing x.

| Grid | Reserved | Anchors | Data cells | Share |
| --- | --- | --- | --- | --- |
| 212 x 120 | 1,824 | 24 | 22,848 | 89.8% |
| 320 x 180 | 4,140 | 60 | 51,540 | 89.5% |
| 480 x 270 | 8,296 | 131 | 117,112 | 90.4% |
| 640 x 360 | 15,540 | 216 | 207,948 | 90.3% |
| 960 x 540 | 33,184 | 504 | 469,088 | 90.5% |

These counts are test vectors: an implementation of sections 4 and 5 must reproduce them.

## 6. Header

Every code frame carries the same 236 coded bits in all four header copies.

### 6.1 Fields

| Field | Bits | Value |
| --- | --- | --- |
| Format version | 4 | 1 |
| Session | 16 | Low 16 bits of the 64-bit session id |
| Frame index | 22 | 0..F-1 |
| Total frames F | 22 | Multiple of 4, at least 4 |
| GridX | 11 | |
| GridY | 11 | |
| Modulation | 3 | Section 7 |
| Inner code | 3 | Section 8 |
| Reserved | 4 | Zero; a decoder must reject non-zero |
| CRC-16 | 16 | CRC-16/CCITT-FALSE (polynomial 0x1021, initial value 0xFFFF, no reflection, no final XOR) over the preceding 96 bits taken as 12 bytes |

Total 112 bits.

### 6.2 Coding

1. Append 6 zero bits (118 bits).
2. Encode with the rate-1/2, constraint-length-7 convolutional code with generators 171 and 133 (octal),
   starting from the all-zero state. For input bit `u[t]`, output first
   `u[t] ^ u[t-1] ^ u[t-2] ^ u[t-3] ^ u[t-6]`, then `u[t] ^ u[t-2] ^ u[t-3] ^ u[t-5] ^ u[t-6]`.
   This gives coded bits `c[0..235]`.
3. XOR with the mask: bit n of the mask is bit n, most significant first, of
   `SHA-256("qrvt/header-mask/v1")`, which is
   `7f02f40ba124b46bb5de9b5e7af0c1d012226027f03b1165cda2dece1ee7bcfd`.
4. Place masked bit n in slot number `s = (67 n) mod 236`, where slot number s is slot
   `(i = s div 4, r = s mod 4)`.

## 7. Modulation

| Id | Name | Bits per cell b | Levels L | Symbol to appearance |
| --- | --- | --- | --- | --- |
| 0 | reserved | | | |
| 1 | Gray2 (black and white) | 1 | 2 | level = bit |
| 2 | Gray4 | 2 | 4 | level l where `l ^ (l >> 1)` equals the 2-bit value |
| 3 | RGB8 (colour) | 3 | 8 | the three bits, most significant first, switch R, G and B between low and high |
| 4 | Gray8 (HDR) | 3 | 8 | level l where `l ^ (l >> 1)` equals the 3-bit value |
| 5-7 | reserved | | | |

For RGB8, level number k (used by the palette) is the 3-bit value k.

**Default appearance.** These are encoder defaults; a decoder must learn levels from the palette and the
data and must not assume them.

| Modulation | Default levels |
| --- | --- |
| Gray2 | Luma 16 and 235; chroma 128 |
| Gray4 | Luma 16, 89, 162, 235; chroma 128 |
| RGB8 | The limited-range BT.709 values in the table below |
| Gray8 | Ten-bit luma 64, 189, 314, 439, 565, 690, 815, 940; neutral chroma |

| RGB8 value | Colour | Y' | Cb | Cr |
| --- | --- | --- | --- | --- |
| 0 | Black | 16 | 128 | 128 |
| 1 | Blue | 32 | 240 | 118 |
| 2 | Green | 173 | 42 | 26 |
| 3 | Cyan | 188 | 154 | 16 |
| 4 | Red | 63 | 102 | 240 |
| 5 | Magenta | 78 | 214 | 230 |
| 6 | Yellow | 219 | 16 | 138 |
| 7 | White | 235 | 128 | 128 |

A decoder may rely on one property only: for grey modulations, luminance increases with the level number.

## 8. Inner code

The inner code is the IEEE 802.11n quasi-cyclic LDPC code with block length 1944 (lifting size 81), at
the four standard rates, shortened so that one codeword carries exactly one shard and its CRC.

| Id | `--protection` | Mother rate (k) | Shard S | Info bits | Shortened | Sent bits | Net rate |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0 | reserved | | | | | | |
| 1 | `fast` | 5/6 (1620) | 192 bytes | 1,568 | 52 | 1,892 | 0.812 |
| 2 | `normal` | 3/4 (1458) | 128 bytes | 1,056 | 402 | 1,542 | 0.664 |
| 3 | (unnamed) | 2/3 (1296) | 128 bytes | 1,056 | 240 | 1,704 | 0.601 |
| 4 | `robust` | 1/2 (972) | 64 bytes | 544 | 428 | 1,516 | 0.338 |
| 5-7 | reserved | | | | | | |

- Info bits are the shard's bytes, most significant bit first, followed by CRC-32C of those bytes
  (Castagnoli polynomial 0x1EDC6F41, reflected, initial value and final XOR 0xFFFFFFFF) as 4 bytes.
- The mother codeword is: info bits, then `k - info` zero bits, then the `1944 - k` parity bits.
- The zero bits are not sent. Sent bit t is info bit t for t below the info count, then the parity bits
  in order.
- The parity-check matrices are those of IEEE Std 802.11-2020, Annex F, for codeword length 1944.
  They must be taken from the standard or from two independent published implementations that agree,
  never reproduced from memory. A transcription slip still yields a working code that cannot
  interoperate, so the matrices in the repository are covered by a test vector (section 13).
- The data file `internal/fec/ldpc/ieee80211n_1944.txt` holds the four base matrices: 24 x (1 - R)
  block rows (12, 8, 6 and 4) of 24 shifts each, -1 for an all-zero 81 x 81 block, otherwise the right
  cyclic shift of the identity. Its SHA-256 is
  `c51a02dd2b4916fb182df0e7d02d9d5824eb6b6c0f6dd7a6895a220cc7a3514e`.
- The parity bits are unique: the parity part of each base matrix (a first column of weight 3 with
  shifts 1, 0, 1, then a dual diagonal of unshifted identities) is invertible, so any encoder that
  satisfies `H c = 0` with the bits above produces the same sent bits.
- The vectors of section 13 are frozen (M1). Worked values: for the all-zero 128-byte shard (codes 2
  and 3) the CRC-32C is `082764db`; for the all-zero 64-byte shard (code 4) `03c8eb67`; for the
  all-zero 192-byte shard (code 1) `74357830`.

## 9. Bits to cells

Let `Nbits = Ncells * b` and let `sent` be the sent-bit count of the inner code.

- Codewords per frame: `C = floor(Nbits / sent)`. C must be at least 2.
- `Pi = Shuffle(Nbits, Seed("qrvt/inner-interleave/v1", session16 | GridX16 | GridY16 | modulation8 | inner8))`,
  where the suffix is the field width in bits.
- Rotation for frame f: `rho = (f * 2654435761) mod Nbits`.
- Sent bit t of codeword c, with c in 0..C-1, goes to position `q = Pi[(c * sent + t + rho) mod Nbits]`.
  Positions that receive no codeword bit hold 0.
- Scrambling: let `out[0], out[1], ...` be successive SplitMix64 outputs from state
  `Seed("qrvt/scramble/v1", session16 | f32)`. The scramble bit for position q is
  `(out[q div 64] >> (63 - q mod 64)) & 1`. Every position is XORed with its scramble bit.
- Data cell e takes the b bits at positions `e * b .. e * b + b - 1`, the first being the most
  significant, and shows the corresponding symbol.

## 10. Stream descriptor

Codeword 0 of every frame carries the descriptor, zero-padded to the shard size S.

| Offset | Bytes | Field |
| --- | --- | --- |
| 0 | 4 | Magic `QRVT` |
| 4 | 1 | Descriptor version, 1 |
| 5 | 8 | Session id |
| 13 | 6 | Stream length L in bytes |
| 19 | 2 | Shard size S |
| 21 | 2 | K, data shards per block |
| 23 | 2 | M, parity shards per block |
| 25 | 3 | B, number of blocks |
| 28 | 3 | F, total frames |
| 31 | 2 | C, codewords per frame |
| 33 | 4 | Outer order seed |
| 37 | 1 | Display rate in frames per second, rounded; 0 if unknown |
| 38 | 1 | Reserved, zero |
| 39 | 16 | First 16 bytes of SHA-256 of the stream |
| 55 | 4 | CRC-32C of bytes 0..54 |

Total 59 bytes.

A decoder must reject a descriptor unless all of these hold: the CRC matches; the magic and version are
as given; byte 38 is zero; the low 16 bits of the
session id equal the header's session; F equals the header's F; S equals the inner code's shard size;
C equals the value computed in section 9; B and K equal the values section 11 derives from L and S;
M is between 1 and 32,768; and F equals the value section 11 derives from B, K, M and C.

## 11. Outer code and transmission order

The stream (section 12) is L bytes.

```
Nd = ceil(L / S)            data shards needed
B  = ceil(Nd / 32768)       blocks
K  = ceil(Nd / B)           data shards per block
M  = chosen by the encoder  parity shards per block, 1 <= M <= 32768
n  = K + M
T  = B * n                  packets
F  = 4 * ceil(ceil(T / (C - 1)) / 4)
```

The encoder default for M is `max(1, ceil(K * parity / 100))` with parity = 20.

- Data shard i of block b, with i below K, holds stream bytes `[(b K + i) S, (b K + i + 1) S)`, with zero
  bytes beyond L.
- Parity shards K..n-1 of each block are computed by Leopard Reed-Solomon over GF(2^16) with K data and M
  parity shards. The reference implementation is `github.com/klauspost/reedsolomon` with
  `WithLeopardGF16(true)`, which is compatible with catid/leopard. GF(2^16) is used for every block,
  including small ones. Every (K, M) this section can produce must be accepted by that implementation;
  a property test checks it.
- `sigma = Shuffle(n, Seed("qrvt/outer-order/v1", seed32))`, using the descriptor's outer order seed.
- Packet p, with p in 0..T-1, is shard `sigma[(p div B + 40503 * (p mod B)) mod n]` of block `p mod B`.
- Codeword c of frame f, for c in 1..C-1, carries packet `(f * (C - 1) + c - 1) mod T`.

Packets beyond T-1 wrap to the start, so the last frames repeat some early packets.

B travels in 3 bytes and F in 22 bits, so B is at most 16,777,215 and F at most 4,194,300. An encoder
must refuse a stream, shard size, M and C for which either is exceeded. (The block limit is reached
first only beyond about 32 TiB at 64-byte shards; the frame limit is the practical one.)

## 12. Stream

The stream is what the outer code protects. Its length L includes every field below.

| Offset | Bytes | Field |
| --- | --- | --- |
| 0 | 4 | Magic `QRVS` |
| 4 | 1 | Stream version, 1 |
| 5 | 1 | Compression: 0 none, 1 zstd, 2 xz |
| 6 | 1 | Flags: 0x01 encrypted, 0x02 signed; all other bits zero |
| 7 | 1 | Reserved, zero |
| 8 | 8 | Archive length in bytes, before compression |
| 16 | 8 | Body length Lb |
| 24 | Lb | Body |
| 24 + Lb | 96 | Only when signed: Ed25519 public key (32 bytes), then signature (64 bytes) |

- **Body**: the archive, compressed as indicated, then, when the encrypted flag is set, encrypted in the
  age v1 binary format to a single scrypt (passphrase) recipient.
- **Signature**: Ed25519 over the 44-byte message `"qrvt/sign/v1" | SHA-256(stream bytes 0 .. 24 + Lb - 1)`.
- **Archive**: a 4-byte manifest length, the manifest, then a tar stream in POSIX pax format.
- **Manifest**: UTF-8 JSON of the form
  `{"version":1,"entries":[{"path":"docs/a.txt","type":"file","size":1234,"mode":420,"mtime":"2026-11-13T10:00:00Z","sha256":"<64 hex digits>"},{"path":"docs","type":"dir","mode":493,"mtime":"..."}]}`.
  `type` is `file` or `dir`. `size` and `sha256` are present for files only. Only the low 9 bits of
  `mode` are meaningful.
- The tar stream contains exactly the manifest's entries, in the same order, with the same paths, as
  regular files and directories only. A directory's tar name is its path followed by one `/`; a
  decoder also accepts it without the `/`.
- The archive is exactly the archive length of the header: decompression must yield neither more nor
  fewer bytes.

**What an encoder writes.**

- Each input named on the command line is stored under its base name: the file `a/b/c.txt` becomes
  `c.txt`; the directory `a/b/d` becomes `d`, and everything beneath it becomes `d/...`. The base name
  is taken from the absolute path, so `.` is stored under the name of the folder it names; a root
  folder has no base name and stops the encode.
- Every directory that contains an entry is itself an entry.
- Entries are sorted by path, compared byte by byte. A directory therefore precedes its contents.
- Two inputs with the same base name, or any path that breaks the path rules below, stop the encode
  with an error. Nothing is renamed silently.
- `mtime` is in whole seconds, UTC, written as RFC 3339 with a `Z` suffix.
- The manifest is written with its keys in the order shown above and no whitespace between tokens.
- Tar headers carry the path, size, the low 9 mode bits and the modification time. User and group ids
  are 0 and their names empty.
- Links and special files (devices, FIFOs, sockets) are not entries; the encoder skips them with a
  warning.
- Default compression: the first 4 MiB of the archive are compressed with zstd; the archive is stored
  with compression 0 unless that sample shrinks by at least 2%, and with zstd otherwise. zstd uses a
  window of 8 MiB and xz a dictionary of 8 MiB; age uses scrypt work factor 18. These are also the
  largest values a decoder accepts (below). An xz body is exactly one xz stream, with no stream
  padding, whose blocks each have the single filter LZMA2.

**What a decoder rejects.** A wrong magic or stream version; an unknown compression value; any flag bit
other than 0x01 and 0x02; a non-zero reserved byte; a stream whose length L is not exactly `24 + Lb`,
plus 96 when the signed flag is set; a manifest that is not valid UTF-8 JSON, has a key other than those
shown or any key twice in one object, writes `version`, `size` or `mode` other than as an integer
without fraction or exponent, has an `mtime` that is not RFC 3339, a `sha256` that is not 64 lowercase
hexadecimal digits, or a version other than 1; a tar entry that is not the next manifest entry with
the same type and size, or that follows the last one; a file whose SHA-256 differs from the manifest.
It also rejects, before allocating for them: a zstd frame whose window (or, for a single-segment
frame, content size) exceeds 8 MiB; an xz block whose filter is not LZMA2 alone or whose dictionary
exceeds 8 MiB; an xz body with anything after its one stream (a second stream, stream padding or
other bytes); an age header whose scrypt work factor exceeds 18. Any conforming pax tar is accepted;
the tar bytes need not match those of the reference encoder.

**Order of checks.** A decoder must check the descriptor's stream hash over the whole recovered stream,
then the stream header, then the signature when one is present or required, and only then decrypt,
decompress and unpack. It must validate the entire manifest before writing any file.

**Path rules.** A decoder must refuse the whole transfer if any manifest path breaks any rule:

- relative, with `/` as the only separator; no empty, `.` or `..` component;
- valid UTF-8 in Unicode normalisation form C;
- no NUL, no character below U+0020, no U+007F, no backslash, no colon, and none of `<`, `>`, `"`, `|`,
  `?`, `*`, which Windows cannot store;
- no component ending in a space or a dot;
- no component that is a Windows device name (`CON`, `PRN`, `AUX`, `NUL`, `CONIN$`, `CONOUT$`,
  `COM1`-`COM9`, `LPT1`-`LPT9`, and `COM` or `LPT` followed by `¹`, `²` or `³`), compared without regard
  to case after removing anything from the first dot and any spaces before it;
- each component at most 255 bytes and the whole path at most 4,096 bytes;
- no two entries equal after Unicode case folding: full case folding exactly as CaseFolding.txt
  statuses C and F (so `Straße` equals `STRASSE`, and the Cherokee small letters fold to the capital
  ones, U+AB70 to U+13A0), then normalisation form C;
- every folder that contains an entry is itself an entry of type `dir`, compared after the same
  folding. This refuses an entry beneath a file at any depth, and bounds the folders a decoder creates
  by the number of entries.

## 13. Test vectors

The repository must contain, under `testdata/golden/`, vectors produced by the reference implementation
and reviewed by hand against this document:

| Vector | Checks |
| --- | --- |
| Layout counts for the five preset grids | Section 5.3 table |
| Header: fixed field values to 236 slot bits | Section 6 |
| Shuffle(10, 1) and the first 4 SplitMix64 outputs for state 0 | Section 1 |
| LDPC (`ldpc.json`, frozen in M1): for each inner code, an all-zero shard and a fixed pseudo-random shard (big-endian SplitMix64 outputs from state = inner-code id) to sent bits, packed most significant bit first with zero padding; and `H c = 0` for both | Section 8 |
| Frame: fixed stream, session and seed to the SHA-256 of the luma plane of frames 0 and 1, for Gray2, Gray4 and RGB8 at 480p | Sections 4 to 11 |
| Stream (`stream.json`): fixed inputs to stream bytes with compression 0, unsigned and signed with a fixed key (seed bytes 0 to 31); zstd, xz and age-encrypted streams of the same inputs that must still decode; one manifest per path rule that must be refused. The tar bytes are those Go's `archive/tar` writes for the headers section 12 lists (pax format, with a pax record only where ustar cannot hold a field, such as a long or non-ASCII path). An encoder that wants identical bytes must match that; a decoder accepts any conforming pax tar | Section 12 |
| Reference video: a small encoded MP4 that every later release must decode | Whole format |

Worked numbers for cross-checking, at 480 x 270, Gray4, `normal`, parity 20:

| Quantity | 10 MiB stream | 1 GiB stream |
| --- | --- | --- |
| Nbits, C | 234,224 and 151 | same |
| Nd | 81,920 | 8,388,608 |
| B, K, M | 3, 27,307, 5,462 | 256, 32,768, 6,554 |
| T | 98,307 | 10,066,432 |
| F | 656 | 67,112 |

Worked values for cross-checking. They were computed by a reference script written from this document;
the interleaver rows were also reproduced independently by a reviewer.

| Item | Input | Result |
| --- | --- | --- |
| SplitMix64 | state 0, first four outputs | `e220a8397b1dcdaf`, `6e789e6aa1b965f4`, `06c45d188009454f`, `f88bb8a8724c81ec` |
| Shuffle | N = 10, seed = 1 | 9, 0, 1, 4, 8, 2, 3, 7, 6, 5 |
| CRC-16 | ASCII `123456789` | `29b1` |
| Header fields | version 1, session `0x1234`, frame index 5, F 656, grid 480 x 270, modulation 2, inner code 2 | bytes `11 23 40 00 01 40 02 90 3c 04 39 20`, CRC-16 `03c7` |
| Interleave seed | session 0, grid 480 x 270, modulation 2, inner code 2 | input bytes `00 00 01 e0 01 0e 02 02`, Seed `6eca5aab84a46a75` |
| Position, frame 0 | codeword 1, sent bit 0 | rho 0, q = 1,885 (data cell 942, second bit), scramble bit 1 |
| Position, frame 1 | codeword 1, sent bit 0 | rho 209,393, q = 138,625 (data cell 69,312, second bit), scramble bit 0 |
| Outer order | seed 1, B = 3, n = 32,769 | packets 0, 1, 2, 3 are shards 22,956, 9,712, 29,115, 2,029 of blocks 0, 1, 2, 0; packet 98,306 is shard 21,168 of block 2 |

## 14. Versions and registries

- The header's format version covers sections 3 to 9 and 11. The descriptor version covers section 10.
  The stream version covers section 12.
- Modulation and inner-code ids are registries. A decoder that meets an unknown version or id must stop
  with an error naming the value; it must not guess.
- Changing a default (levels, quantiser, parity) is not a format change. Changing anything a decoder
  relies on is.
- Open in this draft: the video codec and tags for modulation 4, and whether modulation 2 remains the
  default (decided by step S2). Spike S1 found the embedded libaom too slow for 10-bit AV1 (0.07-0.33
  fps at 1080p); 10-bit H.264 (High 10) is the measured candidate, but many stock players and hardware
  decoders lack High 10. The choice is made in M6.

## 15. Change log

| Date | Change |
| --- | --- |
| 2026-11-13 | First draft |
| 2026-11-13 | M0: section 3 states the grid limits a decoder enforces (GridY at least 30, 15 GridX > GridY); section 11 states the limits on B and F; section 13 writes the example session as `0x1234`. All numbers of sections 5.3 and 13 reproduced by the implementation and by an independent script; no value changed |
| 2026-11-13 | M0 spikes: section 3 allows H.264 Main as well as High profile (the encoder's fastest settings turn off the 8x8 transform, so x264 signals Main, a subset of High); section 14 records the S1 result for the modulation 4 codec. No value a decoder relies on changed |
| 2026-11-13 | M1 render: section 4.5 states mid-grey in a 10-bit frame (luma 504, chroma 512); the 8-bit rule was silent on it. No value a decoder relies on changed |
| 2026-11-13 | M1 stream B: section 8 names the matrices' data file and its SHA-256, states that the parity bits are unique, and gives the CRC-32C of the all-zero shards; section 13 freezes the LDPC vectors in `testdata/golden/ldpc.json` and says how the pseudo-random shard is made and how sent bits are packed in the file. No value changed |
| 2026-11-13 | M1 stream A: section 12 stream vectors frozen (`testdata/golden/stream.json`, reproduced by an independent script that recomputes the Ed25519 signature). Section 12 states: a directory's tar name ends in `/` (accepted without); decompression must yield exactly the archive length; `.` is stored under its folder's name and a root folder is refused; links and special files are skipped with a warning; the default compression rule and the zstd window, xz dictionary and scrypt work factor written; the decoder's manifest strictness (UTF-8, no repeated keys, plain integers, RFC 3339 `mtime`, lowercase `sha256`) and tar checks; case folding is full Unicode folding followed by NFC; "file under a file" applies at any depth. Section 13's stream row names the file and its contents |
| 2026-11-13 | M1 stream A, review of PR 7: section 12 path rules also refuse `<`, `>`, `"`, `|`, `?`, `*`, `CONIN$`, `CONOUT$`, `COM` and `LPT` with a superscript digit, and device names followed by spaces. Case folding is stated as exactly CaseFolding.txt C and F (the Cherokee small letters fold to the capitals). "No entry beneath a file" becomes "every folder that contains an entry is an entry of type `dir`", which the encoder already wrote. The decoder rejects a zstd window over 8 MiB, an xz dictionary over 8 MiB or a filter other than LZMA2, and a scrypt work factor over 18, and accepts any conforming pax tar. Section 13 says where the frozen tar bytes come from; the golden file gains the new refused manifests. The frozen streams did not change |
