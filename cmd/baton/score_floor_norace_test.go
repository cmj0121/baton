//go:build !race

package main

// replayFloorFactor is how far clear of a realistic store's boot
// TestScoreOpenTimeoutIsBoundedBothWays requires scoreOpenTimeout to sit.
//
// An order of magnitude, which is the room a machine ten times slower than the
// one measuring needs. Against the ~85 ms a six-megabyte log boots in here that
// demands about 850 ms, which is what fails on the 500 ms the old
// empty-directory floor admitted.
const replayFloorFactor = 10
