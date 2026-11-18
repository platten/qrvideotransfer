M3 trim, vision tests: `internal/vision`'s always-on tests in the coverage step, 600 to 714 s on macos-26-intel, were cut without changing any bound, tolerance or cost ratio (section 17, coverage-step paragraph; section 19, the CI-size item). The hostile-cost tests skip in a coverage build, as under the race detector, and are asserted without coverage in every plain test run and, on every runner, in the "Hostile-cost ratios" step (`geomCostSkip`). In a coverage build the pose sweeps run their vision code one capture at a time while the simulator films in parallel (`geomVisionLock`), since goroutines writing the shared coverage counters at once contend for them. The always-on sizes of eight tests shrank; their full sizes run with `QRVT_LONG_TESTS=1`, locally only, as no CI job runs the long tests yet: `TestGeometryMoustache` 12 of 40 poses, `TestGeometryMoustacheWindow` 12 of 24, `TestDetectModuleLimit` 12 of 24 per case, `TestGeometryPresetsSmall` 12 of 24 per preset, `TestGeometryPresets4K` 2 of 4, `TestGeometry16x10` 6 of 12, `TestGlareOnFinder` 2 (was 3; 6 long) and `TestSampleGlareFlagged` 1 of 2. The target, half, was not reached: the honesty and phase statistics keep their CI sizes (one capture instead of two failed three of them).

Measured with the coverage step's flags (`go test -tags=noasm,nounsafe -timeout=15m -count=1 -coverprofile`, `CGO_ENABLED=0`), pinned to 4 CPUs (`taskset -c 0-3`, GOMAXPROCS 4), uncached, before (main at df8e9b5) and after, two runs each, back to back alternately on the development machine. Seconds per test as `go test -json` reports them; the parallel tests' times overlap, and each moves with what runs beside it.

| Test | Before | After |
| --- | --- | --- |
| package `internal/vision` | 127.7, 126.8 | 89.6, 84.8 |
| `TestHonestConfidence` | 39.7, 37.8 | 31.4, 39.6 |
| `TestPipelineHonesty` | 20.0, 17.0 | 22.3, 21.0 |
| `TestPhaseWeights` | 12.8, 13.7 | 13.8, 13.9 |
| `TestGlareUnknown` | 11.3, 11.1 | 11.4, 10.5 |
| `TestPipelineGlare` | 10.2, 10.2 | 9.8, 10.7 |
| `TestPipelinePhase` | 8.8, 8.3 | 8.6, 8.6 |
| `TestGeometryMoustache` | 14.1, 13.6 | 4.2, 4.5 |
| `TestGlareOnFinder` | 10.2, 9.5 | 7.3, 7.2 |
| `TestDetectThreeHostile` | 6.9, 7.1 | skipped |
| `TestDetectModuleLimit` | 6.9, 6.3 | 3.0, 3.2 |
| `TestDetectHostileGrid4K` | 6.6, 6.1 | skipped |
| `TestGeometryPresetsSmall` | 6.0, 6.1 | 3.0, 2.6 |
| `TestDetectThreeHostile4K` | 5.7, 5.6 | skipped |
| `TestGeometryPresets4K` | 3.7, 4.0 | 3.1, 2.9 |
| `TestGeometryMoustacheWindow` | 3.4, 3.4 | 1.8, 1.9 |
| `TestSampleGlareFlagged` | 3.2, 3.0 | 1.4, 1.7 |
| `TestGeometry16x10` | 2.0, 2.0 | 1.2, 1.2 |
| `TestDetectHostileGrid` | 1.0, 0.9 | skipped |
| `TestDetectConcurrent` | 2.5, 2.6 | 2.4, 2.7 |

Under the race detector (`-race -timeout=18m -count=1`, same pinning) the package took 125.7 s before and 122.6 s after: its smoke sizes are unchanged. The package's coverage is 98.2% after (98.3% before).
