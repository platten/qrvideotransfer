# S2 request: film the test pattern

This step does not block anything. Work continues while you film, and the recordings are analysed
once the vision code exists (after milestone M2).

## What I need from you

1. **Each monitor's native resolution.** Patterns exist for 1920x1080, 2560x1440 and 3840x2160. If a
   monitor has another native resolution, tell me and I will generate one for it.
2. **The recordings**, made as described below and copied into `recordings/s2/` in the repository folder.
   That folder is ignored by git: nothing in it is ever committed or uploaded.

## Files to play

They are in `recordings/patterns/` in the repository folder. Pick the one that matches the monitor's
native resolution.

| File | Size | Length | SHA-256 |
| --- | --- | --- | --- |
| `pattern-1920x1080.mp4` | 660 MB | 108 s | `2e59ff2546610169155527ce6a56de83a6353529b52fcd73894ec34aaad2b037` |
| `pattern-2560x1440.mp4` | 957 MB | 72 s | `6e7850ba6b47770db2b0183a73fca9ef42c5862cf9c4e246d39c3d1fae9b30d9` |
| `pattern-3840x2160.mp4` | 2.6 GB | 108 s | `b367a6b0b7061d39b026942224487aa2906ba5ffae176e0900aeb61f64099d4d` |

- **What the pattern shows:** each file plays at 60 fps. It is a series of 4-second segments that step
  through:
  - display rates of 12, 30 and 60 code frames per second;
  - cells of 4, 8 and 12 pixels (12 is skipped at 2560x1440);
  - the modulations black-and-white, four greys and eight colours.
- **Segment tables:** `pattern-<W>x<H>.json`, next to each video.
- **Bitrate:** the 4K file runs at about 193 Mbit/s. Play it from a local disk with a player that can
  sustain that, such as mpv or VLC.
- **If playback stutters,** say so when you hand over the recordings. Dropped frames are visible in the
  analysis, but it helps to know.
- **Loop or play once:** either works. One full play-through per take is enough; if you loop, include
  at least one complete pass.

## General filming notes

- Play the video full-screen at the monitor's native resolution, with any picture enhancement off.
- Fill about 80% of the phone's frame with the screen.
- Copy the original file from the phone by cable, AirDrop or a drive. Messaging apps re-compress video
  and ruin it.
- Name files `<device>-<camera mode>-<hand or tripod>`, for example `iphone15pro-4k60-hdr-hand.mov`.

## S2 checklist

- [ ] Tell me each monitor's native resolution.
- [ ] For each phone, record the pattern once in each of these modes:
  - [ ] its highest resolution at its highest frame rate;
  - [ ] 4K at 30 fps;
  - [ ] 1080p at 30 fps.
- [ ] On an iPhone, record once with HDR video on and once with it off.
- [ ] One extra take per phone on a tripod, or propped up.
- [ ] Put the files in `recordings/s2/`.

## How S2 is judged

The judgement uses the best phone in its best combination.

| Bar | If it is not met |
| --- | --- |
| Four greys deliver at least 1.5 bits per cell at 5 camera pixels per cell | The default becomes black-and-white or colour, whichever measures best |
| At least 90% of cells are clean when the camera runs at twice the display rate | The default display rate is lowered |
| Estimated net rate is at least 8% of the recording's bitrate, where estimated net rate = delivered bits per cell x data cells per frame x display rate x 0.75 | The planning table in DESIGN.md section 11 is rescaled by the measured ratio, and you get the new time for 1 GB |

None of these stops the project.
